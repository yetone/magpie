package provider

// A Kiro subscription is used through Kiro's own API, the one kiro-cli
// talks to (gateway/kiro.go speaks it), with the sign-in kiro-cli or the
// Kiro IDE already has: kiro-cli keeps its tokens in its SQLite database,
// the IDE in ~/.aws/sso/cache. A Kiro API key (ksk_…), for those who have
// one instead of a sign-in, is kept as the provider's key. Here are the
// credentials, who the account is, its models and its credits.
//
// A token is refreshed the way its owner would: kiro-cli is asked to
// refresh its own first, and only when it can't is the token refreshed
// here and written back where it came from, so the CLI and the IDE go on
// with the one magpie now holds (a refresh may replace the refresh token).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/proc"
)

// KiroExecutable finds kiro-cli, which refreshes its own sign-in; a var so
// tests can fake it. The installer puts it in ~/.local/bin, and on macOS
// inside Kiro CLI.app as well.
var KiroExecutable = func() string {
	if p, err := exec.LookPath("kiro-cli"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local", "bin", "kiro-cli"),
		"/Applications/Kiro CLI.app/Contents/MacOS/kiro-cli", "/usr/local/bin/kiro-cli", "/opt/homebrew/bin/kiro-cli"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// kiroCLIDB is kiro-cli's database, where its sign-in is kept; a var so
// tests can point it elsewhere.
var kiroCLIDB = func() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		dir := os.Getenv("APPDATA")
		if dir == "" {
			dir = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(dir, "kiro-cli", "data.sqlite3")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "kiro-cli", "data.sqlite3")
	}
	return filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3")
}

// kiroIDEDir is where the Kiro IDE keeps its sign-in.
var kiroIDEDir = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aws", "sso", "cache")
}

// kiroKey is the API key saved on the Kiro provider, if any.
func kiroKey() string {
	for _, p := range load().Providers {
		if p.ID == "kiro" {
			return p.Key
		}
	}
	return ""
}

// kiroCred is a sign-in, as it was read.
type kiroCred struct {
	access, refresh string
	expires         time.Time // zero when it doesn't say
	region          string    // where it signed in, for refreshing
	profile         string    // the profile ARN, when the sign-in names it
	method          string    // social | idc | external-idp | apikey
	clientID        string    // idc, external-idp
	clientSecret    string    // idc
	tokenURL        string    // external-idp
	social          bool      // an IDE sign-in with Google or GitHub

	dbKey   string // the auth_kv row it came from
	idePath string // or the IDE's file
}

func (c kiroCred) fresh() bool {
	return c.method == "apikey" || c.expires.IsZero() || time.Until(c.expires) > 2*time.Minute
}

// readKiroCLI reads kiro-cli's sign-in: with Google or GitHub, with AWS
// (Builder ID, IAM Identity Center), or with a company's own identity
// provider.
func readKiroCLI() (kiroCred, bool) {
	path := kiroCLIDB()
	if !fileExists(path) {
		return kiroCred{}, false
	}
	db, err := openReadOnly(path)
	if err != nil {
		return kiroCred{}, false
	}
	defer db.Close()
	get := func(key string) map[string]any {
		var v string
		if db.QueryRow(`SELECT value FROM auth_kv WHERE key = ?`, key).Scan(&v) != nil {
			return nil
		}
		var m map[string]any
		if json.Unmarshal([]byte(v), &m) != nil {
			return nil
		}
		return m
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	for _, kind := range []string{"social", "odic", "external-idp"} {
		key := "kirocli:" + kind + ":token"
		m := get(key)
		if m == nil || str(m, "access_token") == "" {
			continue
		}
		c := kiroCred{access: str(m, "access_token"), refresh: str(m, "refresh_token"), region: str(m, "region"),
			profile: str(m, "profile_arn"), dbKey: key}
		c.expires, _ = time.Parse(time.RFC3339Nano, str(m, "expires_at"))
		switch kind {
		case "social":
			c.method = "social"
		case "odic":
			c.method = "idc"
			if reg := get("kirocli:odic:device-registration"); reg != nil {
				c.clientID, c.clientSecret = str(reg, "client_id"), str(reg, "client_secret")
			}
		default:
			c.method = "external-idp"
			c.clientID, c.tokenURL = str(m, "client_id"), str(m, "token_endpoint")
			if c.tokenURL == "" && str(m, "issuer_url") != "" {
				c.tokenURL = strings.TrimSuffix(str(m, "issuer_url"), "/") + "/v1/token"
			}
		}
		if c.region == "" {
			c.region = "us-east-1"
		}
		return c, true
	}
	return kiroCred{}, false
}

// readKiroIDE reads the Kiro IDE's sign-in.
func readKiroIDE() (kiroCred, bool) {
	path := filepath.Join(kiroIDEDir(), "kiro-auth-token.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return kiroCred{}, false
	}
	var t struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    string `json:"expiresAt"`
		Region       string `json:"region"`
		ClientIDHash string `json:"clientIdHash"`
		AuthMethod   string `json:"authMethod"`
		ProfileArn   string `json:"profileArn"`
	}
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return kiroCred{}, false
	}
	c := kiroCred{access: t.AccessToken, refresh: t.RefreshToken, region: t.Region, profile: t.ProfileArn, method: "idc", idePath: path}
	c.expires, _ = time.Parse(time.RFC3339Nano, t.ExpiresAt)
	if c.region == "" {
		c.region = "us-east-1"
	}
	if t.ClientIDHash != "" {
		var reg struct {
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
		}
		if b, err := os.ReadFile(filepath.Join(kiroIDEDir(), t.ClientIDHash+".json")); err == nil && json.Unmarshal(b, &reg) == nil {
			c.clientID, c.clientSecret = reg.ClientID, reg.ClientSecret
		}
	}
	if strings.EqualFold(t.AuthMethod, "social") || c.clientID == "" {
		c.method, c.social = "social", true
	}
	return c, true
}

// readKiro is the sign-in to use: the key saved on the provider, else
// kiro-cli's, else the IDE's.
func readKiro(key string) (kiroCred, bool) {
	if key != "" {
		return kiroCred{access: key, method: "apikey", region: "us-east-1"}, true
	}
	if c, ok := readKiroCLI(); ok {
		return c, true
	}
	return readKiroIDE()
}

// kiroSignedIn is whether there is a Kiro sign-in to use, without asking
// anyone.
func kiroSignedIn(key string) bool {
	_, ok := readKiro(key)
	return ok
}

// KiroAuth is what a call to Kiro's API is made with.
type KiroAuth struct {
	Token     string
	TokenType string // the tokentype header: API_KEY, EXTERNAL_IDP, or none
	Profile   string // the profile ARN
	Region    string // where the account's API is
}

// Header sets the headers that say who is calling.
func (a KiroAuth) Header(h http.Header) {
	h.Set("Authorization", "Bearer "+a.Token)
	if a.TokenType != "" {
		h.Set("tokentype", a.TokenType)
	}
}

var kiroAuthCache struct {
	sync.Mutex
	key  string
	cred kiroCred
	ok   bool
}

// KiroAuthOf is the Kiro account's credentials: those of the key saved on
// the provider, or of the sign-in when key is "". A token about to expire
// is refreshed first; stale, after Kiro turned one down, refreshes it now.
func KiroAuthOf(ctx context.Context, key string, stale bool) (KiroAuth, error) {
	kiroAuthCache.Lock()
	defer kiroAuthCache.Unlock()
	// what its owner holds now: it may have refreshed it, signed out, or
	// signed in to another account since
	read, ok := readKiro(key)
	if !ok {
		kiroAuthCache.ok = false
		return KiroAuth{}, errors.New("Kiro isn't signed in; sign in with `kiro-cli login` or the Kiro IDE, or save a Kiro API key on the provider")
	}
	c := kiroAuthCache.cred
	if !kiroAuthCache.ok || kiroAuthCache.key != key || read.access != c.access || !c.fresh() || stale {
		if read.profile == "" && read.access == c.access {
			read.profile = c.profile
		}
		if stale && read.access != c.access && read.fresh() {
			stale = false // the owner refreshed it already
		}
		c = read
		if !c.fresh() || stale {
			if c.method == "apikey" {
				return KiroAuth{}, errors.New("Kiro turned down the API key saved on the provider")
			}
			var err error
			if c, err = refreshKiro(ctx, c); err != nil {
				return KiroAuth{}, err
			}
		}
		kiroAuthCache.key, kiroAuthCache.cred, kiroAuthCache.ok = key, c, true
	}
	if c.profile == "" {
		p, err := kiroProfile(ctx, c)
		if err != nil {
			return KiroAuth{}, err
		}
		c.profile = p
		kiroAuthCache.cred.profile = p
	}
	a := KiroAuth{Token: c.access, Profile: c.profile, Region: kiroRegion(c.profile, c.region)}
	switch {
	case c.method == "apikey":
		a.TokenType = "API_KEY"
	case c.method == "external-idp":
		a.TokenType = "EXTERNAL_IDP"
	}
	return a, nil
}

// kiroRegion is where the account's API is: the region its profile is in,
// else the nearest of Kiro's two to where it signed in.
func kiroRegion(profile, signedIn string) string {
	if parts := strings.Split(profile, ":"); len(parts) > 4 && parts[3] != "" {
		return parts[3]
	}
	if strings.HasPrefix(signedIn, "eu-") {
		return "eu-central-1"
	}
	return "us-east-1"
}

const kiroDesktopUA = "Kiro-Desktop/0.2.13 (darwin; arm64)"

// kiroRefreshURL is where a sign-in with Google or GitHub is refreshed; a
// var so tests can answer it.
var kiroRefreshURL = func(region string) string { return "https://prod." + region + ".auth.desktop.kiro.dev/refreshToken" }

// refreshKiro refreshes a sign-in's token and keeps the new one where the
// old one was.
func refreshKiro(ctx context.Context, c kiroCred) (kiroCred, error) {
	if c.dbKey != "" {
		// kiro-cli's own refresh, which keeps its database its own
		if bin := KiroExecutable(); bin != "" {
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			cmd := proc.CommandContext(rctx, bin, "debug", "refresh-auth-token")
			_ = cmd.Run()
			cancel()
			if n, ok := readKiroCLI(); ok && n.dbKey == c.dbKey && n.access != c.access && n.fresh() {
				return n, nil
			}
		}
	}
	if c.refresh == "" {
		return c, errors.New("Kiro's sign-in has expired; sign in again with `kiro-cli login` or the Kiro IDE")
	}
	var access, refresh string
	var expiresIn int
	switch c.method {
	case "social":
		var out struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresIn    int    `json:"expiresIn"`
			ProfileArn   string `json:"profileArn"`
		}
		body, _ := json.Marshal(map[string]string{"refreshToken": c.refresh})
		err := kiroPost(ctx, kiroRefreshURL(c.region), "application/json", body, map[string]string{"User-Agent": kiroDesktopUA}, &out)
		if err != nil {
			return c, err
		}
		access, refresh, expiresIn = out.AccessToken, out.RefreshToken, out.ExpiresIn
		if out.ProfileArn != "" {
			c.profile = out.ProfileArn
		}
	case "idc":
		var out struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresIn    int    `json:"expiresIn"`
		}
		body, _ := json.Marshal(map[string]string{"clientId": c.clientID, "clientSecret": c.clientSecret,
			"refreshToken": c.refresh, "grantType": "refresh_token"})
		if err := kiroPost(ctx, "https://oidc."+c.region+".amazonaws.com/token", "application/json", body, nil, &out); err != nil {
			return c, err
		}
		access, refresh, expiresIn = out.AccessToken, out.RefreshToken, out.ExpiresIn
	case "external-idp":
		if c.tokenURL == "" {
			return c, errors.New("Kiro's sign-in has expired; sign in again with `kiro-cli login`")
		}
		var out struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		form := url.Values{"grant_type": {"refresh_token"}, "client_id": {c.clientID}, "refresh_token": {c.refresh}}
		if err := kiroPost(ctx, c.tokenURL, "application/x-www-form-urlencoded", []byte(form.Encode()), nil, &out); err != nil {
			return c, err
		}
		access, refresh, expiresIn = out.AccessToken, out.RefreshToken, out.ExpiresIn
	default:
		return c, errors.New("Kiro's sign-in has expired")
	}
	if access == "" {
		return c, errors.New("Kiro's sign-in has expired; sign in again with `kiro-cli login` or the Kiro IDE")
	}
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	c.access, c.expires = access, time.Now().Add(time.Duration(expiresIn)*time.Second)
	if refresh != "" {
		c.refresh = refresh
	}
	saveKiro(c)
	return c, nil
}

// saveKiro writes a refreshed token back where it was read from.
func saveKiro(c kiroCred) {
	expires := c.expires.UTC().Format(time.RFC3339Nano)
	switch {
	case c.dbKey != "":
		db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: sqlitePath(kiroCLIDB()), RawQuery: "_pragma=busy_timeout(3000)"}).String())
		if err != nil {
			return
		}
		defer db.Close()
		var v string
		if db.QueryRow(`SELECT value FROM auth_kv WHERE key = ?`, c.dbKey).Scan(&v) != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal([]byte(v), &m) != nil {
			return
		}
		m["access_token"], m["refresh_token"], m["expires_at"] = c.access, c.refresh, expires
		if c.profile != "" && c.method == "social" {
			m["profile_arn"] = c.profile
		}
		b, _ := json.Marshal(m)
		_, _ = db.Exec(`UPDATE auth_kv SET value = ? WHERE key = ?`, string(b), c.dbKey)
	case c.idePath != "":
		b, err := os.ReadFile(c.idePath)
		if err != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			return
		}
		m["accessToken"], m["refreshToken"], m["expiresAt"] = c.access, c.refresh, expires
		out, _ := json.MarshalIndent(m, "", "  ")
		_ = writeFileAtomic(c.idePath, out)
	}
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".magpie-tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// kiroPost posts to a sign-in endpoint.
func kiroPost(ctx context.Context, u, contentType string, body []byte, headers map[string]string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("refreshing Kiro's sign-in: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		if res.StatusCode == 400 || res.StatusCode == 401 || res.StatusCode == 403 {
			return errors.New("Kiro's sign-in has expired; sign in again with `kiro-cli login` or the Kiro IDE")
		}
		return fmt.Errorf("refreshing Kiro's sign-in: %s", res.Status)
	}
	return json.Unmarshal(b, dst)
}

// kiroManagement calls Kiro's management API: GET with a query, or POST
// with a JSON body when body is set.
func kiroManagement(ctx context.Context, region, token, tokenType, method string, q url.Values, body any, dst any) error {
	u := "https://management." + region + ".kiro.dev/" + method
	verb := http.MethodGet
	var rd io.Reader
	if body != nil {
		verb = http.MethodPost
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, verb, u, rd)
	if err != nil {
		return err
	}
	KiroAuth{Token: token, TokenType: tokenType}.Header(req.Header)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode/100 != 2 {
		return &kiroStatusError{status: res.StatusCode, body: string(b)}
	}
	return json.Unmarshal(b, dst)
}

type kiroStatusError struct {
	status int
	body   string
}

func (e *kiroStatusError) Error() string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(e.body), &m) == nil && m.Message != "" {
		return fmt.Sprintf("Kiro: %s (%d)", m.Message, e.status)
	}
	return "Kiro: " + http.StatusText(e.status)
}

// kiroProfile finds the profile a sign-in that doesn't name one uses: an
// API key's own, or the first Kiro lists in either of its regions.
func kiroProfile(ctx context.Context, c kiroCred) (string, error) {
	if c.method == "apikey" {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://management.us-east-1.kiro.dev/", strings.NewReader("{}"))
		if err != nil {
			return "", err
		}
		KiroAuth{Token: c.access, TokenType: "API_KEY"}.Header(req.Header)
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("X-Amz-Target", "AmazonCodeWhispererService.GetProfile")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if res.StatusCode/100 != 2 {
			return "", fmt.Errorf("Kiro didn't take the API key: %s", res.Status)
		}
		var out struct {
			Profile struct {
				Arn string `json:"arn"`
			} `json:"profile"`
		}
		if json.Unmarshal(b, &out) != nil || out.Profile.Arn == "" {
			return "", errors.New("Kiro didn't say which profile the API key is for")
		}
		return out.Profile.Arn, nil
	}
	tt := ""
	if c.method == "external-idp" {
		tt = "EXTERNAL_IDP"
	}
	var last error
	for _, region := range []string{"us-east-1", "eu-central-1"} {
		var out struct {
			Profiles []struct {
				Arn string `json:"arn"`
			} `json:"profiles"`
		}
		if err := kiroManagement(ctx, region, c.access, tt, "List-Available-Profiles", nil, map[string]any{}, &out); err != nil {
			last = err
			continue
		}
		for _, p := range out.Profiles {
			if p.Arn != "" {
				return p.Arn, nil
			}
		}
	}
	if last == nil {
		last = errors.New("Kiro has no profile for this sign-in")
	}
	return "", last
}

// kiroModels asks Kiro for the account's models, the default first.
func kiroModels(ctx context.Context, a KiroAuth) ([]catalog.Model, error) {
	var out kiroModelList
	q := url.Values{"origin": {"KIRO_CLI"}, "profileArn": {a.Profile}}
	if err := kiroManagement(ctx, a.Region, a.Token, a.TokenType, "List-Available-Models", q, nil, &out); err != nil {
		return nil, err
	}
	ms := out.models()
	if len(ms) == 0 {
		return nil, errors.New("Kiro listed no models")
	}
	return ms, nil
}

// kiroModelList is ListAvailableModels' answer:
//
//	{"models": [{"modelId": "claude-sonnet-4.5", "modelName": "Claude Sonnet 4.5",
//	  "supportedInputTypes": ["TEXT", "IMAGE"],
//	  "tokenLimits": {"maxInputTokens": 200000, "maxOutputTokens": 64000}}],
//	 "defaultModel": {"modelId": "auto"}}
type kiroModelList struct {
	Models []struct {
		ID          string   `json:"modelId"`
		Name        string   `json:"modelName"`
		Inputs      []string `json:"supportedInputTypes"`
		TokenLimits struct {
			Input  int `json:"maxInputTokens"`
			Output int `json:"maxOutputTokens"`
		} `json:"tokenLimits"`
	} `json:"models"`
	Default struct {
		ID string `json:"modelId"`
	} `json:"defaultModel"`
}

func (l kiroModelList) models() []catalog.Model {
	var out []catalog.Model
	for _, m := range l.Models {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if name == "" || name == m.ID {
			name = m.ID
			if m.ID == "auto" {
				name = "Auto"
			}
		}
		images := false
		for _, t := range m.Inputs {
			if strings.EqualFold(t, "IMAGE") {
				images = true
			}
		}
		cm := catalog.Model{ID: m.ID, Name: name, Provider: "kiro", Context: m.TokenLimits.Input, Output: m.TokenLimits.Output,
			Images: images, ImageInput: &images}
		if m.ID == l.Default.ID {
			out = append([]catalog.Model{cm}, out...)
		} else {
			out = append(out, cm)
		}
	}
	return out
}

// kiroLimits is GetUsageLimits' answer, as far as magpie reads it.
type kiroLimits struct {
	SubscriptionInfo struct {
		Title string `json:"subscriptionTitle"`
	} `json:"subscriptionInfo"`
	UserInfo struct {
		Email string `json:"email"`
	} `json:"userInfo"`
	Usage []struct {
		Name      string  `json:"displayName"`
		NamePl    string  `json:"displayNamePlural"`
		Used      float64 `json:"currentUsageWithPrecision"`
		Limit     float64 `json:"usageLimitWithPrecision"`
		NextReset float64 `json:"nextDateReset"`
		FreeTrial *struct {
			Status string  `json:"freeTrialStatus"`
			Used   float64 `json:"currentUsageWithPrecision"`
			Limit  float64 `json:"usageLimitWithPrecision"`
			Expiry float64 `json:"freeTrialExpiry"`
		} `json:"freeTrialInfo"`
	} `json:"usageBreakdownList"`
	NextReset float64 `json:"nextDateReset"`
}

func kiroUsageLimits(ctx context.Context, a KiroAuth, email bool) (kiroLimits, error) {
	var out kiroLimits
	q := url.Values{"origin": {"KIRO_CLI"}, "profileArn": {a.Profile}, "resourceType": {"CREDIT"}, "isEmailRequired": {fmt.Sprint(email)}}
	err := kiroManagement(ctx, a.Region, a.Token, a.TokenType, "Get-Usage-Limits", q, nil, &out)
	return out, err
}

// plan is the subscription's name as Kiro gives it, "KIRO FREE", said the
// way the rest of magpie's plans are: "Kiro Free".
func (l kiroLimits) plan() string {
	words := strings.Fields(strings.ToLower(l.SubscriptionInfo.Title))
	for i, w := range words {
		if w == "kiro" {
			words[i] = "Kiro"
		} else if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// windows are the credits used of each allowance: the month's, and a free
// trial's while it runs.
func (l kiroLimits) windows() []QuotaWindow {
	out := []QuotaWindow{}
	at := func(secs float64) *time.Time {
		if secs <= 0 {
			return nil
		}
		t := time.Unix(int64(secs), 0)
		return &t
	}
	count := func(used, limit float64) string {
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", used), "0"), ".") + " / " + fmt.Sprintf("%g", limit)
	}
	for _, u := range l.Usage {
		if t := u.FreeTrial; t != nil && strings.EqualFold(t.Status, "ACTIVE") && t.Limit > 0 {
			out = append(out, QuotaWindow{Name: "Free trial", Used: 100 * t.Used / t.Limit, ResetsAt: at(t.Expiry), Display: count(t.Used, t.Limit)})
		}
		if u.Limit <= 0 {
			continue
		}
		name := u.NamePl
		if name == "" {
			name = u.Name
		}
		if name == "" {
			name = "Credits"
		}
		reset := u.NextReset
		if reset == 0 {
			reset = l.NextReset
		}
		out = append(out, QuotaWindow{Name: name, Used: 100 * u.Used / u.Limit, ResetsAt: at(reset), Display: count(u.Used, u.Limit),
			Span: 30 * 24 * time.Hour})
	}
	return out
}

func kiroSubscriptionUsage(ctx context.Context) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "kiro", Name: "Kiro", Icon: "kiro-color", Windows: []QuotaWindow{}}
	a, err := KiroAuthOf(ctx, kiroKey(), false)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	l, err := kiroUsageLimits(ctx, a, true)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Plan, q.User, q.Windows = l.plan(), l.UserInfo.Email, l.windows()
	return q
}

var kiroStatus struct {
	sync.Mutex
	at         time.Time
	key        string
	refreshing bool
	user, plan string
}

// kiroIdentity is who the account is and its plan, as Kiro last said:
// asked in the background, as Accounts() runs on every request, and asked
// again after five minutes.
func kiroIdentity(key string) (user, plan string) {
	kiroStatus.Lock()
	defer kiroStatus.Unlock()
	if kiroStatus.key != key {
		kiroStatus.key, kiroStatus.user, kiroStatus.plan, kiroStatus.at = key, "", "", time.Time{}
	}
	if time.Since(kiroStatus.at) > 5*time.Minute && !kiroStatus.refreshing {
		kiroStatus.refreshing = true
		go func() {
			u, p := askKiroIdentity(key)
			kiroStatus.Lock()
			if kiroStatus.key == key {
				if u != "" || p != "" {
					kiroStatus.user, kiroStatus.plan = u, p
				}
				kiroStatus.at = time.Now()
			}
			kiroStatus.refreshing = false
			kiroStatus.Unlock()
		}()
	}
	return kiroStatus.user, kiroStatus.plan
}

// askKiroIdentity asks Kiro; a var so tests don't.
var askKiroIdentity = func(key string) (user, plan string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := KiroAuthOf(ctx, key, false)
	if err != nil {
		return "", ""
	}
	l, err := kiroUsageLimits(ctx, a, true)
	if err != nil {
		return "", ""
	}
	return l.UserInfo.Email, l.plan()
}

func kiroAccount() (Provider, bool) {
	key := kiroKey()
	c, ok := readKiro(key)
	if !ok {
		return Provider{}, false
	}
	user, plan := kiroIdentity(key)
	if user == "" {
		switch c.method {
		case "apikey":
			user = "Kiro API key"
		default:
			user = "Kiro account"
		}
	}
	if plan == "" && c.method == "apikey" {
		plan = "API key"
	}
	acct := &Account{Agent: "kiro", User: user, Plan: plan}
	acct.models = func() []catalog.Model {
		// what the last fetch saved — Available() runs on every request,
		// so Kiro isn't asked here
		if ms, _, ok := catalog.Live("kiro"); ok {
			return ms
		}
		return []catalog.Model{{ID: "auto", Name: "Auto", Provider: "kiro"}}
	}
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		a, err := KiroAuthOf(ctx, key, false)
		if err != nil {
			return nil, err
		}
		ms, err := kiroModels(ctx, a)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive("kiro", "", ms)
	}
	return Provider{ID: "kiro", Name: "Kiro", Icon: "kiro-color", Website: "https://kiro.dev", Key: key, Account: acct}, true
}

// KiroModel is what Kiro last said of one of its models: how many tokens a
// prompt may hold, and whether it takes images; zero and false when it
// hasn't said.
func KiroModel(id string) (context int, images bool) {
	ms, _, _ := catalog.Live("kiro")
	for _, m := range ms {
		if m.ID == id {
			return m.Context, m.Images
		}
	}
	return 0, false
}

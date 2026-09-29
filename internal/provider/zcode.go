package provider

// A ZCode subscription is Z.ai's GLM Coding Plan, which ZCode (Zhipu's
// desktop app) signs in to. The plan is served on an Anthropic-compatible
// endpoint to a plain API key, `<id>.<secret>`, that ZCode mints for the
// account and names zcode-api-key; magpie uses that key as ZCode does.
//
// ZCode's own account is read, never changed, from its credential store,
// ~/.zcode/v2/credentials.json: each value is "enc:v1:" + iv.tag.ciphertext
// (base64url), AES-256-GCM under sha256 of $ZCODE_CREDENTIAL_SECRET, or
// else of "zcode-credential-fallback:<platform>:<home>:<user>". Further
// accounts are signed in by magpie with ZCode's own polling sign-in
// (zcode.z.ai/api/v1/oauth/cli/…), and their key is kept in logins.json.
//
// The plan's models are ZCode's, as its built-in config lists them for the
// coding plan (zcode_models.go); zcodeModels are them before that is read.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Where ZCode's coding plan and its sign-in are; vars so tests can point
// them elsewhere.
var (
	ZCodeZaiBase      = "https://api.z.ai/api/anthropic"
	ZCodeBigModelBase = "https://open.bigmodel.cn/api/anthropic"
	zcodeAPI          = "https://zcode.z.ai"
	zcodeZaiAPI       = "https://api.z.ai"
)

// zcodeAppVersion is the ZCode whose sign-in magpie makes.
const zcodeAppVersion = "3.14.3"

var zcodeModels = []catalog.Model{
	{ID: "GLM-5.3", Name: "GLM-5.3", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "GLM-5.3-Flash", Name: "GLM-5.3-Flash", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "GLM-5.2", Name: "GLM-5.2", Context: 1_000_000, Efforts: []string{"none", "high", "max"}},
	{ID: "GLM-5-Turbo", Name: "GLM-5-Turbo", Context: 200_000, Efforts: []string{"none", "high"}},
}

// zcodeKey is a coding plan's key and where it is served.
type zcodeKey struct {
	Key  string `json:"apiKey"`
	Base string `json:"base"`
}

// ---- ZCode's own account ------------------------------------------------------

func zcodeCredentialsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".zcode", "v2", "credentials.json")
}

// zcodeSecret is the key ZCode encrypts its credentials with.
func zcodeSecret() []byte {
	seed := os.Getenv("ZCODE_CREDENTIAL_SECRET")
	if seed == "" {
		home, _ := os.UserHomeDir()
		name := ""
		if u, err := user.Current(); err == nil {
			name = u.Username
			if i := strings.LastIndexByte(name, '\\'); i >= 0 {
				name = name[i+1:] // DOMAIN\user; node's userInfo() has the user alone
			}
		}
		platform := runtime.GOOS
		if platform == "windows" {
			platform = "win32"
		}
		seed = "zcode-credential-fallback:" + platform + ":" + home + ":" + name
	}
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

func zcodeDecrypt(secret []byte, v string) (string, bool) {
	rest, ok := strings.CutPrefix(v, "enc:v1:")
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return "", false
	}
	var raw [3][]byte
	for i, p := range parts {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p, "="))
		if err != nil {
			return "", false
		}
		raw[i] = b
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(raw[0]))
	if err != nil {
		return "", false
	}
	out, err := gcm.Open(nil, raw[0], append(raw[2], raw[1]...), nil)
	return string(out), err == nil
}

// zcodeOwn is the account ZCode is signed in to and its coding plan's key;
// ok is false when it has none.
func zcodeOwn() (who string, k zcodeKey, ok bool) {
	var store map[string]string
	if !readJSON(zcodeCredentialsPath(), &store) {
		return "", zcodeKey{}, false
	}
	secret := zcodeSecret()
	for name, v := range store {
		// account-provider:coding-plan:account:zai-individual-coding-plan:account:<uuid>:api-key
		if !strings.Contains(name, ":coding-plan:") || !strings.HasSuffix(name, ":api-key") {
			continue
		}
		key, ok := zcodeDecrypt(secret, v)
		if !ok || !strings.Contains(key, ".") {
			continue
		}
		base := ZCodeZaiBase
		if strings.Contains(name, ":bigmodel-") {
			base = ZCodeBigModelBase
		}
		// Z.ai's first, as ZCode lists it
		if k.Key == "" || (base == ZCodeZaiBase && k.Base != ZCodeZaiBase) {
			k = zcodeKey{Key: key, Base: base}
		}
	}
	if k.Key == "" {
		return "", zcodeKey{}, false
	}
	var info struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		ID    string `json:"user_id"`
	}
	if v, ok := store["oauth:zai:user_info"]; ok {
		if s, ok := zcodeDecrypt(secret, v); ok {
			_ = json.Unmarshal([]byte(s), &info)
		}
	}
	return zcodeWho(info.Email, info.Name, info.ID), k, true
}

// zcodeWho names a Z.ai account: its email, or the phone number of one
// signed in by phone (its email is then <phone>@phone.local).
func zcodeWho(email, name, id string) string {
	email = strings.TrimSuffix(email, "@phone.local")
	return firstNonEmpty(email, name, id, "ZCode")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// ---- the accounts -------------------------------------------------------------

type zcodeLoginKey struct {
	Login
	key zcodeKey
}

func zcodeSaved(l savedLogin) (zcodeKey, bool) {
	var k zcodeKey
	if json.Unmarshal(l.Auth, &k) != nil || k.Key == "" {
		return zcodeKey{}, false
	}
	if k.Base == "" {
		k.Base = ZCodeZaiBase
	}
	return k, true
}

// zcodeLogins is every ZCode account signed in, the first in use first.
func zcodeLogins() []zcodeLoginKey {
	ownUser, own, hasOwn := zcodeOwn()
	if !hasOwn {
		ownUser = ""
	}
	var out []zcodeLoginKey
	for _, l := range sideLogins("zcode", ownUser, func(l savedLogin) bool {
		_, ok := zcodeSaved(l)
		return ok
	}) {
		k := own
		if !l.saved.own() {
			k, _ = zcodeSaved(l.saved)
		}
		out = append(out, zcodeLoginKey{l.Login, k})
	}
	return out
}

func zcodeSide() []sideLogin {
	var out []sideLogin
	for _, z := range zcodeLogins() {
		out = append(out, sideLogin{Login: z.Login})
	}
	return out
}

func zcodeLoginList() []Login { return loginsOf(zcodeSide()) }

func switchZCodeLogin(user string) error { return switchSideLogin("zcode", user, zcodeSide()) }

func setZCodeLoginOn(user string, on bool) error {
	return setSideLoginOn("zcode", user, on, zcodeSide())
}

func forgetZCodeLogin(user string) error {
	return forgetSideLogin("zcode", user, zcodeSide(), nil)
}

func zcodeAccount() (Provider, bool) {
	ls := zcodeLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return zcodeProvider(ls[0].User, ls[0].Plan, ls[0].key), true
}

// zcodeAlsoOn is the ZCode accounts in use behind the first.
func zcodeAlsoOn() []Provider {
	var out []Provider
	for _, l := range zcodeLogins() {
		if !l.Active && l.On {
			out = append(out, zcodeProvider(l.User, l.Plan, l.key))
		}
	}
	return out
}

func zcodeProvider(who, plan string, k zcodeKey) Provider {
	acct := &Account{Agent: "zcode", User: who, Plan: plan}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", k.Key)
		req.Header.Set("Authorization", "Bearer "+k.Key)
		return nil
	}
	acct.models = func() []catalog.Model { return zcodeModels }
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := zcodeFetchModels(ctx, k.Base)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive("zcode", k.Base, ms)
	}
	return Provider{ID: "zcode", Name: "ZCode", Icon: "zcode", Anthropic: k.Base, Website: "https://zcode.z.ai", Account: acct}
}

// ---- allowance ----------------------------------------------------------------

// zcodeQuota is a coding plan's allowance: credits per five hours and per
// week, as ZCode shows them.
func zcodeQuota(ctx context.Context, l Login, k zcodeKey) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "zcode", Name: "ZCode", Icon: "zcode", Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	var data struct {
		Limits []struct {
			Type      string  `json:"type"`
			Unit      int     `json:"unit"`
			Number    int     `json:"number"`
			Usage     float64 `json:"usage"`
			Current   float64 `json:"currentValue"`
			Remaining float64 `json:"remaining"`
			Percent   float64 `json:"percentage"`
			Reset     int64   `json:"nextResetTime"`
		} `json:"limits"`
		Level string `json:"level"`
	}
	if err := zcodeGet(ctx, zcodeRoot(k.Base)+"/api/monitor/usage/quota/limit", k.Key, &data); err != nil {
		q.Error = err.Error()
		return q
	}
	if data.Level != "" {
		q.Plan = "GLM Coding " + strings.ToUpper(data.Level[:1]) + data.Level[1:]
	}
	for _, x := range data.Limits {
		span := zcodeSpan(x.Unit, x.Number)
		w := QuotaWindow{Name: zcodeWindowName(span), Used: x.Percent, Span: span}
		if x.Usage > 0 {
			used := x.Usage - x.Remaining
			w.Used = 100 * used / x.Usage
			w.Display = fmt.Sprintf("%s / %s", compactNumber(used), compactNumber(x.Usage))
		}
		if x.Reset > 0 {
			t := time.UnixMilli(x.Reset)
			w.ResetsAt = &t
		}
		q.Windows = append(q.Windows, w)
	}
	q.Until, q.Renew = zhipuTerm(ctx, zcodeRoot(k.Base), k.Key)
	return q
}

// zhipuTerm is how long a GLM Coding plan is paid for, from the
// account's subscriptions as ZCode reads them: the valid one's next
// renewal, a charge when it renews itself, else the end of its time — or
// the last date its "valid" span names.
func zhipuTerm(ctx context.Context, root, key string) (*time.Time, string) {
	var subs []zhipuSubscription
	if zcodeGet(ctx, root+"/api/biz/subscription/list", key, &subs) != nil {
		return nil, ""
	}
	return zhipuTermOf(subs)
}

type zhipuSubscription struct {
	Status    string `json:"status"`
	Valid     string `json:"valid"`
	AutoRenew any    `json:"autoRenew"` // true or 1
	NextRenew string `json:"nextRenewTime"`
}

// zhipuDate finds the dates in a span like "2026-09-18 12:00:00-2026-10-18 12:00:00".
var zhipuDate = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:[ T]\d{2}:\d{2}:\d{2})?`)

// zhipuTime reads the plan's times, "2026-10-18 12:00:00" in Beijing.
func zhipuTime(s string) *time.Time {
	s = strings.Replace(strings.TrimSpace(s), " ", "T", 1)
	cst := time.FixedZone("CST", 8*3600)
	for _, f := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, s, cst); err == nil {
			return &t
		}
	}
	return nil
}

func zhipuTermOf(subs []zhipuSubscription) (*time.Time, string) {
	for _, s := range subs {
		if !strings.EqualFold(s.Status, "VALID") {
			continue
		}
		auto := s.AutoRenew == true || s.AutoRenew == float64(1)
		if t := zhipuTime(s.NextRenew); t != nil {
			if auto {
				return t, "auto"
			}
			return t, "off"
		}
		if ds := zhipuDate.FindAllString(s.Valid, -1); len(ds) > 0 && !auto {
			if t := zhipuTime(ds[len(ds)-1]); t != nil {
				return t, "off"
			}
		}
		return nil, ""
	}
	return nil, ""
}

// zcodeSpan reads a limit's window: unit 3 counts hours, 6 weeks (and
// 4, 5 days and months by the same count).
func zcodeSpan(unit, n int) time.Duration {
	n = max(n, 1)
	switch unit {
	case 1:
		return time.Duration(n) * time.Minute
	case 3:
		return time.Duration(n) * time.Hour
	case 4:
		return time.Duration(n) * 24 * time.Hour
	case 5:
		return time.Duration(n) * 30 * 24 * time.Hour
	case 6:
		return time.Duration(n) * 7 * 24 * time.Hour
	}
	return 0
}

func zcodeWindowName(span time.Duration) string {
	switch {
	case span == 0:
		return "Credits"
	case span < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(span.Hours()))
	case span == 7*24*time.Hour:
		return "Weekly"
	case span >= 28*24*time.Hour:
		return "Monthly"
	}
	return fmt.Sprintf("%d days", int(span.Hours()/24))
}

// zcodeRoot is the site a plan's endpoint is on: https://api.z.ai.
func zcodeRoot(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return zcodeZaiAPI
}

func zcodeLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	for _, z := range zcodeLogins() {
		if strings.EqualFold(z.User, l.User) {
			return zcodeQuota(ctx, l, z.key)
		}
	}
	return SubscriptionQuota{Provider: "zcode", Plan: l.Plan, Windows: []QuotaWindow{}, Error: "not signed in"}
}

// zcodePlan names the coding plan a key has, "" for none.
func zcodePlan(ctx context.Context, k zcodeKey) (string, error) {
	var subs []struct {
		Product string `json:"productName"`
		Status  string `json:"status"`
	}
	if err := zcodeGet(ctx, zcodeRoot(k.Base)+"/api/biz/subscription/list", k.Key, &subs); err != nil {
		return "", err
	}
	for _, s := range subs {
		if strings.EqualFold(s.Status, "VALID") {
			return s.Product, nil
		}
	}
	return "", nil
}

// ---- Z.ai's API ---------------------------------------------------------------

// zcodeCall asks one of Z.ai's JSON endpoints, which wrap what they say in
// {code, msg, data}: code 0 or 200 is a success.
func zcodeCall(ctx context.Context, method, u, auth string, body, dst any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ZCode/"+zcodeAppVersion)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var env struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if json.Unmarshal(b, &env) == nil && env.Msg != "" {
			return fmt.Errorf("%s (%d)", env.Msg, res.StatusCode)
		}
		return &accountStatusError{status: res.StatusCode}
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	if c := strings.Trim(string(env.Code), `"`); c != "" && c != "0" && c != "200" && c != "null" {
		return errors.New(firstNonEmpty(env.Msg, "error "+c))
	}
	if dst == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, dst)
}

// zcodeGet asks with a coding plan's key, which goes bare in Authorization.
func zcodeGet(ctx context.Context, u, key string, dst any) error {
	return zcodeCall(ctx, http.MethodGet, u, key, nil, dst)
}

// ---- signing in ---------------------------------------------------------------

// startZCodeSignIn is ZCode's own polling sign-in: zcode.z.ai opens a flow,
// the browser signs in to Z.ai, and the flow is asked until it is ready.
// The account's coding plan key is then found or made, as ZCode does it.
func startZCodeSignIn(s *signInFlow) error {
	ctx, cancel := context.WithCancel(context.Background())
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	poll := "Bearer " + hex.EncodeToString(b)
	var flow struct {
		ID       string  `json:"flow_id"`
		URL      string  `json:"authorize_url"`
		Expires  float64 `json:"expires_at"`
		Interval float64 `json:"poll_interval_sec"`
	}
	if err := zcodeCall(ctx, http.MethodPost, zcodeAPI+"/api/v1/oauth/cli/init", poll, map[string]string{"provider": "zai"}, &flow); err != nil {
		cancel()
		return fmt.Errorf("ZCode sign-in: %w", err)
	}
	u, err := url.Parse(flow.URL)
	if flow.ID == "" || err != nil || u.Scheme != "https" {
		cancel()
		return errors.New("ZCode gave no sign-in page")
	}
	// where Z.ai sends the browser back, as ZCode sets it
	back := zcodeAPI + "/app/oauth/login?" + url.Values{"redirect": {"zcode://oauth/callback"}, "app_version": {zcodeAppVersion}}.Encode()
	q := u.Query()
	q.Set("redirect_uri", back)
	u.RawQuery = q.Encode()
	s.mu.Lock()
	s.st.URL = u.String()
	s.stop = cancel
	s.mu.Unlock()
	interval := time.Duration(max(flow.Interval, 1)) * time.Second
	deadline := time.Unix(int64(flow.Expires), 0)
	if flow.Expires == 0 {
		deadline = time.Now().Add(5 * time.Minute)
	}
	go func() {
		defer cancel()
		fail := func(msg string) { s.finish(SignInState{State: "failed", Error: msg}) }
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			if time.Now().After(deadline) {
				fail("the sign-in expired; start again")
				return
			}
			var got struct {
				Status string `json:"status"`
				Zai    struct {
					AccessToken string `json:"access_token"`
				} `json:"zai"`
				User struct {
					ID    string `json:"user_id"`
					Email string `json:"email"`
					Name  string `json:"name"`
				} `json:"user"`
			}
			err := zcodeCall(ctx, http.MethodGet, zcodeAPI+"/api/v1/oauth/cli/poll/"+url.PathEscape(flow.ID), poll, nil, &got)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				var st *accountStatusError
				if errors.As(err, &st) && st.status >= 400 && st.status < 500 && st.status != 408 && st.status != 429 {
					fail("ZCode sign-in: " + err.Error())
					return
				}
				continue // a hiccup: ask again
			case got.Status == "pending" || got.Status == "":
				continue
			case got.Status == "failed":
				fail("the sign-in was declined on Z.ai")
				return
			case got.Status != "ready" || got.Zai.AccessToken == "":
				fail("ZCode sign-in: unexpected answer " + got.Status)
				return
			}
			who := zcodeWho(got.User.Email, got.User.Name, got.User.ID)
			k, plan, err := zcodeMintKey(ctx, got.Zai.AccessToken)
			if err != nil {
				fail(err.Error())
				return
			}
			auth, _ := json.Marshal(k)
			ownUser, _, ok := zcodeOwn()
			if !ok {
				ownUser = ""
			}
			if err := addSideLogin(savedLogin{Agent: "zcode", User: who, Plan: plan, Auth: auth}, ownUser, func(savedLogin) {}); err != nil {
				fail(err.Error())
				return
			}
			s.finish(SignInState{State: "done", User: who, Plan: plan, Using: ok && strings.EqualFold(ownUser, who)})
			return
		}
	}()
	return nil
}

// zcodeMintKey turns a Z.ai sign-in into its coding plan key: Z.ai's
// business token, then the key named zcode-api-key in the account's default
// project, made if it isn't there, with its secret.
func zcodeMintKey(ctx context.Context, zaiToken string) (zcodeKey, string, error) {
	var biz struct {
		Token string `json:"access_token"`
	}
	if err := zcodeCall(ctx, http.MethodPost, zcodeZaiAPI+"/api/auth/z/login", "", map[string]string{"token": zaiToken}, &biz); err != nil || biz.Token == "" {
		return zcodeKey{}, "", fmt.Errorf("Z.ai sign-in: %v", firstErr(err, "no token"))
	}
	bearer := "Bearer " + biz.Token
	var info struct {
		Orgs []struct {
			ID       string `json:"organizationId"`
			Name     string `json:"organizationName"`
			Projects []struct {
				ID   string `json:"projectId"`
				Name string `json:"projectName"`
				Type any    `json:"projectType"`
			} `json:"projects"`
		} `json:"organizations"`
	}
	if err := zcodeCall(ctx, http.MethodGet, zcodeZaiAPI+"/api/biz/customer/getCustomerInfo", bearer, nil, &info); err != nil {
		return zcodeKey{}, "", fmt.Errorf("Z.ai account: %w", err)
	}
	// the default organization and project, as ZCode picks them
	org, proj := "", ""
	for _, o := range info.Orgs {
		var ps []string
		def := ""
		for _, p := range o.Projects {
			if p.ID == "" || fmt.Sprint(p.Type) == "2" {
				continue
			}
			ps = append(ps, p.ID)
			if def == "" && strings.Contains(p.Name, "默认项目") {
				def = p.ID
			}
		}
		if o.ID == "" || len(ps) == 0 {
			continue
		}
		if def == "" {
			def = ps[0]
		}
		if org == "" || strings.Contains(o.Name, "默认机构") {
			org, proj = o.ID, def
			if strings.Contains(o.Name, "默认机构") {
				break
			}
		}
	}
	if org == "" {
		return zcodeKey{}, "", errors.New("this Z.ai account has no project for an API key")
	}
	keys := zcodeZaiAPI + "/api/biz/v1/organization/" + url.PathEscape(org) + "/projects/" + url.PathEscape(proj) + "/api_keys"
	type apiKey struct {
		Name   string `json:"name"`
		APIKey string `json:"apiKey"`
	}
	var list []apiKey
	if err := zcodeCall(ctx, http.MethodGet, keys, bearer, nil, &list); err != nil {
		return zcodeKey{}, "", fmt.Errorf("Z.ai API keys: %w", err)
	}
	id := ""
	for _, k := range list {
		if k.Name == "zcode-api-key" {
			id = strings.TrimSpace(k.APIKey)
		}
	}
	if id == "" {
		var made apiKey
		if err := zcodeCall(ctx, http.MethodPost, keys, bearer, map[string]string{"name": "zcode-api-key"}, &made); err != nil {
			return zcodeKey{}, "", fmt.Errorf("Z.ai API key: %w", err)
		}
		id = strings.TrimSpace(made.APIKey)
	}
	var secret struct {
		Secret string `json:"secretKey"`
	}
	if id != "" {
		if err := zcodeCall(ctx, http.MethodGet, keys+"/copy/"+url.PathEscape(id), bearer, nil, &secret); err != nil {
			return zcodeKey{}, "", fmt.Errorf("Z.ai API key: %w", err)
		}
	}
	if id == "" || strings.TrimSpace(secret.Secret) == "" {
		return zcodeKey{}, "", errors.New("Z.ai gave no API key")
	}
	k := zcodeKey{Key: id + "." + strings.TrimSpace(secret.Secret), Base: ZCodeZaiBase}
	plan, err := zcodePlan(ctx, k)
	if err != nil {
		return zcodeKey{}, "", fmt.Errorf("GLM Coding Plan: %w", err)
	}
	if plan == "" {
		return zcodeKey{}, "", errors.New("this Z.ai account has no GLM Coding Plan — subscribe at z.ai/subscribe, then add it again")
	}
	return k, plan, nil
}

func firstErr(err error, otherwise string) string {
	if err != nil {
		return err.Error()
	}
	return otherwise
}

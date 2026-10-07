package provider

// Google Vertex AI serves Google's Gemini models from the user's own
// Google Cloud project, on its generateContent API:
// https://<host>/v1/projects/<project>/locations/<location>/publishers/google/models/<model>:streamGenerateContent,
// asked with an OAuth access token minted from the user's Google
// credentials, never an API key. The host follows the location:
// aiplatform.googleapis.com for global, aiplatform.<us|eu>.rep.googleapis.com
// for the two multi-regions, <region>-aiplatform.googleapis.com for a
// region. Vertex bills the project in the URL, so no quota project is sent.
//
// The credentials are found as Google's own libraries find their
// Application Default Credentials: the file the provider names, else the
// one GOOGLE_APPLICATION_CREDENTIALS names, else the one
// `gcloud auth application-default login` writes. Three kinds are read: a
// person's (authorized_user, a refresh token), a service account key
// (service_account, traded as a signed JWT) and gcloud's
// impersonated_service_account. With Impersonate set, the token those give
// is traded at IAM Credentials for one of that service account's, as
// gcloud's --impersonate-service-account does: the credentials' account
// needs roles/iam.serviceAccountTokenCreator on it. Not read: the metadata
// server of Compute Engine, Cloud Run and GKE, workload identity federation
// (external_account), and Vertex's express mode API keys.

import (
	"cmp"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// VertexPreset is the preset's id.
const VertexPreset = "google-vertex"

// Vertex is where a Vertex AI provider's requests go, and whose
// credentials sign them.
type Vertex struct {
	// Project is the Google Cloud project's id: requests are made, and
	// billed, there.
	Project string `json:"project,omitempty"`
	// Location is global, us, eu or a region (us-central1); each serves
	// models of its own (vertexModels).
	Location string `json:"location,omitempty"`
	// Credentials is a credentials file; "" finds them as Google's
	// libraries do (vertexCredentialsFile).
	Credentials string `json:"credentials,omitempty"`
	// Impersonate is the email of a service account the credentials are
	// traded for, "" to ask as their own account.
	Impersonate string `json:"impersonate,omitempty"`
}

// IsVertex reports whether the provider is Google Vertex AI.
func (p Provider) IsVertex() bool { return p.Preset == VertexPreset }

// Normal is v as it is kept: trimmed, at global when no location is
// given, nil when nothing is. The app's Refresh and Test ask with the
// values typed spelled so.
func (v *Vertex) Normal() *Vertex {
	if v == nil {
		return nil
	}
	n := Vertex{
		Project:     strings.ToLower(strings.TrimSpace(v.Project)),
		Location:    strings.ToLower(strings.TrimSpace(v.Location)),
		Credentials: strings.TrimSpace(v.Credentials),
		Impersonate: strings.ToLower(strings.TrimSpace(v.Impersonate)),
	}
	if n == (Vertex{}) {
		return nil
	}
	n.Location = cmp.Or(n.Location, "global")
	return &n
}

var (
	// a project's id (my-project-123), its number, or a domain-scoped
	// one's (example.com:my-project)
	vertexProject = regexp.MustCompile(`^[a-z0-9][a-z0-9.:-]*[a-z0-9]$`)
	// global, us, eu or a region (us-central1, europe-west4)
	vertexLocation = regexp.MustCompile(`^[a-z]+(-[a-z0-9]+)*$`)
	vertexAccount  = regexp.MustCompile(`^[^\s@/]+@[^\s@/]+$`)
)

// check is why a Vertex AI provider with v can't be saved, nil when it can.
func (v *Vertex) check() error {
	switch {
	case v == nil || v.Project == "":
		return errors.New("Google Vertex AI needs the id of your Google Cloud project")
	case !vertexProject.MatchString(v.Project):
		return fmt.Errorf("%q is no Google Cloud project id", v.Project)
	case !vertexLocation.MatchString(v.Location):
		return fmt.Errorf("%q is no Vertex AI location: global, us, eu or a region such as us-central1", v.Location)
	case v.Credentials != "" && !filepath.IsAbs(v.Credentials) && !strings.HasPrefix(filepath.ToSlash(v.Credentials)+"/", "~/"):
		return fmt.Errorf("give the credentials file's full path, not %q", v.Credentials)
	case v.Impersonate != "" && !vertexAccount.MatchString(v.Impersonate):
		return fmt.Errorf("%q is no service account's email", v.Impersonate)
	}
	return nil
}

// vertexHost is the API's host at a location.
func vertexHost(loc string) string {
	switch loc {
	case "global":
		return "aiplatform.googleapis.com"
	case "us", "eu":
		return "aiplatform." + loc + ".rep.googleapis.com"
	}
	return loc + "-aiplatform.googleapis.com"
}

// Where Vertex AI and IAM Credentials are; vars so tests can point them
// elsewhere. Google's token endpoint is googleTokenURL.
var (
	vertexRoot           = func(loc string) string { return "https://" + vertexHost(loc) }
	googleIAMCredentials = "https://iamcredentials.googleapis.com/v1"
)

// VertexForTest points Vertex AI at root, Google's token endpoint at token
// and IAM Credentials at iam until the returned function runs, and forgets
// the tokens minted. Tests outside this package use it.
func VertexForTest(root, token, iam string) func() {
	oldR, oldT, oldI := vertexRoot, googleTokenURL, googleIAMCredentials
	vertexRoot = func(string) string { return root }
	googleTokenURL, googleIAMCredentials = token, iam
	forgetVertexTokens()
	return func() {
		vertexRoot, googleTokenURL, googleIAMCredentials = oldR, oldT, oldI
		forgetVertexTokens()
	}
}

// vertexBase is where the provider's requests go, the model's path
// (VertexPath) after it: its project at its location. "" until those are
// ones an address can be made of.
func (p Provider) vertexBase() string {
	v := p.Vertex
	if v == nil || !vertexProject.MatchString(v.Project) {
		return ""
	}
	loc := cmp.Or(v.Location, "global")
	if !vertexLocation.MatchString(loc) {
		return ""
	}
	return vertexRoot(loc) + "/v1/projects/" + v.Project + "/locations/" + loc
}

// VertexPath is where, after a Vertex AI provider's base, a request for
// model goes: streamed, as the gateway asks every one there but a Gemini
// client's own asked whole (VertexWholePath).
func VertexPath(model string) string {
	return "/publishers/google/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
}

// VertexWholePath is where a request for model answered as one JSON body
// goes.
func VertexWholePath(model string) string {
	return "/publishers/google/models/" + url.PathEscape(model) + ":generateContent"
}

// vertexTest is the smallest request for model at a Vertex AI provider:
// answered whole, after as little thinking as the model takes (Gemini 3
// low, which each of them takes; 2.5 Pro 128 tokens, which it can't go
// under; 2.5's Flash none).
func vertexTest(q Provider, model string) (string, string) {
	think := `"thinkingConfig":{"thinkingLevel":"low"},`
	switch m := strings.ToLower(model); {
	case strings.HasPrefix(m, "gemini-2.5-pro"):
		think = `"thinkingConfig":{"thinkingBudget":128},`
	case strings.HasPrefix(m, "gemini-2.5"):
		think = `"thinkingConfig":{"thinkingBudget":0},`
	case !strings.HasPrefix(m, "gemini-3"):
		think = ""
	}
	return q.Base(Gemini) + "/publishers/google/models/" + url.PathEscape(model) + ":generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{` + think + `"maxOutputTokens":16}}`
}

// vertexModels are the Gemini models a location serves, as each answered
// on 2026-10-06: every one at global; Gemini 3's Flash models at us and
// eu, which serve no Pro, preview or 2.5; Gemini 2.5 at a region, where a
// few add one more (asia-northeast1's gemini-3.5-flash), typed in by hand.
func vertexModels(loc string) []string {
	switch cmp.Or(loc, "global") {
	case "global":
		return Preset(VertexPreset).Models
	case "us", "eu":
		return []string{"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-3.1-flash-lite"}
	}
	return []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"}
}

// vertexScope is what a token is minted for: Google Cloud's APIs.
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

// vertexRefreshLead is how long before a token lapses another is minted.
const vertexRefreshLead = 5 * time.Minute

// vertexMintWait bounds minting one, every exchange it takes included; a
// test shortens it.
var vertexMintWait = 30 * time.Second

// vertexTokens are the access tokens minted, by the credentials file and
// the account they were traded for. A request waits while its entry's
// token is minted, so requests made together mint it once, and are given
// its error when that mint fails.
var vertexTokens = struct {
	sync.Mutex
	m map[string]*vertexToken
}{m: map[string]*vertexToken{}}

type vertexToken struct {
	// held while the token is read or minted: a request given up on stops
	// waiting for another's mint, which may take vertexMintWait
	held   chan struct{}
	stamp  string // the file's size and time when the token was minted from it
	token  string
	expiry int64 // Unix ms, by the wall clock, which a computer's sleep doesn't stop
	// mints counts the mints ended, failed is the last one's error: a
	// request that waited through one that failed is given its error
	// rather than minting again, which would have each in turn wait as long
	mints  atomic.Int64
	failed error
}

func forgetVertexTokens() {
	vertexTokens.Lock()
	vertexTokens.m = map[string]*vertexToken{}
	vertexTokens.Unlock()
}

// vertexAccessToken is the token a request to the provider is signed
// with: the one minted before, until it nearly lapses or its file changes
// (gcloud signed in again).
func (p Provider) vertexAccessToken(ctx context.Context) (string, error) {
	v := p.Vertex
	if v == nil {
		return "", errors.New("Google Vertex AI needs the id of your Google Cloud project")
	}
	file, err := vertexCredentialsFile(v.Credentials)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(file)
	if err != nil {
		switch {
		case !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("Google credentials: %w", err)
		case v.Credentials != "":
			return "", fmt.Errorf("no Google credentials at %s: correct the provider's credentials file, or clear it to use Application Default Credentials", file)
		case os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "":
			// as Google's libraries, which look no further; gcloud's
			// file isn't read while the variable is set
			return "", fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS names %s, which isn't there: correct or unset it, or give the provider a credentials file", file)
		}
		return "", fmt.Errorf("no Google credentials at %s: sign in with `gcloud auth application-default login`, or give the provider a credentials file", file)
	}
	stamp := fmt.Sprint(st.Size(), " ", st.ModTime().UnixNano())
	key := file + "\x00" + v.Impersonate
	vertexTokens.Lock()
	t := vertexTokens.m[key]
	if t == nil {
		t = &vertexToken{held: make(chan struct{}, 1)}
		vertexTokens.m[key] = t
	}
	vertexTokens.Unlock()
	seen := t.mints.Load()
	select {
	case t.held <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-t.held }()
	if t.mints.Load() != seen && t.failed != nil {
		return "", t.failed
	}
	if t.token != "" && t.stamp == stamp && time.Now().UnixMilli() < t.expiry-vertexRefreshLead.Milliseconds() {
		return t.token, nil
	}
	mctx, cancel := context.WithTimeout(ctx, vertexMintWait)
	defer cancel()
	tok, exp, err := mintVertex(mctx, file, v.Impersonate)
	if ctx.Err() == nil { // a request given up on says nothing of the mint
		t.failed = err
		t.mints.Add(1)
	}
	if err != nil {
		return "", err
	}
	t.token, t.expiry, t.stamp = tok, exp.UnixMilli(), stamp
	return tok, nil
}

// vertexCredentialsFile is the credentials file tokens are minted from:
// the one given, else Application Default Credentials' —
// GOOGLE_APPLICATION_CREDENTIALS's, else the one gcloud keeps in its
// folder (CLOUDSDK_CONFIG, else ~/.config/gcloud, %APPDATA%\gcloud on
// Windows). A ~ that starts any of them is the home folder.
func vertexCredentialsFile(given string) (string, error) {
	if given != "" {
		return homePath(given)
	}
	if f, err := vertexEnvPath("GOOGLE_APPLICATION_CREDENTIALS"); f != "" || err != nil {
		return f, err
	}
	dir, err := gcloudDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "application_default_credentials.json"), nil
}

// gcloudDir is the folder gcloud keeps its files in: CLOUDSDK_CONFIG, else
// ~/.config/gcloud, %APPDATA%\gcloud on Windows.
func gcloudDir() (string, error) {
	if dir, err := vertexEnvPath("CLOUDSDK_CONFIG"); dir != "" || err != nil {
		return dir, err
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(appdir.Getenv("APPDATA"), "gcloud"), nil
	}
	home, err := appdir.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "gcloud"), nil
}

// vertexEnvPath is the path a variable of Google's names, "" when it is
// unset. It is read as set, not through appdir.Getenv, which takes one
// that isn't a full path as unset: here that is an error, since the file
// read in its place, gcloud's own, may be another account's.
func vertexEnvPath(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", nil
	}
	f, err := homePath(v)
	if err != nil {
		return "", err
	}
	if f == v && appdir.Getenv(name) == "" { // no ~, and not a full path
		return "", fmt.Errorf("%s is %q, not a full path: give the whole path, unset it, or give the provider a credentials file", name, v)
	}
	return f, nil
}

// homePath is p with a ~ that starts it made the home folder.
func homePath(p string) (string, error) {
	rest, ok := strings.CutPrefix(p, "~")
	if !ok || rest != "" && rest[0] != '/' && rest[0] != filepath.Separator {
		return p, nil
	}
	home, err := appdir.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}

// googleCreds is a credentials file as Google's tools write it: the
// fields of the kinds a token is minted from.
type googleCreds struct {
	Type string `json:"type"`
	// authorized_user
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	// service_account
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	// impersonated_service_account
	ImpersonationURL string          `json:"service_account_impersonation_url"`
	Delegates        []string        `json:"delegates"`
	Source           json.RawMessage `json:"source_credentials"`
	// a sovereign cloud's, when not googleapis.com
	UniverseDomain string `json:"universe_domain"`
}

// mintVertex mints a token from the credentials in file, traded for
// impersonate's when that is set.
func mintVertex(ctx context.Context, file, impersonate string) (string, time.Time, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("Google credentials: %w", err)
	}
	var c googleCreds
	if err := json.Unmarshal(b, &c); err != nil {
		return "", time.Time{}, fmt.Errorf("%s holds no Google credentials: %v", file, err)
	}
	tok, exp, err := c.mint(ctx, true)
	if err != nil {
		if errors.As(err, new(personRefused)) {
			err = fmt.Errorf("%w; %s", err, signInAgain(file, c.impersonated()))
		}
		return "", time.Time{}, fmt.Errorf("the Google credentials in %s: %w", file, err)
	}
	if impersonate != "" {
		return impersonateFor(ctx, tok, impersonate, nil)
	}
	return tok, exp, nil
}

// mint is a token from c. top is false for an impersonation's source,
// which is a person's credentials or a key, never another impersonation.
func (c googleCreds) mint(ctx context.Context, top bool) (string, time.Time, error) {
	if c.UniverseDomain != "" && c.UniverseDomain != "googleapis.com" {
		return "", time.Time{}, fmt.Errorf("they are for %s; Vertex AI is asked at googleapis.com", c.UniverseDomain)
	}
	switch c.Type {
	case "authorized_user":
		if c.RefreshToken == "" || c.ClientID == "" {
			return "", time.Time{}, errors.New("they have no refresh token")
		}
		tok, exp, err := tokenGrant(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID},
			"client_secret": {c.ClientSecret}, "refresh_token": {c.RefreshToken}})
		var r *tokenRefused
		if errors.As(err, &r) && r.status < 500 && r.status != http.StatusRequestTimeout && r.status != http.StatusTooManyRequests {
			// revoked, or expired by the organization's session length
			err = personRefused{err}
		}
		return tok, exp, err
	case "service_account":
		return c.signed(ctx)
	case "impersonated_service_account":
		if !top {
			return "", time.Time{}, errors.New("an impersonation's source is another impersonation")
		}
		account := c.impersonated()
		if account == "" {
			return "", time.Time{}, errors.New("their service_account_impersonation_url names no service account")
		}
		var src googleCreds
		if err := json.Unmarshal(c.Source, &src); err != nil || src.Type == "" {
			return "", time.Time{}, errors.New("their source_credentials can't be read")
		}
		tok, _, err := src.mint(ctx, false)
		if err != nil {
			return "", time.Time{}, err
		}
		return impersonateFor(ctx, tok, account, c.Delegates)
	case "external_account", "external_account_authorized_user":
		return "", time.Time{}, fmt.Errorf("workload identity federation (%s) isn't read: use a person's credentials, a service account key, or impersonation", c.Type)
	}
	return "", time.Time{}, fmt.Errorf("they are of type %q; authorized_user, service_account and impersonated_service_account are read", c.Type)
}

// impersonationURL is gcloud's …/serviceAccounts/<email>:generateAccessToken,
// whose email is asked for at IAM Credentials: the token never goes to the
// host the file names.
var impersonationURL = regexp.MustCompile(`/serviceAccounts/([^/:]+):generateAccessToken$`)

// impersonated is the service account an impersonated_service_account
// file's URL names, "" for another kind.
func (c googleCreds) impersonated() string {
	if c.Type != "impersonated_service_account" {
		return ""
	}
	m := impersonationURL.FindStringSubmatch(c.ImpersonationURL)
	if m == nil {
		return ""
	}
	account, _ := url.PathUnescape(m[1])
	return account
}

// personRefused is Google refusing a person's sign-in: revoked, or past the
// organization's session length. Signing in again mends it (signInAgain).
type personRefused struct{ error }

func (e personRefused) Unwrap() error { return e.error }

// legacyADC is the file gcloud keeps a signed-in account's credentials in
// for other tools, under its folder: legacy_credentials/<account>/adc.json.
var legacyADC = regexp.MustCompile(`^legacy_credentials/([^/]+)/adc\.json$`)

// signInAgain says how the person's sign-in in file is made anew: gcloud
// writes its own files again, as they were made (impersonating account,
// for an impersonation's); another file is the user's to make again.
// Signing in to gcloud alone would leave that one as it was.
func signInAgain(file, account string) string {
	dir, err := gcloudDir()
	rel, rerr := filepath.Rel(dir, file)
	if err != nil || rerr != nil {
		rel = ""
	}
	rel = filepath.ToSlash(rel)
	switch m := legacyADC.FindStringSubmatch(rel); {
	case rel == "application_default_credentials.json" && account != "":
		return "sign in again with `gcloud auth application-default login --impersonate-service-account=" + account + "`"
	case rel == "application_default_credentials.json":
		return "sign in again with `gcloud auth application-default login`"
	case m != nil:
		return "sign in again with `gcloud auth login " + m[1] + "`"
	}
	return "sign in again and write that file anew, or give the provider another credentials file"
}

// signed trades a service account key for a token with a JWT it signs
// (RFC 7523), as Google's libraries do.
func (c googleCreds) signed(ctx context.Context) (string, time.Time, error) {
	if c.ClientEmail == "" {
		return "", time.Time{}, errors.New("their client_email is missing")
	}
	block, _ := pem.Decode([]byte(c.PrivateKey))
	if block == nil {
		return "", time.Time{}, errors.New("their private_key is no PEM key")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, _ = k.(*rsa.PrivateKey)
	} else if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	}
	if key == nil {
		return "", time.Time{}, errors.New("their private_key is no RSA key")
	}
	// a little before now, for a clock ahead of Google's; an hour is the
	// most a JWT may run
	iat := time.Now().Add(-10 * time.Second)
	head := map[string]string{"alg": "RS256", "typ": "JWT"}
	if c.PrivateKeyID != "" {
		head["kid"] = c.PrivateKeyID
	}
	claims := map[string]any{"iss": c.ClientEmail, "scope": vertexScope, "aud": googleTokenURL,
		"iat": iat.Unix(), "exp": iat.Add(time.Hour).Unix()}
	h, _ := json.Marshal(head)
	cl, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(h) + "." + enc.EncodeToString(cl)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", time.Time{}, err
	}
	return tokenGrant(ctx, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion": {unsigned + "." + enc.EncodeToString(sig)}})
}

// tokenGrant asks Google's token endpoint for an access token.
func tokenGrant(ctx context.Context, form url.Values) (string, time.Time, error) {
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := postTokenAs(ctx, googleTokenURL, "application/x-www-form-urlencoded", "", []byte(form.Encode()), &out); err != nil {
		return "", time.Time{}, err
	}
	if out.AccessToken == "" {
		return "", time.Time{}, errors.New("Google gave no access token")
	}
	return out.AccessToken, time.Now().Add(time.Duration(cmp.Or(out.ExpiresIn, 3600)) * time.Second), nil
}

// impersonateFor trades tok for a token of the service account account at
// IAM Credentials, along delegates' chain when there is one.
func impersonateFor(ctx context.Context, tok, account string, delegates []string) (string, time.Time, error) {
	req := map[string]any{"scope": []string{vertexScope}, "lifetime": "3600s"}
	if len(delegates) > 0 {
		chain := make([]string, len(delegates))
		for i, d := range delegates {
			if !strings.HasPrefix(d, "projects/") {
				d = "projects/-/serviceAccounts/" + d
			}
			chain[i] = d
		}
		req["delegates"] = chain
	}
	body, _ := json.Marshal(req)
	var out struct {
		AccessToken string    `json:"accessToken"`
		ExpireTime  time.Time `json:"expireTime"`
	}
	u := googleIAMCredentials + "/projects/-/serviceAccounts/" + url.PathEscape(account) + ":generateAccessToken"
	if err := postTokenAs(ctx, u, "application/json", tok, body, &out); err != nil {
		var r *tokenRefused
		if errors.As(err, &r) && r.status == http.StatusForbidden {
			return "", time.Time{}, fmt.Errorf("impersonating %s: %w; the account signed in needs the Service Account Token Creator role (roles/iam.serviceAccountTokenCreator) on it", account, err)
		}
		return "", time.Time{}, fmt.Errorf("impersonating %s: %w", account, err)
	}
	if out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("impersonating %s: IAM Credentials gave no access token", account)
	}
	if out.ExpireTime.IsZero() {
		out.ExpireTime = time.Now().Add(time.Hour)
	}
	return out.AccessToken, out.ExpireTime, nil
}

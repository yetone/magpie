package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kiroFakeSignIn answers Kiro's auth service and AWS's sign-in, checking
// what the IDE would send; it returns the sign-in's page and a browser that
// doesn't follow redirects.
func kiroFakeSignIn(t *testing.T) (start func() (SignInState, url.Values), browser *http.Client) {
	t.Helper()
	kiroSandbox(t)
	var challenge, redirect string
	var awsChallenge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		str := func(k string) string { s, _ := in[k].(string); return s }
		verify := func(verifier, want string) bool {
			sum := sha256.Sum256([]byte(verifier))
			return base64.RawURLEncoding.EncodeToString(sum[:]) == want
		}
		switch r.URL.Path {
		case "/oauth/token": // Kiro's, for Google and GitHub
			tokens := map[string]string{"gcode": "social-at", "gcode2": "social-at-2"}
			if tokens[str("code")] == "" || !verify(str("code_verifier"), challenge) || str("redirect_uri") != redirect+"/oauth/callback?login_option=google" ||
				!strings.HasPrefix(r.Header.Get("User-Agent"), "KiroIDE-") {
				t.Errorf("Kiro token request %v", in)
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"accessToken": tokens[str("code")], "refreshToken": "social-rt", "profileArn": "arn:aws:codewhisperer:us-east-1:1:profile/P", "expiresIn": 3600})
		case "/client/register":
			if in["issuerUrl"] != "https://view.awsapps.com/start" || in["clientType"] != "public" {
				t.Errorf("register %v", in)
			}
			json.NewEncoder(w).Encode(map[string]any{"clientId": "cid", "clientSecret": "csecret", "clientSecretExpiresAt": time.Now().Add(90 * 24 * time.Hour).Unix()})
		case "/token": // AWS's
			if str("grantType") != "authorization_code" || str("code") != "awscode" || str("clientId") != "cid" || str("clientSecret") != "csecret" ||
				!verify(str("codeVerifier"), awsChallenge) || !strings.HasPrefix(str("redirectUri"), "http://127.0.0.1:") {
				t.Errorf("AWS token request %v", in)
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"accessToken": "idc-at", "refreshToken": "idc-rt", "expiresIn": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	auth, oidc, ports, ask := kiroAuthService, kiroOIDC, kiroCallbackPorts, askKiroIdentity
	kiroAuthService, kiroOIDC, kiroCallbackPorts = srv.URL, func(string) string { return srv.URL }, []int{0}
	askKiroIdentity = func(key, home string) (string, string) {
		c, _ := readKiroAt(key, home)
		switch c.access {
		case "social-at":
			return "me@example.com", "KIRO PRO"
		case "social-at-2":
			return "two@example.com", "KIRO PRO+"
		case "idc-at":
			return "builder@example.com", "KIRO FREE"
		case "cli-at":
			return "cli@example.com", "KIRO POWER"
		}
		return "", ""
	}
	t.Cleanup(func() { kiroAuthService, kiroOIDC, kiroCallbackPorts, askKiroIdentity = auth, oidc, ports, ask })
	browser = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start = func() (SignInState, url.Values) {
		st, err := StartSignIn("kiro")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { CancelSignIn(st.ID) })
		u, _ := url.Parse(st.URL)
		q := u.Query()
		if !strings.HasPrefix(st.URL, "https://app.kiro.dev/signin?") || q.Get("redirect_from") != "KiroIDE" || q.Get("code_challenge_method") != "S256" {
			t.Fatalf("page %s", st.URL)
		}
		challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
		return st, q
	}
	kiroSetAWSChallenge = func(c string) { awsChallenge = c }
	return start, browser
}

// kiroSetAWSChallenge tells the fake AWS the challenge its page was sent.
var kiroSetAWSChallenge func(string)

// kiroSignIn signs in on Kiro's page with Google, the code saying which
// account, and answers the sign-in's end.
func kiroSignIn(t *testing.T, start func() (SignInState, url.Values), browser *http.Client, code string) SignInState {
	t.Helper()
	st, q := start()
	res, err := browser.Get(q.Get("redirect_uri") + "/oauth/callback?login_option=google&code=" + code + "&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	st, _ = SignInStatus(st.ID)
	return st
}

// Signed in with Google on Kiro's page beside kiro-cli's sign-in, the
// account is magpie's own, in use behind kiro-cli's, which is left as it was.
func TestKiroSignInWithGoogle(t *testing.T) {
	start, browser := kiroFakeSignIn(t)
	writeKiroCLI(t, map[string]any{"kirocli:social:token": map[string]any{"access_token": "cli-at", "expires_at": "2099-01-01T00:00:00Z"}})
	kiroSaidNow("", "", "cli@example.com", "KIRO POWER")
	if st := kiroSignIn(t, start, browser, "gcode"); st.State != "done" || st.User != "me@example.com" || st.Plan != "KIRO PRO" || st.Using {
		t.Fatalf("status %+v", st)
	}
	ls := kiroLogins()
	if len(ls) != 2 || ls[0].User != "cli@example.com" || !ls[0].Active || ls[0].Home != "" ||
		ls[1].User != "me@example.com" || !ls[1].On || ls[1].Home == "" {
		t.Fatalf("logins %+v", ls)
	}
	c, ok := readKiroAt("", ls[1].Home)
	if !ok || c.access != "social-at" || c.refresh != "social-rt" || c.method != "social" || c.profile != "arn:aws:codewhisperer:us-east-1:1:profile/P" || !c.fresh() {
		t.Fatalf("cred %+v", c)
	}
	if row := readKiroRow(t, "kirocli:social:token"); row["access_token"] != "cli-at" {
		t.Fatalf("kiro-cli's sign-in changed: %v", row)
	}
	p, ok := kiroAccount()
	if !ok || p.Account.User != "cli@example.com" || p.Account.Home != "" {
		t.Fatalf("account %+v", p.Account)
	}
	if also := p.AlsoOn(); len(also) != 1 || also[0].Account.User != "me@example.com" || also[0].Account.Home != ls[1].Home || also[0].Account.Plan != "KIRO PRO" {
		t.Fatalf("also on %+v", also)
	}
}

// Kiro keeps several accounts of magpie's: the first signed in is used
// first while kiro-cli has none, another goes behind it, either can be put
// first, and one forgotten takes its home with it.
func TestKiroSeveralAccounts(t *testing.T) {
	start, browser := kiroFakeSignIn(t)
	if st := kiroSignIn(t, start, browser, "gcode"); st.State != "done" || !st.Using {
		t.Fatalf("first %+v", st)
	}
	if st := kiroSignIn(t, start, browser, "gcode2"); st.State != "done" || st.User != "two@example.com" || st.Using {
		t.Fatalf("second %+v", st)
	}
	ls := Logins("kiro")
	if len(ls) != 2 || ls[0].User != "me@example.com" || !ls[0].Active || ls[1].User != "two@example.com" || !ls[1].On {
		t.Fatalf("logins %+v", ls)
	}
	// signed in again, an account is not listed twice
	if st := kiroSignIn(t, start, browser, "gcode2"); st.State != "done" || len(Logins("kiro")) != 2 {
		t.Fatalf("again %+v %+v", st, Logins("kiro"))
	}
	if err := SwitchLogin("kiro", "two@example.com"); err != nil {
		t.Fatal(err)
	}
	p, _ := kiroAccount()
	if c, _ := readKiro(""); p.Account.User != "two@example.com" || c.access != "social-at-2" {
		t.Fatalf("first now %+v %+v", p.Account, c)
	}
	var home string
	for _, l := range kiroLogins() {
		if l.User == "me@example.com" {
			home = l.Home
		}
	}
	if err := ForgetLogin("kiro", "two@example.com"); err == nil {
		t.Fatal("forgot the account in use first")
	}
	if err := ForgetLogin("kiro", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) || len(Logins("kiro")) != 1 {
		t.Fatalf("home %s left: %v, %+v", home, err, Logins("kiro"))
	}
}

// The one account an earlier magpie signed in, kept alone beside
// logins.json, is taken among the rest; while Kiro can't say whose it is,
// it stays where it was.
func TestKiroAdoptsEarlierSignIn(t *testing.T) {
	kiroFakeSignIn(t)
	old := kiroLegacyPath()
	os.MkdirAll(filepath.Dir(old), 0o700)
	b, _ := json.Marshal(kiroSaved{AccessToken: "offline", RefreshToken: "r", ExpiresAt: "2099-01-01T00:00:00Z", AuthMethod: "social", Provider: "Google"})
	os.WriteFile(old, b, 0o600)
	if ls := kiroLogins(); len(ls) != 0 {
		t.Fatalf("logins %+v", ls)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("the sign-in was lost while Kiro couldn't be asked")
	}
	b, _ = json.Marshal(kiroSaved{AccessToken: "social-at", RefreshToken: "r", ExpiresAt: "2099-01-01T00:00:00Z", AuthMethod: "social", Provider: "Google"})
	os.WriteFile(old, b, 0o600)
	kiroAdopt.Lock()
	kiroAdopt.tried = time.Time{}
	kiroAdopt.Unlock()
	ls := kiroLogins()
	if len(ls) != 1 || ls[0].User != "me@example.com" || !ls[0].Active || ls[0].Home == "" {
		t.Fatalf("logins %+v", ls)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("the earlier file is still there")
	}
	if c, ok := readKiro(""); !ok || c.access != "social-at" {
		t.Fatalf("cred %+v", c)
	}
}

// Builder ID goes on from Kiro's page to AWS's, with a client registered
// for it, and comes back an AWS sign-in magpie can refresh.
func TestKiroSignInWithBuilderID(t *testing.T) {
	start, browser := kiroFakeSignIn(t)
	st, q := start()
	res, err := browser.Get(q.Get("redirect_uri") + "/signin/callback?login_option=builderid&issuer_url=" + url.QueryEscape("https://view.awsapps.com/start") +
		"&idc_region=us-east-1&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	a := loc.Query()
	if res.StatusCode != http.StatusFound || loc.Path != "/authorize" || a.Get("client_id") != "cid" || a.Get("response_type") != "code" ||
		!strings.Contains(a.Get("scopes"), "codewhisperer:conversations") || !strings.HasSuffix(a.Get("redirect_uri"), "/oauth/callback") {
		t.Fatalf("on to AWS: %d %s", res.StatusCode, loc)
	}
	kiroSetAWSChallenge(a.Get("code_challenge"))
	// a stale tab's answer is turned away
	if res, err = browser.Get(a.Get("redirect_uri") + "?code=awscode&state=nope"); err == nil {
		res.Body.Close()
	}
	if st, _ = SignInStatus(st.ID); st.State != "waiting" {
		t.Fatalf("a stranger's answer ended it: %+v", st)
	}
	if res, err = browser.Get(a.Get("redirect_uri") + "?code=awscode&state=" + url.QueryEscape(a.Get("state"))); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if st, _ = SignInStatus(st.ID); st.State != "done" || st.User != "builder@example.com" || !st.Using {
		t.Fatalf("status %+v", st)
	}
	c, ok := readKiro("")
	if !ok || c.access != "idc-at" || c.method != "idc" || c.clientID != "cid" || c.clientSecret != "csecret" || c.region != "us-east-1" {
		t.Fatalf("cred %+v", c)
	}
}

// A company's own identity provider is left to kiro-cli, and said so.
func TestKiroSignInExternalIdP(t *testing.T) {
	start, browser := kiroFakeSignIn(t)
	st, q := start()
	res, err := browser.Get(q.Get("redirect_uri") + "/oauth/callback?login_option=external_idp&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if st, _ = SignInStatus(st.ID); st.State != "failed" || !strings.Contains(st.Error, "kiro-cli login") {
		t.Fatalf("status %+v", st)
	}
}

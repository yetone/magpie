package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hostsServer stands in for github.com and any <name>.ghe.com at once: a
// request to https://<sub>.<host>/<path> arrives as /<sub>.<host>/<path>,
// github.com's as /github.com/... Every host's device flow hands over the
// token "gho_<host>", whose account is "mona" on every host.
func hostsServer(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		host = strings.TrimPrefix(host, "api.")
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
		switch path {
		case "login/device/code":
			json.NewEncoder(w).Encode(map[string]any{"device_code": "dc-" + host, "user_code": "ABCD-1234", "verification_uri": "https://" + host + "/login/device", "interval": 0})
		case "login/oauth/access_token":
			r.ParseForm()
			if r.Form.Get("device_code") != "dc-"+host {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_" + host})
		case "user":
			if auth != "gho_"+host {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"login": "mona"})
		case "copilot_internal/user":
			if auth != "gho_"+host {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"copilot_plan": map[bool]string{true: "individual", false: "enterprise"}[host == "github.com"]})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	oldOrigin := copilotGHEOrigin
	copilotGHEOrigin = func(host, sub string) string {
		if sub != "" {
			host = sub + "." + host
		}
		return srv.URL + "/" + host
	}
	oldDev, oldTok, oldCU, oldGU := gitHubDeviceURL, gitHubTokenURL, CopilotUserURL, GitHubUserURL
	gitHubDeviceURL, gitHubTokenURL = srv.URL+"/github.com/login/device/code", srv.URL+"/github.com/login/oauth/access_token"
	CopilotUserURL, GitHubUserURL = srv.URL+"/api.github.com/copilot_internal/user", srv.URL+"/api.github.com/user"
	t.Cleanup(func() {
		copilotGHEOrigin = oldOrigin
		gitHubDeviceURL, gitHubTokenURL, CopilotUserURL, GitHubUserURL = oldDev, oldTok, oldCU, oldGU
	})
}

func copilotSignInAt(t *testing.T, host string) SignInState {
	t.Helper()
	st, err := StartSignInAt("copilot", host)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err = WaitSignIn(ctx, st.ID)
	if err != nil || st.State != "done" {
		t.Fatalf("sign-in at %s: %+v %v", host, st, err)
	}
	return st
}

// copilotTokens is each listed Copilot account's token and host, by name.
func copilotTokens() map[string]string {
	out := map[string]string{}
	for _, c := range copilotLogins(copilotConfigDir()) {
		out[c.User] = c.app.Token + " " + c.app.Host
	}
	return out
}

func copilotNames() string {
	var s []string
	for _, l := range Logins("copilot") {
		s = append(s, l.User+map[bool]string{true: "*", false: ""}[l.Active])
	}
	return strings.Join(s, " ")
}

// One GitHub login on two enterprises' hosts is two accounts (#1220): both
// are kept, each with its own token, and one switched to or removed leaves
// the other as it was.
func TestCopilotSameLoginOnTwoHosts(t *testing.T) {
	signIn(t) // the editors' own: octocat on github.com
	hostsServer(t)

	if st := copilotSignInAt(t, "a.ghe.com"); st.User != "mona@a.ghe.com" || st.Plan != "Enterprise" || st.Using {
		t.Fatalf("a: %+v", st)
	}
	if st := copilotSignInAt(t, "b.ghe.com"); st.User != "mona@b.ghe.com" || st.Using {
		t.Fatalf("b: %+v", st)
	}
	if got := copilotNames(); got != "octocat* mona@a.ghe.com mona@b.ghe.com" {
		t.Fatalf("listed: %s", got)
	}
	toks := copilotTokens()
	if toks["mona@a.ghe.com"] != "gho_a.ghe.com a.ghe.com" || toks["mona@b.ghe.com"] != "gho_b.ghe.com b.ghe.com" {
		t.Fatalf("tokens: %v", toks)
	}
	// each is served as itself, by its own name
	p, _ := find(All(), "copilot")
	var also []string
	for _, a := range p.AlsoOn() {
		also = append(also, a.Account.User)
	}
	if strings.Join(also, " ") != "mona@a.ghe.com mona@b.ghe.com" {
		t.Fatalf("also on: %v", also)
	}
	if LoginID("copilot", "mona@a.ghe.com") == LoginID("copilot", "mona@b.ghe.com") {
		t.Fatal("two accounts share one routing id")
	}

	if err := SwitchLogin("copilot", "mona@b.ghe.com"); err != nil {
		t.Fatal(err)
	}
	if got := copilotNames(); got != "mona@b.ghe.com* mona@a.ghe.com octocat" {
		t.Fatalf("after switch: %s", got)
	}
	if err := SetLoginOn("copilot", "mona@a.ghe.com", false); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("copilot", "mona@a.ghe.com"); err != nil {
		t.Fatal(err)
	}
	if got := copilotNames(); got != "mona@b.ghe.com* octocat" {
		t.Fatalf("after forget: %s", got)
	}
	if toks := copilotTokens(); toks["mona@b.ghe.com"] != "gho_b.ghe.com b.ghe.com" {
		t.Fatalf("b's token after a was removed: %v", toks)
	}
	// signed in again on a, b is still left alone
	copilotSignInAt(t, "a.ghe.com")
	if err := ForgetLogin("copilot", "mona@b.ghe.com"); err != nil {
		t.Fatal(err)
	}
	if toks := copilotTokens(); toks["mona@a.ghe.com"] != "gho_a.ghe.com a.ghe.com" || toks["mona@b.ghe.com"] != "" {
		t.Fatalf("after b was removed: %v", toks)
	}
}

// The editors signed in to mona on github.com, mona on an enterprise's
// host signed in from magpie is another account, kept beside it, not
// dropped as the editors' own (#1220).
func TestCopilotOwnAndSameNamedGHE(t *testing.T) {
	home := signIn(t)
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "mona", "oauth_token": "gho_own"},
	})
	hostsServer(t)
	if got := copilotNames(); got != "mona*" {
		t.Fatalf("before: %s", got)
	}
	st := copilotSignInAt(t, "a.ghe.com")
	if st.User != "mona@a.ghe.com" || st.Using {
		t.Fatalf("signed in: %+v", st)
	}
	if got := copilotNames(); got != "mona* mona@a.ghe.com" {
		t.Fatalf("listed: %s", got)
	}
	if toks := copilotTokens(); toks["mona"] != "gho_own " || toks["mona@a.ghe.com"] != "gho_a.ghe.com a.ghe.com" {
		t.Fatalf("tokens: %v", toks)
	}
	// github.com's mona signed in from magpie is still the editors' own
	if st := copilotSignInAt(t, "github.com"); st.User != "mona" || !st.Using {
		t.Fatalf("github.com: %+v", st)
	}
	if got := copilotNames(); got != "mona* mona@a.ghe.com" {
		t.Fatalf("after github.com: %s", got)
	}
}

// A logins.json written before #1220 reads as it did: an enterprise's
// account saved under its login alone is named with its host and keeps its
// id and token, github.com's are unchanged, and a same-named account added
// on github.com stands beside it rather than over it.
func TestCopilotLoginsBeforeHosts(t *testing.T) {
	home := signIn(t)
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, "copilot-home"))
	old := `[
  {"id": "1111111111111111", "agent": "copilot", "user": "mona", "plan": "Enterprise", "seen": "2026-10-05T10:00:00Z", "on": true,
   "auth": {"host": "a.ghe.com", "oauth_token": "gho_a"}},
  {"id": "2222222222222222", "agent": "copilot", "user": "hubot", "plan": "Pro", "seen": "2026-10-05T10:00:00Z", "on": true,
   "auth": {"oauth_token": "gho_h"}}
]`
	if err := os.MkdirAll(filepath.Dir(loginsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loginsPath(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := copilotNames(); got != "mona@a.ghe.com* hubot" {
		t.Fatalf("listed: %s", got)
	}
	if LoginID("copilot", "mona@a.ghe.com") != "1111111111111111" || LoginID("copilot", "hubot") != "2222222222222222" {
		t.Fatalf("ids: %s %s", LoginID("copilot", "mona@a.ghe.com"), LoginID("copilot", "hubot"))
	}
	if err := addCopilotLogin("mona", "Pro", "gho_m", ""); err != nil {
		t.Fatal(err)
	}
	toks := copilotTokens()
	if toks["mona@a.ghe.com"] != "gho_a a.ghe.com" || toks["mona"] != "gho_m " || toks["hubot"] != "gho_h " {
		t.Fatalf("tokens: %v", toks)
	}
	if LoginID("copilot", "mona@a.ghe.com") != "1111111111111111" {
		t.Fatal("the enterprise account's id changed")
	}
}

// The editors' own account on an enterprise's host, remembered by its
// login alone before #1220, is the same account named with its host: still
// hidden when it was removed in magpie, with its id.
func TestCopilotOwnGHERenamed(t *testing.T) {
	home := signIn(t)
	t.Setenv("COPILOT_HOME", filepath.Join(home, "copilot-home"))
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"a.ghe.com:Iv1.x": map[string]any{"user": "mona", "oauth_token": "gho_own"},
	})
	old := `[{"id": "3333333333333333", "agent": "copilot", "user": "mona", "plan": "Enterprise", "seen": "2026-10-05T10:00:00Z", "auth": null, "hidden": "hidden"}]`
	os.MkdirAll(filepath.Dir(loginsPath()), 0o700)
	if err := os.WriteFile(loginsPath(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := copilotNames(); got != "" {
		t.Fatalf("a hidden account came back: %s", got)
	}
	var own []savedLogin
	for _, l := range readLogins() {
		if l.Agent == "copilot" {
			own = append(own, l)
		}
	}
	if len(own) != 1 || own[0].User != "mona@a.ghe.com" || own[0].ID != "3333333333333333" || own[0].Hidden == "" || own[0].Plan != "Enterprise" {
		t.Fatalf("own: %+v", own)
	}
	// its plan, read again, is kept on it
	refreshCopilotEntitlement(copilotApp{User: "mona@a.ghe.com", Token: "gho_own", Host: "a.ghe.com"}, "Business", "")
	for _, l := range readLogins() {
		if l.Agent == "copilot" && l.Plan != "Business" {
			t.Fatalf("plan not kept: %+v", l)
		}
	}
}

// oldGHEFiles writes logins.json as magpie kept it before #1220 — mona
// on a.ghe.com saved under her login alone — and providers.json with
// settings, data, under that login.
func oldGHEFiles(t *testing.T, providers string) {
	t.Helper()
	home := signIn(t)
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, "copilot-home"))
	renameFailedAt = time.Time{}
	old := `[
  {"id": "1111111111111111", "agent": "copilot", "user": "mona", "plan": "Enterprise", "seen": "2026-10-05T10:00:00Z", "on": true,
   "auth": {"host": "a.ghe.com", "oauth_token": "gho_a"}},
  {"agent": "copilot", "user": "hubot", "plan": "Pro", "seen": "2026-10-05T10:00:00Z", "on": true,
   "auth": {"oauth_token": "gho_h"}}
]`
	if err := os.MkdirAll(filepath.Dir(loginsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loginsPath(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(providers), 0o600); err != nil {
		t.Fatal(err)
	}
}

func savedCopilotUsers(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(loginsPath())
	if err != nil {
		t.Fatal(err)
	}
	var ls []savedLogin
	if err := json.Unmarshal(b, &ls); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range ls {
		out = append(out, l.User)
	}
	return strings.Join(out, " ")
}

// An enterprise account's proxy, cap, models and concurrency, saved under
// its login alone before #1220, are its own still once it is named with
// its host; a github.com account of that login added after has none of
// them (#1220).
func TestCopilotRenameKeepsSettings(t *testing.T) {
	oldGHEFiles(t, `{"providers": [{"id": "copilot",
  "accountProxies": {"mona": "http://127.0.0.1:9"},
  "accountCaps": {"mona": 70},
  "accountModels": {"mona": ["gpt-5"]},
  "accountConcurrency": {"mona": 2, "hubot": 3}}]}`)
	hubotID := LoginID("copilot", "hubot")
	if got := copilotNames(); got != "mona@a.ghe.com* hubot" {
		t.Fatalf("listed: %s", got)
	}
	if got := savedCopilotUsers(t); got != "hubot mona@a.ghe.com" {
		t.Fatalf("logins.json: %s", got)
	}
	if got := ProxyOfLogin("copilot", "mona@a.ghe.com"); got != "http://127.0.0.1:9" {
		t.Fatalf("proxy: %q", got)
	}
	if got := AccountCapOf("copilot", "mona@a.ghe.com"); got != 70 {
		t.Fatalf("cap: %d", got)
	}
	p, _ := storedPicks("copilot")
	if got := p.AccountModels["mona@a.ghe.com"]; len(got) != 1 || got[0] != "gpt-5" {
		t.Fatalf("models: %v", p.AccountModels)
	}
	if p.AccountConcurrency["mona@a.ghe.com"] != 2 || p.AccountConcurrency["hubot"] != 3 {
		t.Fatalf("concurrency: %v", p.AccountConcurrency)
	}
	if LoginID("copilot", "mona@a.ghe.com") != "1111111111111111" || LoginID("copilot", "hubot") != hubotID {
		t.Fatal("an account's id changed")
	}
	if err := addCopilotLogin("mona", "Pro", "gho_m", ""); err != nil {
		t.Fatal(err)
	}
	if got := ProxyOfLogin("copilot", "mona"); got != "" {
		t.Fatalf("github.com's mona took the enterprise one's proxy: %q", got)
	}
	if got := AccountCapOf("copilot", "mona"); got != 0 {
		t.Fatalf("github.com's mona took the enterprise one's cap: %d", got)
	}
	if got := AccountCapOf("copilot", "mona@a.ghe.com"); got != 70 {
		t.Fatalf("cap after adding github.com's mona: %d", got)
	}
}

// The editors' own enterprise account, renamed with its host, keeps its
// cap too.
func TestCopilotOwnGHERenameKeepsSettings(t *testing.T) {
	home := signIn(t)
	t.Setenv("COPILOT_HOME", filepath.Join(home, "copilot-home"))
	renameFailedAt = time.Time{}
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"a.ghe.com:Iv1.x": map[string]any{"user": "mona", "oauth_token": "gho_own"},
	})
	os.MkdirAll(filepath.Dir(loginsPath()), 0o700)
	if err := os.WriteFile(loginsPath(), []byte(`[{"id": "3333333333333333", "agent": "copilot", "user": "mona", "seen": "2026-10-05T10:00:00Z", "auth": null}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"providers": [{"id": "copilot", "accountCaps": {"mona": 60}, "accountProxies": {"mona": "direct"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := copilotNames(); got != "mona@a.ghe.com*" {
		t.Fatalf("listed: %s", got)
	}
	if AccountCapOf("copilot", "mona@a.ghe.com") != 60 || ProxyOfLogin("copilot", "mona@a.ghe.com") != "direct" {
		t.Fatalf("settings: cap %d proxy %q", AccountCapOf("copilot", "mona@a.ghe.com"), ProxyOfLogin("copilot", "mona@a.ghe.com"))
	}
	if got := savedCopilotUsers(t); got != "mona@a.ghe.com" {
		t.Fatalf("logins.json: %s", got)
	}
}

// A providers.json that can't be read is not one without settings: the
// account isn't written under its new name, its settings stranded under
// the old, until they can be moved.
func TestCopilotRenameWaitsForSettings(t *testing.T) {
	oldGHEFiles(t, `{"providers": [`)
	if got := copilotNames(); got != "mona@a.ghe.com* hubot" {
		t.Fatalf("listed: %s", got)
	}
	if got := savedCopilotUsers(t); got != "hubot mona" {
		t.Fatalf("logins.json renamed without its settings: %s", got)
	}
	// an account written meanwhile keeps the old name on disk too
	if err := addCopilotLogin("octocat", "Pro", "gho_o", ""); err != nil {
		t.Fatal(err)
	}
	if got := savedCopilotUsers(t); !strings.Contains(got, "mona ") && !strings.HasSuffix(got, "mona") {
		t.Fatalf("logins.json renamed without its settings: %s", got)
	}
	if b, _ := os.ReadFile(Path()); string(b) != `{"providers": [` {
		t.Fatalf("providers.json written over: %s", b)
	}
	if err := os.WriteFile(Path(), []byte(`{"providers": [{"id": "copilot", "accountProxies": {"mona": "http://127.0.0.1:9"}, "accountCaps": {"mona": 70}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	renameFailedAt = time.Time{}
	copilotNames()
	if ProxyOfLogin("copilot", "mona@a.ghe.com") != "http://127.0.0.1:9" || AccountCapOf("copilot", "mona@a.ghe.com") != 70 {
		t.Fatalf("settings not moved once readable: %+v", load().Providers)
	}
	if got := savedCopilotUsers(t); !strings.Contains(got, "mona@a.ghe.com") {
		t.Fatalf("logins.json: %s", got)
	}
}

// A setting the new name has already is the one kept.
func TestCopilotRenameKeepsNewNamesSetting(t *testing.T) {
	oldGHEFiles(t, `{"providers": [{"id": "copilot", "accountCaps": {"mona": 70, "mona@a.ghe.com": 40}, "accountProxies": {"mona": "direct"}}]}`)
	copilotNames()
	if AccountCapOf("copilot", "mona@a.ghe.com") != 40 || ProxyOfLogin("copilot", "mona@a.ghe.com") != "direct" {
		t.Fatalf("settings: %+v", load().Providers)
	}
}

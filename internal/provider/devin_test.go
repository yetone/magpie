package provider

import (
	"encoding/json"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/testenv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseDevinStatus(t *testing.T) {
	user, plan, ok := parseDevinStatus(`Logged in (via Devin).

Credentials:
  File:              /home/u/.local/share/devin/credentials.toml
  API server:        https://server.codeium.com

User:
  Name:              someuser
  Email:             someuser@example.com
  User ID:           user-123

Account:
  Tier:              Devin Pro
  Plan:              Pro
  Enterprise:        no
`)
	if !ok || user != "someuser@example.com" || plan != "Devin Pro" {
		t.Fatalf("user=%q plan=%q ok=%v", user, plan, ok)
	}
	if _, _, ok := parseDevinStatus("Not logged in."); ok {
		t.Fatal("a signed-out status parsed as signed in")
	}
	// no email field: the handle still identifies the account
	user, _, ok = parseDevinStatus("Logged in (via Devin).\n\nUser:\n  Name:  handle\n")
	if !ok || user != "handle" {
		t.Fatalf("handle fallback: %q %v", user, ok)
	}
}

func TestParseDevinModels(t *testing.T) {
	families := parseDevinModels([]byte(`{"families":[
		{"family_label":"SWE-2","family_uid":"swe-2","slug":"swe-2","aliases":["swe"],
		 "variants":[{"model_uid":"swe-2-max","label":"SWE-2 Max","max_context_tokens":262000,"max_output_tokens":128000},
		             {"model_uid":"swe-2-min","label":"SWE-2 Min"}]},
		{"family_label":"Claude Opus 5.5","family_uid":"claude-opus-5-5","slug":"claude-opus-5.5","aliases":["opus"],
		 "variants":[{"model_uid":"claude-opus-5-5-high","label":"Claude Opus 5.5 High","max_context_tokens":1000000,"max_output_tokens":128000}]},
		{"family_label":"No Numbers","family_uid":"no-numbers","slug":"no-numbers",
		 "variants":[{"model_uid":"no-numbers-high","label":"No Numbers High"}]}
	]}`))
	if len(families) != 3 {
		t.Fatalf("families: %v", families)
	}
	f := families[0]
	if f.UID != "swe-2" || f.Label != "SWE-2" || len(f.Models) != 2 || f.Models[0].ID != "swe-2-max" {
		t.Fatalf("family: %+v", f)
	}
	// Devin's own numbers, which its list gives each variant
	if f.Models[0].Context != 262000 || f.Models[0].Output != 128000 {
		t.Fatalf("a variant's numbers: %+v", f.Models[0])
	}
	flat := devinModelsFlatten(families)
	if len(flat) != 7 || flat[0].ID != "swe-2" || flat[1].ID != "swe-2-max" || flat[4].ID != "claude-opus-5-5-high" {
		t.Fatalf("flat: %v", flat)
	}
	// the family takes the numbers of the variant its id follows, a variant
	// without numbers takes its family's (swe-2-min), and models.dev is left
	// for the families Devin gave none for (so swe-2's 262K is used at all)
	for _, want := range []struct {
		id            string
		context, most int
	}{{"swe-2", 262000, 128000}, {"swe-2-max", 262000, 128000}, {"swe-2-min", 262000, 128000},
		{"claude-opus-5-5", 1000000, 128000}, {"claude-opus-5-5-high", 1000000, 128000}} {
		var got *catalog.Model
		for i := range flat {
			if flat[i].ID == want.id {
				got = &flat[i]
			}
		}
		if got == nil || got.Context != want.context || got.Output != want.most {
			t.Fatalf("%s: got %+v, want %d/%d", want.id, got, want.context, want.most)
		}
	}
	if w := catalog.ContextOf("no-numbers"); w > 0 && flat[5].Context != w {
		t.Fatalf("a family Devin didn't size takes models.dev's %d: %+v", w, flat[5])
	}
	for _, m := range flat {
		if m.Provider != "devin" {
			t.Fatalf("provider: %v", m)
		}
	}
	if parseDevinModels([]byte("not json")) != nil || parseDevinModels([]byte(`{"families":[]}`)) != nil {
		t.Fatal("bad payloads must not parse")
	}
}

func TestWithDevinContexts(t *testing.T) {
	// the CLI list last read is the truth for what it names: models.dev has no
	// swe-2, and its glm-5.2 (1M) isn't the 200K Devin serves
	devinFamiliesCached([]DevinFamily{
		{UID: "swe-2", Label: "SWE-2", Models: []catalog.Model{{ID: "swe-2-high", Context: 262000, Output: 128000}}},
		{UID: "glm-5.2", Label: "GLM-5.2", Models: []catalog.Model{
			{ID: "glm-5-2", Context: 200000, Output: 128000},
			{ID: "glm-5-2-1m", Context: 1000000, Output: 128000},
		}},
	})
	t.Cleanup(func() { devinFamiliesCached(nil) })
	saved := withDevinContexts([]catalog.Model{
		{ID: "glm-5-2", Context: 1000000, Output: 131072}, // an older magpie's models.dev numbers
		{ID: "glm-5-2-1m"},
		{ID: "swe-2-high"},
		{ID: "glm-5.2"},
		{ID: "nobody-high"},
	})
	if saved[0].Context != 200000 || saved[0].Output != 128000 {
		t.Fatalf("Devin's numbers over models.dev's: %+v", saved[0])
	}
	if saved[1].Context != 1000000 || saved[1].Output != 128000 {
		t.Fatalf("a variant's own numbers: %+v", saved[1])
	}
	if saved[2].Context != 262000 || saved[2].Output != 128000 {
		t.Fatalf("a model models.dev doesn't have: %+v", saved[2])
	}
	if saved[3].Context != 200000 || saved[3].Output != 128000 {
		t.Fatalf("a family id: %+v", saved[3])
	}
	if saved[4].Context != 0 || saved[4].Output != 0 {
		t.Fatalf("a model nobody names stays unset: %+v", saved[4])
	}
	// what Devin's list doesn't name (once it has been read) keeps models.dev's
	if w := catalog.ContextOf("claude-opus-5-5"); w > 0 {
		if m := withDevinContexts([]catalog.Model{{ID: "claude-opus-5-5-high-fast"}})[0]; m.Context != w {
			t.Fatalf("models.dev fallback: %+v", m)
		}
	}
}

func TestDevinCredentialsPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG paths are unix")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	got := DevinCredentialsPath()
	if got != filepath.Join(home, "data", "devin", "credentials.toml") {
		t.Fatalf("path: %q", got)
	}
	t.Setenv("XDG_DATA_HOME", "")
	if got := DevinCredentialsPath(); got != filepath.Join(home, ".local", "share", "devin", "credentials.toml") {
		t.Fatalf("default path: %q", got)
	}
	_ = os.Getenv("HOME")
}

func TestDevinCredentials(t *testing.T) {
	b := string(devinCredentials(`k"ey`, "", "", ""))
	for _, want := range []string{
		`windsurf_api_key = "k\"ey"`,
		`api_server_url = "https://server.codeium.com"`,
		`devin_webapp_host = "app.devin.ai"`,
		`devin_api_url = "https://api.devin.ai"`,
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("credentials %s", b)
		}
	}
}

func TestDevinSignIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake devin is a shell script")
	}
	home := claudeHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	// a CLI that answers `auth status` once the exchange wrote its file
	exe := filepath.Join(home, "devin")
	testenv.Program(t, exe, "#!/bin/sh\ncat <<'X'\nLogged in (via Devin).\n\nUser:\n  Email:             dev@example.com\n\nAccount:\n  Tier:              Devin Pro\nX\n")
	oldExe := DevinExecutable
	DevinExecutable = func() string { return exe }
	t.Cleanup(func() { DevinExecutable = oldExe })

	var gotCode, gotVerifier, gotRedirect string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		gotCode, gotVerifier, gotRedirect = body["code"], body["code_verifier"], body["redirect_uri"]
		json.NewEncoder(w).Encode(map[string]any{
			"sessionToken":    "devin-session-token$sk-test",
			"devinWebappHost": "app.devin.ai", "devinApiUrl": "https://api.devin.ai",
		})
	}))
	defer fake.Close()
	oldTok := devinExchangeURL
	devinExchangeURL = fake.URL
	t.Cleanup(func() { devinExchangeURL = oldTok })

	st, err := StartSignIn("devin")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	if u.Host != "app.devin.ai" || u.Path != "/auth/cli/continue" {
		t.Fatalf("url %s", st.URL)
	}
	q := u.Query()
	if q.Get("prompt") != "select_account" || q.Get("code_challenge") == "" ||
		q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") == "" || q.Get("state") == "" {
		t.Fatalf("params %s", st.URL)
	}
	page := finishInBrowser(t, st, "dv-code")
	if !strings.Contains(page, "signed in") {
		t.Fatalf("page %s", page)
	}
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "dev@example.com" || st.Plan != "Devin Pro" || !st.Using {
		t.Fatalf("state %+v", st)
	}
	if gotCode != "dv-code" || gotVerifier == "" || gotRedirect == "" {
		t.Fatalf("exchange code=%q verifier=%q redirect=%q", gotCode, gotVerifier, gotRedirect)
	}
	// credentials.toml as `devin auth login` would write it
	b, err := os.ReadFile(DevinCredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`windsurf_api_key = "devin-session-token$sk-test"`, `api_server_url = "https://server.codeium.com"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("credentials %s", b)
		}
	}
	// the CLI's own account, the one in use
	if ls := Logins("devin"); len(ls) != 1 || ls[0].User != "dev@example.com" || !ls[0].Active {
		t.Fatalf("logins %v", ls)
	}
}

// TestDevinSeveralAccounts signs a second Devin account in beside the CLI's
// own: it gets a home of magpie's, the CLI's file is left alone, and the
// gateway can use either.
func TestDevinSeveralAccounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake devin is a shell script")
	}
	home := claudeHome(t)
	data := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", data)
	os.MkdirAll(filepath.Join(data, "devin"), 0o700)
	own := string(devinCredentials("devin-session-token$own", "", "", ""))
	os.WriteFile(filepath.Join(data, "devin", "credentials.toml"), []byte(own), 0o600)

	// a CLI that says whose key is in the credentials.toml of its data folder
	exe := filepath.Join(home, "devin")
	testenv.Program(t, exe, `#!/bin/sh
f="$XDG_DATA_HOME/devin/credentials.toml"
[ -f "$f" ] || { echo "Not logged in"; exit 1; }
if grep -q own "$f"; then who=dev@example.com; tier="Devin Pro"; else who=two@example.com; tier="Devin Max"; fi
printf 'Logged in (via Devin).

User:
  Email:             %s

Account:
  Tier:              %s
' "$who" "$tier"
`)
	oldExe := DevinExecutable
	DevinExecutable = func() string { return exe }
	t.Cleanup(func() { DevinExecutable = oldExe })
	forgetDevinStatus()
	t.Cleanup(forgetDevinStatus)

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"sessionToken":    "devin-session-token$two",
			"devinWebappHost": "app.devin.ai", "devinApiUrl": "https://api.devin.ai",
		})
	}))
	defer fake.Close()
	oldTok := devinExchangeURL
	devinExchangeURL = fake.URL
	t.Cleanup(func() { devinExchangeURL = oldTok })

	signIn := func() SignInState {
		st, err := StartSignIn("devin")
		if err != nil {
			t.Fatal(err)
		}
		finishInBrowser(t, st, "dv-code")
		return waitDone(t, st.ID)
	}
	if st := signIn(); st.State != "done" || st.User != "two@example.com" || st.Plan != "Devin Max" || st.Using {
		t.Fatalf("state %+v", st)
	}
	// the CLI's own sign-in is as it was
	if b, _ := os.ReadFile(DevinCredentialsPath()); string(b) != own {
		t.Fatalf("CLI credentials %s", b)
	}
	ls := devinLogins()
	if len(ls) != 2 || ls[0].User != "dev@example.com" || ls[0].Home != "" || !ls[0].Active ||
		ls[1].User != "two@example.com" || ls[1].Home == "" || !ls[1].On {
		t.Fatalf("logins %+v", ls)
	}
	if key, _, err := DevinAuthAt(ls[1].Home); err != nil || key != "devin-session-token$two" {
		t.Fatalf("second key %q %v", key, err)
	}
	p, ok := devinAccount()
	if !ok || p.Account.User != "dev@example.com" || p.Account.Plan != "Devin Pro" {
		t.Fatalf("account %+v", p.Account)
	}
	also := p.AlsoOn()
	if len(also) != 1 || also[0].Account.User != "two@example.com" || also[0].Account.Home != ls[1].Home {
		t.Fatalf("also on %+v", also)
	}

	// signed in again, it is still the one account, in its newer home
	first := ls[1].Home
	signIn()
	if ls = devinLogins(); len(ls) != 2 || ls[1].Home == first {
		t.Fatalf("again %+v", ls)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("old home kept: %v", err)
	}

	// switched, the gateway uses it first
	if err := SwitchLogin("devin", "two@example.com"); err != nil {
		t.Fatal(err)
	}
	if p, _ := devinAccount(); p.Account.User != "two@example.com" || p.Account.Home != ls[1].Home {
		t.Fatalf("switched %+v", p.Account)
	}
	if err := SwitchLogin("devin", "dev@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("devin", "two@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ls[1].Home); !os.IsNotExist(err) {
		t.Fatalf("home kept: %v", err)
	}
	if ls = devinLogins(); len(ls) != 1 || ls[0].User != "dev@example.com" {
		t.Fatalf("after forget %+v", ls)
	}
}

// devinSigned is the CLI's own report, as `devin auth status` prints it.
const devinSigned = `printf 'Logged in (via Devin).\n\nUser:\n  Email:             dev@example.com\n\nAccount:\n  Tier:              Devin Pro\n'`

// devinKeyRefused is what a real CLI prints for a token Devin's servers
// refused, a revoked or expired one: it exits 0, the report still begins
// `Logged in`, and there is no account in it.
const devinKeyRefused = `printf '%s\n' 'Logged in (via Devin).' '' 'User / team info:' '  Failed to fetch from server: Authentication required: failed to get primary API key; try logging out and logging in again: failed to validate Devin token: Invalid token (trace ID: 5f0c1d2e)'`

// devinUnreachable is the same report with Devin's servers unreachable,
// which must not drop the account.
const devinUnreachable = `printf '%s\n' 'Logged in (via Devin).' '' 'User / team info:' '  Failed to fetch from server: Connection failed: Connect HTTP error: connect: connection refused'`

// fakeDevin points DevinExecutable at a fake CLI of this test's, and puts
// the CLI and the identity back when it ends: an ask that couldn't tell now
// leaves the last account served, so a test after this one must not find
// either.
// shellFakes skips a test whose fake CLI is a shell script, which Windows
// can't run.
func shellFakes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a shell script")
	}
}

func fakeDevin(t *testing.T, exe string) {
	t.Helper()
	shellFakes(t)
	old := DevinExecutable
	DevinExecutable = func() string { return exe }
	t.Cleanup(func() {
		DevinExecutable = old
		devinStatus.Lock()
		devinStatus.user, devinStatus.plan, devinStatus.ok = "", "", false
		devinStatus.Unlock()
		forgetDevinStatus()
	})
}

// a `devin auth status` that fails, runs out of time (it asks Devin's
// servers) or prints something else couldn't tell, and the account stays as
// it was, where it had dropped Devin from the Providers page and routing
// (#154): only a CLI that says nobody is signed in, or keeps no
// credentials.toml, is sure of nobody.
func TestAskDevinStatus(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	exe := filepath.Join(home, "devin")
	fakeDevin(t, exe)

	for _, c := range []struct {
		name, script string
		creds        bool // the CLI keeps a credentials.toml
		user         string
		sure         bool // the CLI is sure of nobody, not merely unable to tell
	}{
		{"signed in", devinSigned, true, "dev@example.com", true},
		{"says nobody", `echo 'Not logged in.'`, false, "", true},
		{"says nobody, a key left behind", `echo 'Not logged in. Please run auth login first.'`, true, "", true},
		{"the key was refused", devinKeyRefused, true, "", true},
		{"Devin's servers unreachable", devinUnreachable, true, "", false},
		{"fails", `echo 'fetch failed' >&2; exit 1`, true, "", false},
		{"fails, no key", `exit 1`, false, "", true},
		{"prints something else", `echo 'Something went wrong'`, true, "", false},
	} {
		creds := DevinCredentialsPath()
		if c.creds {
			if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(creds, devinCredentials("devin-session-token$k", "", "", ""), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(creds); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		testenv.Program(t, exe, "#!/bin/sh\n"+c.script+"\n")
		u, _, ok, err := askDevinStatus()
		if u != c.user || ok != (c.user != "") || (err == nil) != c.sure {
			t.Errorf("%s: %q %v %v", c.name, u, ok, err)
		}
	}
}

// the identity magpie serves: an ask that fails keeps the account, and one
// that says nobody (or keeps no key) drops it, on disk too
func TestDevinStatusKeepsTheAccount(t *testing.T) {
	home := claudeHome(t)
	keepingIdentities(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	exe := filepath.Join(home, "devin")
	fakeDevin(t, exe)
	creds := DevinCredentialsPath()
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, devinCredentials("devin-session-token$k", "", "", ""), 0o600); err != nil {
		t.Fatal(err)
	}

	// signed in once, then asked again behind what is served
	ask := func(script string) {
		t.Helper()
		testenv.Program(t, exe, "#!/bin/sh\n"+script+"\n")
		devinStatus.Lock()
		devinStatus.at = time.Now().Add(-2 * time.Minute)
		done := devinStatus.refresh()
		devinStatus.Unlock()
		<-done
	}

	ask(devinSigned)
	if u, p, ok := devinStatus.get(); !ok || u != "dev@example.com" || p != "Devin Pro" {
		t.Fatalf("never signed in: %q %q %v", u, p, ok)
	}
	if k := readIdentities()["devin"]; !k.OK || k.User != "dev@example.com" {
		t.Fatalf("signed in, not kept: %+v", k)
	}

	ask(`echo 'fetch failed' >&2; exit 1`)
	if u, p, ok := devinStatus.get(); !ok || u != "dev@example.com" || p != "Devin Pro" {
		t.Fatalf("a failed ask dropped the account: %q %q %v", u, p, ok)
	}
	if k := readIdentities()["devin"]; !k.OK || k.User != "dev@example.com" {
		t.Fatalf("a failed ask was kept: %+v", k)
	}

	ask(`echo 'Not logged in.'`)
	if _, _, ok := devinStatus.get(); ok {
		t.Fatal("signed out, still served")
	}
	if k := readIdentities()["devin"]; k.OK {
		t.Fatalf("signed out, still kept: %+v", k)
	}

	// a token Devin's servers refused is signed out too, kept on disk as
	// such: magpie must not go on routing to a dead key
	ask(devinSigned)
	if _, _, ok := devinStatus.get(); !ok {
		t.Fatal("signed in again, not served")
	}
	ask(devinKeyRefused)
	if _, _, ok := devinStatus.get(); ok {
		t.Fatal("a refused key, still served")
	}
	if k := readIdentities()["devin"]; k.OK {
		t.Fatalf("a refused key, still kept: %+v", k)
	}

	// Devin's servers unreachable is not that: the account stays
	ask(devinSigned)
	if _, _, ok := devinStatus.get(); !ok {
		t.Fatal("signed in again, not served")
	}
	ask(devinUnreachable)
	if _, _, ok := devinStatus.get(); !ok {
		t.Fatal("Devin's servers unreachable dropped the account")
	}
}

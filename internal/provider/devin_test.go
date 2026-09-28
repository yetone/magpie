package provider

import (
	"encoding/json"
	"github.com/yetone/magpie/internal/catalog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	home := claudeHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	// a CLI that answers `auth status` once the exchange wrote its file
	exe := filepath.Join(home, "devin")
	os.WriteFile(exe, []byte("#!/bin/sh\ncat <<'X'\nLogged in (via Devin).\n\nUser:\n  Email:             dev@example.com\n\nAccount:\n  Tier:              Devin Pro\nX\n"), 0o755)
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
	// devin keeps the one account it is signed in to: nothing beside it
	if ls := Logins("devin"); len(ls) != 0 {
		t.Fatalf("logins %v", ls)
	}
}

// `devin auth status` failing, or saying neither, is a CLI that didn't
// answer; "Not logged in." is one that did
func TestAskDevinIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the CLI is a shell script here")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "devin")
	oldExe := DevinExecutable
	DevinExecutable = func() string { return exe }
	t.Cleanup(func() { DevinExecutable = oldExe })
	for _, c := range []struct {
		name, script string
		ok, answered bool
	}{
		{"signed in", "cat <<'X'\nLogged in (via Devin).\n\nUser:\n  Email:  dev@example.com\nX\n", true, true},
		{"signed out", "echo 'Not logged in.'\n", false, true},
		{"failed", "echo 'network error' >&2; exit 1\n", false, false},
		{"neither", "echo 'Updating devin…'\n", false, false},
	} {
		os.WriteFile(exe, []byte("#!/bin/sh\n"+c.script), 0o755)
		_, _, ok, err := askDevinIdentity()
		if ok != c.ok || (err == nil) != c.answered {
			t.Errorf("%s: ok=%v err=%v", c.name, ok, err)
		}
	}
}

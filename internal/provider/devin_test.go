package provider

import (
	"encoding/json"
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
		 "variants":[{"model_uid":"swe-2-max","label":"SWE-2 Max"},{"model_uid":"swe-2-min","label":"SWE-2 Min"}]},
		{"family_label":"Claude Opus 5.5","family_uid":"claude-opus-5-5","slug":"claude-opus-5.5","aliases":["opus"],
		 "variants":[{"model_uid":"claude-opus-5-5-high","label":"Claude Opus 5.5 High"}]}
	]}`))
	if len(families) != 2 {
		t.Fatalf("families: %v", families)
	}
	f := families[0]
	if f.UID != "swe-2" || f.Label != "SWE-2" || len(f.Models) != 2 || f.Models[0].ID != "swe-2-max" {
		t.Fatalf("family: %+v", f)
	}
	flat := devinModelsFlatten(families)
	if len(flat) != 5 || flat[0].ID != "swe-2" || flat[1].ID != "swe-2-max" || flat[4].ID != "claude-opus-5-5-high" {
		t.Fatalf("flat: %v", flat)
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

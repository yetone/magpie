package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseGrokModels(t *testing.T) {
	b := []byte(`{"object":"list","data":[
		{"id":"grok-4.7","name":"Grok 4.7","context_window":500000,"api_backend":"responses","reasoning_efforts":[{"value":"xhigh"},{"value":"high"},{"value":"medium"},{"value":"low"}]},
		{"id":"grok-4.7-build-fast","context_window":256000,"api_backend":"responses"},
		{"id":"grok-old","api_backend":"chat_completions"}]}`)
	ms := parseGrokModels(b)
	if len(ms) != 2 || ms[0].ID != "grok-4.7" || ms[0].Name != "Grok 4.7" || ms[0].Context != 500000 ||
		strings.Join(ms[0].Efforts, ",") != "low,medium,high,xhigh" || ms[1].Name != "grok-4.7-build-fast" || ms[1].Efforts != nil {
		t.Fatalf("models = %+v", ms)
	}
}

// A request is signed with the sign-in of its account's home, as the CLI
// signs its own.
func TestGrokSigns(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"data":[{"id":"grok-4.7","api_backend":"responses"}]}`))
	}))
	defer up.Close()
	base := GrokBase
	GrokBase = up.URL
	defer func() { GrokBase = base }()
	home := t.TempDir()
	grokSignedIn(t, home, "me@x.ai")
	acct := &Account{Agent: "grok"}
	grokSigned(acct, home)
	req, _ := http.NewRequest("POST", up.URL+"/responses", nil)
	if err := acct.sign(context.Background(), req, []byte(`{"model":"grok-4.7","prompt_cache_key":"c1"}`)); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer k-me@x.ai" || req.Header.Get("x-grok-client-version") == "" ||
		req.Header.Get("x-grok-model-override") != "grok-4.7" || req.Header.Get("x-grok-conv-id") != "c1" ||
		!strings.HasPrefix(req.Header.Get("User-Agent"), "grok-shell/") {
		t.Fatalf("headers = %v", req.Header)
	}
	ms, err := grokModels(context.Background(), acct.sign)
	if err != nil || len(ms) != 1 || got.Get("Authorization") != "Bearer k-me@x.ai" {
		t.Fatalf("%v %+v %v", err, ms, got)
	}
}

func TestGrokTokenReadsTheCLIsSignIn(t *testing.T) {
	home := t.TempDir()
	exp := time.Now().Add(2 * time.Hour).UTC()
	auth := map[string]any{"https://auth.x.ai::u1": map[string]any{
		"key": "tok", "email": "me@example.com", "auth_mode": "oidc", "refresh_token": "r",
		"expires_at": exp.Format(time.RFC3339Nano), "oidc_issuer": "https://auth.x.ai"}}
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if u, ok := GrokUser(home); !ok || u != "me@example.com" {
		t.Fatalf("user = %q %v", u, ok)
	}
	c, err := grokAccessToken(home, "", false)
	if err != nil || c.Key != "tok" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := grokAccessToken(t.TempDir(), "", false); err == nil {
		t.Fatal("no sign-in, yet a token")
	}
}

// Codex's freeform apply_patch is left out: Grok's backend turns away a
// request with a tool type it doesn't know.
func TestGrokBodyLeavesOutCustomTools(t *testing.T) {
	in := []byte(`{"model":"grok-4.7","tools":[{"type":"function","name":"shell"},{"type":"custom","name":"apply_patch","format":{"type":"grammar"}},{"type":"namespace","name":"multi_agent_v1","tools":[]},{"type":"web_search","external_web_access":false}],"tool_choice":{"type":"custom","name":"apply_patch"},"max_output_tokens":100}`)
	var got struct {
		Tools  []map[string]any `json:"tools"`
		Choice any              `json:"tool_choice"`
		Max    int              `json:"max_output_tokens"`
	}
	if err := json.Unmarshal(grokBody(in), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 2 || got.Tools[0]["type"] != "function" || len(got.Tools[1]) != 1 || got.Tools[1]["type"] != "web_search" || got.Choice != nil || got.Max != 100 {
		t.Fatalf("%+v", got)
	}
	same := []byte(`{"tools":[{"type":"function","name":"x"}],"tool_choice":"auto"}`)
	if string(grokBody(same)) != string(same) {
		t.Fatal("a body Grok takes was changed")
	}
}

// Codex's reasoning, handed back with a null content, is sent without it.
func TestGrokBodyDropsNullReasoningContent(t *testing.T) {
	in := []byte(`{"input":[{"type":"reasoning","id":"rs_1","summary":[],"content":null,"encrypted_content":"a+b/c="},{"type":"message","role":"user","content":"hi"}]}`)
	want := `{"input":[{"encrypted_content":"a+b/c=","id":"rs_1","summary":[],"type":"reasoning"},{"content":"hi","role":"user","type":"message"}]}`
	if got := string(grokBody(in)); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	same := []byte(`{"input":[{"type":"reasoning","content":[],"encrypted_content":"x"}]}`)
	if string(grokBody(same)) != string(same) {
		t.Fatal("reasoning with content was changed")
	}
}

// On Windows the CLI is grok.exe, looked for in the installer's bin first,
// then on PATH, then ~/.local/bin; only the installer's own folders are
// taken on trust (#180).
func TestGrokCandidates(t *testing.T) {
	home := t.TempDir()
	npm := filepath.Join(home, "AppData", "Roaming", "npm")
	custom := filepath.Join(home, "tools")
	for _, c := range []struct {
		goos, binDir, grokHome string
		want                   []grokCandidate
	}{
		{"windows", "", filepath.Join(home, ".grok"), []grokCandidate{
			{filepath.Join(home, ".grok", "bin", "grok.exe"), true},
			{filepath.Join(npm, "grok.exe"), false},
			{filepath.Join(home, ".local", "bin", "grok.exe"), false},
		}},
		{"windows", custom, filepath.Join(home, "gh"), []grokCandidate{
			{filepath.Join(custom, "grok.exe"), true},
			{filepath.Join(home, "gh", "bin", "grok.exe"), true},
			{filepath.Join(home, ".grok", "bin", "grok.exe"), true},
			{filepath.Join(npm, "grok.exe"), false},
			{filepath.Join(home, ".local", "bin", "grok.exe"), false},
		}},
		{"darwin", "", filepath.Join(home, ".grok"), []grokCandidate{
			{filepath.Join(home, ".grok", "bin", "grok"), true},
			{filepath.Join(npm, "grok"), false},
			{filepath.Join(home, ".local", "bin", "grok"), false},
		}},
	} {
		// the registry's PATH repeats the process' and has a relative entry
		path := []string{npm, "", "relative", filepath.Join(home, ".grok", "bin"), npm}
		got := grokCandidates(c.goos, home, c.grokHome, c.binDir, path)
		if len(got) != len(c.want) {
			t.Fatalf("%s %q: %+v", c.goos, c.binDir, got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s %q: [%d] = %+v, want %+v", c.goos, c.binDir, i, got[i], c.want[i])
			}
		}
	}
}

// An installed Grok Build is found where its installer put it even with
// another grok ahead of it on PATH, and one on PATH is taken only when it
// lives under a .grok folder.
func TestGrokExecutableFinds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GROK_HOME", "")
	t.Setenv("GROK_BIN_DIR", "")
	name := "grok"
	if runtime.GOOS == "windows" {
		name = "grok.exe"
	}
	put := func(p string) string {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	npm := filepath.Join(home, "npm")
	put(filepath.Join(npm, name))
	t.Setenv("PATH", npm)
	if p := GrokExecutable(); p != "" && !strings.Contains(p, ".grok") {
		t.Fatalf("another grok taken: %s", p)
	}
	pathed := put(filepath.Join(home, ".grok", "versions", "1", name))
	t.Setenv("PATH", npm+string(os.PathListSeparator)+filepath.Dir(pathed))
	if p := GrokExecutable(); p != pathed {
		t.Fatalf("on PATH: %q", p)
	}
	installed := put(filepath.Join(home, ".grok", "bin", name))
	if p := GrokExecutable(); p != installed {
		t.Fatalf("installed: %q", p)
	}
	custom := put(filepath.Join(home, "tools", name))
	t.Setenv("GROK_BIN_DIR", filepath.Dir(custom))
	if p := GrokExecutable(); p != custom {
		t.Fatalf("GROK_BIN_DIR: %q", p)
	}
}

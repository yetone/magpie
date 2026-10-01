package provider

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// magpie in Docker (xugui on Discord: "docker版登不上grok和command code"):
// its image has no bash, curl or wget, so Grok Build's installer couldn't
// run and Grok's sign-in never began. Grok Build is then installed as its
// installer installs it, from the same place, without a shell.
func TestGrokInstallsWithoutShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake grok is a shell script")
	}
	home := claudeHome(t)
	t.Setenv("GROK_HOME", "")
	t.Setenv("GROK_BIN_DIR", "")
	t.Setenv("PATH", t.TempDir()) // no grok anywhere else
	platform, err := grokPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte("#!/bin/sh\necho 'grok 1.2.3 (fake)'\n"))
	zw.Close()
	var asked []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/cli/stable":
			w.Write([]byte("1.2.3\n"))
		case "/cli/grok-1.2.3-" + platform + ".gz":
			w.Write(gz.Bytes())
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	oldBases, oldShell := grokCLIBases, shellInstallerRuns
	// x.ai unreachable: the bucket behind it, as the installer falls back
	grokCLIBases = []string{"http://127.0.0.1:1/cli", fake.URL + "/cli"}
	shellInstallerRuns = func() bool { return false }
	t.Cleanup(func() { grokCLIBases, shellInstallerRuns = oldBases, oldShell })

	c, ok := missingCLI("grok")
	if !ok {
		t.Fatal("grok is installed already")
	}
	if err := installCLI(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	exe := GrokExecutable()
	if want := filepath.Join(home, ".grok", "bin", "grok"); exe != want {
		t.Fatalf("grok at %q, want %q (asked %v)", exe, want, asked)
	}
	for _, name := range []string{"grok", "agent"} {
		if l, err := os.Readlink(filepath.Join(home, ".grok", "bin", name)); err != nil || l != filepath.Join("..", "downloads", "grok-"+platform) {
			t.Fatalf("%s links to %q, %v", name, l, err)
		}
	}
	if out, err := exec.Command(exe, "--version").Output(); err != nil || !strings.Contains(string(out), "grok 1.2.3") {
		t.Fatalf("grok --version: %q, %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".grok", "config.toml")); string(b) != "[cli]\ninstaller = \"internal\"\n" {
		t.Fatalf("config.toml %q", b)
	}
	// where the shell installer runs, it is still the one used
	shellInstallerRuns = func() bool { return true }
	if c, _ := cliFor("grok"); c.native == nil || !strings.Contains(c.sh, "x.ai/cli/install.sh") {
		t.Fatalf("grok's installers %+v", c)
	}
}

// a download that isn't there says so, and the CLI isn't half installed
func TestGrokInstallWithoutShellFails(t *testing.T) {
	home := claudeHome(t)
	t.Setenv("GROK_HOME", "")
	t.Setenv("GROK_BIN_DIR", "")
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cli/stable" {
			w.Write([]byte("1.2.3"))
			return
		}
		w.WriteHeader(404)
	}))
	defer fake.Close()
	old := grokCLIBases
	grokCLIBases = []string{fake.URL + "/cli"}
	t.Cleanup(func() { grokCLIBases = old })
	if _, err := grokPlatform(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Skip(err)
	}
	err := installGrokBuild(context.Background())
	if err == nil || !strings.Contains(err.Error(), "downloading Grok Build 1.2.3") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".grok", "bin", "grok")); !os.IsNotExist(err) {
		t.Fatalf("a grok was left: %v", err)
	}
}

// Command Code's Studio posts the new key to the callback on 127.0.0.1 in
// the background, so in Docker the browser ends on a page that won't load
// and whose address has nothing in it. As Command Code's CLI takes one
// ("Authorize in browser, or paste API key here"), a key made on Studio's
// keys page and pasted finishes the sign-in.
func TestCommandCodeSignInPastedKey(t *testing.T) {
	signIn(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key != "made-key" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/alpha/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": "u9", "userName": "pasted", "email": "p@example.com"}})
		case "/alpha/billing/subscriptions":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"planId": "individual-pro-monthly", "status": "active"}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldAPI := cmdAPI
	cmdAPI = srv.URL
	defer func() { cmdAPI = oldAPI }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := StartSignIn(CommandCodePlanID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.PasteKey || st.KeysURL != "https://commandcode.ai/settings/keys" {
		t.Fatalf("started %+v", st)
	}
	u, _ := url.Parse(st.URL)
	// the address the browser ended on carries nothing: said so, and the
	// sign-in still waits for a key
	if err := SubmitSignInCallback(st.ID, u.Query().Get("callback")); err == nil || !strings.Contains(err.Error(), "keys page") {
		t.Fatalf("an address pasted: %v", err)
	}
	if err := SubmitSignInCallback(st.ID, "  "); err == nil {
		t.Fatal("nothing pasted was taken")
	}
	if st, _ := SignInStatus(st.ID); st.State != "waiting" {
		t.Fatalf("after a wrong paste %+v", st)
	}
	if err := SubmitSignInCallback(st.ID, " made-key\n"); err != nil {
		t.Fatal(err)
	}
	st, _ = WaitSignIn(ctx, st.ID)
	if st.State != "done" || st.User != "pasted" || st.Plan != "Pro" {
		t.Fatalf("finished %+v", st)
	}
	found := false
	for _, l := range cmdLogins() {
		if l.User == "pasted" && l.auth.APIKey == "made-key" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the key isn't kept: %+v", cmdLogins())
	}
	// Studio's post coming after it is turned away: the sign-in is over
	res, err := http.PostForm(u.Query().Get("callback"), url.Values{"apiKey": {"made-key"}, "state": {u.Query().Get("state")}})
	if err == nil {
		res.Body.Close()
	}
	if n := len(cmdLogins()); n != 1 {
		t.Fatalf("%d accounts after a late post", n)
	}

	// a key Command Code turns down fails the sign-in, as Studio's would
	st, _ = StartSignIn(CommandCodePlanID)
	if err := SubmitSignInCallback(st.ID, "wrong-key"); err == nil || !strings.Contains(err.Error(), "didn't take") {
		t.Fatalf("a wrong key: %v", err)
	}
}

// The community Grok plugin installed beside the built-in (not moved onto)
// runs `grok login` itself, and said "install Grok Build first: curl … |
// bash" in Docker, where there's no shell to run it: the CLI is installed
// first, as it is for a moved one.
func TestGrokPluginSignInInstallsCLI(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	home := claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := filepath.Join(home, "grok-plugin")
	os.MkdirAll(dir, 0o755)
	src := filepath.Join(dir, "index.js")
	os.WriteFile(src, []byte(`import { existsSync } from "node:fs"
export const P = async () => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider.grok = { name: "Grok", npm: "@ai-sdk/openai-compatible", api: "https://fake.invalid/v1", models: { m: { name: "M" } } }
  },
  auth: {
    provider: "grok",
    methods: [{
      type: "oauth",
      label: "Grok (device code)",
      authorize: async () => {
        if (!existsSync(process.env.FAKE_GROK)) throw new Error("install Grok Build first")
        return {
          url: "https://fake.invalid/device",
          instructions: "Approve the sign-in",
          method: "auto",
          callback: async () => ({ type: "success", refresh: "h", access: "a", expires: 0, accountId: "me@grok" }),
        }
      },
    }],
  },
})
`), 0o644)
	exe := filepath.Join(home, ".grok", "bin", "grok")
	t.Setenv("FAKE_GROK", exe)
	if _, err := plugin.Add(ctx, src); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	old := pluginOwnUser
	pluginOwnUser = func(string) string { return "" }
	t.Cleanup(func() { pluginOwnUser = old })
	oldExe := GrokExecutable
	GrokExecutable = func() string {
		if isFile(exe) {
			return exe
		}
		return ""
	}
	t.Cleanup(func() { GrokExecutable = oldExe })
	var ran string
	oldRun := runInstaller
	runInstaller = func(ctx context.Context, c agentCLI) ([]byte, error) {
		ran = c.Name
		os.MkdirAll(filepath.Dir(exe), 0o755)
		return nil, os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	}
	t.Cleanup(func() { runInstaller = oldRun })

	st, err := StartPluginSignIn("grok", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "installing" || st.Installing != "Grok Build" {
		t.Fatalf("started %+v", st)
	}
	st = waitPast(t, st.ID, "installing")
	if st.State == "waiting" {
		st = waitPast(t, st.ID, "waiting")
	}
	if st.State != "done" || st.User != "me@grok" || ran != "Grok Build" {
		t.Fatalf("finished %+v (installer %q)", st, ran)
	}
}

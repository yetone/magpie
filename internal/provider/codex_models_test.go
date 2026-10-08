package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// A ChatGPT account lists the models of its own plan, asked with its own
// token; the cache Codex CLI keeps may be another account's.
func TestCodexModelsOfTheAccount(t *testing.T) {
	signIn(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/models" || r.Header.Get("chatgpt-account-id") != "acct-1" || r.URL.Query().Get("client_version") == "" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"models": [
			{"slug": "gpt-5.6-luna", "display_name": "GPT-5.6 Luna", "visibility": "list", "priority": 2,
			 "supported_reasoning_levels": [{"effort": "low"}, {"effort": "medium"}]},
			{"slug": "codex-auto-review", "visibility": "hide", "priority": 1}]}`))
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	p, ok := find(All(), "codex")
	if !ok {
		t.Fatal("no codex account")
	}
	if ms := p.Available(); len(ms) != 1 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("before fetching, Codex CLI's cache: %+v", ms)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	ms := p.Available()
	if len(ms) != 1 || ms[0].ID != "gpt-5.6-luna" || len(ms[0].Efforts) != 2 {
		t.Fatalf("after fetching: %+v", ms)
	}
}

// The models list is asked for with the newest Codex known: the CLI
// installed over the version its cache was written with.
func TestCodexVersion(t *testing.T) {
	shellFakes(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeFile(t, filepath.Join(home, ".codex", "models_cache.json"), map[string]any{"client_version": "0.160.0"})
	exe := filepath.Join(t.TempDir(), "codex")
	testenv.Program(t, exe, "#!/bin/sh\necho codex-cli 0.161.1\n")
	old := codexExecutable
	t.Cleanup(func() { codexExecutable = old; codexVersionCache.at = time.Time{}; codexSeen = seenVersion{} })
	codexSeen = seenVersion{}
	for _, c := range []struct{ exe, want string }{{exe, "0.161.1"}, {"", "0.160.0"}} {
		codexExecutable = func() string { return c.exe }
		codexVersionCache.at = time.Time{}
		if got := codexVersion(); got != c.want {
			t.Errorf("with %q: %s, want %s", c.exe, got, c.want)
		}
	}
	// Codex CLI's state under CODEX_HOME, when that is set
	t.Setenv("CODEX_HOME", t.TempDir())
	codexVersionCache.at = time.Time{}
	if got := codexVersion(); got != codexClientVersion {
		t.Errorf("CODEX_HOME with no cache: %s", got)
	}
	writeFile(t, filepath.Join(os.Getenv("CODEX_HOME"), "models_cache.json"), map[string]any{"client_version": "0.160.2"})
	codexVersionCache.at = time.Time{}
	if got := codexVersion(); got != "0.160.2" {
		t.Errorf("CODEX_HOME's cache: %s", got)
	}
}

// A Codex that came through the gateway newer than any magpie can find
// counts at once, the cached lookup notwithstanding: the account's model
// list is asked for as that Codex, and its turns say they are it. Only
// Codex's requests count, and never an older version.
func TestCodexVersionSeen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	old := codexExecutable
	codexExecutable = func() string { return "" }
	codexSeen = seenVersion{}
	codexVersionCache.at = time.Time{}
	t.Cleanup(func() { codexExecutable = old; codexVersionCache.at = time.Time{}; codexSeen = seenVersion{} })
	if got := codexVersion(); got != codexClientVersion {
		t.Fatalf("nothing seen: %s", got)
	}
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	for _, c := range []struct {
		h    http.Header
		want string
	}{
		{h("User-Agent", "claude-cli/2.1.300 (external, cli)"), codexClientVersion},
		{h("User-Agent", "Mozilla/5.0", "version", "9.9.9"), codexClientVersion},
		{h("User-Agent", "codex_cli_rs/0.160.0 (Mac OS 26.6.0; arm64) ghostty/1.2.0"), "0.160.0"},
		{h("User-Agent", "Codex Desktop/0.162.3 (Windows 10.0.26100; x86_64) unknown", "originator", "Codex Desktop"), "0.162.3"},
		{h("User-Agent", "codex_cli_rs/0.150.0", "version", "0.150.0"), "0.162.3"},
	} {
		SawCodexClient(c.h)
		if got := codexVersion(); got != c.want {
			t.Fatalf("after %v: %s, want %s", c.h, got, c.want)
		}
	}

	var asked, ua, ver string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked, ua, ver = r.URL.Query().Get("client_version"), r.Header.Get("User-Agent"), r.Header.Get("version")
		w.Write([]byte(`{"models":[{"slug":"gpt-6.1-sol","visibility":"list"}]}`))
	}))
	defer fake.Close()
	was := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = was })
	sign := codexSign(func(context.Context) (string, string, error) { return "tok", "acct-1", nil })
	if _, err := codexModels(context.Background(), sign); err != nil {
		t.Fatal(err)
	}
	if asked != "0.162.3" || !strings.HasPrefix(ua, "codex_cli_rs/0.162.3 (") || ver != "0.162.3" {
		t.Fatalf("models asked as client_version %q, User-Agent %q, version %q", asked, ua, ver)
	}
}

// Codex CLI installed where a desktop app's PATH doesn't reach — under an
// nvm Node, or an npm prefix ~/.npmrc names — is found.
func TestCodexExecutableOffPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix install locations")
	}
	for name, dir := range map[string]string{
		"nvm":        ".nvm/versions/node/v22.3.0/bin",
		"mise":       ".local/share/mise/installs/node/24.14.0/bin",
		"npm prefix": "custom-npm/bin",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("PATH", "/usr/bin:/bin")
			os.WriteFile(filepath.Join(home, ".npmrc"), []byte("registry=https://registry.npmjs.org/\nprefix = ~/custom-npm\n"), 0o644)
			exe := filepath.Join(home, dir, "codex")
			os.MkdirAll(filepath.Dir(exe), 0o755)
			testenv.Program(t, exe, "#!/bin/sh\necho codex-cli 0.161.0\n")
			if got := codexExecutable(); got != exe {
				t.Fatalf("found %q, want %q", got, exe)
			}
		})
	}
}

// A model that takes more than its window when asked keeps that most, so
// the context field can offer it: GPT-6's 872K over the 272K said.
func TestCodexModelsMaxContext(t *testing.T) {
	ms := parseCodexModels([]byte(`{"models":[
		{"slug":"gpt-6","context_window":272000,"max_context_window":872000},
		{"slug":"gpt-5.5","context_window":272000,"max_context_window":272000}]}`))
	if len(ms) != 2 || ms[0].Context != 272000 || ms[0].MaxContext != 872000 || ms[1].MaxContext != 0 {
		t.Fatalf("%+v", ms)
	}
}

// A codex CLI that fails to run — macOS stopping it as malware, with an
// alert each time (#864) — isn't run again every ten minutes, and an npm
// install's version is read from its package, not run.
func TestCodexVersionNotRerun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	exe := filepath.Join(dir, "codex")
	testenv.Program(t, exe, "#!/bin/sh\necho x >> '"+runs+"'\nexit 137\n")
	old := codexExecutable
	t.Cleanup(func() { codexExecutable = old; codexVersionCache.at = time.Time{}; codexSeen = seenVersion{} })
	codexSeen = seenVersion{}
	codexExecutable = func() string { return exe }
	for range 3 { // ten minutes apart
		codexVersionCache.at = time.Time{}
		if got := codexVersion(); got != codexClientVersion {
			t.Fatalf("got %s", got)
		}
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "x") != 1 {
		t.Fatalf("the failing codex was run %d times", strings.Count(string(b), "x"))
	}

	pkg := filepath.Join(dir, "node_modules", "@openai", "codex")
	os.MkdirAll(filepath.Join(pkg, "bin"), 0o755)
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"version":"0.199.0"}`), 0o644)
	testenv.Program(t, filepath.Join(pkg, "bin", "codex.js"), "#!/bin/sh\necho x >> '"+runs+"'\necho codex-cli 0.199.0\n")
	link := filepath.Join(dir, "bin", "codex")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink(filepath.Join(pkg, "bin", "codex.js"), link)
	codexExecutable = func() string { return link }
	codexVersionCache.at = time.Time{}
	if got := codexVersion(); got != "0.199.0" {
		t.Fatalf("npm codex: %s", got)
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "x") != 1 {
		t.Fatal("the npm codex was run")
	}
}

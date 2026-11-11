package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// #6111 (Abandon332 on X): the CLI sets a remote server's headers, env and
// timeout; a word it would have passed on as an argument is refused.
func TestLibraryMCPHeadersEnvTimeout(t *testing.T) {
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "APPDATA": "", "LOCALAPPDATA": ""} {
		t.Setenv(k, v)
	}
	if err := os.MkdirAll(filepath.Join(h, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	get := func(n string) library.Server {
		v, err := library.Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range v.Servers {
			if s.Name == n {
				return *s.Server
			}
		}
		t.Fatalf("no %s", n)
		return library.Server{}
	}
	run := func(a ...string) error { return libraryCmd(append([]string{"library", "mcp"}, a...)) }

	if err := run("add", "gh", "https://example.com/mcp", "header=Authorization=Bearer abc", "header=Accept: text/event-stream"); err != nil {
		t.Fatal(err)
	}
	if s := get("gh"); s.Headers["Authorization"] != "Bearer abc" || s.Headers["Accept"] != "text/event-stream" || len(s.Args) != 0 {
		t.Fatalf("%+v", s)
	}
	if err := run("set", "gh", "header=Authorization=Bearer new", "timeout=45"); err != nil {
		t.Fatal(err)
	}
	if s := get("gh"); s.Headers["Authorization"] != "Bearer new" || s.StartupTimeout != 45 {
		t.Fatalf("%+v", s)
	}
	if err := run("add", "t1", "uvx", "foo", "serve", "env=A=B", "timeout=30"); err != nil {
		t.Fatal(err)
	}
	if s := get("t1"); s.Env["A"] != "B" || s.StartupTimeout != 30 || len(s.Args) != 2 {
		t.Fatalf("env/timeout went into argv: %+v", s)
	}
	if err := run("add", "t2", "uvx", "--", "env=A=B"); err != nil {
		t.Fatal(err)
	}
	if s := get("t2"); len(s.Args) != 1 || s.Args[0] != "env=A=B" || s.Env != nil {
		t.Fatalf("after --: %+v", s)
	}
	for _, bad := range [][]string{
		{"add", "t3", "uvx", "headers=A=b"},
		{"add", "t4", "https://x.example/mcp", "env=A=B"},
		{"add", "t5", "uvx", "header=A=b"},
		{"set", "gh", "agents=claude"},
		{"set", "nope", "timeout=3"},
		{"show", "nope"},
	} {
		if err := run(bad...); err == nil {
			t.Errorf("%v was taken", bad)
		}
	}
	if err := run("show", "gh"); err != nil {
		t.Fatal(err)
	}
}

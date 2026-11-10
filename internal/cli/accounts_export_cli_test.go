package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// magpie accounts export (heyiwei5 and Rk1ngggg on X): warns first, writes
// nothing until agreed to, writes 0600 files that import back, never prints
// a token and never writes over a file.
func TestAccountsExport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		who := strings.TrimPrefix(body["refresh_token"], "r-")
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": importJWT(map[string]any{"email": who + "@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-" + who}}),
			"access_token":  importJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "n-" + who,
		})
	}))
	defer fake.Close()
	t.Cleanup(provider.CodexTokenURLForTest(fake.URL))
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	oldIn := os.Stdin
	os.Stdin = null
	t.Cleanup(func() { os.Stdin = oldIn })

	src := filepath.Join(t.TempDir(), "a.json")
	os.WriteFile(src, []byte(`{"OPENAI_API_KEY":null,"tokens":{"id_token":"x","access_token":"y","refresh_token":"r-a","account_id":"acct-a"}}`), 0o600)
	if out, err := said(t, func() error { return accountsCmd([]string{"accounts", "import", "codex", src, "--yes"}) }); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	held := provider.Logins("codex")
	if len(held) != 1 {
		t.Fatalf("logins %+v", held)
	}

	out := t.TempDir()
	run := func(args ...string) (string, error) {
		t.Helper()
		return said(t, func() error { return accountsCmd(append([]string{"accounts", "export"}, args...)) })
	}
	// not agreed to: the warning, no file
	says, err := run("codex", "--to", out)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("exported without --yes: %v\n%s", err, says)
	}
	if es, _ := os.ReadDir(out); len(es) != 0 {
		t.Fatalf("wrote before agreed: %v", es)
	}
	for _, format := range []string{"sub2api", "cliproxyapi"} {
		says, err = run("codex", "--format", format, "--to", out, "--yes")
		if err != nil {
			t.Fatalf("%s: %v\n%s", format, err, says)
		}
		if strings.Contains(says, "n-a") || strings.Contains(says, "r-a") {
			t.Errorf("a token was printed:\n%s", says)
		}
	}
	es, _ := os.ReadDir(out)
	if len(es) != 2 {
		t.Fatalf("files %v", es)
	}
	for _, e := range es {
		p := filepath.Join(out, e.Name())
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
				t.Errorf("%s is %v, not 0600", e.Name(), fi.Mode().Perm())
			}
		}
	}
	// again: the first files stay, new ones are named beside them
	if _, err = run("codex", "--format", "sub2api", "--to", out, "--yes"); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(out); len(es) != 3 {
		t.Errorf("an export wrote over a file: %v", es)
	}
	if _, err := run("codex", "--format", "csv", "--to", out, "--yes"); err == nil {
		t.Error("an unknown format was taken")
	}
}

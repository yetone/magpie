package gui

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Warm up now's route answers a failure with why, as the row shows it: an
// account that isn't signed in, or an agent with no warm-up, is a 400 with
// the reason, and nothing is sent.
func TestWarmNowAPISaysWhy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("MAGPIE_NO_ACCOUNTS", "1") // no sign-in read, the Keychain's included
	h := Handler(nil, nil)
	for _, c := range []struct{ body, want string }{
		{`{"agent":"codex","user":"nobody@example.com"}`, `no ChatGPT account "nobody@example.com"`},
		{`{"agent":"claude","user":"nobody@example.com"}`, `no Claude account "nobody@example.com"`},
		{`{"agent":"copilot","user":"octocat"}`, `"copilot" has no warm-up`},
		{`{"agent":"codex"}`, `no ChatGPT account ""`},
		// Warm up all: no account, or no warm-up
		{`{"agent":"claude","all":true}`, `no Claude account to warm up`},
		{`{"agent":"codex","all":true}`, `no ChatGPT account to warm up`},
		{`{"agent":"copilot","all":true}`, `"copilot" has no warm-up`},
	} {
		route := "/api/warm-now"
		if strings.Contains(c.body, `"all"`) {
			route = "/api/warm-all"
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", route, strings.NewReader(c.body)))
		var out struct{ Error string }
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != 400 || !strings.Contains(out.Error, c.want) {
			t.Errorf("%s: %d %s, want 400 saying %s", c.body, rec.Code, rec.Body, c.want)
		}
	}
}

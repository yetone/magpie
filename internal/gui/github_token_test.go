package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The GitHub token is set and removed on its own, and the page is told it
// masked, with where it is from: Settings, or GITHUB_TOKEN / GH_TOKEN.
func TestGitHubTokenSetting(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	call := func(method, path, body string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	const tok = "ghp_abcdefghijklmnopqrstuvwxyz0123"
	code, body := call("POST", "/api/settings/github-token", `{"token":" `+tok+` "}`)
	if code != http.StatusOK || settings.Load().GitHubToken != tok {
		t.Fatalf("set: %d %s, saved %q", code, body, settings.Load().GitHubToken)
	}
	for _, path := range []string{"/api/settings/github-token", "/api/settings"} {
		if path == "/api/settings" {
			code, body = call("GET", path, "")
		}
		if strings.Contains(body, tok) || !strings.Contains(body, `"githubTokenMask":"ghp_…0123"`) || !strings.Contains(body, `"githubTokenFrom":"settings"`) {
			t.Errorf("%s told the page %s", path, body)
		}
	}
	// a save of the Settings page keeps it
	if code, body = call("POST", "/api/settings", `{"theme":"dark","githubToken":""}`); code != http.StatusOK || settings.Load().GitHubToken != tok {
		t.Fatalf("save: %d %s, token %q", code, body, settings.Load().GitHubToken)
	}
	if code, _ = call("POST", "/api/settings/github-token", `{"token":"two words"}`); code == http.StatusOK || settings.Load().GitHubToken != tok {
		t.Errorf("a token with a space was taken: %d", code)
	}
	// removed, the environment's is used, and said so
	t.Setenv("GH_TOKEN", "gho_fromtheenvironment99")
	code, body = call("POST", "/api/settings/github-token", `{"token":""}`)
	if code != http.StatusOK || settings.Load().GitHubToken != "" {
		t.Fatalf("remove: %d %s", code, body)
	}
	if strings.Contains(body, "gho_fromtheenvironment99") || !strings.Contains(body, `"githubTokenFrom":"GH_TOKEN"`) || !strings.Contains(body, `"githubTokenMask":"gho_…nt99"`) {
		t.Errorf("from the environment: %s", body)
	}
}

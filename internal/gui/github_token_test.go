package gui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/ghtoken"
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
		if code != http.StatusOK || strings.Contains(body, tok) || !strings.Contains(body, `"githubTokenMask":"ghp_…0123"`) || !strings.Contains(body, `"githubTokenFrom":"settings"`) {
			t.Errorf("%s answered %d and told the page %s", path, code, body)
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

// A Settings token GitHub refused is still shown in Settings, masked and
// said to be refused, not as if none were set, while requests go without
// one (#3926).
func TestGitHubTokenRefusedShown(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	state := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
		return rec.Body.String()
	}
	// ghtoken keeps the tokens GitHub refused for as long as the process
	// runs, so each run refuses a token of its own: with one constant a
	// second run (-count=2) found it refused before GitHub was asked.
	tok := fmt.Sprintf("ghp_refused%025d4567", time.Now().UnixNano())
	s := settings.Load()
	s.GitHubToken = tok
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if body := state(); strings.Contains(body, "githubTokenRefused") {
		t.Fatalf("refused before GitHub was asked: %s", body)
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer gh.Close()
	req, _ := http.NewRequest("GET", gh.URL+"/repos/o/r", nil)
	resp, _, err := ghtoken.Do(req, gh.URL, http.DefaultClient.Do)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the request without the token got %d", resp.StatusCode)
	}
	body := state()
	if strings.Contains(body, tok) || !strings.Contains(body, `"githubTokenRefused":true`) || !strings.Contains(body, `"githubTokenFrom":"settings"`) || !strings.Contains(body, `"githubTokenMask":"ghp_…4567"`) {
		t.Errorf("after the refusal the page was told %s", body)
	}
}

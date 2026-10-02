package library

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// fakeGitHub stands in for GitHub's API and codeload: it notes the
// Authorization each was asked with, and answers the API as answer says.
type fakeGitHub struct {
	mu      sync.Mutex
	apiAuth []string // the API's requests' Authorization, in order
	tarAuth []string // codeload's
	ifNone  []string // the API's requests' If-None-Match
	answer  func(w http.ResponseWriter, r *http.Request) bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{}
	files := map[string]string{"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs\n---\n"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.apiAuth = append(g.apiAuth, r.Header.Get("Authorization"))
		g.ifNone = append(g.ifNone, r.Header.Get("If-None-Match"))
		answer := g.answer
		g.mu.Unlock()
		if answer != nil && answer(w, r) {
			return
		}
		fmt.Fprint(w, `[{"sha":"p1","commit":{"message":"Change","committer":{"date":"2026-09-01T10:00:00Z"}}}]`)
	}))
	tar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.tarAuth = append(g.tarAuth, r.Header.Get("Authorization"))
		g.mu.Unlock()
		w.Write(tarball(t, files))
	}))
	oldT, oldA := tarballURL, githubAPI
	tarballURL = func(repo, ref string) string { return tar.URL + "/tar/" + repo }
	githubAPI = api.URL
	t.Cleanup(func() {
		tarballURL, githubAPI = oldT, oldA
		api.Close()
		tar.Close()
	})
	return g
}

func (g *fakeGitHub) lastAPIAuth() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.apiAuth) == 0 {
		return "(not asked)"
	}
	return g.apiAuth[len(g.apiAuth)-1]
}

func setGitHubToken(t *testing.T, tok string) {
	t.Helper()
	s := settings.Load()
	s.GitHubToken = tok
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// Each test installs from a repository of its own: InstallSkills keeps
// what it found of one by name (probes), for the other tests too.

// The API is asked with the token set in Settings, else GITHUB_TOKEN's,
// else GH_TOKEN's, and with none when there is none; codeload never gets it.
func TestGitHubTokenSent(t *testing.T) {
	sandbox(t)
	g := newFakeGitHub(t)
	ok(t)(InstallSkills("owner/ghtoken-sent", []string{"skills/pdf"}, []string{"claude"}))

	for _, c := range []struct {
		name, setting, github, gh, want string
	}{
		{"none", "", "", "", ""},
		{"GH_TOKEN", "", "", "gh-env", "Bearer gh-env"},
		{"GITHUB_TOKEN", "", "github-env", "gh-env", "Bearer github-env"},
		{"settings", "ghp_settings", "github-env", "gh-env", "Bearer ghp_settings"},
	} {
		t.Setenv("GITHUB_TOKEN", c.github)
		t.Setenv("GH_TOKEN", c.gh)
		setGitHubToken(t, c.setting)
		got := check(t)
		if got["pdf"].Status == "unknown" {
			t.Errorf("%s: %+v", c.name, got["pdf"])
		}
		if a := g.lastAPIAuth(); a != c.want {
			t.Errorf("%s: the API was asked with Authorization %q, want %q", c.name, a, c.want)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.tarAuth) == 0 {
		t.Fatal("codeload was never asked")
	}
	for _, a := range g.tarAuth {
		if a != "" {
			t.Errorf("codeload was sent Authorization %q", a)
		}
	}
}

// The token goes to GitHub's API's host alone.
func TestGitHubTokenOnlyToTheAPI(t *testing.T) {
	sandbox(t)
	t.Setenv("GITHUB_TOKEN", "secret-token")
	for u, want := range map[string]bool{
		"https://api.github.com/repos/a/b/commits":                true,
		"https://api.github.com/repos/rtk-ai/rtk/releases/latest": true,
		"https://codeload.github.com/a/b/tar.gz/HEAD":             false,
		"https://raw.githubusercontent.com/a/b/main/x":            false,
		"https://api.github.com.evil.example/repos":               false,
		"https://example.com/api.github.com":                      false,
		"http://127.0.0.1:3425/v1/models":                         false,
	} {
		req, _ := http.NewRequest("GET", u, nil)
		if got := withGitHubToken(req); got != want || (req.Header.Get("Authorization") != "") != want {
			t.Errorf("%s: sent %v (%q), want %v", u, got, req.Header.Get("Authorization"), want)
		}
	}
	// a redirect off GitHub's API drops it, as Go's client does for
	// another host
	var other string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other = r.Header.Get("Authorization")
		fmt.Fprint(w, "[]")
	}))
	defer elsewhere.Close()
	g := newFakeGitHub(t)
	g.answer = func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, strings.Replace(elsewhere.URL, "127.0.0.1", "localhost", 1)+"/x", http.StatusFound)
		return true
	}
	lastCommit(Source{Kind: "github", Repo: "a/b"})
	if g.lastAPIAuth() != "Bearer secret-token" {
		t.Errorf("the API was asked with %q", g.lastAPIAuth())
	}
	if other != "" {
		t.Errorf("the host redirected to was sent Authorization %q", other)
	}
}

// Rate limited, the check says so plainly: until when, and, without a
// token, that one in Settings raises the limit; the page is told the same.
func TestGitHubRateLimitSaid(t *testing.T) {
	sandbox(t)
	g := newFakeGitHub(t)
	ok(t)(InstallSkills("owner/ghtoken-limited", []string{"skills/pdf"}, []string{"claude"}))
	reset := time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC)
	g.answer = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
		w.WriteHeader(403)
		fmt.Fprint(w, `{"message":"API rate limit exceeded for 1.2.3.4."}`)
		return true
	}
	at := reset.Local().Format("15:04")

	c := check(t)["pdf"]
	if c.Status != "unknown" || c.Limited == nil || c.Limited.Token || c.Limited.Until != "2030-01-02T03:04:00Z" {
		t.Fatalf("without a token: %+v %+v", c, c.Limited)
	}
	for _, w := range []string{"60 an hour", "until " + at, "GitHub token in Settings → Network and sharing", "5,000"} {
		if !strings.Contains(c.Error, w) {
			t.Errorf("without a token, %q doesn't say %q", c.Error, w)
		}
	}

	setGitHubToken(t, "ghp_x")
	c = check(t)["pdf"]
	if c.Limited == nil || !c.Limited.Token || !strings.Contains(c.Error, "your GitHub token") || !strings.Contains(c.Error, "until "+at) ||
		strings.Contains(c.Error, "Add a GitHub token") {
		t.Errorf("with a token: %+v", c)
	}
	if strings.Contains(c.Error, "ghp_x") {
		t.Errorf("the token is in the error: %q", c.Error)
	}

	// a secondary limit: 403 with Retry-After
	g.answer = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(403)
		fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit."}`)
		return true
	}
	c = check(t)["pdf"]
	if c.Limited == nil || c.Limited.Until == "" {
		t.Errorf("secondary limit: %+v %+v", c, c.Limited)
	}

	// a token GitHub refuses says which one
	g.answer = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
		return true
	}
	c = check(t)["pdf"]
	if c.Limited != nil || c.Error != "GitHub refused the GitHub token in Settings → Network and sharing: it may have expired or been revoked" {
		t.Errorf("refused: %+v", c)
	}
	setGitHubToken(t, "")
	t.Setenv("GH_TOKEN", "bad")
	c = check(t)["pdf"]
	if !strings.Contains(c.Error, "GitHub refused GH_TOKEN") || strings.Contains(c.Error, "bad") {
		t.Errorf("refused from the environment: %+v", c)
	}
}

// A folder asked about again is asked with the ETag GitHub gave, and its
// 304, which doesn't count against the limit, answers as the first did.
func TestGitHubConditionalRequests(t *testing.T) {
	sandbox(t)
	g := newFakeGitHub(t)
	ok(t)(InstallSkills("owner/ghtoken-etag", []string{"skills/pdf"}, []string{"claude"}))
	g.answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `[{"sha":"e1","commit":{"message":"Etagged","committer":{"date":"2026-09-01T10:00:00Z"}}}]`)
		return true
	}
	first := check(t)["pdf"]
	second := check(t)["pdf"]
	if first.Commit != "e1" || second.Commit != "e1" || second.Message != "Etagged" || second.Status != first.Status {
		t.Errorf("first %+v, second %+v", first, second)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := len(g.ifNone); n < 2 || g.ifNone[0] != "" || g.ifNone[n-1] != `"v1"` {
		t.Errorf("If-None-Match sent: %q", g.ifNone)
	}
}

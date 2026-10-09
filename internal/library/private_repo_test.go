package library

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 37FlowAI on X: a skill in a private repository installs with the GitHub
// token set in Settings, through GitHub's API, which redirects to codeload;
// the token goes to the API alone. Without a token, or with one that can't
// read it, the error says where the token is set.
func TestPrivateRepoInstallsWithToken(t *testing.T) {
	sandbox(t)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	files := map[string]string{"skills/pdf/SKILL.md": "---\nname: pdf\ndescription: PDFs\n---\n"}
	var mu sync.Mutex
	var signedAuth []string
	tar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/signed/") {
			http.NotFound(w, r) // codeload has no private repository without a token
			return
		}
		mu.Lock()
		signedAuth = append(signedAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Write(tarball(t, files))
	}))
	t.Cleanup(tar.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_reads" || !strings.HasPrefix(r.URL.Path, "/repos/owner/private-skills/tarball") {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, tar.URL+"/signed/owner/private-skills?token=signed", http.StatusFound)
	}))
	t.Cleanup(api.Close)
	oldT, oldA := tarballURL, githubAPI
	tarballURL = func(repo, ref string) string { return tar.URL + "/tar/" + repo }
	// another host than codeload's, as api.github.com is
	githubAPI = strings.Replace(api.URL, "127.0.0.1", "localhost", 1)
	t.Cleanup(func() { tarballURL, githubAPI = oldT, oldA })

	_, err := InstallSkills("owner/private-skills", []string{"skills/pdf"}, []string{"claude"})
	if err == nil || !strings.Contains(err.Error(), "Settings → Network and sharing") {
		t.Fatalf("no token: %v", err)
	}
	setGitHubToken(t, "ghp_other")
	_, err = InstallSkills("owner/private-skills", []string{"skills/pdf"}, []string{"claude"})
	if err == nil || !strings.Contains(err.Error(), "that the GitHub token in Settings → Network and sharing can read") {
		t.Fatalf("a token that can't read it: %v", err)
	}
	setGitHubToken(t, "ghp_reads")
	ok(t)(InstallSkills("owner/private-skills", []string{"skills/pdf"}, []string{"claude"}))
	mu.Lock()
	defer mu.Unlock()
	if len(signedAuth) == 0 {
		t.Fatal("codeload's signed URL was never asked")
	}
	for _, a := range signedAuth {
		if a != "" {
			t.Errorf("codeload was sent Authorization %q", a)
		}
	}
}

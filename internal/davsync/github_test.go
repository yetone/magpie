package davsync

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

type githubFixture struct {
	mu            sync.Mutex
	objects       map[string][]byte
	writes        int
	status        int
	message       string
	redirect      string
	empty         bool
	refError      string
	initialBranch string
	defaultBranch string
	public        bool
	// The Git Data API's objects: a branch's head, blobs by SHA, trees
	// made on a base tree, and commits with their tree and parent.
	heads   map[string]string
	blobs   map[string][]byte
	trees   map[string]fixtureTree
	commits map[string]fixtureCommit
	made    int
	// contentsLimit is the largest file the Contents API's PUT takes;
	// GitHub answers 422 "too large" over its own (undocumented) limit.
	contentsLimit int
	// blobLimit is the largest blob one POST takes. GitHub documents no
	// request size; it refused a 50.8 MB blob (about 68 MB of base64 body)
	// with 422 "Sorry, your input was too large to process" (Discord),
	// and took a 19 MB file's Contents PUT (about 25 MB of body).
	blobLimit int
	// biggestBlob is the largest blob a POST sent.
	biggestBlob int
	// beforeRef runs, under the lock, before a ref update is judged: a
	// test commits there as another computer would.
	beforeRef func()
	// requests counts each method and path, to show which API wrote.
	requests []string
	// commitsBy is each commit's author and committer, as
	// "API: author | committer", in the order they were made.
	commitsBy []string
}

// fixtureIdent is a commit's author or committer as GitHub's API takes it.
type fixtureIdent struct{ Name, Email string }

func (f *githubFixture) by(api string, author, committer *fixtureIdent) {
	say := func(i *fixtureIdent) string {
		if i == nil {
			return "none"
		}
		return i.Name + " <" + i.Email + ">"
	}
	f.commitsBy = append(f.commitsBy, api+": "+say(author)+" | "+say(committer))
}

type fixtureTree struct {
	base    string
	entries []struct {
		Path string
		SHA  *string
	}
}

type fixtureCommit struct{ tree, parent string }

// head is the branch's head commit, made the first time it is asked for.
func (f *githubFixture) head(branch string) string {
	if f.heads[branch] == "" {
		f.advance(branch)
	}
	return f.heads[branch]
}

// advance commits on the branch outside the Git Data API (the Contents API,
// or another computer's commit set up by a test).
func (f *githubFixture) advance(branch string) {
	f.made++
	sha := fmt.Sprintf("%040x", f.made)
	f.commits[sha] = fixtureCommit{tree: "tree-" + sha, parent: f.heads[branch]}
	f.heads[branch] = sha
}

// branchAt is the branch a contents read's ref names: a branch, or a
// commit that is a branch's head now.
func (f *githubFixture) branchAt(ref string) (string, bool) {
	for b, h := range f.heads {
		if h == ref {
			return b, true
		}
	}
	if _, ok := f.commits[ref]; ok {
		return "", false // an older commit: the fixture keeps no old trees
	}
	return ref, true
}

type githubTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t githubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.github.com" {
		return nil, fmt.Errorf("GitHub token sent outside the API: %s", r.URL.Host)
	}
	c := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = t.target.Scheme, t.target.Host
	c.URL = &u
	return t.base.RoundTrip(c)
}

func githubSHA(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func newGithubFixture(t *testing.T) *githubFixture {
	t.Helper()
	f := &githubFixture{objects: map[string][]byte{}, defaultBranch: "main", heads: map[string]string{},
		blobs: map[string][]byte{}, trees: map[string]fixtureTree{}, commits: map[string]fixtureCommit{}, contentsLimit: 1 << 20, blobLimit: 24 << 20}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	old := syncClient
	syncClient = &http.Client{Transport: githubTransport{old.Transport, target}}
	t.Cleanup(func() { syncClient = old })
	return f
}

func (f *githubFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	send := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, message string) { send(status, map[string]string{"message": message}) }
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer fixture-github-token" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
		fail(401, "Bad credentials")
		return
	}
	if f.status != 0 {
		fail(f.status, f.message)
		return
	}
	if f.redirect != "" {
		http.Redirect(w, r, f.redirect, http.StatusFound)
		return
	}
	base := "/repos/alice/private"
	if r.URL.Path == base {
		send(200, map[string]any{"default_branch": f.defaultBranch, "private": !f.public})
		return
	}
	if branch, ok := strings.CutPrefix(r.URL.Path, base+"/git/ref/heads/"); ok {
		if f.empty {
			fail(409, "Git Repository is empty.")
			return
		}
		if f.refError != "" {
			fail(409, f.refError)
			return
		}
		if branch != f.defaultBranch && branch != "sync/settings" {
			fail(404, "Not Found")
			return
		}
		send(200, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]string{"type": "commit", "sha": f.head(branch)}})
		return
	}
	if f.serveGit(w, r, base, send, fail) {
		return
	}
	if strings.HasPrefix(r.URL.Path, base+"/git/blobs/") {
		sha := strings.TrimPrefix(r.URL.Path, base+"/git/blobs/")
		// GitHub serves any blob the repository has by its SHA, one a
		// later commit no longer holds too.
		b, ok := f.blobs[sha]
		for _, o := range f.objects {
			if !ok && githubSHA(o) == sha {
				b, ok = o, true
			}
		}
		if ok && r.Header.Get("Accept") == "application/vnd.github.raw+json" {
			w.Write(b)
			return
		}
		fail(404, "Not Found")
		return
	}
	path, ok := strings.CutPrefix(r.URL.Path, base+"/contents/")
	if !ok {
		fail(404, "Not Found")
		return
	}
	if r.Method == http.MethodGet {
		if f.empty {
			fail(404, "This repository is empty.")
			return
		}
		branch, ok := f.branchAt(r.URL.Query().Get("ref"))
		if !ok {
			fail(404, "No commit found for the ref")
			return
		}
		b, ok := f.objects[branch+"|"+path]
		if ok {
			c := githubContent{Type: "file", SHA: githubSHA(b), Size: int64(len(b)), Encoding: "base64", Content: base64.StdEncoding.EncodeToString(b)}
			if len(b) > 1<<20 {
				c.Encoding, c.Content = "none", ""
			}
			send(200, c)
			return
		}
		var entries []githubContent
		for key, b := range f.objects {
			if name, ok := strings.CutPrefix(key, branch+"|"+path+"/"); ok && !strings.Contains(name, "/") {
				entries = append(entries, githubContent{Type: "file", Name: name, SHA: githubSHA(b), Size: int64(len(b))})
			}
		}
		if len(entries) > 0 {
			send(200, entries)
			return
		}
		fail(404, "Not Found")
		return
	}
	var in struct {
		Message, Branch, SHA, Content string
		Author, Committer             *fixtureIdent
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Message == "" {
		fail(422, "Invalid request")
		return
	}
	f.by("contents", in.Author, in.Committer)
	initialBranch := in.Branch
	if in.Branch == "" {
		in.Branch = f.defaultBranch
	}
	key := in.Branch + "|" + path
	old, exists := f.objects[key]
	if exists && in.SHA == "" {
		fail(422, "Invalid request.\n\n\"sha\" wasn't supplied.")
		return
	}
	if exists && in.SHA != githubSHA(old) || !exists && in.SHA != "" {
		fail(409, "the SHA does not match")
		return
	}
	if r.Method == http.MethodDelete {
		delete(f.objects, key)
		f.advance(in.Branch)
		f.writes++
		send(200, map[string]any{"content": nil})
		return
	}
	b, err := base64.StdEncoding.DecodeString(in.Content)
	if err != nil || r.Method != http.MethodPut {
		fail(422, "Invalid content")
		return
	}
	if len(b) > f.contentsLimit {
		fail(422, "Sorry, the file is too large to be processed. Consider creating/updating the file in a local clone and pushing it to GitHub.")
		return
	}
	f.objects[key] = b
	f.advance(in.Branch)
	f.writes++
	if f.empty {
		f.initialBranch = initialBranch
		f.empty = false
	}
	status := http.StatusOK
	if !exists {
		status = http.StatusCreated
	}
	send(status, map[string]any{"content": githubContent{Type: "file", SHA: githubSHA(b), Size: int64(len(b))}})
}

// serveGit answers the Git Data API as GitHub does: blobs, trees on a base
// tree, commits, and a ref update that refuses anything but a fast-forward.
func (f *githubFixture) serveGit(w http.ResponseWriter, r *http.Request, base string, send func(int, any), fail func(int, string)) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, base+"/git/")
	if !ok || strings.HasPrefix(rest, "blobs/") && r.Method == http.MethodGet {
		return false
	}
	if f.empty {
		fail(409, "Git Repository is empty.")
		return true
	}
	switch {
	case rest == "blobs" && r.Method == http.MethodPost:
		var in struct{ Content, Encoding string }
		json.NewDecoder(r.Body).Decode(&in)
		b, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil || in.Encoding != "base64" {
			fail(422, "Invalid blob")
			return true
		}
		if len(b) > f.blobLimit {
			fail(422, "Sorry, your input was too large to process. Consider creating the blob in a local clone of the repository and then pushing it to GitHub.")
			return true
		}
		f.biggestBlob = max(f.biggestBlob, len(b))
		f.blobs[githubSHA(b)] = b
		send(201, map[string]string{"sha": githubSHA(b)})
	case strings.HasPrefix(rest, "commits/") && r.Method == http.MethodGet:
		c, ok := f.commits[strings.TrimPrefix(rest, "commits/")]
		if !ok {
			fail(404, "Not Found")
			return true
		}
		send(200, map[string]any{"tree": map[string]string{"sha": c.tree}})
	case rest == "trees" && r.Method == http.MethodPost:
		var in struct {
			BaseTree string `json:"base_tree"`
			Tree     []struct {
				Path, Mode, Type string
				SHA              *string
			}
		}
		raw, _ := io.ReadAll(r.Body)
		if json.Unmarshal(raw, &in) != nil || in.BaseTree == "" || len(in.Tree) == 0 || !strings.Contains(string(raw), `"sha":`) {
			fail(422, "Invalid tree")
			return true
		}
		var t fixtureTree
		t.base = in.BaseTree
		for _, e := range in.Tree {
			if e.Mode != "100644" || e.Type != "blob" || e.SHA != nil && f.blobs[*e.SHA] == nil {
				fail(422, "Invalid tree entry")
				return true
			}
			t.entries = append(t.entries, struct {
				Path string
				SHA  *string
			}{e.Path, e.SHA})
		}
		f.made++
		sha := fmt.Sprintf("%040x", f.made)
		f.trees[sha] = t
		send(201, map[string]string{"sha": sha})
	case rest == "commits" && r.Method == http.MethodPost:
		var in struct {
			Message, Tree     string
			Parents           []string
			Author, Committer *fixtureIdent
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.by("git/commits", in.Author, in.Committer)
		if _, ok := f.trees[in.Tree]; !ok || in.Message == "" || len(in.Parents) != 1 {
			fail(422, "Invalid commit")
			return true
		}
		f.made++
		sha := fmt.Sprintf("%040x", f.made)
		f.commits[sha] = fixtureCommit{tree: in.Tree, parent: in.Parents[0]}
		send(201, map[string]string{"sha": sha})
	case strings.HasPrefix(rest, "refs/heads/") && r.Method == http.MethodPatch:
		branch := strings.TrimPrefix(rest, "refs/heads/")
		var in struct {
			SHA   string
			Force *bool
		}
		json.NewDecoder(r.Body).Decode(&in)
		if f.beforeRef != nil {
			f.beforeRef()
		}
		c, ok := f.commits[in.SHA]
		if f.heads[branch] == "" || !ok {
			fail(422, "Reference does not exist")
			return true
		}
		if in.Force == nil || *in.Force || c.parent != f.heads[branch] {
			fail(422, "Update is not a fast forward")
			return true
		}
		t := f.trees[c.tree]
		if t.base != f.commits[c.parent].tree {
			fail(422, "the fixture builds only on the parent's tree")
			return true
		}
		for _, e := range t.entries {
			if e.SHA == nil {
				delete(f.objects, branch+"|"+e.Path)
			} else {
				f.objects[branch+"|"+e.Path] = f.blobs[*e.SHA]
			}
		}
		f.heads[branch] = in.SHA
		f.writes++
		send(200, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]string{"sha": in.SHA}})
	default:
		fail(404, "Not Found")
	}
	return true
}

func githubConfig() Config {
	return Config{URL: "github://alice/private/team", Password: "fixture-github-token",
		Passphrase: "fixture-independent-passphrase", Keys: true, Agents: true}
}

// GitHub uses the same damaged-file diagnostics as WebDAV and S3, but
// the recovery command and location must name the configured repository.
func TestGitHubNotBackup(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		replace    bool
	}{
		{"empty", "", true},
		{"another app", `{"format":"another-app"}`, true},
		{"web page", "<html><title>Sign in</title></html>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGithubFixture(t)
			c := githubConfig()
			f.objects["main|team/magpie/"+file] = []byte(tc.body)
			newComputer(t).use(t)
			if err := Configure(c); err != nil {
				t.Fatal(err)
			}
			err := Now(context.Background())
			if err == nil || !strings.Contains(err.Error(), "GitHub") || !strings.Contains(err.Error(), "alice/private") || strings.Contains(err.Error(), "WebDAV") {
				t.Fatalf("the recovery advice should name the GitHub repository: %v", err)
			}
			if tc.replace && !strings.Contains(err.Error(), "team/magpie/"+file) || strings.Contains(err.Error(), "magpie/magpie/") {
				t.Fatalf("the advice should name the file where it is, team/magpie/%s: %v", file, err)
			}
			v := Status()
			if v.File == nil || v.File.Replace != tc.replace {
				t.Fatalf("the file's replacement status: %+v", v.File)
			}
			if v.File.Replace && !strings.Contains(err.Error(), "magpie github upload") {
				t.Fatalf("the recovery advice should name the GitHub upload command: %v", err)
			}
			if f.writes != 0 {
				t.Fatal("a failed sync replaced the repository's file")
			}
			if tc.replace {
				if err := Upload(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, err := backup.Open(f.objects["main|team/magpie/"+file], c.Passphrase); err != nil {
					t.Fatalf("the uploaded GitHub backup doesn't open: %v", err)
				}
			}
		})
	}
}

func TestGitHubRemote(t *testing.T) {
	f := newGithubFixture(t)
	c := githubConfig()
	c.Branch = "sync/settings"
	g, err := newGitHub(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if b, _, err := g.get(ctx, version{}); err != nil || b != nil {
		t.Fatalf("empty repository folder: %q %v", b, err)
	}
	first := []byte("sealed fixture")
	v, err := g.put(ctx, first, "")
	if err != nil || v.ETag != githubSHA(first) {
		t.Fatalf("create: %+v %v", v, err)
	}
	if b, got, err := g.get(ctx, version{}); err != nil || string(b) != string(first) || got != v {
		t.Fatalf("download: %q %+v %v", b, got, err)
	}
	if _, _, err := g.get(ctx, v); !errors.Is(err, errNotModified) {
		t.Fatalf("unchanged: %v", err)
	}
	if _, err := g.put(ctx, []byte("racing creation"), ""); !errors.Is(err, errChanged) {
		t.Fatalf("another computer created it: %v", err)
	}
	if _, err := g.put(ctx, []byte("stale"), "old-sha"); !errors.Is(err, errChanged) {
		t.Fatalf("another computer updated it: %v", err)
	}
	large := []byte(strings.Repeat("encrypted", 140000))
	v, err = g.put(ctx, large, v.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if b, _, err := g.get(ctx, version{}); err != nil || string(b) != string(large) {
		t.Fatalf("a backup larger than 1 MB: size %d, %v", len(b), err)
	}
	if _, ok := f.objects["main|team/magpie/"+file]; ok {
		t.Fatal("the selected branch was ignored")
	}
	if len(f.objects["sync/settings|team/magpie/"+file]) != len(large) {
		t.Fatal("the prefix or branch was ignored")
	}
	for _, name := range []string{"computer-2026-10-06.magpie-usage", "computer.magpie-quotas"} {
		if err := g.write(ctx, name, first); err != nil {
			t.Fatal(err)
		}
		if err := g.write(ctx, name, []byte("updated")); err != nil {
			t.Fatal(err)
		}
		if b, err := g.read(ctx, name); err != nil || string(b) != "updated" {
			t.Fatalf("usage read: %q %v", b, err)
		}
	}
	list, err := g.list(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("usage listing: %v %v", list, err)
	}
	if err := g.remove(ctx, "computer-2026-10-06.magpie-usage"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.objects["sync/settings|team/magpie/usage/computer-2026-10-06.magpie-usage"]; ok {
		t.Fatal("the usage file was not removed")
	}
	if err := g.remove(ctx, "computer-2026-10-06.magpie-usage"); err != nil {
		t.Fatalf("already removed: %v", err)
	}
}

func TestGitHubSync(t *testing.T) {
	f := newGithubFixture(t)
	ctx := context.Background()
	c := githubConfig()
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "fixture-provider-key"}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	sealed := f.objects["main|team/magpie/"+file]
	bundle, err := backup.Open(sealed, c.Passphrase)
	if err != nil || len(bundle.Providers) != 1 || strings.Contains(string(sealed), "fixture-provider-key") {
		t.Fatalf("encrypted backup on GitHub: %v", err)
	}
	before := f.writes
	if err := Now(ctx); err != nil || f.writes != before {
		t.Fatalf("unchanged setup committed again: %v, writes %d", err, f.writes-before)
	}
	b.use(t)
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil || !slices.Equal(ids(), []string{"deepseek=fixture-provider-key"}) {
		t.Fatalf("the second computer joins: %v, providers %v", err, ids())
	}
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "fixture-second-key"}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	a.use(t)
	if err := Now(ctx); err != nil || len(ids()) != 2 {
		t.Fatalf("the first computer receives the second's change: %v, %v", err, ids())
	}
	if err := provider.Delete("kimi"); err != nil {
		t.Fatal(err)
	}
	parts, err := Restore(ctx)
	if err != nil || !slices.Contains(parts, "providers") || len(ids()) != 2 {
		t.Fatalf("restore from GitHub: %v %v %v", parts, err, ids())
	}
	if _, err := Undo(); err != nil || len(ids()) != 1 {
		t.Fatalf("undo GitHub restore: %v %v", err, ids())
	}
}

func TestGitHubEmptyRepository(t *testing.T) {
	for _, tc := range []struct{ defaultBranch, branch string }{
		{defaultBranch: "main"}, {defaultBranch: "main", branch: "main"},
		{defaultBranch: "trunk"}, {defaultBranch: "trunk", branch: "trunk"},
	} {
		t.Run("default="+tc.defaultBranch+"/branch="+tc.branch, func(t *testing.T) {
			f := newGithubFixture(t)
			f.empty, f.defaultBranch = true, tc.defaultBranch
			newComputer(t).use(t)
			c := githubConfig()
			c.Branch = tc.branch
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "fixture-provider-key"}); err != nil {
				t.Fatal(err)
			}
			if err := Configure(c); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := Now(ctx); err != nil {
				t.Fatalf("the first encrypted backup must initialize the empty repository: %v", err)
			}
			sealed := f.objects[tc.defaultBranch+"|team/magpie/"+file]
			bundle, err := backup.Open(sealed, c.Passphrase)
			if err != nil || len(bundle.Providers) != 1 || strings.Contains(string(sealed), "fixture-provider-key") {
				t.Fatalf("the initial commit must contain an encrypted backup: %v", err)
			}
			if f.empty || f.writes != 1 || len(f.objects) != 1 || f.initialBranch != "" {
				t.Fatalf("the backup itself must initialize the default branch: empty %v, writes %d, files %d, branch %q", f.empty, f.writes, len(f.objects), f.initialBranch)
			}
			if err := Now(ctx); err != nil || f.writes != 1 {
				t.Fatalf("the unchanged second sync must not create another commit: %v, writes %d", err, f.writes)
			}
		})
	}
}

func TestGitHubEmptyRepositoryFailures(t *testing.T) {
	for _, tc := range []struct {
		name, branch, refError, want string
		empty                        bool
	}{
		{name: "empty repository with another branch", branch: "new-backup", empty: true, want: "default branch"},
		{name: "existing repository with a missing branch", branch: "new-backup", want: "HTTP 404"},
		{name: "another ref conflict", refError: "Git Database is unavailable.", want: "Git Database is unavailable."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGithubFixture(t)
			f.empty, f.refError = tc.empty, tc.refError
			c := githubConfig()
			c.Branch = tc.branch
			g, err := newGitHub(c)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = g.get(context.Background(), version{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || f.writes != 0 {
				t.Fatalf("an inaccessible branch must not become an empty backup: %v, writes %d", err, f.writes)
			}
		})
	}
	t.Run("initial creation races another computer", func(t *testing.T) {
		f := newGithubFixture(t)
		f.empty = true
		g, err := newGitHub(githubConfig())
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, _, err := g.get(ctx, version{}); err != nil {
			t.Fatal(err)
		}
		other := []byte("another computer's sealed backup")
		f.mu.Lock()
		f.empty = false
		f.objects["main|team/magpie/"+file] = other
		f.mu.Unlock()
		if _, err := g.put(ctx, []byte("this computer's sealed backup"), ""); !errors.Is(err, errChanged) {
			t.Fatalf("a racing initial commit must require the normal merge: %v", err)
		}
		if f.writes != 0 || string(f.objects["main|team/magpie/"+file]) != string(other) {
			t.Fatal("the racing computer's backup was overwritten")
		}
	})
}

func TestGitHubConfig(t *testing.T) {
	newComputer(t).use(t)
	c := githubConfig()
	for _, address := range []string{"github://alice", "github://alice/private/../other", "github://alice/private/a//b",
		"github://alice/private?token=x", "github://user:secret@alice/private", "github://alice:443/private", "github://./private"} {
		bad := c
		bad.URL = address
		if err := Check(bad); err == nil {
			t.Errorf("accepted invalid address %s", address)
		}
	}
	dav := Config{URL: "https://dav.example/dav", User: "user", Password: "dav-secret", Passphrase: c.Passphrase}
	s3 := Config{URL: "s3://bucket/team", User: "AKID", Password: "s3-secret", Passphrase: c.Passphrase}
	for _, cfg := range []Config{dav, s3, c} {
		if err := Configure(cfg); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := Load()
	for _, kind := range []string{"webdav", "s3"} {
		if s, ok := current.Kept(kind); !ok || s.Password == "" {
			t.Fatalf("lost the %s setup while adding a third kind", kind)
		}
	}
	view, _ := json.Marshal(Status())
	if strings.Contains(string(view), c.Password) || strings.Contains(string(view), "dav-secret") || strings.Contains(string(view), c.Passphrase) {
		t.Fatal("sync status exposed a credential")
	}
	next := c
	next.URL, next.Branch, next.Password, next.Passphrase = "github://alice/private/other", "sync/settings", "", ""
	if err := Configure(next); err != nil {
		t.Fatalf("same repository keeps its token: %v", err)
	}
	current, _ = Load()
	if current.Password != c.Password || current.Branch != next.Branch || current.Passphrase != c.Passphrase {
		t.Fatal("did not keep the token, branch or passphrase")
	}
	next.URL = "github://alice/another"
	if err := Configure(next); err == nil {
		t.Fatal("reused the saved token for another repository")
	}
	same := c
	same.Passphrase = c.Password
	if err := Configure(same); err == nil {
		t.Fatal("accepted the GitHub token as the encryption passphrase")
	}
	dav.Password, dav.Passphrase = "", ""
	if err := Configure(dav); err != nil {
		t.Fatalf("return to the first of three kinds: %v", err)
	}
	current, _ = Load()
	if current.Password != "dav-secret" {
		t.Fatal("did not restore the original WebDAV credential")
	}
}

func TestGitHubFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{{401, "token"}, {403, "Contents"}, {404, "repository or branch"}, {422, "HTTP 422"}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := newGithubFixture(t)
			f.status, f.message = tc.status, "fixture refusal"
			g, _ := newGitHub(githubConfig())
			_, _, err := g.get(context.Background(), version{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || f.writes != 0 {
				t.Fatalf("failure treated as an empty remote: %v, writes %d", err, f.writes)
			}
		})
	}
	res := &http.Response{StatusCode: 403, Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "Retry-After": {"120"}},
		Body: io.NopCloser(strings.NewReader(`{"message":"API rate limit exceeded"}`))}
	var limited *rateLimited
	if err := githubError(res); !errors.As(err, &limited) || limited.after != 2*time.Minute {
		t.Fatalf("rate limit without backoff: %v", err)
	}
	t.Run("protected branch", func(t *testing.T) {
		f := newGithubFixture(t)
		g, _ := newGitHub(githubConfig())
		if err := g.prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.status, f.message = 422, "Protected branch update failed: changes must be made through a pull request"
		f.mu.Unlock()
		_, err := g.put(context.Background(), []byte("encrypted fixture"), "")
		if err == nil || errors.Is(err, errChanged) || !strings.Contains(err.Error(), "Protected branch") {
			t.Fatalf("repository rules treated as a racing file creation: %v", err)
		}
	})
	t.Run("token cannot follow an external redirect", func(t *testing.T) {
		f := newGithubFixture(t)
		f.redirect = "https://outside.api.github.com/token"
		g, _ := newGitHub(githubConfig())
		_, _, err := g.get(context.Background(), version{})
		if err == nil || !strings.Contains(err.Error(), "refused a redirect outside") {
			t.Fatalf("external redirect was followed: %v", err)
		}
	})
}

// A public repository would give anyone every sealed version of the setup,
// its whole history, to try passphrases on: sync refuses it before writing.
func TestGitHubPublicRepositoryRefused(t *testing.T) {
	f := newGithubFixture(t)
	f.public = true
	newComputer(t).use(t)
	if err := Configure(githubConfig()); err != nil {
		t.Fatal(err)
	}
	err := Now(context.Background())
	if err == nil || !strings.Contains(err.Error(), "public") || !strings.Contains(err.Error(), "alice/private") {
		t.Fatalf("a public repository should be refused by name: %v", err)
	}
	if f.writes != 0 || len(f.objects) != 0 {
		t.Fatalf("a public repository got the backup: writes %d, files %d", f.writes, len(f.objects))
	}
}

// Zhang-Wei's backup (102 skills) passed the Contents API at about 19 MB
// and then grew past it: GitHub answered 422 "Sorry, the file is too large
// to be processed". A backup goes up as a blob, one commit per update, and
// is read back by its blob SHA.
func TestGitHubLargeBackup(t *testing.T) {
	f := newGithubFixture(t)
	f.contentsLimit = 10 << 20
	g, err := newGitHub(githubConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	small := []byte("an earlier sealed backup")
	v, err := g.put(ctx, small, "")
	if err != nil {
		t.Fatal(err)
	}
	large := make([]byte, 15<<20) // over the Contents API's, under githubPart
	for i := range large {
		large[i] = byte(i*7919 + i>>11)
	}
	before := f.writes
	v, err = g.put(ctx, large, v.ETag)
	if err != nil {
		t.Fatalf("a 15 MB backup: %v", err)
	}
	if v.ETag != githubSHA(large) || f.writes != before+1 {
		t.Fatalf("one commit per update, versioned by the blob: %+v, commits %d", v, f.writes-before)
	}
	if !bytesEqual(f.objects["main|team/magpie/"+file], large) {
		t.Fatal("the branch doesn't hold the large backup")
	}
	b, got, err := g.get(ctx, version{})
	if err != nil || got != v || !bytesEqual(b, large) {
		t.Fatalf("reading the 15 MB backup back: size %d, %+v, %v", len(b), got, err)
	}
	if !slices.Contains(f.requests, "GET /repos/alice/private/git/blobs/"+v.ETag) {
		t.Fatal("a file over 1 MB must be downloaded by its blob SHA")
	}
	for _, r := range f.requests {
		if strings.HasPrefix(r, "PUT ") || strings.HasPrefix(r, "DELETE ") {
			t.Fatalf("a repository with commits was written through the Contents API: %s", r)
		}
	}
	if _, err := g.put(ctx, small, "old-sha"); !errors.Is(err, errChanged) || !bytesEqual(f.objects["main|team/magpie/"+file], large) {
		t.Fatalf("a stale version must not replace the backup: %v", err)
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

// The ref update refuses a head that moved after it was read. A commit of
// another file is built on again; one that changed the backup is the
// normal merge.
func TestGitHubBranchMovedMeanwhile(t *testing.T) {
	setup := func(t *testing.T) (*githubFixture, *github, version) {
		f := newGithubFixture(t)
		g, err := newGitHub(githubConfig())
		if err != nil {
			t.Fatal(err)
		}
		v, err := g.put(context.Background(), []byte("first"), "")
		if err != nil {
			t.Fatal(err)
		}
		return f, g, v
	}
	t.Run("another file", func(t *testing.T) {
		f, g, v := setup(t)
		f.beforeRef = func() {
			f.beforeRef = nil
			f.objects["main|team/magpie/usage/other.magpie-usage"] = []byte("other computer")
			f.advance("main")
		}
		if _, err := g.put(context.Background(), []byte("second"), v.ETag); err != nil {
			t.Fatal(err)
		}
		if string(f.objects["main|team/magpie/"+file]) != "second" || string(f.objects["main|team/magpie/usage/other.magpie-usage"]) != "other computer" {
			t.Fatal("both commits must be on the branch")
		}
	})
	t.Run("the backup", func(t *testing.T) {
		f, g, v := setup(t)
		f.beforeRef = func() {
			f.beforeRef = nil
			f.objects["main|team/magpie/"+file] = []byte("other computer's backup")
			f.advance("main")
		}
		if _, err := g.put(context.Background(), []byte("second"), v.ETag); !errors.Is(err, errChanged) {
			t.Fatalf("another computer's backup must go through the merge: %v", err)
		}
		if string(f.objects["main|team/magpie/"+file]) != "other computer's backup" {
			t.Fatal("another computer's backup was written over")
		}
	})
	t.Run("branch deleted", func(t *testing.T) {
		f, g, v := setup(t)
		f.beforeRef = func() { delete(f.heads, "main") }
		_, err := g.put(context.Background(), []byte("second"), v.ETag)
		if err == nil || errors.Is(err, errChanged) || !strings.Contains(err.Error(), "Reference does not exist") {
			t.Fatalf("a deleted branch must be an error, not a merge: %v", err)
		}
	})
}

// An empty repository has no branch for the Git Data API to build on. A
// first backup too large for the Contents API follows a short note that
// creates the default branch.
func TestGitHubEmptyRepositoryLargeBackup(t *testing.T) {
	f := newGithubFixture(t)
	f.empty, f.contentsLimit = true, 1<<10
	g, err := newGitHub(githubConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := g.get(ctx, version{}); err != nil {
		t.Fatal(err)
	}
	large := []byte(strings.Repeat("sealed ", 1000))
	v, err := g.put(ctx, large, "")
	if err != nil {
		t.Fatalf("a large first backup in an empty repository: %v", err)
	}
	if f.writes != 2 || len(f.objects["main|team/magpie/README.md"]) == 0 || string(f.objects["main|team/magpie/"+file]) != string(large) {
		t.Fatalf("a note, then the backup: commits %d, files %v", f.writes, len(f.objects))
	}
	if b, got, err := g.get(ctx, version{}); err != nil || got != v || string(b) != string(large) {
		t.Fatalf("reading it back: %v", err)
	}
}

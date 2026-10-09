package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub answers as GitHub's search, raw.githubusercontent.com and npm
// do, from testdata/github (npm's real answers for the packages it has): search.json is a real answer for a topic (five
// repositories), with three more items made from its first — a fork, an
// archived one and magpie-community's — and someone/word-guard, whose
// package.json is word-guard's own, a middleware alone; alfaoz's
// opencode-see-image (real), whose main is a dist the repository hasn't;
// and someone/flaky-entry, whose main GitHub doesn't answer for.
// reads counts what fakeGitHub answered other than searches: package.json,
// npm and entry files.
var reads atomic.Int32

func fakeGitHub(t *testing.T, up *atomic.Bool, asked *atomic.Int32) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MAGPIE_PLUGIN_MARKET", "")
	b, err := os.ReadFile("testdata/github/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	items := r["items"].([]any)
	like := func(full string, set map[string]any) map[string]any {
		var it map[string]any
		raw, _ := json.Marshal(items[0])
		_ = json.Unmarshal(raw, &it)
		it["full_name"], it["html_url"] = full, "https://github.com/"+full
		it["owner"].(map[string]any)["login"] = strings.Split(full, "/")[0]
		for k, v := range set {
			it[k] = v
		}
		return it
	}
	items = append(items,
		like("someone/word-guard", map[string]any{"description": "word guard", "stargazers_count": 3, "license": nil}),
		like("alfaoz/opencode-see-image", nil),
		like("someone/flaky-entry", map[string]any{"license": map[string]any{"key": "other", "spdx_id": "NOASSERTION"}}),
		like("cyberElar/magpie-x-search", nil),
		like("forker/opencode-claude-auth", map[string]any{"fork": true}),
		like("old/opencode-old", map[string]any{"archived": true}),
		like("magpie-community/plugins", nil))
	r["items"] = items
	search, _ := json.Marshal(r)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path != "/search/repositories" {
			reads.Add(1)
		}
		if !up.Load() || q.URL.Path == "/raw/someone/flaky-entry/main/index.js" {
			http.Error(w, "down", 503)
			return
		}
		if q.URL.Path == "/search/repositories" {
			asked.Add(1)
			if got := q.URL.Query().Get("q"); got != "topic:magpie-plugin archived:false fork:false" {
				t.Errorf("searched %q", got)
			}
			w.Write(search)
			return
		}
		if name, ok := strings.CutPrefix(q.URL.Path, "/npm/"); ok {
			f, err := os.ReadFile(filepath.Join("testdata/github/npm", strings.ReplaceAll(strings.TrimSuffix(name, "/latest"), "/", "_")+".json"))
			if err != nil {
				http.Error(w, `"Not Found"`, 404)
				return
			}
			w.Write(f)
			return
		}
		f, err := os.ReadFile(filepath.Join("testdata/github/raw", filepath.FromSlash(strings.TrimPrefix(q.URL.Path, "/raw/"))))
		if err != nil {
			http.NotFound(w, q)
			return
		}
		w.Write(f)
	}))
	t.Cleanup(srv.Close)
	api, raw, reg := githubAPI, githubRaw, npmRegistry
	githubAPI, githubRaw, npmRegistry = srv.URL, srv.URL+"/raw", srv.URL+"/npm"
	forget := func() {
		taggedMu.Lock()
		taggedL = nil
		taggedMu.Unlock()
		readMu.Lock()
		clear(readRepo)
		readMu.Unlock()
	}
	forget()
	reads.Store(0)
	t.Cleanup(func() { githubAPI, githubRaw, npmRegistry = api, raw, reg; forget() })
}

// Repositories tagged magpie-plugin: each with what its package.json says,
// in GitHub's order; forks, archived ones and magpie-community's left out.
// Asked once in six hours; with GitHub down, what it said last.
func TestTaggedRepos(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	ctx := context.Background()

	l := TaggedRepos(ctx)
	var repos []string
	for _, x := range l {
		repos = append(repos, x.Repo)
	}
	// iPolloWork and learn-opencode (a workspace and a course) have no
	// index.js to load, nor see-image its dist: installed from GitHub,
	// none would load. magpie-x-search (real, #1327) is on npm from its
	// repository, but a command alone, an MCP server: npm's copy names no
	// file to load, and it has no index.js
	want := "rynfar/meridian griffinmartin/opencode-claude-auth slkiser/opencode-quota someone/word-guard someone/flaky-entry"
	if strings.Join(repos, " ") != want {
		t.Fatalf("repos %v\nwant %s", repos, want)
	}
	byRepo := map[string]Tagged{}
	for _, x := range l {
		byRepo[x.Repo] = x
	}
	c := byRepo["griffinmartin/opencode-claude-auth"]
	if c.Spec != "opencode-claude-auth" || c.Package != "opencode-claude-auth" || c.Version != "2.2.1" ||
		c.Owner != "griffinmartin" || c.License != "MIT" || c.Stars != 1301 || c.Kind != "" || c.Pushed.IsZero() || c.URL != "https://github.com/griffinmartin/opencode-claude-auth" {
		t.Fatalf("claude auth: %+v", c)
	}
	if x := byRepo["someone/flaky-entry"]; x.License != "" {
		t.Fatalf("NOASSERTION is no license: %+v", x)
	}
	if x := byRepo["someone/word-guard"]; x.Kind != "middleware" || x.Package != "@magpie-community/middleware-word-guard" || x.License != "" {
		t.Fatalf("a middleware alone: %+v", x)
	}
	// npm's copy when npm has it from this very repository (its build is
	// on npm only); else the repository: not on npm, or npm's is another's
	for repo, spec := range map[string]string{
		"rynfar/meridian": "@rynfar/meridian", "slkiser/opencode-quota": "@slkiser/opencode-quota",
		"someone/word-guard": "github:someone/word-guard", "someone/flaky-entry": "github:someone/flaky-entry",
	} {
		if x := byRepo[repo]; x.Spec != spec {
			t.Errorf("%s installs as %q, want %q", repo, x.Spec, spec)
		}
	}
	if x := byRepo["someone/word-guard"]; !IsGit(x.Spec) {
		t.Fatalf("%s isn't a spec Add installs from git", x.Spec)
	}

	TaggedRepos(ctx)
	if n := asked.Load(); n != 1 {
		t.Fatalf("asked %d times in ten minutes", n)
	}

	// ten minutes on: GitHub's search asked again, so a repository tagged
	// since shows; the repositories it read already aren't read again,
	// but for flaky-entry, whose entry GitHub didn't answer for
	taggedMu.Lock()
	taggedAt = time.Now().Add(-taggedTTL - time.Second)
	taggedMu.Unlock()
	before := reads.Load()
	if l := TaggedRepos(ctx); len(l) != 5 || asked.Load() != 2 {
		t.Fatalf("ten minutes on: asked %d times, %d repos", asked.Load(), len(l))
	}
	if n := reads.Load() - before; n != 3 {
		t.Fatalf("ten minutes on: %d reads, want flaky-entry's package.json, npm and entry again", n)
	}
	asked.Store(1)

	// started again within the six hours: the copy on disk, GitHub not
	// asked
	forget := func() { taggedMu.Lock(); taggedL = nil; taggedMu.Unlock() }
	forget()
	if err := os.Chtimes(taggedCache(), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if l := TaggedRepos(ctx); len(l) != 5 || asked.Load() != 1 {
		t.Fatalf("restarted: asked %d times, %d repos", asked.Load(), len(l))
	}

	// started again later, with GitHub down: the copy on disk all the same
	old := time.Now().Add(-7 * time.Hour)
	if err := os.Chtimes(taggedCache(), old, old); err != nil {
		t.Fatal(err)
	}
	up.Store(false)
	forget()
	if l := TaggedRepos(ctx); len(l) != 5 || l[1].Package != "opencode-claude-auth" || asked.Load() != 1 {
		t.Fatalf("down: %+v", l)
	}

	// nothing ever kept, GitHub down: none, not an error
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	taggedMu.Lock()
	taggedL = nil
	taggedMu.Unlock()
	if l := TaggedRepos(ctx); l == nil || len(l) != 0 {
		t.Fatalf("down, nothing kept: %#v", l)
	}

	// off: GitHub never asked
	up.Store(true)
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	taggedMu.Lock()
	taggedL = nil
	taggedMu.Unlock()
	before = asked.Load()
	if l := TaggedRepos(ctx); len(l) != 0 || asked.Load() != before {
		t.Fatalf("off: %+v", l)
	}
}

// A tagged repository's page, before it is installed, is its README on
// GitHub; installed, the folder's.
func TestReadmeOfTaggedRepo(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := Readme(context.Background(), "github:griffinmartin/opencode-claude-auth")
	if err != nil || !strings.Contains(p.Readme, "opencode-claude-auth") {
		t.Fatalf("readme %q, %v", p.Readme[:min(80, len(p.Readme))], err)
	}
	if _, err := Readme(context.Background(), "github:rynfar/meridian"); err == nil {
		t.Fatal("a repository with no README read as one")
	}
	for spec, want := range map[string]string{
		"github:a/b": "a/b", "github:a/b#v1": "a/b", "https://github.com/a/b": "a/b", "https://github.com/a/b.git": "a/b",
		"https://github.com/a/b/tree/main": "a/b", "gitlab:a/b": "", "github:a": "",
	} {
		if got := githubRepo(spec); got != want {
			t.Errorf("githubRepo(%q) = %q, want %q", spec, got, want)
		}
	}
}

// The file importing a package loads, as Bun resolves it.
func TestPkgEntry(t *testing.T) {
	for _, c := range []struct{ exports, main, want string }{
		{"", "", "index.js"},
		{"", "./dist/index.js", "./dist/index.js"},
		{`"./x.js"`, "./dist/index.js", "./x.js"},
		{`{".": "./a.js", "./b": "./b.js"}`, "", "./a.js"},
		{`{".": {"types": "./a.d.ts", "import": "./a.mjs", "require": "./a.cjs"}}`, "", "./a.mjs"},
		{`{"import": "./a.mjs", "default": "./a.js"}`, "", "./a.mjs"},
		{`{"./server": "./s.js"}`, "./m.js", "./m.js"},
	} {
		if got := pkgEntry(json.RawMessage(c.exports), c.main); got != c.want {
			t.Errorf("pkgEntry(%s, %q) = %q, want %q", c.exports, c.main, got, c.want)
		}
	}
}

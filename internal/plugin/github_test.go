package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub answers as GitHub's search, raw.githubusercontent.com and npm
// do, from testdata/github (npm's real answers for the packages it has): search.json is a real answer for a topic (five
// repositories), with three more items made from its first — a fork, an
// archived one and magpie-community's — and someone/word-guard, whose
// package.json is word-guard's own, a middleware alone; someone/agent-aider,
// magpie-community's agent-aider package.json and module, an agent alone;
// someone/page-glance, a page alone (internal/gui/testdata/page-plugin's
// package.json), and someone/page-gone, whose page file isn't there; alfaoz's
// opencode-see-image (real), whose main is a dist the repository hasn't;
// and someone/flaky-entry, whose main GitHub doesn't answer for.
// reads counts what fakeGitHub answered other than searches: package.json,
// npm and entry files.
var reads atomic.Int32

// held is a path fakeGitHub doesn't answer until its channel closes: a
// read through a slow proxy.
type held struct {
	path string
	go_  chan struct{}
}

var holding atomic.Pointer[held]

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
		like("someone/agent-aider", nil),
		like("someone/page-glance", nil),
		like("someone/page-gone", nil),
		like("alfaoz/opencode-see-image", nil),
		like("someone/flaky-entry", map[string]any{"license": map[string]any{"key": "other", "spdx_id": "NOASSERTION"}}),
		like("cyberElar/magpie-x-search", nil),
		like("forker/opencode-claude-auth", map[string]any{"fork": true}),
		like("old/opencode-old", map[string]any{"archived": true}),
		like("magpie-community/plugins", nil))
	// one page of the search, as GitHub cuts it
	search := func(q url.Values) []byte {
		per, _ := strconv.Atoi(q.Get("per_page"))
		page, _ := strconv.Atoi(q.Get("page"))
		from, to := min(max(page-1, 0)*per, len(items)), len(items)
		if per > 0 {
			to = min(from+per, len(items))
		}
		b, _ := json.Marshal(map[string]any{"total_count": len(items), "incomplete_results": false, "items": items[from:to]})
		return b
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path != "/search/repositories" {
			reads.Add(1)
		}
		if h := holding.Load(); h != nil && q.URL.Path == h.path {
			<-h.go_
		}
		if q.Header.Get("Authorization") == "Bearer "+staleToken {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
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
			w.Write(search(q.URL.Query()))
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
		clear(readSoon)
		readMu.Unlock()
	}
	forget()
	reads.Store(0)
	t.Cleanup(func() { githubAPI, githubRaw, npmRegistry = api, raw, reg; forget() })
}

// Repositories tagged magpie-plugin: each with what its package.json says,
// in GitHub's order; forks, archived ones and magpie-community's left out.
// Asked once in six hours; with GitHub down, what it said last.
// Every page of the search is read: with a page short of what is tagged,
// the list is the same as from one page, in GitHub's order (HoGee_xxl on X:
// 59 tagged, and the 9 past GitHub's first page of 50 never showed).
func taggedList(ctx context.Context) []Tagged {
	l, _ := TaggedRepos(ctx)
	return l
}

func TestTaggedReposEveryPage(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	was := taggedPage
	taggedPage = 4
	t.Cleanup(func() { taggedPage = was })

	var repos []string
	for _, x := range taggedList(context.Background()) {
		repos = append(repos, x.Repo)
	}
	want := "rynfar/meridian griffinmartin/opencode-claude-auth slkiser/opencode-quota someone/word-guard someone/agent-aider someone/page-glance someone/flaky-entry"
	if strings.Join(repos, " ") != want {
		t.Fatalf("repos %v\nwant %s", repos, want)
	}
	// 15 items, four to a page
	if n := asked.Load(); n != 4 {
		t.Fatalf("asked %d pages, want 4", n)
	}
}

func TestTaggedRepos(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	ctx := context.Background()

	l := taggedList(ctx)
	var repos []string
	for _, x := range l {
		repos = append(repos, x.Repo)
	}
	// iPolloWork and learn-opencode (a workspace and a course) have no
	// index.js to load, nor see-image its dist: installed from GitHub,
	// none would load. magpie-x-search (real, #1327) is on npm from its
	// repository, but a command alone, an MCP server: npm's copy names no
	// file to load, and it has no index.js
	want := "rynfar/meridian griffinmartin/opencode-claude-auth slkiser/opencode-quota someone/word-guard someone/agent-aider someone/page-glance someone/flaky-entry"
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
	// GitHub's NOASSERTION (a LICENSE file it can't name) is no license,
	// and package.json's stands in for it; one with neither shows none
	// (plugins#468)
	if x := byRepo["someone/flaky-entry"]; x.License != "MIT" {
		t.Fatalf("NOASSERTION, package.json MIT: %+v", x)
	}
	if x := byRepo["someone/page-glance"]; x.License != "" {
		t.Fatalf("NOASSERTION and no package.json license: %+v", x)
	}
	// its own picture, package.json's magpie.icon, as an installed
	// plugin's provider has it; none when it gives none
	if x := byRepo["someone/flaky-entry"]; !strings.HasPrefix(x.Icon, "data:image/svg+xml;base64,") {
		t.Fatalf("magpie.icon: %q", x.Icon)
	}
	if c.Icon != "" {
		t.Fatalf("no magpie.icon, yet %q", c.Icon)
	}
	if x := byRepo["someone/agent-aider"]; x.Kind != "agent" || x.Spec != "github:someone/agent-aider" {
		t.Fatalf("an agent alone: %+v", x)
	}
	// a page alone: no module, magpie.page's file in the repository; one
	// whose file isn't there would show nothing, and isn't listed
	if x := byRepo["someone/page-glance"]; x.Kind != "page" || x.Spec != "github:someone/page-glance" || x.Package != "magpie-usage-glance" {
		t.Fatalf("a page alone: %+v", x)
	}
	if x := byRepo["someone/word-guard"]; x.Kind != "middleware" || x.Package != "@magpie-community/middleware-word-guard" || x.License != "MIT" {
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

	taggedList(ctx)
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
	if l := taggedList(ctx); len(l) != 7 || asked.Load() != 2 {
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
	if l := taggedList(ctx); len(l) != 7 || asked.Load() != 1 {
		t.Fatalf("restarted: asked %d times, %d repos", asked.Load(), len(l))
	}

	// started again later, with GitHub down: the copy on disk all the same
	old := time.Now().Add(-7 * time.Hour)
	if err := os.Chtimes(taggedCache(), old, old); err != nil {
		t.Fatal(err)
	}
	up.Store(false)
	forget()
	// what GitHub said before, and that it couldn't be asked now
	if l, err := TaggedRepos(ctx); len(l) != 7 || l[1].Package != "opencode-claude-auth" || asked.Load() != 1 || err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("down: %v %+v", err, l)
	}
	// the minute it is held, still said
	if _, err := TaggedRepos(ctx); err == nil {
		t.Fatal("down, held: no error")
	}

	// nothing ever kept, GitHub down: none, and an error, never none
	// passed off as GitHub's answer (till on Discord: the section just
	// wasn't there)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	taggedMu.Lock()
	taggedL = nil
	taggedMu.Unlock()
	if l, err := TaggedRepos(ctx); l == nil || len(l) != 0 || err == nil {
		t.Fatalf("down, nothing kept: %v %#v", err, l)
	}
	// Try again asks GitHub at once, up again: its answer, no error
	up.Store(true)
	TaggedAskAgain()
	if l, err := TaggedRepos(ctx); len(l) != 7 || err != nil {
		t.Fatalf("again: %v, %d repos", err, len(l))
	}
	// a whole answer stands its ten minutes: Try again doesn't ask
	before = asked.Load()
	TaggedAskAgain()
	if _, err := TaggedRepos(ctx); err != nil || asked.Load() != before {
		t.Fatalf("again on a whole answer: %v, asked %d", err, asked.Load()-before)
	}

	// off: GitHub never asked
	up.Store(true)
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	taggedMu.Lock()
	taggedL = nil
	taggedMu.Unlock()
	before = asked.Load()
	if l := taggedList(ctx); len(l) != 0 || asked.Load() != before {
		t.Fatalf("off: %+v", l)
	}
}

// A repository whose package.json comes after the page stops waiting (a
// slow proxy: larchsis on X) is as the list had it last, not left out; the
// list is asked again soon, not in ten minutes; and the read, finished
// meanwhile, is what the next ask has.
func TestTaggedReposSlowRead(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	ctx := context.Background()
	if l := taggedList(ctx); len(l) != 7 {
		t.Fatalf("%d repos", len(l))
	}

	// magpie started again eleven minutes on, word-guard's package.json
	// slow to come
	wait, retry := taggedWait, taggedRetry
	taggedWait, taggedRetry = 300*time.Millisecond, time.Second
	t.Cleanup(func() { taggedWait, taggedRetry = wait, retry })
	h := &held{path: "/raw/someone/word-guard/main/package.json", go_: make(chan struct{})}
	holding.Store(h)
	let := sync.OnceFunc(func() { close(h.go_) })
	t.Cleanup(func() { let(); holding.Store(nil) })
	taggedMu.Lock()
	taggedL = nil
	taggedMu.Unlock()
	readMu.Lock()
	clear(readRepo)
	readMu.Unlock()
	old := time.Now().Add(-taggedTTL - time.Minute)
	if err := os.Chtimes(taggedCache(), old, old); err != nil {
		t.Fatal(err)
	}
	has := func(l []Tagged, repo string) *Tagged {
		for i := range l {
			if l[i].Repo == repo {
				return &l[i]
			}
		}
		return nil
	}
	cutAsked := time.Now()
	l := taggedList(ctx)
	cutDone := time.Now()
	if x := has(l, "someone/word-guard"); len(l) != 7 || x == nil || x.Kind != "middleware" {
		t.Fatalf("slow package.json: %d repos, word-guard %+v", len(l), x)
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("asked %d times", n)
	}
	// the list cut short is held for taggedRetry from when it was asked,
	// no more and no less: bounds from the clock around the ask, so no
	// slack a loaded runner could outgrow (#1889)
	taggedMu.Lock()
	until := taggedAt.Add(taggedTTL)
	taggedMu.Unlock()
	if until.Before(cutAsked.Add(taggedRetry)) || until.After(cutDone.Add(taggedRetry)) {
		t.Fatalf("a list cut short held till %v, asked between %v and %v", until, cutAsked, cutDone)
	}
	// nor is the list cut short kept ten minutes by a magpie started again
	if fi, err := os.Stat(taggedCache()); err != nil || time.Since(fi.ModTime()) < taggedTTL-taggedRetry-time.Second {
		t.Fatalf("the copy on disk stands till %v", fi.ModTime().Add(taggedTTL))
	}

	// the read comes in after the page stopped waiting, and is kept
	let()
	key := ""
	for deadline := time.Now().Add(10 * time.Second); ; {
		readMu.Lock()
		for k, v := range readRepo {
			if strings.HasPrefix(k, "someone/word-guard@") && v != nil {
				key = k
			}
		}
		readMu.Unlock()
		if key != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("word-guard's late read was never kept")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// within the ten minutes, but not yet past taggedRetry: not asked
	// again, and word-guard's package.json not read again. The poll for
	// the late read can take longer than taggedRetry on a loaded runner
	// (once it did, at load ~28, #1889), and a list cut short is then
	// asked again rightly; that it is held for taggedRetry is checked
	// above, against the clock around the ask. Here it is marked as cut
	// short now, as TaggedRepos marks one, and must not be asked again.
	taggedMu.Lock()
	taggedAt = time.Now().Add(-taggedTTL + taggedRetry)
	taggedMu.Unlock()
	taggedList(ctx)
	if n := asked.Load(); n != 2 {
		t.Fatalf("asked again at once: %d", n)
	}
	time.Sleep(taggedRetry + 100*time.Millisecond)
	before := reads.Load()
	l = taggedList(ctx)
	if n := asked.Load(); n != 3 {
		t.Fatalf("a list cut short kept: asked %d times", n)
	}
	if x := has(l, "someone/word-guard"); len(l) != 7 || x == nil || x.Package != "@magpie-community/middleware-word-guard" {
		t.Fatalf("asked again: %d repos, word-guard %+v", len(l), x)
	}
	// flaky-entry's package.json, npm and entry, as ten minutes on
	if n := reads.Load() - before; n != 3 {
		t.Fatalf("asked again: %d reads", n)
	}
	taggedMu.Lock()
	at := taggedAt
	taggedMu.Unlock()
	if time.Since(at) > time.Second {
		t.Fatalf("a whole list kept only till %v", at.Add(taggedTTL))
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

// GitHub's search answers but raw.githubusercontent.com doesn't (blocked
// on the reader's line, as it is in places where api.github.com isn't),
// and nothing was kept: the list is empty, and the error says how many
// couldn't be read and why, not that none are tagged (till on Discord:
// on their desktop the section wasn't there, on their server it had many).
// One read before is kept and isn't counted.
// staleToken is a token fakeGitHub answers 401, as GitHub does an
// expired one.
const staleToken = "stale-till"

// till on Discord: a stale GITHUB_TOKEN in the environment, which GitHub
// refuses, left the Plugins page's GitHub section saying "GitHub refused
// GITHUB_TOKEN" while GitHub answered fine without it. The search is
// asked again without the token, and the list is GitHub's.
func TestTaggedReposStaleEnvTokenDropped(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	t.Setenv("GITHUB_TOKEN", staleToken)
	t.Setenv("GH_TOKEN", "")
	l, err := TaggedRepos(context.Background())
	if err != nil || len(l) != 7 {
		t.Fatalf("with a stale GITHUB_TOKEN: %v, %d repos", err, len(l))
	}
}

// A search page slower than taggedWait still lists (MYCF711, #3923: from
// mainland China a page from api.github.com takes about 30s, and the
// page's 8s wait left the list empty). taggedWait bounds the
// repositories' reads only; a page has taggedSearch, and one that never
// answers in it is still a failure.
func TestTaggedReposSlowSearch(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	wait, search := taggedWait, taggedSearch
	taggedWait = time.Second
	t.Cleanup(func() { taggedWait, taggedSearch = wait, search })
	h := &held{path: "/search/repositories", go_: make(chan struct{})}
	holding.Store(h)
	let := sync.OnceFunc(func() { close(h.go_) })
	t.Cleanup(func() { let(); holding.Store(nil) })

	// the search answers three taggedWaits on
	time.AfterFunc(3*taggedWait, let)
	l, err := TaggedRepos(context.Background())
	var m *TaggedMissing
	if err != nil && !errors.As(err, &m) {
		t.Fatalf("a search slower than taggedWait: %v", err)
	}
	if asked.Load() != 1 || len(l) == 0 {
		t.Fatalf("asked %d, %d repos", asked.Load(), len(l))
	}

	// a search that never answers fails at taggedSearch
	let2 := make(chan struct{})
	holding.Store(&held{path: "/search/repositories", go_: let2})
	t.Cleanup(func() { close(let2) })
	taggedSearch = 300 * time.Millisecond
	taggedMu.Lock()
	taggedL = nil
	taggedMu.Unlock()
	if err := os.Remove(taggedCache()); err != nil {
		t.Fatal(err)
	}
	if _, err := TaggedRepos(context.Background()); err == nil || errors.As(err, &m) {
		t.Fatalf("a search that never answers: %v", err)
	}
}

func TestTaggedReposUnreadable(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	ctx := context.Background()
	rawWas := githubRaw
	githubRaw = "http://127.0.0.1:1/raw" // nothing listens: refused at once
	t.Cleanup(func() { githubRaw = rawWas })
	l, err := TaggedRepos(ctx)
	var m *TaggedMissing
	if len(l) != 0 || !errors.As(err, &m) || m.Missing != 12 || !strings.Contains(m.Why.Error(), "127.0.0.1:1") {
		t.Fatalf("raw unreachable: %v (%+v), %d repos", err, m, len(l))
	}
	// Try again, raw back: the list, no error
	githubRaw = rawWas
	TaggedAskAgain()
	if l, err := TaggedRepos(ctx); len(l) != 7 || err != nil {
		t.Fatalf("again: %v, %d repos", err, len(l))
	}
	// asked again, raw gone again: the seven read before are as they were,
	// and only what was never read counts as missing
	taggedMu.Lock()
	taggedAt = time.Time{}
	taggedMu.Unlock()
	readMu.Lock()
	clear(readRepo)
	readMu.Unlock()
	githubRaw = "http://127.0.0.1:1/raw"
	l, err = TaggedRepos(ctx)
	if len(l) != 7 || !errors.As(err, &m) || m.Missing != 5 {
		t.Fatalf("raw gone, seven kept: %v (%+v), %d repos", err, m, len(l))
	}
}

// soonFake is GitHub's search, raw and npm faked for someone/fresh,
// pushed a minute ago, and someone/old, an hour ago: each with
// word-guard's files (a middleware alone), npm having neither.
type soonFake struct {
	pj       atomic.Value                  // the version of fresh's package.json raw has, "" its 404
	entry    atomic.Bool                   // raw has fresh's middleware file, not its 404
	down     atomic.Bool                   // raw answers 503 for fresh's files
	hold     atomic.Pointer[chan struct{}] // raw answers fresh's package.json when it closes
	oldReads atomic.Int32                  // old's package.json read
}

func fakeSoon(t *testing.T) *soonFake {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("MAGPIE_PLUGIN_MARKET", "")
	f := &soonFake{}
	f.pj.Store("")
	var items []map[string]any
	for _, r := range []struct {
		full string
		ago  time.Duration
	}{{"someone/fresh", time.Minute}, {"someone/old", time.Hour}} {
		items = append(items, map[string]any{"full_name": r.full, "html_url": "https://github.com/" + r.full, "default_branch": "main",
			"pushed_at": time.Now().Add(-r.ago).UTC().Format(time.RFC3339), "owner": map[string]any{"login": "someone"}})
	}
	search, _ := json.Marshal(map[string]any{"total_count": len(items), "items": items})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		p := q.URL.Path
		if p == "/search/repositories" {
			w.Write(search)
			return
		}
		v := ""
		switch {
		case strings.HasPrefix(p, "/raw/someone/fresh/main/"):
			if f.down.Load() {
				http.Error(w, "down", 503)
				return
			}
			if h := f.hold.Load(); h != nil && strings.HasSuffix(p, "/package.json") {
				<-*h
			}
			if v = f.pj.Load().(string); !strings.HasSuffix(p, "/package.json") && !f.entry.Load() {
				v = ""
			}
		case strings.HasPrefix(p, "/raw/someone/old/main/"):
			v = "0.1.1"
			if strings.HasSuffix(p, "/package.json") {
				f.oldReads.Add(1)
			}
		}
		b, err := os.ReadFile("testdata/github/raw/someone/word-guard/main/" + p[strings.LastIndex(p, "/")+1:])
		if v == "" || err != nil {
			http.NotFound(w, q)
			return
		}
		w.Write([]byte(strings.Replace(string(b), `"version": "0.1.1"`, `"version": "`+v+`"`, 1)))
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
		clear(readSoon)
		readMu.Unlock()
	}
	forget()
	t.Cleanup(func() { githubAPI, githubRaw, npmRegistry = api, raw, reg; forget() })
	return f
}

// ask asks GitHub again, as when the list's ten minutes have run out:
// each repository at its version, and what TaggedRepos says is missing.
func (f *soonFake) ask() (string, error) {
	taggedMu.Lock()
	taggedAt = time.Time{}
	taggedMu.Unlock()
	l, err := TaggedRepos(context.Background())
	var got []string
	for _, x := range l {
		got = append(got, x.Repo+"@"+x.Version)
	}
	return strings.Join(got, " "), err
}

// A repository read within rawFresh of its last push is shown, and read
// again at the next ask: raw.githubusercontent.com's answers, a 404 too,
// say they stand five minutes, so a file pushed a moment ago can read as
// missing, or as it was (MengXiangxi on #3821: the file a package.json
// names, #4067). When it was read is what counts, not when it is asked
// for: the next ask is ten minutes on, as a whole list stands. One read
// rawFresh after its push, someone/old's too, is read once.
func TestTaggedReadSoonAfterAPushIsReadAgain(t *testing.T) {
	f := fakeSoon(t)
	// pushed a minute ago, raw still answering 404s from before the push:
	// for its package.json, then for the middleware file it names
	if got, _ := f.ask(); got != "someone/old@0.1.1" {
		t.Fatalf("raw's 404 for the package.json: %q", got)
	}
	f.pj.Store("0.1.1")
	if got, _ := f.ask(); got != "someone/old@0.1.1" {
		t.Fatalf("raw's 404 for the middleware file: %q", got)
	}
	// raw has both now: the next ask shows it, not the next push
	f.entry.Store(true)
	if got, _ := f.ask(); got != "someone/fresh@0.1.1 someone/old@0.1.1" {
		t.Fatalf("raw has its files now: %q", got)
	}
	// what raw had then was its copy from before the push: the next ask
	// has the one pushed
	f.pj.Store("0.1.2")
	if got, _ := f.ask(); got != "someone/fresh@0.1.2 someone/old@0.1.1" {
		t.Fatalf("raw has the pushed package.json now: %q", got)
	}
	// ten minutes on (rawFresh now under the minute since the push): read
	// a minute after the push, it is read again
	was := rawFresh
	rawFresh = 30 * time.Second
	t.Cleanup(func() { rawFresh = was })
	f.pj.Store("0.1.3")
	if got, _ := f.ask(); got != "someone/fresh@0.1.3 someone/old@0.1.1" {
		t.Fatalf("ten minutes on, read a minute after the push: %q", got)
	}
	// read rawFresh after the push, it is kept for the push
	f.pj.Store("0.1.4")
	if got, _ := f.ask(); got != "someone/fresh@0.1.3 someone/old@0.1.1" {
		t.Fatalf("read rawFresh after the push: %q", got)
	}
	if n := f.oldReads.Load(); n != 1 {
		t.Fatalf("someone/old's package.json read %d times in six asks, want once", n)
	}
}

// A read that comes in after the page stops waiting is shown at the next
// ask, one soon after a push too: through a slow proxy each ask's read of
// a repository pushed a moment ago comes in late, and it would show only
// rawFresh after the push. Read again then and not answered, it shows as
// it was read; read again and late again, as it was read till the new
// read comes in.
func TestTaggedLateReadSoonAfterAPushIsShown(t *testing.T) {
	f := fakeSoon(t)
	f.pj.Store("0.1.1")
	f.entry.Store(true)
	// someone/old read first, so that each ask below reads someone/fresh
	// alone
	f.down.Store(true)
	if got, _ := f.ask(); got != "someone/old@0.1.1" {
		t.Fatalf("raw down for someone/fresh: %q", got)
	}
	f.down.Store(false)
	wait := taggedWait
	taggedWait = 2 * time.Second
	t.Cleanup(func() { taggedWait = wait })
	hold := func() {
		h := make(chan struct{})
		f.hold.Store(&h)
	}
	release := func() {
		if h := f.hold.Swap(nil); h != nil {
			close(*h)
		}
	}
	t.Cleanup(release)
	// kept waits for someone/fresh's late read to be kept at v
	kept := func(v string) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); ; {
			got := ""
			readMu.Lock()
			for k, x := range readRepo {
				if strings.HasPrefix(k, "someone/fresh@") && x != nil {
					got = x.Version
				}
			}
			readMu.Unlock()
			if got == v {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("someone/fresh's late read never kept: %q, want %q", got, v)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// its package.json slow to come: the page stops waiting without it
	hold()
	var m *TaggedMissing
	if got, err := f.ask(); got != "someone/old@0.1.1" || !errors.As(err, &m) || m.Missing != 1 {
		t.Fatalf("a slow read: %q, %v", got, err)
	}
	release()
	kept("0.1.1")
	// read again, raw failing at once: as it came in
	f.down.Store(true)
	if got, err := f.ask(); got != "someone/fresh@0.1.1 someone/old@0.1.1" || err != nil {
		t.Fatalf("read late, then not answered: %q, %v", got, err)
	}
	// read again, slow again: as it came in, till the new read does
	f.down.Store(false)
	f.pj.Store("0.1.2")
	hold()
	if got, err := f.ask(); got != "someone/fresh@0.1.1 someone/old@0.1.1" || err != nil {
		t.Fatalf("read late twice: %q, %v", got, err)
	}
	release()
	kept("0.1.2")
	if got, err := f.ask(); got != "someone/fresh@0.1.2 someone/old@0.1.1" || err != nil {
		t.Fatalf("the late read in: %q, %v", got, err)
	}
	if n := f.oldReads.Load(); n != 1 {
		t.Fatalf("someone/old's package.json read %d times in five asks, want once", n)
	}
}

// The owner, 11-27: Discover's week waited on GitHub's whole list, ~20s on
// a first look. TaggedStanding is what TaggedRepos had, however old, at
// once and without asking, and while an ask holds taggedMu what is on
// disk; nothing when it never had one.
func TestTaggedStandingAtOnce(t *testing.T) {
	var up atomic.Bool
	var asked atomic.Int32
	up.Store(true)
	fakeGitHub(t, &up, &asked)
	if l, fresh, _ := TaggedStanding(); l != nil || fresh {
		t.Fatalf("never asked: %d, fresh %v", len(l), fresh)
	}
	want := len(taggedList(context.Background()))
	if l, fresh, err := TaggedStanding(); len(l) != want || !fresh || err != nil {
		t.Fatalf("just asked: %d of %d, fresh %v, %v", len(l), want, fresh, err)
	}
	taggedMu.Lock()
	taggedAt = time.Now().Add(-taggedTTL - time.Second)
	taggedMu.Unlock()
	if l, fresh, _ := TaggedStanding(); len(l) != want || fresh {
		t.Fatalf("ten minutes on: %d of %d, fresh %v", len(l), want, fresh)
	}
	// an ask under way: what is on disk, not a wait for it
	taggedMu.Lock()
	got := make(chan int, 1)
	go func() { l, _, _ := TaggedStanding(); got <- len(l) }()
	select {
	case n := <-got:
		taggedMu.Unlock()
		if n != want {
			t.Fatalf("during an ask: %d of %d", n, want)
		}
	case <-time.After(time.Minute):
		taggedMu.Unlock()
		t.Fatal("TaggedStanding waited for an ask under way")
	}
	// magpie started again: the disk's copy
	taggedMu.Lock()
	taggedL = nil
	taggedMu.Unlock()
	if l, fresh, _ := TaggedStanding(); len(l) != want || fresh {
		t.Fatalf("started again: %d of %d, fresh %v", len(l), want, fresh)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("GitHub asked %d times, want once", n)
	}
}

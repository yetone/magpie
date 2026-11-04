package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Plugins on GitHub that nobody listed: a repository its author tagged
// with the magpie-plugin topic. The market shows them apart from the
// community's, as not reviewed by anyone: installed from npm when its
// author published it there from the repository, else from the repository
// itself (github:owner/repo).

// Topic is the GitHub topic that puts a repository on the Plugins page.
const Topic = "magpie-plugin"

// Tagged is a repository tagged with Topic.
type Tagged struct {
	Spec        string `json:"spec"`  // what Install adds: its npm name when npm has it from this repository, else github:owner/repo
	Repo        string `json:"repo"`  // owner/repo
	URL         string `json:"url"`   // its page on GitHub
	Owner       string `json:"owner"` // its owner's login
	OwnerAvatar string `json:"ownerAvatar,omitempty"`
	Description string `json:"description,omitempty"`
	Stars       int    `json:"stars"`
	// License is its SPDX id as GitHub reads its LICENSE file, else as
	// its package.json says (PkgLicense): a LICENSE GitHub can't match to
	// one (NOASSERTION: MIT's text with a line of credit added) or none at
	// all left the card with no licence, while npm shows package.json's
	// (plugins#468)
	License    string    `json:"license,omitempty"`
	PkgLicense string    `json:"pkgLicense,omitempty"`
	Pushed     time.Time `json:"pushed"`
	// from its package.json on the default branch: what bun will install
	// it as, its version, and whether it is gateway middleware or an agent
	Package string `json:"package,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"` // "agent" when it adds an agent (a module of its own too or not); "middleware", "page" or "hooks" when that is all it is
	// its package.json's magpie.icon, as an installed plugin gives its
	// provider one: an https URL or a data:image URI, as it is written
	Icon string `json:"icon,omitempty"`
	// Name and Summary are what its own package.json says it is
	// (magpie.name, magpie.summary): its card shows them as the package's
	// own words, never as reviewed, and they never resolve a name (#3608).
	// npm's copy says them when the card installs from npm.
	Name    string            `json:"name,omitempty"`
	Summary map[string]string `json:"summary,omitempty"`
}

// Where GitHub is asked, moved by tests.
var (
	githubAPI = "https://api.github.com"
	githubRaw = "https://raw.githubusercontent.com"
)

// taggedTTL is how long what GitHub said stands: short, so a repository
// tagged a moment ago shows within minutes (lee04052822 on X tagged theirs
// and, asked six hours apart, didn't see it). A search is one request of
// the ten a minute; what each repository's package.json and npm said is
// kept while its last push stays the same (but see rawFresh), so asking
// again costs no more.
const taggedTTL = 10 * time.Minute

// taggedSearch bounds each page of GitHub's search: from mainland China
// a page straight from api.github.com takes about 30s (MYCF711, #3923),
// and with the search under taggedWait the list was empty there.
// taggedWait is how long the page then waits for the repositories' reads;
// taggedEach bounds a repository's reads (package.json, npm, its entry file), which go on past
// that wait and are kept for the next ask; taggedAtOnce repositories are
// read at once; and a list that left a repository out for want of an
// answer is asked again after taggedRetry. Through a proxy far away each
// read takes a few hundred milliseconds (larchsis on X: a list cut short
// at the wait was kept ten minutes).
var (
	taggedSearch = 60 * time.Second
	taggedWait   = 8 * time.Second
	taggedEach   = 20 * time.Second
	taggedAtOnce = 12
	taggedRetry  = 20 * time.Second
	// taggedPage is how many repositories one page of the search holds
	// (GitHub's most is 100)
	taggedPage = 100
	// rawFresh is how long after a push raw.githubusercontent.com may
	// still answer from before it: its answers, a 404 too, say they
	// stand five minutes, so a file pushed a moment ago (the package.json,
	// the file it names) can read as missing, or as it was (MengXiangxi
	// on #3821, #4067). What a read that soon after the push made of a
	// repository is shown, as any read's is, and it is read again at the
	// next ask.
	rawFresh = 10 * time.Minute
)

var (
	taggedMu sync.Mutex
	taggedL  []Tagged
	taggedAt time.Time
	// taggedWhy is why taggedL isn't GitHub's whole answer: the search
	// failed (taggedL is what it said before), or repositories it named
	// couldn't be read. nil when it is.
	taggedWhy error
	// owner/repo@pushed → what its package.json and npm made of it, nil
	// when it was left out
	readMu   sync.Mutex
	readRepo = map[string]*Tagged{}
	// readRepo's keys read less than rawFresh after the push
	readSoon = map[string]bool{}
)

func taggedCache() string { return filepath.Join(filepath.Dir(marketCache()), "plugin-github.json") }

// taggedOnDisk is the list as GitHub said it last, kept on disk; nil
// when there is none.
func taggedOnDisk() []Tagged {
	var l []Tagged
	if b, err := os.ReadFile(taggedCache()); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	return l
}

// ErrNoAnswer is GitHub not answering a read before the page stopped
// waiting for it.
var ErrNoAnswer = errors.New("no answer")

// TaggedMissing is the error TaggedRepos gives when GitHub's search
// answered but repositories it named couldn't be read: Missing of them
// aren't in the list, for Why.
type TaggedMissing struct {
	Missing int
	Why     error
}

func (e *TaggedMissing) Error() string {
	return fmt.Sprintf("%d repositories tagged %s couldn't be read from GitHub: %v", e.Missing, Topic, e.Why)
}

func (e *TaggedMissing) Unwrap() error { return e.Why }

// TaggedRepos are the repositories tagged with Topic, the most starred
// first: GitHub asked at most every ten minutes (its search lets 10 a minute
// through without a token), else what it said last, kept on disk. Archived
// repositories, forks and magpie-community's own (the market lists those)
// are left out, as is one whose package.json doesn't parse, which bun
// couldn't install either. A repository whose package.json didn't come in
// time is as the list had it last, and the list is asked again in
// taggedRetry. MAGPIE_PLUGIN_MARKET=off keeps to none.
//
// The error says the list isn't GitHub's whole answer, and why: the search
// failed (the list is what GitHub said before, empty when it never
// answered), or, a *TaggedMissing, repositories it named couldn't be read.
// An empty list is never a failed read passed off as there being none
// (till on Discord: GitHub unreachable from their desktop, and the
// section just wasn't there).
func TaggedRepos(ctx context.Context) ([]Tagged, error) {
	taggedMu.Lock()
	defer taggedMu.Unlock()
	if taggedL != nil && time.Since(taggedAt) < taggedTTL {
		return taggedL, taggedWhy
	}
	if os.Getenv("MAGPIE_PLUGIN_MARKET") == "off" {
		return []Tagged{}, nil
	}
	// a magpie started again within the ten minutes: what GitHub said then,
	// without the wait of asking (a package.json read for each)
	if taggedL == nil {
		if fi, err := os.Stat(taggedCache()); err == nil && time.Since(fi.ModTime()) < taggedTTL {
			if l := taggedOnDisk(); l != nil {
				taggedL, taggedAt, taggedWhy = l, fi.ModTime(), nil
				return l, nil
			}
		}
	}
	last := taggedL
	if last == nil {
		last = taggedOnDisk()
	}
	if l, whole, missing, err := askTagged(ctx, last); err == nil {
		taggedL, taggedAt, taggedWhy = l, time.Now(), nil
		if !whole {
			taggedAt = time.Now().Add(-taggedTTL + taggedRetry)
		}
		if missing != nil {
			taggedWhy = missing
		}
		if b, err := json.Marshal(l); err == nil {
			_ = os.MkdirAll(filepath.Dir(taggedCache()), 0o755)
			_ = os.WriteFile(taggedCache(), b, 0o644)
			// a magpie started again meanwhile asks again too
			_ = os.Chtimes(taggedCache(), taggedAt, taggedAt)
		}
		return l, taggedWhy
	} else {
		// GitHub didn't answer: what it said last, asked again in a
		// minute, and why it is that
		l := last
		if l == nil {
			l = []Tagged{}
		}
		taggedL, taggedAt, taggedWhy = l, time.Now().Add(-taggedTTL+time.Minute), err
		return l, err
	}
}

// TaggedAskAgain has the next TaggedRepos ask GitHub at once, when what
// stands now isn't its whole answer: the Plugins page's Try again. A list
// GitHub gave whole stands its ten minutes all the same.
func TaggedAskAgain() {
	taggedMu.Lock()
	defer taggedMu.Unlock()
	if taggedWhy != nil {
		taggedAt = time.Time{}
	}
}

// askTagged asks GitHub's search, and reads each repository it names
// that wasn't read at its last push, or was read less than rawFresh after
// it. whole is false when a repository was left out for want of an answer
// in taggedWait: it is then as last had it, if it had it, and missing
// counts those last didn't have.
func askTagged(ctx context.Context, last []Tagged) (l []Tagged, whole bool, missing *TaggedMissing, err error) {
	type item struct {
		FullName      string    `json:"full_name"`
		HTMLURL       string    `json:"html_url"`
		Description   string    `json:"description"`
		Stars         int       `json:"stargazers_count"`
		DefaultBranch string    `json:"default_branch"`
		Pushed        time.Time `json:"pushed_at"`
		Archived      bool      `json:"archived"`
		Fork          bool      `json:"fork"`
		Owner         struct {
			Login     string `json:"login"`
			AvatarURL string `json:"avatar_url"`
		} `json:"owner"`
		License *struct {
			SPDX string `json:"spdx_id"`
		} `json:"license"`
	}
	var r struct{ Items []item }
	// every page, not the first: one page left out every repository past
	// it. GitHub's search answers at most 1000 results.
	listed := map[string]bool{}
	for page := 1; page*taggedPage <= 1000; page++ {
		v := url.Values{"q": {"topic:" + Topic + " archived:false fork:false"}, "sort": {"stars"}, "order": {"desc"},
			"per_page": {strconv.Itoa(taggedPage)}, "page": {strconv.Itoa(page)}}
		sc, cancel := context.WithTimeout(ctx, taggedSearch)
		b, err := fetchJSON(sc, githubAPI+"/search/repositories?"+v.Encode(), 4<<20)
		cancel()
		if err != nil {
			return nil, false, nil, err
		}
		var p struct {
			Total int    `json:"total_count"`
			Items []item `json:"items"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, false, nil, err
		}
		for _, it := range p.Items {
			// a repository starred between two pages can show on both
			if k := strings.ToLower(it.FullName); !listed[k] {
				listed[k] = true
				r.Items = append(r.Items, it)
			}
		}
		if len(p.Items) < taggedPage || page*taggedPage >= p.Total {
			break
		}
	}
	c, cancel := context.WithTimeout(ctx, taggedWait)
	defer cancel()
	before := map[string]Tagged{}
	for _, t := range last {
		before[strings.ToLower(t.Repo)] = t
	}
	// what the search says now, over what is kept of a repository
	fresh := func(r, t Tagged) Tagged {
		r.URL, r.Owner, r.OwnerAvatar, r.Description, r.Stars, r.License, r.Pushed = t.URL, t.Owner, t.OwnerAvatar, t.Description, t.Stars, t.License, t.Pushed
		if r.License == "" {
			r.License = r.PkgLicense
		}
		return r
	}
	var mu sync.Mutex // out, keep, done, why; late once the page stops waiting
	late := false
	var why error // a read GitHub or npm didn't answer, the last one
	out := make([]Tagged, len(r.Items))
	keep := make([]bool, len(r.Items))
	done := make([]bool, len(r.Items))
	asked := make([]Tagged, len(r.Items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, taggedAtOnce)
	// a read goes on after the page stops waiting, and is kept for the
	// next ask
	bg := context.WithoutCancel(ctx)
	seen := map[string]bool{}
	for i, it := range r.Items {
		if it.Archived || it.Fork || strings.EqualFold(it.Owner.Login, "magpie-community") || !strings.Contains(it.FullName, "/") {
			done[i] = true
			continue
		}
		t := Tagged{
			Spec: "github:" + it.FullName, Repo: it.FullName, URL: it.HTMLURL, Owner: it.Owner.Login, OwnerAvatar: it.Owner.AvatarURL,
			Description: it.Description, Stars: it.Stars, Pushed: it.Pushed,
		}
		if it.License != nil && it.License.SPDX != "NOASSERTION" {
			t.License = it.License.SPDX
		}
		asked[i] = t
		key := it.FullName + "@" + it.Pushed.Format(time.RFC3339)
		seen[key] = true
		readMu.Lock()
		was, read := readRepo[key]
		soon := readSoon[key]
		readMu.Unlock()
		if read {
			if was != nil {
				out[i], keep[i] = fresh(*was, t), true
			}
			done[i] = true
			if !soon {
				continue
			}
		}
		wg.Add(1)
		go func(i int, t Tagged, branch string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pc, cancel := context.WithTimeout(bg, taggedEach)
			defer cancel()
			got, ok, read, rerr := readTagged(pc, t, branch)
			if read {
				readMu.Lock()
				if ok {
					readRepo[key] = &got
				} else {
					readRepo[key] = nil
				}
				readSoon[key] = time.Since(it.Pushed) < rawFresh
				readMu.Unlock()
			}
			mu.Lock()
			// read again and not answered: as it was read before
			if !late && (ok || read || !done[i]) {
				out[i], keep[i] = got, ok
				// a package.json GitHub didn't answer for: not known
				done[i] = ok || read
				if !done[i] && rerr != nil {
					why = rerr
				}
			}
			mu.Unlock()
		}(i, t, it.DefaultBranch)
	}
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-c.Done():
	}
	mu.Lock()
	late = true
	mu.Unlock()
	// a repository pushed to again, or no longer tagged, is forgotten
	readMu.Lock()
	for k := range readRepo {
		if !seen[k] {
			delete(readRepo, k)
			delete(readSoon, k)
		}
	}
	readMu.Unlock()
	l, whole = []Tagged{}, true
	gone := 0
	for i := range out {
		switch {
		case keep[i]:
			l = append(l, out[i])
		case !done[i]:
			whole = false
			if was, ok := before[strings.ToLower(asked[i].Repo)]; ok {
				l = append(l, fresh(was, asked[i]))
			} else {
				gone++
			}
		}
	}
	if gone > 0 {
		mu.Lock()
		w := why
		mu.Unlock()
		if w == nil {
			w = fmt.Errorf("%w in %s", ErrNoAnswer, taggedWait)
		}
		missing = &TaggedMissing{Missing: gone, Why: w}
	}
	return l, whole, missing, nil
}

// readTagged reads a tagged repository's package.json (on branch), and
// npm's copy of the package it names: t as it is installed, whether it is
// kept, and whether that is known (GitHub or npm not answering is not,
// and it is read again next time), with the read that didn't answer.
func readTagged(pc context.Context, t Tagged, branch string) (Tagged, bool, bool, error) {
	if branch == "" {
		branch = "HEAD"
	}
	pb, err := fetchJSON(pc, githubRaw+"/"+t.Repo+"/"+url.PathEscape(branch)+"/package.json", 2<<20)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return t, false, true, nil
		}
		return t, false, false, err
	}
	var pj struct {
		Name    string          `json:"name"`
		Version string          `json:"version"`
		Main    string          `json:"main"`
		Exports json.RawMessage `json:"exports"`
		License any             `json:"license"`
		Magpie  struct {
			Middleware string `json:"middleware"`
			Agent      string `json:"agent"`
			Hooks      string `json:"hooks"`
			Icon       string `json:"icon"`
			Name       string `json:"name"`
			// a map of language to sentence, anything else read as none
			Summary json.RawMessage `json:"summary"`
			Page    struct {
				File string `json:"file"`
			} `json:"page"`
		} `json:"magpie"`
	}
	if json.Unmarshal(pb, &pj) != nil || !pkgName.MatchString(pj.Name) {
		return t, false, true, nil
	}
	t.Package, t.Version = pj.Name, pj.Version
	// the licence it names, a string as npm takes it, for when GitHub
	// couldn't tell one from its LICENSE file
	if l, ok := pj.License.(string); ok {
		t.PkgLicense = strings.TrimSpace(l)
		if t.License == "" {
			t.License = t.PkgLicense
		}
	}
	// a picture, as host.js's iconOf takes one: magpie checks and
	// keeps it before the page shows it (the GUI's /api/plugins/github)
	t.Icon = ownIcon(pj.Magpie.Icon)
	// and what it says of itself, shown on its card as its own words
	// rather than the community's (#3608)
	t.Name, t.Summary = ownName(pj.Magpie.Name), ownSummary(pj.Magpie.Summary)
	// runs is whether Bun loads a module of its own (main, exports)
	runs := pj.Main != "" || len(pj.Exports) > 0
	switch {
	case !runs && strings.TrimSpace(pj.Magpie.Middleware) != "":
		t.Kind = "middleware"
	// a package that adds an agent is an agent plugin, its Bun module
	// (main) only what keeps the agent working, not a provider
	// (plugins#737)
	case strings.TrimSpace(pj.Magpie.Agent) != "":
		t.Kind = "agent"
	case !runs && strings.TrimSpace(pj.Magpie.Page.File) != "":
		t.Kind = "page"
	// gateway hooks alone (magpie.hooks) need no entry of their
	// own, as the host loads them: the hooks file is the one to
	// find (#3821)
	case !runs && strings.TrimSpace(pj.Magpie.Hooks) != "":
		t.Kind = "hooks"
	}
	// published to npm from this repository: npm's copy is the one
	// built to be installed (a repository often leaves its dist out,
	// built only to publish), so it is installed from npm
	b, err := fetchNPM(pc, pj.Name, "/"+npmPath(pj.Name)+"/latest", 1<<20)
	known := err == nil || errors.Is(err, errNotFound)
	var unknown error
	if !known {
		unknown = err
	}
	// npmNames is whether npm's copy names what it loads (main,
	// exports); one that names nothing loads an index file
	npmNames := false
	if err == nil {
		var l npmLatest
		if json.Unmarshal(b, &l) == nil && l.Version != "" && strings.EqualFold(githubRepo(repoURL(l.Repository)), t.Repo) {
			t.Spec, t.Version = pj.Name, l.Version
			// the copy installed says what it is: its words when it
			// says any, else the repository's
			if n := ownName(l.Magpie.Name); n != "" {
				t.Name = n
			}
			if s := ownSummary(l.Magpie.Summary); s != nil {
				t.Summary = s
			}
			npmNames = l.Main != "" || len(l.Exports) > 0
		}
	}
	// installed from the repository, the file it loads must be in
	// it: one whose dist is built only to publish couldn't load
	// (GitHub not answering is no answer, and keeps it; so is a
	// name Bun would add .js or /index.js to). Installed from npm,
	// a copy that names no file loads index.js, which the
	// repository has too when it is a plugin at all: a package
	// that is only a command (bin), an MCP server, has none and
	// would never load (#1327)
	if IsGit(t.Spec) || !npmNames {
		// a package with a module of its own loads that module, an
		// agent plugin's too; else the one file it declares
		entry := pkgEntry(pj.Exports, pj.Main)
		if !IsGit(t.Spec) {
			entry = pkgEntry(nil, "")
		}
		if !runs {
			switch t.Kind {
			case "middleware":
				entry = pj.Magpie.Middleware
			case "agent":
				entry = pj.Magpie.Agent
			case "page":
				entry = pj.Magpie.Page.File
			case "hooks":
				entry = pj.Magpie.Hooks
			}
		}
		if path.Ext(entry) != "" {
			_, err := fetchJSON(pc, githubRaw+"/"+t.Repo+"/"+url.PathEscape(branch)+"/"+strings.TrimPrefix(path.Clean("/"+entry), "/"), 1)
			if errors.Is(err, errNotFound) {
				return t, false, known, unknown
			}
			if err != nil {
				known, unknown = false, err
			}
		}
	}
	return t, true, known, unknown
}

// pkgEntry is the file importing a package loads, as Bun resolves it: its
// exports' "." (import, else default), else main, else index.js.
func pkgEntry(exports json.RawMessage, main string) string {
	var pick func(v any) string
	pick = func(v any) string {
		switch v := v.(type) {
		case string:
			return v
		case map[string]any:
			if d, ok := v["."]; ok {
				return pick(d)
			}
			for _, k := range []string{"bun", "import", "default", "node", "require"} {
				if s := pick(v[k]); s != "" {
					return s
				}
			}
		}
		return ""
	}
	var v any
	if len(exports) > 0 && json.Unmarshal(exports, &v) == nil {
		if s := pick(v); s != "" {
			return s
		}
	}
	if main != "" {
		return main
	}
	return "index.js"
}

// githubReadme is the README of a GitHub repository not installed yet, as
// its default branch has it.
func githubReadme(ctx context.Context, repo string) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var last error
	for _, f := range []string{"README.md", "readme.md", "Readme.md", "README"} {
		b, err := fetchJSON(ctx, githubRaw+"/"+repo+"/HEAD/"+f, 200<<10)
		if err == nil {
			return Page{Readme: string(b)}, nil
		}
		last = err
	}
	return Page{}, last
}

// githubRepo is owner/repo of a github: spec or a GitHub page's URL, ""
// for any other.
func githubRepo(spec string) string {
	s := strings.TrimSuffix(strings.SplitN(spec, "#", 2)[0], ".git")
	switch {
	case strings.HasPrefix(s, "github:"):
		s = strings.TrimPrefix(s, "github:")
	case strings.HasPrefix(s, "https://github.com/"), strings.HasPrefix(s, "https://www.github.com/"):
		s = s[strings.Index(s, "github.com/")+len("github.com/"):]
	default:
		return ""
	}
	p := strings.Split(strings.Trim(s, "/"), "/")
	if len(p) < 2 || p[0] == "" || p[1] == "" {
		return ""
	}
	return p[0] + "/" + p[1]
}

package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
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
	Spec        string    `json:"spec"`  // what Install adds: its npm name when npm has it from this repository, else github:owner/repo
	Repo        string    `json:"repo"`  // owner/repo
	URL         string    `json:"url"`   // its page on GitHub
	Owner       string    `json:"owner"` // its owner's login
	OwnerAvatar string    `json:"ownerAvatar,omitempty"`
	Description string    `json:"description,omitempty"`
	Stars       int       `json:"stars"`
	License     string    `json:"license,omitempty"` // SPDX id
	Pushed      time.Time `json:"pushed"`
	// from its package.json on the default branch: what bun will install
	// it as, its version, and whether it is gateway middleware
	Package string `json:"package,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"` // "middleware" when that is all it is
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
// kept while its last push stays the same, so asking again costs no more.
const taggedTTL = 10 * time.Minute

var (
	taggedMu sync.Mutex
	taggedL  []Tagged
	taggedAt time.Time
	// owner/repo@pushed → what its package.json and npm made of it, nil
	// when it was left out
	readMu   sync.Mutex
	readRepo = map[string]*Tagged{}
)

func taggedCache() string { return filepath.Join(filepath.Dir(marketCache()), "plugin-github.json") }

// TaggedRepos are the repositories tagged with Topic, the most starred
// first: GitHub asked at most every ten minutes (its search lets 10 a minute
// through without a token), else what it said last, kept on disk. Archived
// repositories, forks and magpie-community's own (the market lists those)
// are left out, as is one whose package.json doesn't parse, which bun
// couldn't install either. MAGPIE_PLUGIN_MARKET=off keeps to none.
func TaggedRepos(ctx context.Context) []Tagged {
	taggedMu.Lock()
	defer taggedMu.Unlock()
	if taggedL != nil && time.Since(taggedAt) < taggedTTL {
		return taggedL
	}
	if os.Getenv("MAGPIE_PLUGIN_MARKET") == "off" {
		return []Tagged{}
	}
	// a magpie started again within the ten minutes: what GitHub said then,
	// without the wait of asking (a package.json read for each)
	if taggedL == nil {
		if fi, err := os.Stat(taggedCache()); err == nil && time.Since(fi.ModTime()) < taggedTTL {
			var l []Tagged
			if b, err := os.ReadFile(taggedCache()); err == nil && json.Unmarshal(b, &l) == nil && l != nil {
				taggedL, taggedAt = l, fi.ModTime()
				return l
			}
		}
	}
	if l, err := askTagged(ctx); err == nil {
		taggedL, taggedAt = l, time.Now()
		if b, err := json.Marshal(l); err == nil {
			_ = os.MkdirAll(filepath.Dir(taggedCache()), 0o755)
			_ = os.WriteFile(taggedCache(), b, 0o644)
		}
		return l
	}
	// GitHub didn't answer: what it said last, asked again in a minute
	var l []Tagged
	if b, err := os.ReadFile(taggedCache()); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	if l == nil {
		l = []Tagged{}
	}
	taggedL, taggedAt = l, time.Now().Add(-taggedTTL+time.Minute)
	return l
}

func askTagged(ctx context.Context) ([]Tagged, error) {
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	v := url.Values{"q": {"topic:" + Topic + " archived:false fork:false"}, "sort": {"stars"}, "order": {"desc"}, "per_page": {"50"}}
	b, err := fetchJSON(c, githubAPI+"/search/repositories?"+v.Encode(), 4<<20)
	if err != nil {
		return nil, err
	}
	var r struct {
		Items []struct {
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
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	out := make([]Tagged, len(r.Items))
	keep := make([]bool, len(r.Items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	seen := map[string]bool{}
	for i, it := range r.Items {
		if it.Archived || it.Fork || strings.EqualFold(it.Owner.Login, "magpie-community") || !strings.Contains(it.FullName, "/") {
			continue
		}
		t := Tagged{
			Spec: "github:" + it.FullName, Repo: it.FullName, URL: it.HTMLURL, Owner: it.Owner.Login, OwnerAvatar: it.Owner.AvatarURL,
			Description: it.Description, Stars: it.Stars, Pushed: it.Pushed,
		}
		if it.License != nil && it.License.SPDX != "NOASSERTION" {
			t.License = it.License.SPDX
		}
		key := it.FullName + "@" + it.Pushed.Format(time.RFC3339)
		seen[key] = true
		readMu.Lock()
		was, read := readRepo[key]
		readMu.Unlock()
		if read {
			if was != nil {
				r := *was
				// what the search says now: stars, description, license
				r.URL, r.Owner, r.OwnerAvatar, r.Description, r.Stars, r.License, r.Pushed = t.URL, t.Owner, t.OwnerAvatar, t.Description, t.Stars, t.License, t.Pushed
				out[i], keep[i] = r, true
			}
			continue
		}
		wg.Add(1)
		go func(i int, t Tagged, branch string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// read is whether this repository's answer is known: GitHub or
			// npm not answering is not, and it is read again next time
			read := false
			defer func() {
				if read {
					readMu.Lock()
					if keep[i] {
						r := out[i]
						readRepo[key] = &r
					} else {
						readRepo[key] = nil
					}
					readMu.Unlock()
				}
			}()
			pc, cancel := context.WithTimeout(c, 6*time.Second)
			defer cancel()
			if branch == "" {
				branch = "HEAD"
			}
			pb, err := fetchJSON(pc, githubRaw+"/"+t.Repo+"/"+url.PathEscape(branch)+"/package.json", 256<<10)
			if err != nil {
				read = errors.Is(err, errNotFound)
				return
			}
			var pj struct {
				Name    string          `json:"name"`
				Version string          `json:"version"`
				Main    string          `json:"main"`
				Exports json.RawMessage `json:"exports"`
				Magpie  struct {
					Middleware string `json:"middleware"`
				} `json:"magpie"`
			}
			if json.Unmarshal(pb, &pj) != nil || !pkgName.MatchString(pj.Name) {
				read = true
				return
			}
			t.Package, t.Version = pj.Name, pj.Version
			if strings.TrimSpace(pj.Magpie.Middleware) != "" && pj.Main == "" && len(pj.Exports) == 0 {
				t.Kind = "middleware"
			}
			// published to npm from this repository: npm's copy is the one
			// built to be installed (a repository often leaves its dist out,
			// built only to publish), so it is installed from npm
			b, err := fetchJSON(pc, npmRegistry+"/"+npmPath(pj.Name)+"/latest", 1<<20)
			known := err == nil || errors.Is(err, errNotFound)
			if err == nil {
				var l npmLatest
				if json.Unmarshal(b, &l) == nil && l.Version != "" && strings.EqualFold(githubRepo(repoURL(l.Repository)), t.Repo) {
					t.Spec, t.Version = pj.Name, l.Version
				}
			}
			// installed from the repository, the file it loads must be in
			// it: one whose dist is built only to publish couldn't load
			// (GitHub not answering is no answer, and keeps it; so is a
			// name Bun would add .js or /index.js to)
			if IsGit(t.Spec) {
				entry := pj.Magpie.Middleware
				if t.Kind != "middleware" {
					entry = pkgEntry(pj.Exports, pj.Main)
				}
				if path.Ext(entry) != "" {
					_, err := fetchJSON(pc, githubRaw+"/"+t.Repo+"/"+url.PathEscape(branch)+"/"+strings.TrimPrefix(path.Clean("/"+entry), "/"), 1)
					if errors.Is(err, errNotFound) {
						read = known
						return
					}
					known = known && err == nil
				}
			}
			out[i], keep[i], read = t, true, known
		}(i, t, it.DefaultBranch)
	}
	wg.Wait()
	// a repository pushed to again, or no longer tagged, is forgotten
	readMu.Lock()
	for k := range readRepo {
		if !seen[k] {
			delete(readRepo, k)
		}
	}
	readMu.Unlock()
	l := []Tagged{}
	for i := range out {
		if keep[i] {
			l = append(l, out[i])
		}
	}
	return l, nil
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

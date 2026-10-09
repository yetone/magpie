package plugin

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/source"
)

// The plugin market: the plugins magpie suggests, from the community
// repo's registry.json (a copy is built in, for when GitHub can't be
// reached), each with what npm says of it now; and npm's search for the
// rest.

//go:embed market.json
var builtinMarket []byte

// MarketURL is where the list is kept; MAGPIE_PLUGIN_MARKET moves it, and
// "off" keeps to the built-in copy.
const MarketURL = "https://raw.githubusercontent.com/magpie-community/plugins/main/registry.json"

// Listing is a plugin the market suggests.
type Listing struct {
	Package   string            `json:"package"`
	Name      string            `json:"name"`
	Icon      string            `json:"icon,omitempty"`
	Providers []string          `json:"providers,omitempty"` // OpenCode's ids of those it signs in to
	Community bool              `json:"community,omitempty"` // written by magpie's community
	Replaces  string            `json:"replaces,omitempty"`  // the built-in subscription it does the work of
	Kind      string            `json:"kind,omitempty"`      // "middleware" for gateway middleware; none for a provider
	Summary   map[string]string `json:"summary,omitempty"`   // by language: en, zh
}

type registry struct {
	Plugins []Listing `json:"plugins"`
}

// NPM is what npm says of a package now.
type NPM struct {
	Version     string `json:"version,omitempty"` // latest; none when npm has no such package
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	License     string `json:"license,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Weekly      int    `json:"weekly"` // downloads last week
}

var (
	marketMu   sync.Mutex
	marketList []Listing
	marketAt   time.Time
	npmMu      sync.Mutex
	npmCache   map[string]npmEntry // nil until read from npmCacheFile
)

// npmEntry is what npm said of a package, and when; kept on disk beside
// the market's copy, so a magpie just started shows it before npm answers.
type npmEntry struct {
	Info NPM       `json:"info"`
	At   time.Time `json:"at"`
}

// Where npm is asked, moved by tests; how many packages are asked at once;
// how long one is waited for, and all of them.
var (
	npmRegistry  = "https://registry.npmjs.org"
	npmDownloads = "https://api.npmjs.org"
	npmAtOnce    = 6
	npmEach      = 6 * time.Second
	npmAll       = 8 * time.Second
)

func npmCacheFile() string { return filepath.Join(filepath.Dir(marketCache()), "plugin-npm.json") }

// npmCached is npmCache, read from disk the first time; npmMu held.
func npmCached() map[string]npmEntry {
	if npmCache == nil {
		npmCache = map[string]npmEntry{}
		if b, err := os.ReadFile(npmCacheFile()); err == nil {
			_ = json.Unmarshal(b, &npmCache)
		}
	}
	return npmCache
}

// ReloadInfo forgets what npm said, as kept in memory: the next ask reads
// it from disk again, as a magpie just started does.
func ReloadInfo() {
	npmMu.Lock()
	npmCache = nil
	npmMu.Unlock()
}

// saveNPM writes npmCache to disk; npmMu held.
func saveNPM() {
	b, err := json.Marshal(npmCache)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(npmCacheFile()), 0o755)
	tmp := npmCacheFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, npmCacheFile())
	}
}

// InfoCached is what npm said last of each package it was asked of, however
// long ago, without asking again; a package never asked isn't in it.
func InfoCached(names []string) map[string]NPM {
	npmMu.Lock()
	defer npmMu.Unlock()
	c := npmCached()
	out := map[string]NPM{}
	for _, n := range names {
		if e, ok := c[n]; ok {
			out[n] = e.Info
		}
	}
	return out
}

func marketCache() string { return filepath.Join(appdir.Cache(), "plugin-market.json") }

func parseMarket(b []byte) ([]Listing, error) {
	var r registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	out := r.Plugins[:0]
	for _, l := range r.Plugins {
		// magpie's community's alone: others' OpenCode plugins weren't
		// written against magpie's sign-ins, and are found by a search
		if l.Package != "" && pkgName.MatchString(l.Package) && l.Community {
			if l.Name == "" {
				l.Name = l.Package
			}
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the list has no plugins")
	}
	return out, nil
}

// Market is the list of plugins magpie suggests: fetched at most every six
// hours, else the copy fetched last, else the one built in.
func Market(ctx context.Context) []Listing {
	marketMu.Lock()
	defer marketMu.Unlock()
	if marketList != nil && time.Since(marketAt) < 6*time.Hour {
		return marketList
	}
	src := os.Getenv("MAGPIE_PLUGIN_MARKET")
	if src == "" {
		src = MarketURL
	}
	if src != "off" {
		c, cancel := context.WithTimeout(ctx, 6*time.Second)
		// the list names the packages installed: a copy of the very file
		// may stand in for it (with 「国内镜像」), a proxy may not
		b, err := fetchJSONFaithful(c, src, 1<<20)
		cancel()
		if err == nil {
			if l, err := parseMarket(b); err == nil {
				marketList, marketAt = l, time.Now()
				_ = os.MkdirAll(filepath.Dir(marketCache()), 0o755)
				_ = os.WriteFile(marketCache(), b, 0o644)
				return l
			}
		}
		if b, err := os.ReadFile(marketCache()); err == nil {
			if l, err := parseMarket(b); err == nil {
				// tried again in a minute, not six hours
				marketList, marketAt = l, time.Now().Add(-6*time.Hour+time.Minute)
				return l
			}
		}
	}
	l, _ := parseMarket(builtinMarket)
	marketList, marketAt = l, time.Now().Add(-6*time.Hour+time.Minute)
	return l
}

// RefreshMarket has the next Market fetch the list again, as with the
// 「国内镜像」 switch just turned on; the one held is kept till then.
func RefreshMarket() {
	marketMu.Lock()
	marketAt = time.Time{}
	marketMu.Unlock()
}

func fetchJSON(ctx context.Context, u string, limit int64) ([]byte, error) {
	return fetchJSONFrom(ctx, u, limit, source.Do)
}

func fetchJSONFaithful(ctx context.Context, u string, limit int64) ([]byte, error) {
	return fetchJSONFrom(ctx, u, limit, source.DoFaithful)
}

func fetchJSONFrom(ctx context.Context, u string, limit int64, do func(*http.Client, *http.Request) (*http.Response, error)) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "magpie")
	req.Header.Set("Accept", "application/json")
	res, err := do(http.DefaultClient, req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, &statusError{URL: u, Code: res.StatusCode, Status: res.Status}
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

var errNotFound = errors.New("not found")

// statusError is a server's answer other than 200 or 404.
type statusError struct {
	URL    string
	Code   int
	Status string
}

func (e *statusError) Error() string { return e.URL + ": " + e.Status }

// npmPath is a package's name as the registry's paths take it.
func npmPath(name string) string { return strings.Replace(url.PathEscape(name), "%40", "@", 1) }

// Info is what npm says of each package, asked at most hourly: a few at a
// time, and for no longer than npmAll in all (or ctx). A package npm didn't
// answer for in time is what it said last, or missing when it never has;
// its answer, when it comes, is kept for the next time. A package npm
// doesn't have has no Version.
func Info(ctx context.Context, names []string) map[string]NPM {
	out := map[string]NPM{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, npmAtOnce)
	ctx, cancel := context.WithTimeout(ctx, npmAll)
	defer cancel()
	// asked apart from ctx: one that answers after the page stopped
	// waiting is still kept
	bg := context.WithoutCancel(ctx)
	asked := map[string]bool{}
	npmMu.Lock()
	cache := npmCached()
	for _, n := range names {
		if asked[n] || !pkgName.MatchString(n) {
			continue
		}
		asked[n] = true
		e, hit := cache[n]
		if hit {
			out[n] = e.Info // what it said last, until it says otherwise
			if time.Since(e.At) < time.Hour {
				continue
			}
		}
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(bg, npmEach)
			info, ok := npmInfo(c, n)
			cancel()
			if !ok {
				return // npm didn't answer: what it said last
			}
			npmMu.Lock()
			npmCached()[n] = npmEntry{info, time.Now()}
			saveNPM()
			npmMu.Unlock()
			mu.Lock()
			if ctx.Err() == nil {
				out[n] = info
			}
			mu.Unlock()
		}(n)
	}
	npmMu.Unlock()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	mu.Lock()
	defer mu.Unlock()
	res := make(map[string]NPM, len(out))
	for k, v := range out {
		res[k] = v
	}
	return res
}

type npmLatest struct {
	Version     string `json:"version"`
	Description string `json:"description"`
	License     any    `json:"license"`
	Homepage    string `json:"homepage"`
	Repository  any    `json:"repository"`
	Author      any    `json:"author"`
	// Main and Exports are what importing it loads (pkgEntry)
	Main    string          `json:"main"`
	Exports json.RawMessage `json:"exports"`
	NPMUser struct {
		Name string `json:"name"`
	} `json:"_npmUser"`
}

func person(v any) string {
	switch x := v.(type) {
	case string:
		if i := strings.IndexAny(x, "<("); i > 0 {
			return strings.TrimSpace(x[:i])
		}
		return x
	case map[string]any:
		s, _ := x["name"].(string)
		return s
	}
	return ""
}

// repoURL is a package.json repository as a web address.
func repoURL(v any) string {
	s, _ := v.(string)
	if m, ok := v.(map[string]any); ok {
		s, _ = m["url"].(string)
	}
	s = strings.TrimPrefix(s, "git+")
	s = strings.TrimSuffix(s, ".git")
	s = strings.Replace(s, "git://", "https://", 1)
	s = strings.Replace(s, "ssh://git@", "https://", 1)
	if strings.HasPrefix(s, "github:") {
		s = "https://github.com/" + strings.TrimPrefix(s, "github:")
	} else if !strings.Contains(s, "://") && strings.Count(s, "/") == 1 {
		s = "https://github.com/" + s
	}
	if !strings.HasPrefix(s, "https://") {
		return ""
	}
	return s
}

func npmInfo(ctx context.Context, name string) (NPM, bool) {
	info, err := npmAsk(ctx, name)
	if err != nil {
		return NPM{}, errors.Is(err, errNotFound) // not on npm: known, and kept
	}
	return info, true
}

// npmAsk is what npm says of the package now, or why it said nothing:
// errNotFound when it has no such package.
func npmAsk(ctx context.Context, name string) (NPM, error) {
	var info NPM
	var wg sync.WaitGroup
	var latestErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		b, err := fetchJSON(ctx, npmRegistry+"/"+npmPath(name)+"/latest", 1<<20)
		if err != nil {
			latestErr = err
			return
		}
		var l npmLatest
		if err := json.Unmarshal(b, &l); err != nil {
			latestErr = err
			return
		}
		info.Version, info.Description, info.Homepage = l.Version, l.Description, l.Homepage
		info.License, _ = l.License.(string)
		info.Repository = repoURL(l.Repository)
		info.Publisher = person(l.Author)
		if info.Publisher == "" {
			info.Publisher = l.NPMUser.Name
		}
	}()
	var weekly int
	go func() {
		defer wg.Done()
		b, err := fetchJSON(ctx, npmDownloads+"/downloads/point/last-week/"+npmPath(name), 64<<10)
		if err != nil {
			return
		}
		var d struct{ Downloads int }
		if json.Unmarshal(b, &d) == nil {
			weekly = d.Downloads
		}
	}()
	wg.Wait()
	info.Weekly = weekly
	if latestErr != nil {
		return NPM{}, latestErr
	}
	return info, nil
}

// Hit is a package npm's search found.
type Hit struct {
	Package string `json:"package"`
	NPM
}

// Search asks npm for plugins matching q: OpenCode plugins, and pi
// packages, which list "pi-package" among their keywords as pi's own
// gallery asks.
func Search(ctx context.Context, q string) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []Hit{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var (
		wg       sync.WaitGroup
		oc, pi   []Hit
		ocE, piE error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		// a plugin, not a tool that mentions OpenCode: its name says so,
		// or its keywords name an OpenCode plugin
		oc, ocE = searchNPM(ctx, q+" opencode", func(text string, _ []string) bool {
			return strings.Contains(text, "opencode") && (strings.Contains(text, "auth") || strings.Contains(text, "plugin") || strings.Contains(text, "provider"))
		})
	}()
	go func() {
		defer wg.Done()
		// a pi package that signs in to a provider or brings one; one
		// that only adds pi a command or a tool has nothing for magpie
		pi, piE = searchNPM(ctx, "keywords:pi-package "+q, func(text string, kw []string) bool {
			return slices.Contains(kw, "pi-package") && (strings.Contains(text, "auth") || strings.Contains(text, "provider"))
		})
	}()
	wg.Wait()
	if ocE != nil && piE != nil {
		return nil, ocE
	}
	out, seen := []Hit{}, map[string]bool{}
	for _, h := range append(oc, pi...) {
		if !seen[h.Package] {
			seen[h.Package] = true
			out = append(out, h)
		}
	}
	return out, nil
}

// searchNPM is npm's search for text, the packages keep says are plugins
// (given their name and keywords, lowercased, and the keywords).
func searchNPM(ctx context.Context, text string, keep func(string, []string) bool) ([]Hit, error) {
	v := url.Values{"text": {text}, "size": {"30"}}
	b, err := fetchJSON(ctx, npmRegistry+"/-/v1/search?"+v.Encode(), 4<<20)
	if err != nil {
		return nil, err
	}
	var r struct {
		Objects []struct {
			Package struct {
				Name        string   `json:"name"`
				Version     string   `json:"version"`
				Description string   `json:"description"`
				Keywords    []string `json:"keywords"`
				License     string   `json:"license"`
				Publisher   struct {
					Username string `json:"username"`
				} `json:"publisher"`
				Links struct {
					Homepage   string `json:"homepage"`
					Repository string `json:"repository"`
				} `json:"links"`
			} `json:"package"`
			Downloads struct {
				Weekly int `json:"weekly"`
			} `json:"downloads"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	var out []Hit
	for _, o := range r.Objects {
		p := o.Package
		if !keep(strings.ToLower(p.Name+" "+strings.Join(p.Keywords, " ")), p.Keywords) {
			continue
		}
		out = append(out, Hit{Package: p.Name, NPM: NPM{
			Version: p.Version, Description: p.Description, Publisher: p.Publisher.Username, License: p.License,
			Homepage: p.Links.Homepage, Repository: repoURL(p.Links.Repository), Weekly: o.Downloads.Weekly,
		}})
	}
	return out, nil
}

// Page is a package's page: its README and when it last changed.
type Page struct {
	Readme  string    `json:"readme"`
	Updated time.Time `json:"updated"`
}

// Readme is the package's README, as npm has it, or — for a plugin added
// from a folder on this computer or a git repository — the one its folder
// carries.
func Readme(ctx context.Context, name string) (Page, error) {
	if IsGit(name) {
		// one not installed yet (a repository tagged magpie-plugin): its
		// README as GitHub has it
		if _, err := os.Stat(Target(name)); err != nil {
			if repo := githubRepo(name); repo != "" {
				return githubReadme(ctx, repo)
			}
		}
	}
	if IsPath(name) || IsGit(name) {
		return folderReadme(Target(name))
	}
	if !pkgName.MatchString(name) {
		return Page{}, fmt.Errorf("%q isn't an npm package name", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	b, err := fetchJSON(ctx, npmRegistry+"/"+npmPath(name), 32<<20)
	if err != nil {
		return Page{}, err
	}
	var d struct {
		Readme string               `json:"readme"`
		Time   map[string]time.Time `json:"time"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return Page{}, err
	}
	const most = 200 << 10
	if len(d.Readme) > most {
		d.Readme = d.Readme[:most]
	}
	return Page{Readme: d.Readme, Updated: d.Time["modified"]}, nil
}

// folderReadme is the README a plugin's own folder carries: README.md,
// whatever its case, else a markdown or text one named beside it. A folder
// without one says so, rather than answering with npm's silence.
func folderReadme(dir string) (Page, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return Page{}, err
	}
	named := map[string]os.DirEntry{}
	for _, e := range ents {
		if !e.IsDir() {
			named[strings.ToLower(e.Name())] = e
		}
	}
	for _, want := range []string{"readme.md", "readme.markdown", "readme.txt", "readme"} {
		e, ok := named[want]
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return Page{}, err
		}
		const most = 200 << 10
		if len(b) > most {
			b = b[:most]
		}
		var at time.Time
		if fi, err := e.Info(); err == nil {
			at = fi.ModTime()
		}
		return Page{Readme: string(b), Updated: at}, nil
	}
	return Page{}, fmt.Errorf("%s has no README: add a README.md beside its package.json", dir)
}

// Installed is the version of the plugin spec installed, "" for a path or
// one not installed.
func Installed(spec string) string {
	if IsPath(spec) {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(Target(spec), "package.json"))
	if err != nil {
		return ""
	}
	var p struct{ Version string }
	_ = json.Unmarshal(b, &p)
	return p.Version
}

// Upgrade installs the newest version of one plugin: npm's, or its git
// repository's commit now. One bun won't install says why.
func Upgrade(ctx context.Context, name string) error {
	for _, e := range Load().Plugins {
		if (Name(e.Spec) == name || e.Spec == name) && IsGit(e.Spec) {
			err := reinstall(ctx, e.Spec)
			Restart()
			return err
		}
		if Name(e.Spec) == name && !IsPath(e.Spec) {
			// the version npm has now, not bun's "latest" (installAt);
			// npm not answering, bun's as before
			_, err := add(ctx, name, newestOf(ctx, name))
			return err
		}
	}
	return fmt.Errorf("no plugin %q", name)
}

// Icon is the icon the market gives the plugin spec, or else any listed
// plugin signing in to provider id; "" when none does. It never goes to
// the network: the list fetched last, else the cached copy, else the one
// built in.
func Icon(spec, id string) string {
	marketMu.Lock()
	l := marketList
	marketMu.Unlock()
	if l == nil {
		if b, err := os.ReadFile(marketCache()); err == nil {
			l, _ = parseMarket(b)
		}
		if l == nil {
			l, _ = parseMarket(builtinMarket)
		}
	}
	pkg := Name(spec)
	for _, x := range l {
		if x.Package == pkg && x.Icon != "" {
			return x.Icon
		}
	}
	if ic, ok := otherIcons[pkg]; ok {
		return ic
	}
	for _, x := range l {
		for _, p := range x.Providers {
			if p == id && x.Icon != "" {
				return x.Icon
			}
		}
	}
	if ic, ok := otherIcons[id]; ok {
		return ic
	}
	return ""
}

// otherIcons are the icons of OpenCode plugins the market no longer lists,
// by package, and of the providers they sign in to, so one installed still
// shows its vendor's.
var otherIcons = map[string]string{
	"opencode-gemini-auth":                "gemini-color",
	"opencode-antigravity-auth":           "antigravity-color",
	"opencode-copilot-auth":               "githubcopilot",
	"opencode-openai-codex-auth":          "openai",
	"oc-codex-multi-auth":                 "openai",
	"@ex-machina/opencode-anthropic-auth": "claude-color",
	"@servoy/opencode-kiro-auth":          "kiro-color",
	"opencode-qoder-bridge":               "qoder",
	"google":                              "gemini-color",
	"github-copilot":                      "githubcopilot",
	"openai":                              "openai",
	"anthropic":                           "claude-color",
}

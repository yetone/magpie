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
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/ghtoken"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/source"
	"github.com/yetone/magpie/internal/update"
)

// The plugin market: the plugins magpie suggests, from the community
// repo's registry.json (a copy is built in, for when GitHub can't be
// reached), each with what npm says of it now; and npm's search for the
// rest.

//go:embed market.json
var builtinMarket []byte

// builtinParsed is the built-in copy, parsed once; shared, so only read.
var builtinParsed = sync.OnceValue(func() []Listing {
	l, _ := parseMarket(builtinMarket)
	return l
})

// MarketURL is where the list is kept; MAGPIE_PLUGIN_MARKET moves it, and
// "off" keeps to the built-in copy.
const MarketURL = "https://raw.githubusercontent.com/magpie-community/plugins/main/registry.json"

// Listing is a plugin the market suggests. One not Community is an
// author's own package the list names, published from their repository
// (the owner on magpie-community/plugins#479: PinK, WispTerm): the page
// shows it to a search alone, under its publisher; it is never browsed,
// suggested as an agent or called by a short name.
type Listing struct {
	Package   string            `json:"package"`
	Name      string            `json:"name"`
	Icon      string            `json:"icon,omitempty"`
	Providers []string          `json:"providers,omitempty"` // OpenCode's ids of those it signs in to
	Community bool              `json:"community,omitempty"` // written by magpie's community; see Listing
	Replaces  string            `json:"replaces,omitempty"`  // the built-in subscription it does the work of
	Kind      string            `json:"kind,omitempty"`      // "middleware" for gateway middleware, "agent" for an agent, "tools" for tools alone, "page" for a page alone; none for a provider
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
	// Icon is the picture the package gives in package.json's
	// magpie.icon, as npm has it: magpie checks and keeps it before the
	// page shows it, as a GitHub-tagged plugin's (the GUI's listings)
	Icon string `json:"icon,omitempty"`
}

// ownIcon is a picture a package.json's magpie.icon gives, as host.js's
// iconOf takes one: an https URL or a data:image URI, not too big; ""
// for anything else.
func ownIcon(said string) string {
	ic := strings.TrimSpace(said)
	if len(ic) > 3<<19 {
		return ""
	}
	if l := strings.ToLower(ic); strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "data:image/") {
		return ic
	}
	return ""
}

var (
	marketMu   sync.Mutex
	marketList []Listing
	marketAt   time.Time
	marketErr  error // why marketList isn't a list just fetched; nil when it is
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
		if e, ok := c[n]; ok && !e.missingStale(time.Hour) {
			out[n] = e.Info
		}
	}
	return out
}

// missingStale is whether e is "not on npm" said over age ago: unknown
// now, not missing. A package only the community repo's dist branch has
// was kept as missing by builds before v0.1.2413 whenever the branch's
// read was cut off (zhudabao8433 on Discord: PowerContext, installable the
// day before, read Coming soon), and an ask that still can't read the
// branch leaves that answer standing; a page told "unknown" offers
// Install instead of a Coming soon nothing has confirmed.
func (e npmEntry) missingStale(age time.Duration) bool {
	return e.Info.Version == "" && time.Since(e.At) >= age
}

func marketCache() string { return filepath.Join(appdir.Cache(), "plugin-market.json") }

func parseMarket(b []byte) ([]Listing, error) {
	var r registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	out := r.Plugins[:0]
	for _, l := range r.Plugins {
		// an author's own package is kept too, marked as not the
		// community's (Community false): the page shows it to a search
		// alone, and only the community's are browsed, suggested as
		// agents or called by a short name (Community)
		if l.Package != "" && pkgName.MatchString(l.Package) {
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

// Community is the listings of l magpie's community wrote, in l's order.
func Community(l []Listing) []Listing {
	out := []Listing{}
	for _, x := range l {
		if x.Community {
			out = append(out, x)
		}
	}
	return out
}

// Market is the list of plugins magpie suggests: fetched at most every six
// hours, else the copy fetched last, else the one built in.
func Market(ctx context.Context) []Listing {
	l, _ := market(ctx)
	return l
}

// market is Market, and why the list it gives isn't one fetched within
// the six hours: the fetch's failure, when it is the copy fetched some
// time before or the one built in. A newer list may name a plugin
// neither does, so what one leaves out is unknown, not absent.
func market(ctx context.Context) ([]Listing, error) {
	marketMu.Lock()
	defer marketMu.Unlock()
	if marketList != nil && time.Since(marketAt) < 6*time.Hour {
		return marketList, marketErr
	}
	src := os.Getenv("MAGPIE_PLUGIN_MARKET")
	if src == "" {
		src = MarketURL
	}
	ferr := errors.New("the list is off (MAGPIE_PLUGIN_MARKET=off)")
	if src != "off" {
		c, cancel := context.WithTimeout(ctx, 6*time.Second)
		// the list names the packages installed: a copy of the very file
		// may stand in for it (with 「国内镜像」), a proxy may not
		b, err := fetchJSONFaithful(c, src, 1<<20)
		cancel()
		if err == nil {
			l, perr := parseMarket(b)
			if perr == nil {
				marketList, marketAt, marketErr = l, time.Now(), nil
				_ = os.MkdirAll(filepath.Dir(marketCache()), 0o755)
				_ = os.WriteFile(marketCache(), b, 0o644)
				return l, nil
			}
			err = perr
		}
		ferr = err
		if b, err := os.ReadFile(marketCache()); err == nil {
			if l, err := parseMarket(b); err == nil {
				// tried again in a minute, not six hours
				marketList, marketAt, marketErr = l, time.Now().Add(-6*time.Hour+time.Minute), ferr
				return l, ferr
			}
		}
	}
	l, _ := parseMarket(builtinMarket)
	marketList, marketAt, marketErr = l, time.Now().Add(-6*time.Hour+time.Minute), ferr
	return l, ferr
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
	// through plugin downloads' proxy, where Settings sets one
	ctx = netproxy.Downloads(ctx)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "magpie")
	req.Header.Set("Accept", "application/json")
	// GitHub's API is asked with the user's GitHub token, as the
	// library's skills are: without one it answers an address 60
	// requests an hour, which a few git plugins' checks use up (#1942);
	// an environment token GitHub refuses is dropped for an anonymous
	// request (till on Discord)
	res, token, err := ghtoken.Do(req, githubAPI, func(r *http.Request) (*http.Response, error) {
		return do(http.DefaultClient, r)
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if res.StatusCode != http.StatusOK && sameHost(u, githubAPI) {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if e, ok := ghtoken.LimitedBy(res, body, token); ok {
			return nil, e
		}
		if res.StatusCode == http.StatusUnauthorized && token {
			return nil, ghtoken.Refused()
		}
	}
	if res.StatusCode != http.StatusOK {
		return nil, &statusError{URL: u, Code: res.StatusCode, Status: res.Status}
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

var errNotFound = errors.New("not found")

// sameHost is whether u is on base's host.
func sameHost(u, base string) bool {
	a, err1 := url.Parse(u)
	b, err2 := url.Parse(base)
	return err1 == nil && err2 == nil && a.Host != "" && strings.EqualFold(a.Host, b.Host)
}

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
	return infoWithin(ctx, names, time.Hour)
}

// infoWithin is Info asking npm of each package it last answered for over
// age ago; 0 asks of every one.
func infoWithin(ctx context.Context, names []string, age time.Duration) map[string]NPM {
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
			// what it said last, until it says otherwise
			if !e.missingStale(time.Hour) {
				out[n] = e.Info
			}
			if time.Since(e.At) < age {
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
	Magpie struct {
		Icon    string          `json:"icon"`
		Name    string          `json:"name"`
		Summary json.RawMessage `json:"summary"`
	} `json:"magpie"`
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
		b, err := fetchNPM(ctx, name, "/"+npmPath(name)+"/latest", 1<<20)
		if err != nil {
			latestErr = err
			return
		}
		var l npmLatest
		if err := json.Unmarshal(b, &l); err != nil {
			latestErr = err
			return
		}
		info = npmOf(l)
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
	// a @magpie-community package npm doesn't have yet, or has an older
	// version of, is as the community repo's dist branch has it, which
	// magpie installs it from (dist.go)
	d, ok, derr := distInfo(ctx, name)
	if ok && (latestErr != nil || update.Newer(d.Version, info.Version)) {
		info, latestErr = d, nil
	}
	// npm not having it is "missing" only once the dist branch has
	// said it hasn't either: the branch not read leaves it unknown, and
	// it is asked again, not kept as missing for an hour (ferock)
	if errors.Is(latestErr, errNotFound) && derr != nil {
		latestErr = derr
	}
	info.Weekly = weekly
	if latestErr != nil {
		return NPM{}, latestErr
	}
	return info, nil
}

// npmOf is what a version's package.json says, as NPM has it.
func npmOf(l npmLatest) NPM {
	info := NPM{Version: l.Version, Description: l.Description, Homepage: l.Homepage}
	info.License, _ = l.License.(string)
	info.Repository = repoURL(l.Repository)
	info.Publisher = person(l.Author)
	info.Icon = ownIcon(l.Magpie.Icon)
	if info.Publisher == "" {
		info.Publisher = l.NPMUser.Name
	}
	return info
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
	b, err := fetchNPM(ctx, "", "/-/v1/search?"+v.Encode(), 4<<20)
	if _, _, official := npmBase(""); err != nil && !official {
		// a registry of the user's that has no search (a private one):
		// npm's own has the plugins' packages
		b, err = fetchJSON(ctx, npmRegistry+"/-/v1/search?"+v.Encode(), 4<<20)
	}
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
func Readme(ctx context.Context, name string) (Page, error) { return ReadmeIn(ctx, name, "") }

// ReadmeIn is Readme in the reader's language (the UI's, "zh-CN", "ja"):
// the README a package carries named for it (README.zh-CN.md, README_zh.md
// and the like) in place of its README.md, from its folder where it is
// installed and from npm's or GitHub's copy where it isn't. Without one,
// or with lang "" or English, the README.md.
func ReadmeIn(ctx context.Context, name, lang string) (Page, error) {
	if IsGit(name) {
		// one not installed yet (a repository tagged magpie-plugin): its
		// README as GitHub has it
		if _, err := os.Stat(Target(name)); err != nil {
			if repo := githubRepo(name); repo != "" {
				if r, ok := cachedReadme(ctx, name); ok {
					return Page{Readme: r}, nil
				}
				if r, ok := githubLangReadme(ctx, repo, lang); ok {
					return Page{Readme: r}, nil
				}
				return githubReadme(ctx, repo)
			}
		}
	}
	if IsPath(name) || IsGit(name) {
		if p, ok := folderLangReadme(Target(name), lang); ok {
			return p, nil
		}
		return folderReadme(Target(name))
	}
	if !pkgName.MatchString(name) {
		return Page{}, fmt.Errorf("%q isn't an npm package name", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if p, ok := folderLangReadme(Target(name), lang); ok {
		return p, nil // installed: its own file, as it was installed
	}
	// the copy the community repo keeps, before npm, which a human check
	// can stand in front of
	if r, ok := cachedReadme(ctx, name); ok {
		return Page{Readme: r}, nil
	}
	b, err := fetchNPM(ctx, name, "/"+npmPath(name), 32<<20)
	if err != nil {
		// one npm doesn't have (yet): the README the community repo's
		// dist branch packed with it
		d, derr := distPkg(ctx, name)
		if d != nil && d.Readme != "" {
			return Page{Readme: d.Readme}, nil
		}
		if errors.Is(err, errNotFound) && derr != nil {
			return Page{}, derr // not known to be missing
		}
		return Page{}, err
	}
	var d struct {
		Readme   string               `json:"readme"`
		Time     map[string]time.Time `json:"time"`
		DistTags map[string]string    `json:"dist-tags"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		// a page that isn't npm's entry (a human check, a portal)
		return Page{}, errors.New("the answer isn't the package's page; npm may be asking for a human check")
	}
	const most = 200 << 10
	if len(d.Readme) > most {
		d.Readme = d.Readme[:most]
	}
	// the registry's entry holds the README.md alone; one named for the
	// reader's language is in the package's files, which only the public
	// registry's copy is asked of jsDelivr for
	if _, _, official := npmBase(name); official {
		if r, ok := npmLangReadme(ctx, name, d.DistTags["latest"], lang); ok {
			d.Readme = r
		}
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

// InstalledIcon is the picture the installed plugin spec's package.json
// gives in magpie.icon, as ownIcon takes one: the plugin's own, for its
// row, apart from those its providers give themselves in the auth hook
// (#1775). "" for none, or a plugin that is one file.
func InstalledIcon(spec string) string { return ownIcon(installedOwn(spec).Icon) }

// InstalledName is the name the installed plugin spec's package.json
// gives itself in magpie.name, for its row when the community's list
// doesn't name it: a package's dependency key has to be ASCII for Bun,
// so one installed from git, or from npm outside the list, had no other
// way to show a readable name (#4558). It is the package's own word,
// never used to resolve a name; "" for none, one that isn't one short
// line, or a plugin that is one file.
func InstalledName(spec string) string { return ownName(installedOwn(spec).Name) }

// installedOwn is the magpie block of the installed plugin spec's
// package.json, as the package says it: zero for none, or one that
// can't be read.
func installedOwn(spec string) (own struct {
	Icon string `json:"icon"`
	Name string `json:"name"`
}) {
	b, err := os.ReadFile(filepath.Join(Target(spec), "package.json"))
	if err != nil {
		return own
	}
	var p struct {
		Magpie json.RawMessage `json:"magpie"`
	}
	if json.Unmarshal(b, &p) != nil || json.Unmarshal(p.Magpie, &own) != nil {
		own.Icon, own.Name = "", ""
	}
	return own
}

// ownName is a package's magpie.name as a row may show it: one line of
// at most 80 bytes, with no control or format characters (a bidi
// override would make it read as another name); "" otherwise.
func ownName(said string) string {
	n := strings.TrimSpace(said)
	if n == "" || len(n) > 80 || !utf8.ValidString(n) {
		return ""
	}
	for _, r := range n {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ""
		}
	}
	return n
}

// ownSummary is what a package.json's magpie.summary says of the package,
// by language ("en", "zh"), as its card shows it (#3608): a sentence a
// language, trimmed, of at most 300 bytes on one line, with nothing that
// could make it read as other text (control or format characters, a bidi
// override); a language that says anything else is left out, and nil is
// none. Not an object of strings at all is none too, never a failed read
// of the whole package.json.
func ownSummary(raw json.RawMessage) map[string]string {
	var said map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &said) != nil {
		return nil
	}
	out := map[string]string{}
	for lang, v := range said {
		s, ok := v.(string)
		l, t := strings.TrimSpace(lang), strings.TrimSpace(s)
		if !ok || l == "" || len(l) > 12 || t == "" || len(t) > 300 || !utf8.ValidString(t) || !utf8.ValidString(l) {
			continue
		}
		bad := false
		for _, r := range l + t {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				bad = true
				break
			}
		}
		if !bad {
			out[l] = t
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Upgrade installs the newest version of one plugin: npm's, or its git
// repository's commit now. One bun won't install says why.
func Upgrade(ctx context.Context, name string) error {
	for _, e := range Load().Plugins {
		if (Name(e.Spec) == name || e.Spec == name) && IsGit(e.Spec) {
			err := reinstall(ctx, e.Spec)
			if err == nil {
				settleWaiting()
			}
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
		// parsed once for each copy, not for each plugin each look: the
		// Providers page parsed it for every plugin's row each poll, 13 MB
		// of what twenty polls allocated with real accounts
		l, _ = filememo.Read("plugin-market", marketCache(), parseMarket)
		if l == nil {
			l = builtinParsed()
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
	for _, x := range Community(l) {
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

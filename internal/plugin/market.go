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
	"strings"
	"sync"
	"time"
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
	npmCache   = map[string]npmEntry{}
)

type npmEntry struct {
	info NPM
	ok   bool
	at   time.Time
}

func marketCache() string {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "magpie", "plugin-market.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "magpie", "plugin-market.json")
}

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
		b, err := fetchJSON(c, src, 1<<20)
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

func fetchJSON(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "magpie")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

var errNotFound = errors.New("not found")

// npmPath is a package's name as the registry's paths take it.
func npmPath(name string) string { return strings.Replace(url.PathEscape(name), "%40", "@", 1) }

// Info is what npm says of each package, asked at most hourly and all at
// once; a package npm doesn't have has no Version.
func Info(ctx context.Context, names []string) map[string]NPM {
	out := map[string]NPM{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, n := range names {
		npmMu.Lock()
		e, hit := npmCache[n]
		npmMu.Unlock()
		if hit && time.Since(e.at) < time.Hour {
			out[n] = e.info
			continue
		}
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			info, ok := npmInfo(ctx, n)
			mu.Lock()
			out[n] = info
			mu.Unlock()
			if ok {
				npmMu.Lock()
				npmCache[n] = npmEntry{info, true, time.Now()}
				npmMu.Unlock()
			} else if hit {
				mu.Lock()
				out[n] = e.info // npm didn't answer: what it said last
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	return out
}

type npmLatest struct {
	Version     string `json:"version"`
	Description string `json:"description"`
	License     any    `json:"license"`
	Homepage    string `json:"homepage"`
	Repository  any    `json:"repository"`
	Author      any    `json:"author"`
	NPMUser     struct {
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
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var info NPM
	var wg sync.WaitGroup
	var latestErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		b, err := fetchJSON(ctx, "https://registry.npmjs.org/"+npmPath(name)+"/latest", 1<<20)
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
		b, err := fetchJSON(ctx, "https://api.npmjs.org/downloads/point/last-week/"+npmPath(name), 64<<10)
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
		return NPM{}, errors.Is(latestErr, errNotFound) // not on npm: known, and kept
	}
	return info, true
}

// Hit is a package npm's search found.
type Hit struct {
	Package string `json:"package"`
	NPM
}

// Search asks npm for OpenCode plugins matching q.
func Search(ctx context.Context, q string) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []Hit{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v := url.Values{"text": {q + " opencode"}, "size": {"30"}}
	b, err := fetchJSON(ctx, "https://registry.npmjs.org/-/v1/search?"+v.Encode(), 4<<20)
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
	out := []Hit{}
	for _, o := range r.Objects {
		p := o.Package
		// a plugin, not a tool that mentions OpenCode: its name says so,
		// or its keywords name an OpenCode plugin
		text := strings.ToLower(p.Name + " " + strings.Join(p.Keywords, " "))
		if !strings.Contains(text, "opencode") || !(strings.Contains(text, "auth") || strings.Contains(text, "plugin") || strings.Contains(text, "provider")) {
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
	if IsPath(name) || IsGit(name) {
		return folderReadme(Target(name))
	}
	if !pkgName.MatchString(name) {
		return Page{}, fmt.Errorf("%q isn't an npm package name", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	b, err := fetchJSON(ctx, "https://registry.npmjs.org/"+npmPath(name), 32<<20)
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
// repository's commit now.
func Upgrade(ctx context.Context, name string) error {
	for _, e := range Load().Plugins {
		if (Name(e.Spec) == name || e.Spec == name) && IsGit(e.Spec) {
			err := reinstall(ctx, e.Spec)
			Restart()
			return err
		}
		if Name(e.Spec) == name && !IsPath(e.Spec) {
			_, err := Add(ctx, name)
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

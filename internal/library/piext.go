package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	posix "path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
)

// Pi's extensions and packages, for each Pi magpie knows of: this
// machine's, and one in each WSL distro (leokon3 on Discord, who runs both
// and moves an extension from one to the other). Everything here is read
// and written as Pi's own package manager and `pi config` do it
// (@earendil-works/pi-coding-agent 1.4.0, dist/core/package-manager.js and
// dist/modes/interactive/components/config-selector.js):
//
//   - an extension is a .ts or .js file in <agent dir>/extensions, or a
//     folder there with a package.json naming its "pi.extensions", or with
//     an index.ts or index.js (collectAutoExtensionEntries), or a path
//     settings.json's "extensions" lists (resolveLocalEntries);
//   - one is switched off by "-<its path from the agent dir>" in
//     "extensions", and on by taking that out, or by "+<path>" when a "!"
//     pattern there still leaves it out (toggleTopLevelResource,
//     isEnabledByOverrides);
//   - a package is an entry of "packages", a source string or an object
//     with that source and filters; Pi installs one it hasn't yet when it
//     starts (resolve → installParsedSource), so a package is given to
//     another Pi by writing its entry there, not by copying its files.
//
// Pi's built-in extensions (builtin:mcp and the rest) aren't listed:
// turning builtin:mcp off would take away the MCP servers magpie gives Pi.

// PiExtensions is every Pi here, with its extensions and packages.
type PiExtensions struct {
	Pis []PiHere `json:"pis"`
}

// PiHere is one Pi: this machine's, or one in a WSL distro.
type PiHere struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
	WSL  string `json:"wsl,omitempty"`
	// Asleep: its distro isn't running, and nothing in it is read, as that
	// would start it (#3896)
	Asleep bool `json:"asleep,omitempty"`
	// Dir is its agent folder, as magpie opens it
	Dir string `json:"dir,omitempty"`
	// Err says why its settings.json can't be read: nothing is listed,
	// and nothing is written to it, rather than taking it for empty
	Err        string      `json:"err,omitempty"`
	Extensions []PiExt     `json:"extensions"`
	Packages   []PiPackage `json:"packages"`
}

// PiExt is one extension of a Pi's own: in its extensions folder, or
// listed by path in its settings.json.
type PiExt struct {
	// ID names it in a request: "extensions/<name>" for one in the
	// extensions folder, the settings.json entry for one listed there
	ID   string `json:"id"`
	Name string `json:"name"`
	// Path is where it is, as the Pi's machine names it
	Path string `json:"path"`
	On   bool   `json:"on"`
	// Listed: settings.json names it, rather than the folder holding it
	Listed bool `json:"listed,omitempty"`
	// Folder: a folder, not one file
	Folder bool `json:"folder,omitempty"`

	keys  []string // the files Pi loads of it, by their paths from the agent dir
	files []string // those files, as magpie opens them
	src   string   // what a copy copies
}

// PiPackage is one entry of a Pi's settings.json "packages".
type PiPackage struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	// Kind is npm, git or local
	Kind string `json:"kind"`
	// Pin is the version or ref the source names, if any
	Pin string `json:"pin,omitempty"`
	On  bool   `json:"on"`
	// Filtered: the entry picks some of the package's resources (filters
	// of the user's, or `pi config`'s), which a switch here would lose;
	// it is changed in `pi config`
	Filtered bool `json:"filtered,omitempty"`
	// Installed: Pi has it on disk; one that isn't is installed when Pi
	// next starts
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`

	raw json.RawMessage
}

// piPlace is a Pi as this file reads it: its agent folder, and where its
// own machine's ~ and / are, as magpie opens them.
type piPlace struct {
	a      *agent.Agent
	dir    string
	home   string
	root   string // a WSL distro's /, "" for this machine
	roots  []string
	asleep bool
	err    error
}

// piPlaces are the Pis here; a var for tests.
var piPlaces = func() []piPlace {
	var out []piPlace
	for _, a := range detected() {
		kind, _, _ := strings.Cut(a.ID, "@")
		if kind != "pi" || a.CLIMissing() {
			continue
		}
		if a.WSL == "" {
			out = append(out, piPlace{a: a, dir: a.Dir, home: home(), roots: piGlobalRoots()})
			continue
		}
		if isAsleep(a) {
			out = append(out, piPlace{a: a, asleep: true})
			continue
		}
		t := wslTargetOf(a)
		if t == nil {
			out = append(out, piPlace{a: a, asleep: true})
			continue
		}
		h := a.Home
		p := piPlace{a: a, dir: filepath.Dir(t.Skills), home: h, roots: wslPiRoots(h), err: t.Unreached}
		if lh := linuxHome(h); lh != "" && strings.HasSuffix(filepath.ToSlash(h), lh) {
			p.root = h[:len(h)-len(lh)]
		}
		out = append(out, p)
	}
	return out
}

var piMu sync.Mutex

// ReadPiExtensions lists every Pi here, with its extensions and packages.
func ReadPiExtensions() *PiExtensions {
	v := &PiExtensions{Pis: []PiHere{}}
	for _, p := range piPlaces() {
		v.Pis = append(v.Pis, p.read())
	}
	return v
}

// piConf is what a Pi's settings.json says of its extensions and
// packages.
type piConf struct {
	exts []string
	pkgs []json.RawMessage
}

func (p piPlace) settingsPath() string { return filepath.Join(p.dir, "settings.json") }

// settings reads p's settings.json; one that isn't there is empty, one
// that can't be read or parsed is an error (unknown, never empty).
func (p piPlace) settings() (piConf, error) {
	var s piConf
	raw, err := edit.Read(p.settingsPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return s, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(jsonc.ToJSON(raw), &top); err != nil {
		return s, fmt.Errorf("%s: %w", p.settingsPath(), err)
	}
	if r, ok := top["extensions"]; ok && string(r) != "null" {
		if err := json.Unmarshal(r, &s.exts); err != nil {
			return s, fmt.Errorf("%s: extensions isn't a list of paths: %w", p.settingsPath(), err)
		}
	}
	if r, ok := top["packages"]; ok && string(r) != "null" {
		if err := json.Unmarshal(r, &s.pkgs); err != nil {
			return s, fmt.Errorf("%s: packages isn't a list: %w", p.settingsPath(), err)
		}
	}
	return s, nil
}

func (p piPlace) read() PiHere {
	h := PiHere{ID: p.a.ID, Name: p.a.Name, Icon: p.a.Icon, WSL: p.a.WSL, Asleep: p.asleep, Dir: p.dir, Extensions: []PiExt{}, Packages: []PiPackage{}}
	if p.asleep {
		return h
	}
	if p.err != nil {
		h.Err = p.err.Error()
		return h
	}
	s, err := p.settings()
	if err != nil {
		h.Err = err.Error()
		return h
	}
	h.Extensions = p.extensions(s)
	h.Packages = p.packages(s)
	return h
}

// ---- extensions -------------------------------------------------------------

// extensions are p's own: those settings.json lists by path, then those
// in its extensions folder, each once.
func (p piPlace) extensions(s piConf) []PiExt {
	var out []PiExt
	seen := map[string]bool{}
	for _, e := range s.exts {
		if piIsPattern(e) || strings.HasPrefix(e, "builtin:") {
			continue
		}
		at := p.resolve(e)
		fi, err := os.Stat(p.open(at))
		if err != nil {
			continue
		}
		var files []string
		if fi.IsDir() {
			files = p.under(at, piDirEntries)
		} else {
			files = []string{at}
		}
		if len(files) == 0 {
			continue
		}
		x := PiExt{ID: e, Name: filepath.Base(at), Path: e, Listed: true, Folder: fi.IsDir(), files: files, src: at}
		out = append(out, p.judge(x, s.exts, true))
		for _, f := range files {
			seen[f] = true
		}
	}
	dir := filepath.Join(p.dir, "extensions")
	// a folder that is itself one extension (a package.json or an index
	// there): its files, as one
	if files := p.under(dir, piResolveEntries); files != nil {
		x := PiExt{ID: "extensions", Name: "extensions", Path: p.shown(dir), Folder: true, files: files}
		if !slices.ContainsFunc(files, func(f string) bool { return seen[f] }) {
			out = append(out, p.judge(x, s.exts, false))
		}
		return out
	}
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		n := e.Name()
		if strings.HasPrefix(n, ".") || n == "node_modules" {
			continue
		}
		at := filepath.Join(dir, n)
		fi, err := os.Stat(p.open(at))
		if err != nil {
			continue
		}
		var files []string
		switch {
		case fi.Mode().IsRegular() && (strings.HasSuffix(n, ".ts") || strings.HasSuffix(n, ".js")):
			files = []string{at}
		case fi.IsDir():
			files = p.under(at, piResolveEntries)
		}
		if len(files) == 0 || slices.ContainsFunc(files, func(f string) bool { return seen[f] }) {
			continue
		}
		x := PiExt{ID: "extensions/" + n, Name: n, Path: p.shown(at), Folder: fi.IsDir(), files: files, src: at}
		out = append(out, p.judge(x, s.exts, false))
	}
	return out
}

// judge fills in x's keys and whether Pi loads it: on when any of its
// files is loaded.
func (p piPlace) judge(x PiExt, entries []string, listed bool) PiExt {
	for _, f := range x.files {
		x.keys = append(x.keys, p.key(f))
	}
	for _, f := range x.files {
		var on bool
		if listed {
			on = p.applied(f, entries)
		} else {
			on = p.enabled(f, entries)
		}
		if on {
			x.On = true
		}
	}
	return x
}

// key is the pattern Pi's `pi config` writes for f: its path from the
// agent folder, with forward slashes.
func (p piPlace) key(f string) string {
	rel, err := filepath.Rel(p.dir, f)
	if err != nil {
		return filepath.ToSlash(f)
	}
	return filepath.ToSlash(rel)
}

// posixAbs is f as its own machine names it: in a WSL distro, the
// distro's path.
func (p piPlace) posixAbs(f string) string {
	if p.root != "" && strings.HasPrefix(strings.ToLower(f), strings.ToLower(p.root)) {
		return "/" + strings.TrimLeft(filepath.ToSlash(f[len(p.root):]), "/")
	}
	return filepath.ToSlash(f)
}

// shown is f as the Pi's own machine names it, ~ for its home.
func (p piPlace) shown(f string) string {
	a := p.posixAbs(f)
	if h := p.posixAbs(p.home); h != "" && strings.HasPrefix(a, strings.TrimRight(h, "/")+"/") {
		return "~" + a[len(strings.TrimRight(h, "/")):]
	}
	if p.root == "" {
		return f
	}
	return a
}

// resolve is a path settings.json names, where magpie opens it: from the
// agent folder, ~ the Pi's home (resolvePathFromBase).
func (p piPlace) resolve(e string) string {
	e = strings.TrimSpace(e)
	switch {
	case e == "~":
		return p.home
	case strings.HasPrefix(e, "~/") || strings.HasPrefix(e, `~\`):
		return filepath.Join(p.home, filepath.FromSlash(e[2:]))
	case p.root != "" && strings.HasPrefix(e, "/"):
		return filepath.Join(p.root, filepath.FromSlash(e))
	case filepath.IsAbs(e):
		return e
	}
	return filepath.Join(p.dir, filepath.FromSlash(e))
}

// open is f where magpie can open it: in a WSL distro, below a link of
// the distro's, where the link leads (agent.WSLOpen).
func (p piPlace) open(f string) string {
	if p.a == nil || p.a.WSL == "" {
		return f
	}
	if o, err := agent.WSLOpen(p.a, f); err == nil {
		return o
	}
	return f
}

// under is find's files of the folder dir, named under dir as Pi names
// them (its path, not where a link there leads), though read where magpie
// opens it.
func (p piPlace) under(dir string, find func(string) []string) []string {
	o := p.open(dir)
	got := find(o)
	if o == dir || got == nil {
		return got
	}
	out := make([]string, 0, len(got))
	for _, f := range got {
		if rel, err := filepath.Rel(o, f); err == nil {
			f = filepath.Join(dir, rel)
		}
		out = append(out, f)
	}
	return out
}

func piIsPattern(s string) bool {
	return strings.HasPrefix(s, "!") || strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") || strings.ContainsAny(s, "*?")
}

func piOverride(s string) bool {
	return strings.HasPrefix(s, "!") || strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-")
}

// enabled is isEnabledByOverrides: a file Pi found in its extensions
// folder is loaded unless a "!" pattern matches it, then a "+" exact path
// brings it back and a "-" one takes it out.
func (p piPlace) enabled(f string, entries []string) bool {
	on := true
	for _, e := range entries {
		if strings.HasPrefix(e, "!") && p.matches(f, e[1:]) {
			on = false
		}
	}
	for _, e := range entries {
		if strings.HasPrefix(e, "+") && p.exact(f, e[1:]) {
			on = true
		}
	}
	for _, e := range entries {
		if strings.HasPrefix(e, "-") && p.exact(f, e[1:]) {
			on = false
		}
	}
	return on
}

// applied is applyPatterns for a file settings.json lists: its glob
// includes, when there are any, then "!", "+" and "-".
func (p piPlace) applied(f string, entries []string) bool {
	var inc []string
	for _, e := range entries {
		if piIsPattern(e) && !piOverride(e) {
			inc = append(inc, e)
		}
	}
	on := len(inc) == 0 || slices.ContainsFunc(inc, func(g string) bool { return p.matches(f, g) })
	for _, e := range entries {
		if strings.HasPrefix(e, "!") && p.matches(f, e[1:]) {
			on = false
		}
	}
	for _, e := range entries {
		if strings.HasPrefix(e, "+") && p.exact(f, e[1:]) {
			on = true
		}
	}
	for _, e := range entries {
		if strings.HasPrefix(e, "-") && p.exact(f, e[1:]) {
			on = false
		}
	}
	return on
}

// exact is matchesAnyExactPattern: the pattern, without a leading ./, is
// f's path from the agent folder or its whole path.
func (p piPlace) exact(f, pat string) bool {
	pat = strings.TrimPrefix(strings.TrimPrefix(pat, "./"), `.\`)
	pat = posix.Clean(filepath.ToSlash(pat))
	return pat == p.key(f) || pat == p.posixAbs(f)
}

// matches is matchesAnyPattern: a glob against f's path from the agent
// folder, its name, or its whole path.
func (p piPlace) matches(f, pat string) bool {
	re := piGlob(filepath.ToSlash(pat))
	return re.MatchString(p.key(f)) || re.MatchString(filepath.Base(f)) || re.MatchString(p.posixAbs(f))
}

// piGlob is a minimatch pattern as a regexp: ** any path, * and ? within
// one part, [..] a class.
func piGlob(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && i+1 < len(g) && g[i+1] == '*':
			i++
			if i+1 < len(g) && g[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			if j := strings.IndexByte(g[i:], ']'); j > 1 {
				cls := g[i+1 : i+j]
				if strings.HasPrefix(cls, "!") {
					cls = "^" + cls[1:]
				}
				b.WriteString("[" + cls + "]")
				i += j
			} else {
				b.WriteString(`\[`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile(`^$.`)
	}
	return re
}

// piResolveEntries is resolveExtensionEntries: the files a folder's
// package.json names under "pi.extensions" that are there, else its
// index.ts, else its index.js; nil for a folder that is no extension.
func piResolveEntries(dir string) []string {
	if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var m struct {
			Pi struct {
				Extensions []string `json:"extensions"`
			} `json:"pi"`
		}
		if json.Unmarshal(raw, &m) == nil && len(m.Pi.Extensions) > 0 {
			var out []string
			for _, e := range m.Pi.Extensions {
				f := filepath.Join(dir, filepath.FromSlash(e))
				if _, err := os.Stat(f); err == nil {
					out = append(out, f)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	for _, n := range []string{"index.ts", "index.js"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return []string{filepath.Join(dir, n)}
		}
	}
	return nil
}

// piDirEntries is collectAutoExtensionEntries, for a folder settings.json
// lists: the folder as one extension, else each in it.
func piDirEntries(dir string) []string {
	if out := piResolveEntries(dir); out != nil {
		return out
	}
	var out []string
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		n := e.Name()
		if strings.HasPrefix(n, ".") || n == "node_modules" {
			continue
		}
		at := filepath.Join(dir, n)
		fi, err := os.Stat(at)
		if err != nil {
			continue
		}
		if fi.Mode().IsRegular() && (strings.HasSuffix(n, ".ts") || strings.HasSuffix(n, ".js")) {
			out = append(out, at)
		} else if fi.IsDir() {
			out = append(out, piResolveEntries(at)...)
		}
	}
	return out
}

// ---- packages ---------------------------------------------------------------

func (p piPlace) packages(s piConf) []PiPackage {
	var out []PiPackage
	for _, r := range s.pkgs {
		pk, ok := piPackageOf(r)
		if !ok {
			continue
		}
		pk.Installed, pk.Version = p.installed(pk)
		out = append(out, pk)
	}
	return out
}

// piFilterKeys are a package entry's filters (PackageSource in
// settings-manager.d.ts).
var piFilterKeys = []string{"extensions", "skills", "prompts", "themes"}

// piPackageOf reads one "packages" entry: a source string, or an object
// with a source and filters. Off is every filter empty, which loads none
// of the package's resources; any other filter is the user's (or `pi
// config`'s), kept as it is.
func piPackageOf(r json.RawMessage) (PiPackage, bool) {
	pk := PiPackage{raw: r, On: true}
	var s string
	if json.Unmarshal(r, &s) != nil {
		var o map[string]json.RawMessage
		if json.Unmarshal(r, &o) != nil || json.Unmarshal(o["source"], &s) != nil {
			return pk, false
		}
		empty, set := 0, 0
		for _, k := range piFilterKeys {
			v, ok := o[k]
			if !ok || string(v) == "null" {
				continue
			}
			set++
			var l []string
			if json.Unmarshal(v, &l) == nil && len(l) == 0 {
				empty++
			}
		}
		// with autoload false only what the filters name loads
		noAuto := string(o["autoload"]) == "false"
		off := set == len(piFilterKeys) && empty == set
		pk.On = !off
		pk.Filtered = noAuto || set > 0 && !off
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return pk, false
	}
	pk.Source = s
	pk.Name, pk.Pin = piPackageName(s)
	pk.Kind = piKind(s)
	return pk, true
}

// piKind is parseSource's type: npm:…, a git source (git:…, a URL, an
// ssh address), or a local path.
func piKind(s string) string {
	switch {
	case strings.HasPrefix(s, "npm:"):
		return "npm"
	case strings.HasPrefix(s, "git:"), strings.HasPrefix(s, "git@"),
		strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"),
		strings.HasPrefix(s, "ssh://"), strings.HasPrefix(s, "git://"):
		return "git"
	}
	return "local"
}

// piGitPlace is a git source's host and path, the folder Pi clones it to
// under <agent dir>/git (getGitInstallPath), without its ref.
func piGitPlace(s string) (host, p string) {
	s = strings.TrimPrefix(s, "git:")
	cut := func(x string) string {
		if i := strings.LastIndex(x, "@"); i > 0 && !strings.Contains(x[i:], "/") {
			return x[:i]
		}
		return x
	}
	switch {
	case strings.HasPrefix(s, "git@"):
		hp := strings.TrimPrefix(s, "git@")
		host, p, _ = strings.Cut(hp, ":")
		p = cut(p)
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return "", ""
		}
		host, p = u.Hostname(), cut(strings.TrimPrefix(u.Path, "/"))
	default:
		host, p, _ = strings.Cut(s, "/")
		p = cut(p)
	}
	return host, strings.TrimSuffix(strings.Trim(p, "/"), ".git")
}

// piIdentity is getPackageIdentity: one package however its source pins
// it, so it isn't written twice.
func (p piPlace) piIdentity(s string) string {
	switch piKind(s) {
	case "npm":
		n, _ := piPackageName(s)
		return "npm:" + n
	case "git":
		h, gp := piGitPlace(s)
		return "git:" + strings.ToLower(h) + "/" + gp
	}
	return "local:" + p.resolve(s)
}

// installed says whether Pi has pk on disk, and its version there.
func (p piPlace) installed(pk PiPackage) (bool, string) {
	var dirs []string
	switch pk.Kind {
	case "npm":
		dirs = append(dirs, filepath.Join(p.dir, "npm", "node_modules", filepath.FromSlash(pk.Name)))
		for _, r := range p.roots {
			dirs = append(dirs, filepath.Join(r, filepath.FromSlash(pk.Name)))
		}
	case "git":
		h, gp := piGitPlace(pk.Source)
		if h == "" || gp == "" {
			return false, ""
		}
		dirs = append(dirs, filepath.Join(p.dir, "git", h, filepath.FromSlash(gp)))
	default:
		dirs = append(dirs, p.resolve(pk.Source))
	}
	for _, d := range dirs {
		if _, err := os.Stat(d); err != nil {
			continue
		}
		var m struct{ Version string }
		if raw, err := os.ReadFile(filepath.Join(d, "package.json")); err == nil {
			json.Unmarshal(raw, &m)
		}
		return true, m.Version
	}
	return false, ""
}

// ---- changes ----------------------------------------------------------------

func piPlaceOf(id string) (piPlace, error) {
	for _, p := range piPlaces() {
		if p.a.ID != id {
			continue
		}
		if p.asleep {
			return p, fmt.Errorf("WSL %s isn't running, and magpie doesn't start it to change %s: start the distro (open its terminal), then try again", p.a.WSL, p.a.Name)
		}
		if p.err != nil {
			return p, p.err
		}
		return p, nil
	}
	return piPlace{}, fmt.Errorf("%s isn't installed", id)
}

// SetPiExtension switches one of a Pi's own extensions on or off, as `pi
// config` does: "-<path>" in settings.json's extensions takes it out, and
// on takes that out again, adding "+<path>" only when a "!" pattern there
// would still leave it out.
func SetPiExtension(pi, id string, on bool) (*PiExtensions, error) {
	piMu.Lock()
	defer piMu.Unlock()
	p, err := piPlaceOf(pi)
	if err != nil {
		return nil, err
	}
	s, err := p.settings()
	if err != nil {
		return nil, err
	}
	var x *PiExt
	for _, e := range p.extensions(s) {
		if e.ID == id {
			x = &e
			break
		}
	}
	if x == nil {
		return nil, fmt.Errorf("%s has no extension %s", p.a.Name, id)
	}
	if x.On == on {
		return ReadPiExtensions(), nil
	}
	keys := map[string]bool{}
	for _, k := range x.keys {
		keys[k] = true
	}
	var next []string
	for _, e := range s.exts {
		// an override of one of its files is replaced, as `pi config`
		// replaces it; the user's paths and globs stay
		if piOverride(e) && keys[posix.Clean(strings.TrimPrefix(filepath.ToSlash(e[1:]), "./"))] {
			continue
		}
		next = append(next, e)
	}
	for _, k := range x.keys {
		if !on {
			next = append(next, "-"+k)
		}
	}
	if on {
		// still left out by a "!" pattern (or, for a listed one, a glob it
		// doesn't match): brought back by "+" as `pi config` does
		for i, f := range x.files {
			loaded := p.enabled(f, next)
			if x.Listed {
				loaded = p.applied(f, next)
			}
			if !loaded {
				next = append(next, "+"+x.keys[i])
			}
		}
	}
	if next == nil {
		next = []string{}
	}
	if err := newBackups().keep(p.a.ID, p.settingsPath()); err != nil {
		return nil, err
	}
	if err := edit.SetJSON(p.settingsPath(), edit.KV{Path: "extensions", Value: next}); err != nil {
		return nil, err
	}
	return ReadPiExtensions(), nil
}

// SetPiPackage switches a whole package on or off: off is its entry with
// every filter empty, so Pi keeps it installed and loads none of it; on is
// its source alone again. One whose entry has other filters is changed in
// `pi config`, which can pick them one by one.
func SetPiPackage(pi, source string, on bool) (*PiExtensions, error) {
	piMu.Lock()
	defer piMu.Unlock()
	p, err := piPlaceOf(pi)
	if err != nil {
		return nil, err
	}
	s, err := p.settings()
	if err != nil {
		return nil, err
	}
	at := -1
	var pk PiPackage
	for i, r := range s.pkgs {
		if x, ok := piPackageOf(r); ok && x.Source == source {
			at, pk = i, x
			break
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("%s has no package %s", p.a.Name, source)
	}
	if pk.On == on {
		return ReadPiExtensions(), nil
	}
	if pk.Filtered {
		return nil, fmt.Errorf("%s picks some of %s's resources; change it with `pi config`", p.a.Name, pk.Name)
	}
	var o map[string]json.RawMessage
	if json.Unmarshal(pk.raw, &o) != nil {
		o = map[string]json.RawMessage{}
		b, _ := json.Marshal(source)
		o["source"] = b
	}
	for _, k := range piFilterKeys {
		if on {
			delete(o, k)
		} else {
			o[k] = json.RawMessage("[]")
		}
	}
	var entry json.RawMessage
	if len(o) == 1 {
		entry, _ = json.Marshal(source)
	} else {
		entry, _ = json.Marshal(o)
	}
	next := slices.Clone(s.pkgs)
	next[at] = entry
	if err := newBackups().keep(p.a.ID, p.settingsPath()); err != nil {
		return nil, err
	}
	if err := edit.SetJSON(p.settingsPath(), edit.KV{Path: "packages", Value: next}); err != nil {
		return nil, err
	}
	return ReadPiExtensions(), nil
}

// PiCopied is what a copy did, for the page to say.
type PiCopied struct {
	*PiExtensions
	// Install: the copied extension has dependencies of its own, which
	// `npm install` in its folder on the other side fetches
	Install string `json:"install,omitempty"`
}

// CopyPi gives one of a Pi's extensions or packages to another Pi. A
// package is its settings.json entry, written into the other's packages;
// Pi there installs it when it next starts. An extension is its file or
// folder, copied into the other's extensions folder, without its
// node_modules (built for the machine it was on).
func CopyPi(from, to, ext, pkg string) (*PiCopied, error) {
	piMu.Lock()
	defer piMu.Unlock()
	if from == to {
		return nil, errors.New("pick another Pi to copy to")
	}
	src, err := piPlaceOf(from)
	if err != nil {
		return nil, err
	}
	dst, err := piPlaceOf(to)
	if err != nil {
		return nil, err
	}
	ss, err := src.settings()
	if err != nil {
		return nil, err
	}
	ds, err := dst.settings()
	if err != nil {
		return nil, err
	}
	if pkg != "" {
		return piCopyPackage(src, dst, ss, ds, pkg)
	}
	var x *PiExt
	for _, e := range src.extensions(ss) {
		if e.ID == ext {
			x = &e
			break
		}
	}
	if x == nil {
		return nil, fmt.Errorf("%s has no extension %s", src.a.Name, ext)
	}
	if x.src == "" {
		return nil, fmt.Errorf("%s's whole extensions folder is one extension; copy it by hand", src.a.Name)
	}
	name := filepath.Base(x.src)
	at := filepath.Join(dst.dir, "extensions", name)
	if _, err := os.Lstat(at); err == nil {
		return nil, fmt.Errorf("%s has an extension named %s already", piLabel(dst), name)
	}
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		return nil, err
	}
	if err := piCopyTree(src.open(x.src), at); err != nil {
		os.RemoveAll(at)
		return nil, err
	}
	out := &PiCopied{PiExtensions: ReadPiExtensions()}
	if x.Folder && piHasDeps(at) {
		out.Install = dst.shown(at)
	}
	return out, nil
}

func piCopyPackage(src, dst piPlace, ss, ds piConf, source string) (*PiCopied, error) {
	var pk *PiPackage
	for _, r := range ss.pkgs {
		if x, ok := piPackageOf(r); ok && x.Source == source {
			pk = &x
			break
		}
	}
	if pk == nil {
		return nil, fmt.Errorf("%s has no package %s", src.a.Name, source)
	}
	if pk.Kind == "local" {
		// a path on the first Pi's machine names nothing on the other's
		return nil, fmt.Errorf("%s is a folder on %s's machine, which %s can't install from; copy the folder there and add it with `pi install`", pk.Name, piLabel(src), piLabel(dst))
	}
	id := src.piIdentity(pk.Source)
	for _, r := range ds.pkgs {
		if x, ok := piPackageOf(r); ok && dst.piIdentity(x.Source) == id {
			return nil, fmt.Errorf("%s has %s already", piLabel(dst), pk.Name)
		}
	}
	next := append(slices.Clone(ds.pkgs), pk.raw)
	if err := newBackups().keep(dst.a.ID, dst.settingsPath()); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dst.dir, 0o755); err != nil {
		return nil, err
	}
	if err := edit.SetJSON(dst.settingsPath(), edit.KV{Path: "packages", Value: next}); err != nil {
		return nil, err
	}
	return &PiCopied{PiExtensions: ReadPiExtensions()}, nil
}

func piLabel(p piPlace) string { return p.a.Name }

// piHasDeps says whether a copied extension folder's package.json names
// dependencies, which its node_modules (left behind) held.
func piHasDeps(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var m struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	return json.Unmarshal(raw, &m) == nil && len(m.Dependencies) > 0
}

// piCopyTree copies a file, or a folder without its node_modules and .git;
// a link inside it is left out, as where it leads on one machine is
// nothing on the other. The top itself is followed: an extension that is
// a link to the user's own checkout is copied from there.
func piCopyTree(from, to string) error {
	fi, err := os.Stat(from)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return piCopyFile(from, to, fi.Mode().Perm())
	}
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if p == from {
			return os.MkdirAll(dst, 0o755)
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(dst, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return piCopyFile(p, dst, info.Mode().Perm())
	})
}

func piCopyFile(from, to string, perm fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

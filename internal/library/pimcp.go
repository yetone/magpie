package library

import (
	"bytes"
	"encoding/json"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
)

// Pi 0.99 (2026-09-29) has MCP of its own, a built-in extension, builtin:mcp
// (packages/coding-agent/src/extensions/mcp/config.ts, loadMcpConfig): it
// reads "mcpServers" from <agent dir>/mcp.json — PI_CODING_AGENT_DIR's,
// else ~/.pi/agent — and a trusted project's .pi/mcp.json, entries shaped
// as other clients have them (core/mcp-servers.ts, validateMcpServerConfig):
// command, args, env, cwd for a command; url, headers, oauth for
// streamable HTTP; "type" optional (stdio, http, streamable-http; "sse"
// refused: it has no SSE); exposure, toolExposure, enabled, timeout being
// the user's. An installed extension that registers /mcp, as
// pi-mcp-adapter does, replaces it, as does "-builtin:mcp" in
// settings.json's extensions (docs/mcp.md, "Other MCP extensions"): then
// the servers go where they did before 0.99.
//
// Before 0.99 Pi had none; two extensions give it one, each with its own
// file (the same entries: command/args/env, url/headers, and the
// transport each names differently, which magpie writes both of):
//
//   - pi-mcp-adapter 3 (3.0.0, 2026-09-26; 3.1.0 now) reads
//     <agent dir>/mcp-adapter.json after ~/.config/mcp/mcp.json and
//     ~/.agents/mcp.json, and no longer <agent dir>/mcp.json, which it
//     leaves to the MCP Pi is to have itself. At every session start it
//     warns "pi-mcp-adapter no longer reads …mcp.json" while that file has
//     any server (or settings, imports, claudePlugins) — whether or not
//     mcp-adapter.json is there too (config.ts,
//     getLegacyMcpMigrationNotices). Before 3 it read mcp.json.
//   - pi-mcp-extension 1.5 reads ~/.pi/agent/mcp.json only — its home's,
//     not PI_CODING_AGENT_DIR's (src/config.ts, loadConfig).
//
// So the adapter's servers go in mcp-adapter.json, and mcp.json is written
// only for the extension, or for an adapter older than 3.

// piPlugins is which MCP extensions Pi loads, from settings.json and
// auto-discovered extensions. Package storage only tells their versions.
type piPlugins struct {
	adapter bool
	major   int // pi-mcp-adapter's, 0 when not known
	ext     bool
	// noBuiltin: settings.json turns Pi's own MCP off (-builtin:mcp)
	noBuiltin bool
}

// piNativeSince is the first Pi that reads MCP servers itself.
const piNativeSince = "0.99.0"

// piVersion is the version of the Pi on PATH, "" when not known.
var piVersion = func(a *agent.Agent) string { return a.InstalledVersion() }

// piNative says whether Pi at version v reads its mcp.json itself.
func piNative(v string) bool {
	return v != "" && !agent.Newer(piNativeSince, v)
}

// piGlobalRoots are the global node_modules folders where Pi before its
// own npm folder installed packages (npm install -g), and still finds them.
var piGlobalRoots = func() []string {
	var out []string
	h := home()
	for _, k := range []string{"NPM_CONFIG_PREFIX", "npm_config_prefix"} {
		if p := os.Getenv(k); p != "" {
			if runtime.GOOS == "windows" {
				out = append(out, filepath.Join(p, "node_modules"))
			} else {
				out = append(out, filepath.Join(p, "lib", "node_modules"))
			}
		}
	}
	if runtime.GOOS == "windows" {
		if d := appdir.Getenv("APPDATA"); d != "" {
			out = append(out, filepath.Join(d, "npm", "node_modules"))
		}
		return out
	}
	return append(out, filepath.Join(h, ".npm-global", "lib", "node_modules"),
		"/usr/local/lib/node_modules", "/opt/homebrew/lib/node_modules", "/usr/lib/node_modules")
}

// piPackageName is the package a settings.json packages entry installs, and
// the version it is pinned to: npm:name@1.2.3, git:host/owner/name@v1.2.3,
// https://host/owner/name.git, or a folder.
func piPackageName(spec string) (name, pin string) {
	s := strings.TrimSpace(spec)
	if rest, ok := strings.CutPrefix(s, "npm:"); ok {
		name = rest
		if i := strings.LastIndex(rest, "@"); i > 0 {
			name, pin = rest[:i], rest[i+1:]
		}
		return name, pin
	}
	s = strings.TrimRight(strings.ReplaceAll(s, `\`, "/"), "/")
	name = s[strings.LastIndex(s, "/")+1:]
	if i := strings.LastIndex(name, "@"); i > 0 {
		name, pin = name[:i], name[i+1:]
	}
	return strings.TrimSuffix(name, ".git"), strings.TrimPrefix(pin, "v")
}

func piDetect(d string) piPlugins {
	var p piPlugins
	var settings struct {
		Packages   []json.RawMessage
		Extensions []string
	}
	if raw, _ := edit.Read(filepath.Join(d, "settings.json")); len(raw) > 0 {
		json.Unmarshal(jsonc.ToJSON(raw), &settings)
	}
	var specs []string
	for _, r := range settings.Packages {
		var s string
		var o struct {
			Source     string
			Extensions []string
			Autoload   *bool
		}
		if json.Unmarshal(r, &s) != nil && json.Unmarshal(r, &o) == nil {
			// With autoload off, Pi only loads explicit extension patterns.
			if len(o.Extensions) == 0 && (o.Extensions != nil || o.Autoload != nil && !*o.Autoload) {
				continue
			}
			s = o.Source
		}
		specs = append(specs, s)
	}
	for _, e := range settings.Extensions {
		// Pi separates override/glob patterns from extension sources.
		if e == "" || strings.ContainsAny(e[:1], "!+-") || strings.ContainsAny(e, "*?") {
			continue
		}
		dir := e
		if strings.HasPrefix(dir, "~/") {
			dir = filepath.Join(home(), dir[2:])
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(d, dir)
		}
		if piExtensionIn(dir, "", d, settings.Extensions) {
			specs = append(specs, dir)
		}
	}
	p.noBuiltin = !piExtensionEnabled("builtin:mcp", d, settings.Extensions)
	version := func(dir string) string {
		var pkg struct{ Name, Version string }
		b, _ := os.ReadFile(filepath.Join(dir, "package.json"))
		if json.Unmarshal(b, &pkg) != nil || pkg.Name != "pi-mcp-adapter" {
			return ""
		}
		return pkg.Version
	}
	var pin string
	var dirs []string
	for _, s := range specs {
		name, v := piPackageName(s)
		switch name {
		case "pi-mcp-extension":
			p.ext = true
		case "pi-mcp-adapter":
			p.adapter, pin = true, v
			if !strings.Contains(s, ":") || filepath.IsAbs(s) {
				dir := s
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(d, dir)
				}
				dirs = append(dirs, dir)
			}
		}
	}
	// Pi discovers extension entry points in extensions/, not npm/git
	// package storage (package-manager.ts, addAutoDiscoveredResources).
	if piExtensionIn(filepath.Join(d, "extensions", "pi-mcp-extension"), "pi-mcp-extension", d, settings.Extensions) {
		p.ext = true
	}
	auto := filepath.Join(d, "extensions", "pi-mcp-adapter")
	if piExtensionIn(auto, "pi-mcp-adapter", d, settings.Extensions) {
		p.adapter = true
		dirs = append(dirs, auto)
	}
	dirs = append(dirs, filepath.Join(d, "npm", "node_modules", "pi-mcp-adapter"))
	git, _ := filepath.Glob(filepath.Join(d, "git", "*", "*", "pi-mcp-adapter"))
	dirs = append(dirs, git...)
	for _, r := range piGlobalRoots() {
		dirs = append(dirs, filepath.Join(r, "pi-mcp-adapter"))
	}
	found := ""
	for _, dir := range dirs {
		if found = version(dir); found != "" {
			break
		}
	}
	if p.adapter {
		for _, v := range []string{pin, found} {
			if n, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0]); err == nil && n > 0 {
				p.major = n
				break
			}
		}
	}
	return p
}

// piExtensionIn recognizes enabled entry points in a known extension folder.
// Overrides apply to the resolved entries, not their containing package.
func piExtensionIn(dir, name, base string, overrides []string) bool {
	var pkg struct {
		Name string
		Pi   struct{ Extensions []string }
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	err := json.Unmarshal(raw, &pkg)
	if name != "" && (err != nil || pkg.Name != name) {
		return false
	}
	var entries []string
	for _, e := range pkg.Pi.Extensions {
		if f := filepath.Join(dir, e); exists(f) {
			entries = append(entries, f)
		}
	}
	// Pi falls back only when no manifest entry exists, not when disabled.
	if len(entries) == 0 {
		for _, e := range []string{"index.ts", "index.js"} {
			if f := filepath.Join(dir, e); exists(f) {
				entries = append(entries, f)
				break
			}
		}
	}
	return slices.ContainsFunc(entries, func(f string) bool { return piExtensionEnabled(f, base, overrides) })
}

// Pi's isEnabledByOverrides applies ! globs, then + exact paths, then -
// exact paths, regardless of their order. A + never discovers a new source.
func piExtensionEnabled(file, base string, overrides []string) bool {
	rel, err := filepath.Rel(base, file)
	if err != nil {
		rel = file
	}
	file, rel = filepath.ToSlash(file), filepath.ToSlash(rel)
	enabled := true
	for _, prefix := range []byte{'!', '+', '-'} {
		for _, p := range overrides {
			if len(p) < 2 || p[0] != prefix {
				continue
			}
			p = filepath.ToSlash(p[1:])
			if prefix == '!' {
				if piExtensionGlob(p, rel) || piExtensionGlob(p, pathpkg.Base(file)) || piExtensionGlob(p, file) {
					enabled = false
				}
			} else if p = strings.TrimPrefix(strings.TrimPrefix(p, "./"), `.\`); p == rel || p == file {
				enabled = prefix == '+'
			}
		}
	}
	return enabled
}

// Match slash-separated extension globs, including Pi's recursive **.
func piExtensionGlob(pattern, file string) bool {
	p, rest, more := strings.Cut(pattern, "/")
	f, tail, nested := strings.Cut(file, "/")
	if p == "**" {
		if more && piExtensionGlob(rest, file) || !more && file == "" {
			return true
		}
		if strings.HasPrefix(f, ".") {
			return false
		}
		return !more && !nested || nested && piExtensionGlob(pattern, tail)
	}
	if strings.HasPrefix(f, ".") && !strings.HasPrefix(p, ".") {
		return false
	}
	// minimatch negates a character class with !; path.Match uses ^.
	for i := 0; i+1 < len(p); i++ {
		if p[i] == '\\' {
			i++
		} else if p[i] == '[' && p[i+1] == '!' {
			p = p[:i+1] + "^" + p[i+2:]
			i++
		}
	}
	match, _ := pathpkg.Match(p, f)
	return match && (more && nested && piExtensionGlob(rest, tail) || !more && !nested)
}

// piMCP is the file Pi's MCP servers go in, and the extension Pi reads them
// through ("" for its own), h being the home, d Pi's agent folder and
// version the Pi on PATH's:
//   - Pi 0.99 or later with no MCP extension installed and its own MCP on:
//     mcp.json, as Pi has it. The servers mcp-adapter.json has, which no
//     one reads now (magpie's for pi-mcp-adapter 3, or the user's), are
//     moved into it, the file backed up; one mcp.json has differently, and
//     the adapter's own settings, stay there, still found. An mcp.json
//     written for pi-mcp-adapter before 3 is read by Pi as it is (the
//     adapter's httpTransport ignored).
//   - otherwise, as before 0.99 — pi-mcp-adapter, still installed, stands
//     in for Pi's own MCP (piFiles).
func piMCP(h, d, version string) (*mcpFile, string) {
	p := piDetect(d)
	if piNative(version) && !p.adapter && !p.ext && !p.noBuiltin {
		native, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
		piMove(adapter, native, true)
		f := &mcpFile{Path: native, Format: fmtPiNative}
		if exists(adapter) {
			f.Extra = []string{adapter}
		}
		return f, ""
	}
	via := "pi-mcp-adapter"
	if p.ext && !p.adapter {
		via = "pi-mcp-extension"
	}
	return piFiles(h, d, p), via
}

// piFiles is the file an extension reads Pi's MCP servers from:
//   - pi-mcp-adapter 3, or one whose version isn't known (a fresh install
//     is 3), or mcp-adapter.json already there: mcp-adapter.json. The
//     servers magpie or the user put in mcp.json are moved over, as the
//     adapter asks, unless pi-mcp-extension reads that file or the
//     adapter isn't found (mcp.json may then be Pi 0.99's own);
//   - with pi-mcp-extension too, both files: each extension runs its own
//     servers, and the adapter's warning stays while mcp.json has any,
//     which is the extension's file;
//   - pi-mcp-adapter before 3, or only pi-mcp-extension: mcp.json;
//   - neither found: mcp.json if there is one (nothing says whose it is,
//     so it isn't moved), mcp-adapter.json if not.
func piFiles(h, d string, p piPlugins) *mcpFile {
	adapter, old := filepath.Join(d, "mcp-adapter.json"), filepath.Join(d, "mcp.json")
	extFile := filepath.Join(h, ".pi", "agent", "mcp.json")
	v3 := exists(adapter) || p.major >= 3 || (p.adapter && p.major == 0)
	switch {
	case !v3 && !p.adapter && !p.ext && exists(old):
		return &mcpFile{Path: old, Format: fmtPi}
	case !v3 && p.adapter:
		return &mcpFile{Path: old, Format: fmtPi}
	case !v3 && p.ext:
		return &mcpFile{Path: extFile, Format: fmtPi}
	}
	f := &mcpFile{Path: adapter, Format: fmtPi}
	if p.ext {
		f.Also = []string{extFile}
	}
	if !p.ext || old != extFile {
		// only for an adapter that's there: without it, mcp.json may be
		// Pi 0.99's own, its version not known (not on PATH)
		if p.adapter {
			piMove(old, adapter, false)
		}
		if exists(old) {
			f.Extra = []string{old}
		}
	}
	return f
}

// piKey is a server name that can be written as one step of a key path.
var piKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// piMove moves what pi-mcp-adapter read from mcp.json into
// mcp-adapter.json: the whole file when there is none yet, as the adapter's
// own warning says; into one there, each server and setting it hasn't got.
// Either way the files are backed up first. A server or setting the two have
// differently stays in mcp.json for the user to merge, as does anything
// the adapter never read; mcp.json goes once nothing is left in it.
//
// serversOnly moves the other way, mcp-adapter.json's servers into the
// mcp.json Pi 0.99 reads itself: only servers, never the whole file, the
// adapter's settings, imports and claudePlugins staying where they are.
func piMove(from, to string, serversOnly bool) {
	raw, err := edit.Read(from)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(jsonc.ToJSON(raw), &doc) != nil || !piHasContent(doc, serversOnly) {
		return
	}
	if !serversOnly && !exists(to) {
		// the user's mcp.json is copied aside before it is moved, as it is
		// before a merge (#1097); one that can't be kept stays where it is
		if newBackups().keep("pi", from) == nil {
			os.Rename(from, to)
		}
		return
	}
	var have map[string]json.RawMessage
	if b, err := edit.Read(to); err != nil || json.Unmarshal(jsonc.ToJSON(orEmpty(b)), &have) != nil {
		return
	}
	var haveServers map[string]json.RawMessage
	json.Unmarshal(have["mcpServers"], &haveServers)
	if haveServers == nil {
		haveServers = map[string]json.RawMessage{}
	}
	same := func(a, b json.RawMessage) bool {
		var x, y bytes.Buffer
		return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
	}
	var put []edit.KV
	var del []string
	for _, key := range []string{"mcpServers", "mcp-servers"} {
		var servers map[string]json.RawMessage
		json.Unmarshal(doc[key], &servers)
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			if !piKey.MatchString(n) {
				continue
			}
			if cur, ok := haveServers[n]; !ok {
				put = append(put, edit.KV{Path: "mcpServers." + n, Value: servers[n]})
				haveServers[n] = servers[n]
			} else if !same(cur, servers[n]) {
				continue
			}
			del = append(del, key+"."+n)
		}
	}
	for _, key := range piSettings(serversOnly) {
		v, ok := doc[key]
		if !ok {
			continue
		}
		if cur, ok := have[key]; !ok {
			put = append(put, edit.KV{Path: key, Value: v})
		} else if !same(cur, v) {
			continue
		}
		del = append(del, key)
	}
	if len(del) == 0 {
		return
	}
	b := newBackups()
	if b.keep("pi", from) != nil || b.keep("pi", to) != nil {
		return
	}
	// both files, or neither
	err = edit.Atomically(func() error {
		if len(put) > 0 {
			if err := edit.SetJSON(to, put...); err != nil {
				return err
			}
		}
		return edit.DelJSON(from, del...)
	}, from, to)
	if err != nil {
		return
	}
	// nothing left but empty server lists: the file goes
	raw, _ = edit.Read(from)
	doc = nil
	if json.Unmarshal(jsonc.ToJSON(orEmpty(raw)), &doc) == nil {
		for _, key := range []string{"mcpServers", "mcp-servers"} {
			var m map[string]json.RawMessage
			if json.Unmarshal(doc[key], &m) == nil && len(m) == 0 {
				delete(doc, key)
			}
		}
		if len(doc) == 0 && !edit.IsLink(from) { // a linked one stays, emptied
			os.Remove(from)
		}
	}
}

// piSettings are pi-mcp-adapter's keys beside its servers, moved with them
// unless only the servers are.
func piSettings(serversOnly bool) []string {
	if serversOnly {
		return nil
	}
	return []string{"settings", "imports", "claudePlugins"}
}

// piHasContent is what makes pi-mcp-adapter warn of an mcp.json: with
// serversOnly, any server.
func piHasContent(doc map[string]json.RawMessage, serversOnly bool) bool {
	for _, key := range []string{"mcpServers", "mcp-servers"} {
		var m map[string]json.RawMessage
		if json.Unmarshal(doc[key], &m) == nil && len(m) > 0 {
			return true
		}
	}
	for _, key := range piSettings(serversOnly) {
		if _, ok := doc[key]; ok {
			return true
		}
	}
	return false
}

func orEmpty(b []byte) []byte {
	if len(bytes.TrimSpace(b)) == 0 {
		return []byte("{}")
	}
	return b
}

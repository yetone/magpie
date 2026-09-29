package library

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
)

// Pi has no MCP of its own; two extensions give it one, each with its own
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

// piPlugins is which of the two Pi has, from the packages its settings.json
// lists and the folders Pi installs them in.
type piPlugins struct {
	adapter bool
	major   int // pi-mcp-adapter's, 0 when not known
	ext     bool
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
		if d := os.Getenv("APPDATA"); d != "" {
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
		var o struct{ Source string }
		if json.Unmarshal(r, &s) != nil && json.Unmarshal(r, &o) == nil {
			s = o.Source
		}
		specs = append(specs, s)
	}
	specs = append(specs, settings.Extensions...)
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
	if exists(filepath.Join(d, "npm", "node_modules", "pi-mcp-extension")) {
		p.ext = true
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
			p.adapter = true
			break
		}
	}
	for _, v := range []string{pin, found} {
		if n, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0]); err == nil && n > 0 {
			p.major = n
			break
		}
	}
	return p
}

// piMCP is the file Pi's MCP servers go in, h being the home and d Pi's
// agent folder:
//   - pi-mcp-adapter 3, or one whose version isn't known (a fresh install
//     is 3), or mcp-adapter.json already there: mcp-adapter.json. The
//     servers magpie or the user put in mcp.json are moved over, as the
//     adapter asks, unless pi-mcp-extension reads that file;
//   - with pi-mcp-extension too, both files: each extension runs its own
//     servers, and the adapter's warning stays while mcp.json has any,
//     which is the extension's file;
//   - pi-mcp-adapter before 3, or only pi-mcp-extension: mcp.json;
//   - neither found: mcp.json if there is one (nothing says whose it is,
//     so it isn't moved), mcp-adapter.json if not.
func piMCP(h, d string) *mcpFile {
	adapter, old := filepath.Join(d, "mcp-adapter.json"), filepath.Join(d, "mcp.json")
	extFile := filepath.Join(h, ".pi", "agent", "mcp.json")
	p := piDetect(d)
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
		piMove(old, adapter)
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
// own warning says; into one there, each server and setting it hasn't got
// (the file backed up first). A server or setting the two have
// differently stays in mcp.json for the user to merge, as does anything
// the adapter never read; mcp.json goes once nothing is left in it.
func piMove(from, to string) {
	raw, err := edit.Read(from)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(jsonc.ToJSON(raw), &doc) != nil || !piHasContent(doc) {
		return
	}
	if !exists(to) {
		os.Rename(from, to)
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
	for _, key := range []string{"settings", "imports", "claudePlugins"} {
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
	if len(del) == 0 || newBackups().keep("pi", from) != nil {
		return
	}
	if len(put) > 0 && edit.SetJSON(to, put...) != nil {
		return
	}
	if edit.DelJSON(from, del...) != nil {
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
		if len(doc) == 0 {
			os.Remove(from)
		}
	}
}

// piHasContent is what makes pi-mcp-adapter warn of an mcp.json.
func piHasContent(doc map[string]json.RawMessage) bool {
	for _, key := range []string{"mcpServers", "mcp-servers"} {
		var m map[string]json.RawMessage
		if json.Unmarshal(doc[key], &m) == nil && len(m) > 0 {
			return true
		}
	}
	for _, key := range []string{"settings", "imports", "claudePlugins"} {
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

package library

import (
	"encoding/json"
	"errors"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
)

// Claude Code's own plugins, the ones installed in Claude Code through its
// /plugin marketplaces (AXmake110 on X), go with the library: the
// marketplaces it knows and the plugins turned on for the user, so a
// computer that syncs ends up with the same plugins. They are not the
// library's plugin bundles (plugin.go), which magpie keeps and gives to
// every agent itself.
//
// Claude Code keeps them in its folder (claudeDir, CLAUDE_CONFIG_DIR too):
//
//	plugins/known_marketplaces.json  {name: {source: {source: "github", repo}, installLocation, lastUpdated}}
//	plugins/installed_plugins.json   {version: 2, plugins: {"p@m": [{scope, installPath, version, …}]}}
//	settings.json                    enabledPlugins: {"p@m": true}, extraKnownMarketplaces: {name: {source}}
//
// What is carried is each marketplace's source and the user's enabled
// plugin ids. installLocation, installPath and the downloaded code in
// plugins/marketplaces and plugins/cache are this computer's, and Claude
// Code fetches them again from the sources. On the other computer they
// are written where Claude Code reads a declared setup from:
// settings.json's extraKnownMarketplaces and enabledPlugins. At its next
// interactive start Claude Code clones the marketplaces and installs the
// plugins into its own files (tried with Claude Code 2.1.319 in a sandbox
// CLAUDE_CONFIG_DIR: commit-commands@claude-code-plugins installed from
// those two settings alone; `claude -p` does not install them).
//
// Only adding: a marketplace or plugin the other computer has under that
// name, enabled or turned off there, stays as it is there, and a plugin
// removed on one computer is not removed on the others.
type ClaudePlugins struct {
	// Marketplaces are by name, each as extraKnownMarketplaces holds one:
	// {"source": {…}} and its autoUpdate when set.
	Marketplaces map[string]json.RawMessage `json:"marketplaces,omitempty"`
	// Enabled are the plugins turned on for the user, as plugin@marketplace.
	Enabled []string `json:"enabled,omitempty"`
}

// claudeOfficial is the marketplace Claude Code adds by itself: its
// plugins are carried, the marketplace is not (a settings entry under its
// name adds nothing, as Claude Code says).
const claudeOfficial = "claude-plugins-official"

func claudeSettings() string { return filepath.Join(claudeDir(), "settings.json") }
func claudeMarketplaces() string {
	return filepath.Join(claudeDir(), "plugins", "known_marketplaces.json")
}
func claudeInstalled() string { return filepath.Join(claudeDir(), "plugins", "installed_plugins.json") }

// readClaudeJSON is a file of Claude Code's as JSON: nil when there is
// none or it is empty, an error when it can't be read as an object.
func readClaudeJSON(path string) ([]byte, error) {
	doc, err := readHooksFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return doc, err
}

// userinfo is a URL with a user (and maybe a password or token) in it.
var userinfo = lazyre.New(`(?i)[a-z][a-z0-9+.-]*://[^/\s"@]+@`)

// portableMarketplace is a marketplace's entry as it goes, or false when it
// stays on this computer: a folder or a file of this computer's, or a
// source with a sign-in in it (a token in a git URL, a request header).
func portableMarketplace(entry gjson.Result) (json.RawMessage, bool) {
	src := entry.Get("source")
	if !src.IsObject() {
		return nil, false
	}
	switch src.Get("source").String() {
	case "", "directory", "file":
		return nil, false
	}
	if src.Get("path").Exists() || src.Get("headers").Exists() || userinfo.Regexp().MatchString(src.Raw) {
		return nil, false
	}
	out := map[string]json.RawMessage{"source": json.RawMessage(src.Raw)}
	if a := entry.Get("autoUpdate"); a.IsBool() {
		out["autoUpdate"] = json.RawMessage(a.Raw)
	}
	j, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return j, true
}

// carriedClaudePlugins is this computer's Claude Code plugins as they go,
// nil when there are none, or when a file of them can't be read: unknown
// is never carried as none.
func carriedClaudePlugins() *ClaudePlugins {
	known, err := readClaudeJSON(claudeMarketplaces())
	if err != nil {
		return nil
	}
	set, err := readClaudeJSON(claudeSettings())
	if err != nil {
		return nil
	}
	cp := &ClaudePlugins{Marketplaces: map[string]json.RawMessage{}}
	add := func(r gjson.Result) {
		r.ForEach(func(k, v gjson.Result) bool {
			name := k.String()
			if _, ok := cp.Marketplaces[name]; ok || name == claudeOfficial {
				return true
			}
			if j, ok := portableMarketplace(v); ok {
				cp.Marketplaces[name] = j
			}
			return true
		})
	}
	add(gjson.GetBytes(set, "extraKnownMarketplaces"))
	add(gjson.ParseBytes(known))
	gjson.GetBytes(set, "enabledPlugins").ForEach(func(k, v gjson.Result) bool {
		id := k.String()
		_, m, ok := strings.Cut(id, "@")
		if !ok || v.Type != gjson.True {
			return true
		}
		if _, carried := cp.Marketplaces[m]; carried || m == claudeOfficial {
			cp.Enabled = append(cp.Enabled, id)
		}
		return true
	})
	slices.Sort(cp.Enabled)
	if len(cp.Enabled) == 0 && len(cp.Marketplaces) == 0 {
		return nil
	}
	if len(cp.Marketplaces) == 0 {
		cp.Marketplaces = nil
	}
	return cp
}

// claudePluginsChanged is when Claude Code last added or took away a
// marketplace or a plugin here, zero when it never did.
func claudePluginsChanged() time.Time {
	var t time.Time
	for _, p := range []string{claudeMarketplaces(), claudeInstalled()} {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

// putClaudePlugins adds to Claude Code's settings.json the marketplaces and
// enabled plugins cp carries that it doesn't have yet, written the way
// Claude Code writes it (a temp file renamed in, every other setting as
// it was); the file it replaces is kept with the change's backups. A
// marketplace this computer knows under that name, and a plugin it has
// any value for (turned off here too), is left as it is. A settings.json
// or known_marketplaces.json that can't be read is not written to.
func putClaudePlugins(cp *ClaudePlugins, kept *backups) error {
	p := claudeSettings()
	set, err := readClaudeJSON(p)
	if err != nil {
		log.Printf("library: Claude Code's plugins not added: %v", err)
		return nil
	}
	known, err := readClaudeJSON(claudeMarketplaces())
	if err != nil {
		log.Printf("library: Claude Code's plugins not added: %v", err)
		return nil
	}
	have := func(name string) bool {
		return name == claudeOfficial || gjson.GetBytes(set, "extraKnownMarketplaces."+gjson.Escape(name)).Exists() ||
			gjson.GetBytes(known, gjson.Escape(name)).Exists()
	}
	var kvs []edit.KV
	came := map[string]bool{} // the carried marketplaces fit to add
	for _, name := range slices.Sorted(maps.Keys(cp.Marketplaces)) {
		var v any
		if strings.TrimSpace(name) == "" || json.Unmarshal(cp.Marketplaces[name], &v) != nil {
			continue
		}
		if _, ok := portableMarketplace(gjson.ParseBytes(cp.Marketplaces[name])); !ok {
			continue
		}
		came[name] = true
		if !have(name) {
			kvs = append(kvs, edit.KV{Path: "extraKnownMarketplaces." + gjson.Escape(name), Value: v})
		}
	}
	for _, id := range cp.Enabled {
		_, m, ok := strings.Cut(id, "@")
		if !ok || gjson.GetBytes(set, "enabledPlugins."+gjson.Escape(id)).Exists() {
			continue
		}
		if !came[m] && !have(m) {
			continue
		}
		kvs = append(kvs, edit.KV{Path: "enabledPlugins." + gjson.Escape(id), Value: true})
	}
	if len(kvs) == 0 {
		return nil
	}
	raw, err := edit.Read(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out, err := edit.PatchJSON(raw, kvs, nil)
	if err != nil {
		log.Printf("library: Claude Code's plugins not added to %s: %v", p, err)
		return nil
	}
	if err := kept.keep("claude", p); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(p, out)
}

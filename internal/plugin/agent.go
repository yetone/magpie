package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Agent is the agent a plugin at target adds magpie's wiring for: the
// file its package.json's magpie.agent names, or the file itself for a
// path to a *.agent.js (or .mjs). only is whether it is nothing else: no
// OpenCode plugin for the host to load (no main, no exports).
func Agent(target string) (file string, only bool) {
	st, err := os.Stat(target)
	if err != nil {
		return "", false
	}
	if !st.IsDir() {
		base := strings.ToLower(filepath.Base(target))
		if strings.HasSuffix(base, ".agent.js") || strings.HasSuffix(base, ".agent.mjs") {
			return target, true
		}
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil {
		return "", false
	}
	var pkg struct {
		Main    string          `json:"main"`
		Exports json.RawMessage `json:"exports"`
		Magpie  struct {
			Agent string `json:"agent"`
		} `json:"magpie"`
	}
	if json.Unmarshal(b, &pkg) != nil || strings.TrimSpace(pkg.Magpie.Agent) == "" {
		return "", false
	}
	file = filepath.Join(target, filepath.FromSlash(strings.TrimSpace(pkg.Magpie.Agent)))
	return file, pkg.Main == "" && len(pkg.Exports) == 0
}

// InMagpieOnly is whether everything a plugin at target has runs in
// magpie's own process, its gateway middleware, its agent and what it adds
// to the window, or a privacy model launcher, so the host has nothing of it to load.
func InMagpieOnly(target string) bool {
	if Gateway(target) != "" {
		return false
	}
	mw, mwOnly := Middleware(target)
	ag, agOnly := Agent(target)
	pg, pgOnly := Window(target)
	privacy, privacyOnly := PrivacyModels(target)
	if mw == "" && ag == "" && pg == nil && privacy == nil {
		return false
	}
	return (mw == "" || mwOnly) && (ag == "" || agOnly) && (pg == nil || pgOnly) && (privacy == nil || privacyOnly)
}

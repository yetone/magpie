package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// legacyID is what agents knew the gateway by before magpie was called magpie.
const legacyID = "dial"

// RenameLegacy rewrites what an older dial left in the agents' files so it
// says magpie: a model spelled dial/… becomes magpie/… (which also writes
// the magpie provider entry the agent needs), and then the dial entry,
// Codex's table and catalog file, and Claude Code's token go. It runs at
// start-up; once nothing says dial any more it changes nothing. Errors are
// swallowed on purpose — a file magpie cannot rewrite is one the user can
// still fix from the picker.
func RenameLegacy() {
	for _, a := range All() {
		if _, err := os.Stat(a.Path); err != nil {
			continue
		}
		if a.ID == "codex" {
			// Set() would stash "dial" as the provider to go back to; say
			// magpie first so it sees a config that is already routed.
			if v, _ := edit.GetTOMLTop(a.Path, "model_provider"); v == legacyID {
				_ = edit.SetTOMLTop(a.Path, edit.KV{Path: "model_provider", Value: magpieID})
				if f := a.Field("model"); f != nil {
					_ = f.Set(f.Get())
				}
			}
		}
		for i := range a.Fields {
			f := &a.Fields[i]
			if rest, ok := strings.CutPrefix(f.Get(), legacyID+"/"); ok {
				_ = f.Set(magpieID + "/" + rest)
			}
		}
		switch a.ID {
		case "opencode":
			_ = edit.DelJSON(a.Path, "provider."+legacyID)
		case "crush":
			_ = edit.DelJSON(a.Path, "providers."+legacyID)
		case "pi":
			_ = edit.DelJSON(filepath.Join(a.Dir, "models.json"), "providers."+legacyID)
		case "codex":
			if tables, err := edit.TOMLTables(a.Path); err == nil && slices.Contains(tables, "model_providers."+legacyID) {
				_ = edit.DelTOMLTable(a.Path, "model_providers."+legacyID)
			}
			os.Remove(filepath.Join(a.Dir, legacyID+"-models.json"))
		case "claude":
			if t, _ := edit.GetJSON(a.Path, "env.ANTHROPIC_AUTH_TOKEN"); t == legacyID {
				_ = edit.SetJSON(a.Path, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token})
			}
		}
	}
}

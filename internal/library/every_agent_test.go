package library

import (
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// noLibrary are the built-in agents the Library gives nothing to, each
// with why. Every other agent has a place for skills, MCP servers or
// instructions: OpenBot was added with none, so it was in no picker of the
// Library (SSNQT on X). A new agent fails TestEveryAgentInLibrary until it
// gets a target or a line here.
var noLibrary = map[string]string{
	"claude-science":    "not looked at (#5897)",
	"antigravity":       "not looked at (#5897)",
	"openchamber":       "not looked at (#5897)",
	"mothx":             "not looked at (#5897)",
	"penguin":           "not looked at (#5897)",
	"aside":             "not looked at (#5897)",
	"cursor-local":      "not looked at (#5897)",
	"vscode":            "not looked at (#5897)",
	"vscode-insiders":   "not looked at (#5897)",
	"vscodium":          "not looked at (#5897)",
	"zoo-code":          "not looked at (#5897)",
	"copilot-jetbrains": "not looked at (#5897)",
	"air":               "not looked at (#5897)",
	"air-ide":           "not looked at (#5897)",
	"ds-bot":            "not looked at (#5897)",
	"morph":             "not looked at (#5897)",
	"muse":              "not looked at (#5897)",
	"empryo":            "not looked at (#5897)",
	"nanobot":           "not looked at (#5897)",
	"ante":              "not looked at (#5897)",
	"astrbot":           "not looked at (#5897)",
	"qwenpaw":           "not looked at (#5897)",
	"openclaw":          "not looked at (#5897)",
	"pencil":            "not looked at (#5897)",
	"openpencil":        "not looked at (#5897)",
	"newmax":            "not looked at (#5897)",
	"raycast":           "not looked at (#5897)",
	"kirocrew":          "not looked at (#5897)",
	"t3code":            "not looked at (#5897)",
	"openmausbot":       "not looked at (#5897)",
	"snow":              "not looked at (#5897)",
	"aionui":            "not looked at (#5897)",
	"cherrystudio":      "not looked at (#5897)",
	"lody":              "not looked at (#5897)",
	"miniq":             "not looked at (#5897)",
	"paimon":            "not looked at (#5897)",
}

func TestEveryAgentInLibrary(t *testing.T) {
	sandbox(t)
	for _, a := range agent.All() {
		if a.WSL != "" || a.Plugin != "" {
			continue // a distro's twin and a plugin's agent have their own
		}
		why, listed := noLibrary[a.ID]
		tg := targetOf(a)
		switch {
		case tg == nil && !listed:
			t.Errorf("%s (%s): no place in the Library; give it a target in targetOf, or a reason in noLibrary", a.ID, a.Name)
		case tg != nil && listed:
			t.Errorf("%s: in noLibrary (%q) but has a target", a.ID, why)
		case tg != nil && tg.Skills == "" && tg.MCP == nil && tg.Instructions == "":
			t.Errorf("%s: a target with nothing in it", a.ID)
		}
	}
}

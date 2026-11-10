package provider

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// Claude Desktop runs the Claude Code of its Code tab with
// CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=1, and Claude Code then drops
// CLAUDE_CODE_SUBAGENT_MODEL, CLAUDE_CODE_SUBAGENT_MODEL_FORCE and the
// tiers' variables from the env of its settings files ("Ignoring … this
// session's provider routing is managed by the host", Claude Code
// 2.1.295). Desktop's own config has no key for it either (the env it
// builds for the Code tab, Desktop 2.31226). So the subagent model magpie
// writes into ~/.claude doesn't reach Desktop: its subagents run on the
// lead's model or on a tier's, and only the tiers could be picked.
// settings.DesktopSubagentModel is the model magpie puts every subagent
// turn of Desktop's on, whatever its lead or tier asked for: the gateway
// rewrites a subagent's request (x-claude-code-agent-id, with tools) to
// it, read on each request, so Desktop needn't restart. Its lead's turns
// and the tool-less helper calls Claude Code makes inside a subagent stay
// on the model they asked for. "" leaves every subagent as it was.

// DesktopSubagentModel is the model Desktop's subagents are put on, ""
// for the one the lead asks for. A provider renamed since it was set is
// named by its new id (RenamedRef), as a profile's model is.
func DesktopSubagentModel() string {
	id := strings.TrimSpace(heldSettings().DesktopSubagentModel)
	if id == "" {
		return ""
	}
	return RenamedRef(id)
}

// SetDesktopSubagentModel puts Desktop's subagent turns on id, a model or
// routing group of magpie's (provider/model, group/<id>); "" leaves them
// on the model their lead asks for. One magpie doesn't serve now is kept
// all the same, as Codex's subagent model is, so a backup or profile put
// back on a computer whose providers differ still applies; the gateway
// leaves subagents on the model asked for until it is served. A bare name
// is refused: it names no model of magpie's.
func SetDesktopSubagentModel(id string) error {
	id = strings.TrimSpace(id)
	if pid, model, ok := strings.Cut(id, "/"); id != "" && (!ok || pid == "" || model == "") {
		return fmt.Errorf("Claude Desktop's subagents can't be put on %q: pick one of magpie's models (provider/model)", id)
	}
	s := settings.Load()
	if s.DesktopSubagentModel == id {
		return nil
	}
	s.DesktopSubagentModel = id
	if err := settings.Save(s); err != nil {
		return err
	}
	Changed()
	return nil
}

// DesktopSubagentUnserved says why Desktop's subagents can't be put on
// id now, "" when magpie serves that exact model or routing group.
// Resolve isn't enough: it takes any model name under a provider that is
// on, and the subagent would go to the vendor for a model it lacks.
func DesktopSubagentUnserved(id string) string { return Unserved(id) }

// Unserved says why a request can't be put on id, a model or routing
// group a setting of magpie's names, "" when magpie serves that exact one.
func Unserved(id string) string {
	id = RenamedRef(id)
	for _, e := range Served() {
		if e.ID == id {
			return ""
		}
	}
	return "magpie doesn't serve it now"
}

// DesktopSubagentEffort is the effort set for Desktop's subagent turns,
// independent of their model. Empty leaves each request's effort alone.
func DesktopSubagentEffort() string {
	return heldSettings().DesktopSubagentEffort
}

// SetDesktopSubagentEffort sets a level the gateway fits to the model
// each subagent reaches. It survives model changes; "" clears the pick.
func SetDesktopSubagentEffort(level string) error {
	level = strings.ToLower(strings.TrimSpace(level))
	if level != "" && !slices.Contains(MemberEfforts, level) {
		return fmt.Errorf("%q is not a reasoning effort: one of %s", level, strings.Join(MemberEfforts, ", "))
	}
	s := settings.Load()
	if s.DesktopSubagentEffort == level {
		return nil
	}
	s.DesktopSubagentEffort = level
	if err := settings.Save(s); err != nil {
		return err
	}
	Changed()
	return nil
}

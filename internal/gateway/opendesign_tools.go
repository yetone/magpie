package gateway

import (
	"encoding/json"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// Open Design Cloud's amr-link answers a request it, or the vendor behind
// it, turns away with a 400 "[code=upstream_error]" and often no more than
// "Provider returned error". Codex's compaction is the one request Codex
// makes with no tools ("tools": [], codex-26.1007-compaction.json), and
// magpie's summary ask for it adds the tool_choice and parallel_tool_calls
// a relay of the ChatGPT backend checks for (#292): every model of the
// plan answered that summary 400 upstream_error while the turns around it,
// tools declared, went through (Mugenrikka on X). With no tools those two
// ask nothing, and a Chat API (OpenAI's own) refuses them, and an empty
// tools list, outright; so none of the three goes to amr-link.

// openDesign is whether p is Open Design Cloud: its plugin, or a provider
// at amr-link's address.
func openDesign(p provider.Provider) bool {
	if p.PluginProvider() == "opendesign" {
		return true
	}
	for _, proto := range []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic} {
		if provider.HostOf(p.Base(proto)) == "amr-link.open-design.ai" {
			return true
		}
	}
	return false
}

// withoutEmptyTools is a Responses or Chat body without an empty tools
// list and the tool_choice and parallel_tool_calls beside it; a body
// that declares a tool goes as it came.
func withoutEmptyTools(body []byte) []byte {
	if !bodyHas(body, `"tools"`) && !bodyHas(body, `"tool_choice"`) && !bodyHas(body, `"parallel_tool_calls"`) {
		return body
	}
	var q struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if decodeJSON(body, &q) != nil || len(q.Tools) > 0 {
		return body
	}
	return withoutFields(body, "tools", "tool_choice", "parallel_tool_calls")
}

// vendorUnsaidWords is amr-link's word for a refusal it passes on from the
// vendor behind it, said or not why: a summary ask so refused is asked
// again with the roles as Codex sent them, then as text, then made
// locally (codexCompact), as for a relay's "Upstream request failed".
var vendorUnsaidWords = lazyre.New(`(?i)\[code=upstream_error\]`)

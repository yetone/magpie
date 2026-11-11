package usage

import "maps"

//go:generate go run ../gui/genpurposes -out ../gui/assets/purposes.js

// PurposeKind is the shared filtering key and untranslated display name of a kind.
type PurposeKind struct {
	Purpose string `json:"purpose"`
	Name    string `json:"name"`
}

var purposeKinds = func() map[string]PurposeKind {
	out := map[string]PurposeKind{}
	add := func(name string, kinds ...string) {
		for _, kind := range kinds {
			out[kind] = PurposeKind{"kind:" + kinds[0], name}
		}
	}
	add("Approval check", "guardian", "auto_review", "guardian_review")
	add("Review", "review")
	add("Compaction", "compact")
	add("Memory", "memory_consolidation", "memgen", "memory")
	add("Title", "thread_title", "thread_title_reconsideration", "title_generation", "title")
	add("Description", "thread_description")
	add("Prediction", "composer_predictions")
	add("Subagent", "collab_spawn", "thread_spawn", "agent_job")
	add("Luna Reserve", "luna_reserve")
	add("Suggestions", "ambient_suggestions", "ambient_suggestion_safety")
	add("Web search", "web_search")
	add("Image description", "vision")
	add("Routing classifier", "routing")
	add("Connection check", "connection_check") // Claude Desktop's (gateway.KindDesktopCheck)
	add("Test", "provider_test")                // magpie's own (probelog.KindTest)
	add("Detect APIs", "api_detect")            // probelog.KindDetect
	add("Model list", "model_list")             // probelog.KindList
	add("MCP tool", KindMCP)                    // an MCP tool call (SourceMCP)
	add("Cache keep-alive", KindKeepalive)      // a plugin's own ping (plugin_calls.go)
	// Claude Code names neither of these requests, so the gateway tells them
	// by their content: the *_inferred kinds. They share the purpose of the
	// plain kind (a client's own marker, were one sent), and say in their
	// name that magpie read it from the body, not that the client said so.
	pair := func(name, kind string) {
		out[kind] = PurposeKind{"kind:" + kind, name}
		out[kind+InferredSuffix] = PurposeKind{"kind:" + kind, name + " (inferred)"}
	}
	pair("Progress summary", KindAgentSummary) // Claude Code's AgentSummary
	pair("Auto mode check", KindAutoMode)      // Claude Code's auto mode classifier
	return out
}()

// KindAgentSummary and KindAutoMode are the kinds of Claude Code's
// sub-agent progress summary and its auto mode classifier; InferredSuffix
// follows one that was told by the request's content.
const (
	KindAgentSummary = "agent_summary"
	KindAutoMode     = "auto_mode_check"
	InferredSuffix   = "_inferred"
)

// PurposeKinds returns a copy of the catalog used to generate the UI's catalog.
// Add aliases here, then run go generate ./internal/usage from the repo root.
func PurposeKinds() map[string]PurposeKind { return maps.Clone(purposeKinds) }

// PurposeOf groups the agent's equivalent call-kind spellings for filtering.
// The recorded Kind stays untouched. Prefixing nonempty kinds keeps future
// names distinct from the unmarked bucket (which is not necessarily a chat).
func PurposeOf(kind string) string {
	if kind == "" {
		return "unmarked"
	}
	if p, ok := purposeKinds[kind]; ok {
		return p.Purpose
	}
	return "kind:" + kind
}

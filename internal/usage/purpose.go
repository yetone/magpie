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
	add("Subagent", "collab_spawn", "thread_spawn", "agent_job")
	add("Luna Reserve", "luna_reserve")
	add("Suggestions", "ambient_suggestions", "ambient_suggestion_safety")
	add("Web search", "web_search")
	add("Image description", "vision")
	return out
}()

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

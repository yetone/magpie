package usage

// PurposeOf groups the agent's equivalent call-kind spellings for filtering.
// The recorded Kind stays untouched. Prefixing nonempty kinds keeps future
// names distinct from the unmarked bucket (which is not necessarily a chat).
// Keep the aliases aligned with KIND / purposeOf in the Routing page.
func PurposeOf(kind string) string {
	switch kind {
	case "":
		return "unmarked"
	case "auto_review", "guardian_review":
		kind = "guardian"
	case "memgen", "memory":
		kind = "memory_consolidation"
	case "thread_title_reconsideration", "title_generation", "title":
		kind = "thread_title"
	case "thread_spawn", "agent_job":
		kind = "collab_spawn"
	case "ambient_suggestion_safety":
		kind = "ambient_suggestions"
	}
	return "kind:" + kind
}

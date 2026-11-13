package agent

import (
	"fmt"
	"slices"

	"github.com/yetone/magpie/internal/edit"
)

// omp's advisor (src/advisor in @oh-my-pi/pi-coding-agent): a second model,
// on the advisor role, that reviews the main agent's turns and sends it
// notes. Its model is a role (omp.go); whether it runs, when it reviews,
// how long the main agent waits for it and which note calls a finished
// agent back are these settings of config.yml (advisor/settings.ts, omp
// 18.20.0). A WATCHDOG.yml roster with advisors in it sets each one's own
// review and wait instead; omp reads the global ones only without one
// (#6408).
//
// omp's enum settings take strings only (config/registry.ts accepts), so
// syncBacklog's "1" is written as the string, never the number.
var (
	ompAdvisorReviews = []string{"off", "turn", "agent-end"}
	ompAdvisorWaits   = []string{"off", "1", "3", "5", "strict"}
	ompAdvisorWakes   = []string{"nit", "concern", "blocker"}
)

// ompAdvisorOn reads advisor.enabled: "" while unset (omp's default, off)
func ompAdvisorOn(path string) string {
	v, _ := edit.GetYAML(path, "advisor.enabled")
	return v
}

// ompAdvisorTurnedOn is the stash's mark that magpie turned the advisor on
// at the config.yml at path, picking its model: the role's reset turns it
// off again, and the user setting it themselves takes the mark away
func ompAdvisorTurnedOn(path string) string { return "omp:" + path + ":advisor.enabled" }

// ompAdvisorFields are the advisor's settings in the config.yml at path.
// Each is Quiet: a row lists none of them until set.
func ompAdvisorFields(path string) []Field {
	setting := func(key, label, setting string, values []string) Field {
		return Field{
			Key: key, Label: label, Quiet: true,
			Get: func() string { v, _ := edit.GetYAML(path, setting); return v },
			Set: func(v string) error {
				if v == "" {
					return edit.DelYAML(path, setting)
				}
				if !slices.Contains(values, v) {
					return fmt.Errorf("%q is not one of omp's values for %s: %v", v, setting, values)
				}
				return edit.SetYAML(path, edit.KV{Path: setting, Value: v})
			},
			Options: func(map[string]string) []Option { return static(values...) },
		}
	}
	return []Field{
		{
			// whether it runs and when it reviews, in one: off, each turn
			// (each tool-call round) or only when the agent ends its run.
			// Turned off, the review mode it had stays for the next time
			Key: "advisor_review", Label: "advisor review", Quiet: true,
			Get: func() string {
				switch ompAdvisorOn(path) {
				case "":
					return ""
				case "true":
					if mode, _ := edit.GetYAML(path, "advisor.reviewMode"); mode == "agent-end" {
						return mode
					}
					return "turn"
				}
				return "off"
			},
			Set: func(v string) error {
				forget(ompAdvisorTurnedOn(path))
				switch v {
				case "":
					return edit.DelYAML(path, "advisor.enabled")
				case "off":
					return edit.SetYAML(path, edit.KV{Path: "advisor.enabled", Value: false})
				case "turn", "agent-end":
					return edit.SetYAML(path, edit.KV{Path: "advisor.enabled", Value: true}, edit.KV{Path: "advisor.reviewMode", Value: v})
				}
				return fmt.Errorf("%q is not one of omp's advisor reviews: %v", v, ompAdvisorReviews)
			},
			Options: func(map[string]string) []Option { return static(ompAdvisorReviews...) },
		},
		// how far the main agent waits for a review it is owed before going
		// on: not at all, up to 30 seconds while 1, 3 or 5 are behind, or
		// until every review is in (strict, no time limit)
		setting("advisor_wait", "advisor wait", "advisor.syncBacklog", ompAdvisorWaits),
		// the least note that calls back an agent that has finished; one
		// below it waits for the user's next message
		setting("advisor_wake", "advisor wakes on", "advisor.continueSeverity", ompAdvisorWakes),
	}
}

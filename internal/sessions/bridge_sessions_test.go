package sessions

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/bridgesid"
)

// A session the Claude subscription bridge's Claude Code saved, in the
// user's own Claude folder or a saved account's, is not one of the user's:
// the gateway counts its calls already, under the agent that asked. Read
// as a local session too, each call was listed and counted twice (iwinux
// on X: a Pi request through a Claude subscription group, again as
// "Claude Code · Local session", the same tokens and cost). Its
// sub-agents' files go with it; the user's sessions, beside it in the
// same project, are counted as before.
func TestBridgeSessionsAreNotLocal(t *testing.T) {
	d := setupCalls(t)
	config := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", config)
	own, other := bridgesid.New(), bridgesid.New()
	saved := filepath.Join(config, "magpie", "claude-accounts", "a1")
	user := func(id string) string {
		return `{"type":"user","timestamp":"` + stamp(0) + `","message":{"role":"user","content":"hello"},"cwd":"/work/app","sessionId":"` + id + `"}`
	}
	// the user's own session, and a bridge run's in the same project
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", "main1.jsonl"),
		user("main1"), swap(claudeMsg("m1", "claude-opus-5-5", 1, 2, 0, 0, 1), `"sess1"`, `"main1"`))
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", own+".jsonl"),
		user(own), swap(claudeMsg("m2", "claude-opus-5-5", 2, 490, 0, 0, 2), `"sess1"`, `"`+own+`"`))
	writeLines(t, filepath.Join(d.claude, "projects", "-work-app", own, "subagents", "agent-a.jsonl"),
		swap(claudeMsg("m3", "claude-opus-5-5", 3, 4, 0, 0, 3), `"sess1"`, `"`+own+`"`))
	// a saved account's run
	writeLines(t, filepath.Join(saved, "projects", "-work-app", other+".jsonl"),
		user(other), swap(claudeMsg("m4", "claude-opus-5-5", 5, 6, 0, 0, 4), `"sess1"`, `"`+other+`"`))

	got := map[string]int{}
	for _, c := range Calls(time.Time{}) {
		got[c.RequestID]++
	}
	if len(got) != 1 || got["req_m1"] != 1 {
		t.Fatalf("calls %v, want the user's req_m1 alone", got)
	}
	rows := map[string]int{}
	for _, s := range List(0) {
		rows[s.ID]++
	}
	if len(rows) != 1 || rows["main1"] != 1 {
		t.Fatalf("sessions %v, want the user's main1 alone", rows)
	}
}

package sessions

import "testing"

// Deleting a thread in Codex leaves its subagent rollouts behind: a review
// thread whose parent thread is gone was a row of its own, titled by the
// prompt Codex asked it (flandern). One whose parent is here is listed, to
// sit under it (keptSubagents).
func TestCodexSubagentWhoseParentIsGoneIsNoRow(t *testing.T) {
	d := setupCalls(t)
	spawn := func(parent string) string {
		return `{"subagent":{"thread_spawn":{"parent_thread_id":"` + parent + `"}}}`
	}
	codexIdentityRollout(t, d.codex, "main",
		codexIdentityMeta(t, "main", "main", `"vscode"`, "", ""),
		codexIdentityUsage(1, "response-main", "main", ""))
	codexIdentityRollout(t, d.codex, "child",
		codexIdentityMeta(t, "child", "main", spawn("main"), "", ""),
		codexIdentityUsage(2, "response-child", "main", ""))
	codexIdentityRollout(t, d.codex, "orphan",
		codexIdentityMeta(t, "orphan", "gone", spawn("gone"), "", ""),
		codexIdentityUsage(3, "response-orphan", "gone", ""))

	got := map[string]Managed{}
	for _, m := range ListAgent("codex") {
		got[m.ID] = m
	}
	if _, ok := got["orphan"]; ok {
		t.Error("the subagent thread of a deleted parent is listed as a session of its own")
	}
	main, ok := got["main"]
	if !ok || main.Role != codexRoleMain {
		t.Fatalf("the parent thread is not listed: %+v", main)
	}
	child, ok := got["child"]
	if !ok || child.Role != codexRoleSubagent || child.ParentSession != "main" {
		t.Fatalf("the subagent thread under a listed parent is not listed: %+v", child)
	}
}

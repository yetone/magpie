package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #1633 (laacmilan): Codex's lead on token/gpt-6.1-sol, a provider on Chat
// Completions, its subagents wanted on token too. The lead's spawn_agent
// task reaches the subagent as text (magpie served the lead), so a
// subagent the lead put on one of Codex's own models goes on the token
// model set for Codex's subagents, and ChatGPT isn't asked. The lead's
// own turn stays on the model it asked for.
func TestCodexSubagentPutOnAChatProviderUnderAChatLead(t *testing.T) {
	up := codexSubagentSetup(t)
	token := chatRecordedOn(t, "token", "gpt-6.1-sol")
	if err := provider.SetCodexSubagentModel("token/gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := codexAsks(s, "gpt-6-astra", "collab_spawn", plainTask)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 0 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if got := token.requests(); len(got) != 1 || !strings.Contains(got[0], `"gpt-6.1-sol"`) || !strings.Contains(got[0], "read the README") {
		t.Fatalf("token was asked %v", got)
	}
	r := lastRoute(s)
	if r.Subagent == nil || *r.Subagent != (SubagentPick{Asked: "gpt-6-astra", To: "token/gpt-6.1-sol"}) || r.Provider != "token" {
		t.Fatalf("trace: provider %q subagent %+v", r.Provider, r.Subagent)
	}
	// the lead on its own model, magpie's token one
	if rec := codexAsks(s, "token/gpt-6.1-sol", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.Subagent != nil {
		t.Fatalf("lead traced %+v", r.Subagent)
	}
}

// #1613's other side: the lead on one of Codex's own models, answered by
// the ChatGPT backend, which sealed the subagent's task. token can't open
// it, so the subagent stays on the model its lead asked for — answered by
// ChatGPT as with no model set, not turned away — and the trace says why.
// A subagent's request holding only a sealed message, not a task, still
// goes on token: that message is left out with a note (#1613).
func TestCodexSubagentSealedTaskStaysOffAChatProvider(t *testing.T) {
	up := codexSubagentSetup(t)
	token := chatRecordedOn(t, "token", "gpt-6.1-sol")
	if err := provider.SetCodexSubagentModel("token/gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := codexAsks(s, "gpt-5.5", "collab_spawn", sealedTaskFernet)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "on it") {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("ChatGPT was asked for %v, not the model asked", got)
	}
	if n := len(token.requests()); n != 0 {
		t.Fatalf("token was asked %d times", n)
	}
	if r := lastRoute(s); r.Subagent == nil || r.Subagent.To != "token/gpt-6.1-sol" || r.Subagent.Kept != sealedKept {
		t.Fatalf("trace: %+v", r.Subagent)
	}
	rec = codexAsks(s, "gpt-5.5", "collab_spawn", plainTask+","+sealedWorkerMessage)
	if rec.Code != 200 {
		t.Fatalf("subagent with a sealed message: %d %s", rec.Code, rec.Body.String())
	}
	if got := token.requests(); len(got) != 1 || strings.Contains(got[0], "gAAAAATest_worker_message") {
		t.Fatalf("token was asked %v", got)
	}
	if r := lastRoute(s); !r.Subagent.Moved() {
		t.Fatalf("trace: %+v", r.Subagent)
	}
}

// cleverpigo on X: Codex's lead on WorkBuddy's DeepSeek through magpie, the
// subagent model left at what read "Same as the lead", and the Routing
// page showed a subagent on gpt-5.6-sol through Codex's own sign-in: the
// lead named it in spawn_agent, where Codex lists its own models first.
// Set to the lead's model, every subagent goes on the model the lead's own
// turns go on, whatever the lead names, and ChatGPT isn't asked.
func TestCodexSubagentFollowsTheLeadWhenSet(t *testing.T) {
	up := codexSubagentSetup(t)
	token := chatRecordedOn(t, "token", "deepseek-v4.1-flash")
	if err := provider.SetCodexSubagentModel(provider.CodexSubagentLead); err != nil {
		t.Fatal(err)
	}
	s := New()
	if rec := codexSubTurn(s, "token/deepseek-v4.1-flash", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	// a subagent the lead named one of Codex's own models for, and one it
	// named another of magpie's for, each its own thread (a resumed one
	// stays on the model it started on)
	for i, asked := range []string{"gpt-5.6-sol", "third/gpt-6-astra"} {
		rec := codexAsksIn(s, fmt.Sprintf("worker-%d", i+1), asked, "collab_spawn", plainTask)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
			t.Fatalf("%s: %d %s", asked, rec.Code, rec.Body.String())
		}
		if got := up.asked(); len(got) != 0 {
			t.Fatalf("%s: ChatGPT was asked for %v", asked, got)
		}
		r := lastRoute(s)
		if r.Subagent == nil || *r.Subagent != (SubagentPick{Asked: asked, To: "token/deepseek-v4.1-flash", Lead: true}) || r.Provider != "token" {
			t.Fatalf("%s: trace: provider %q subagent %+v", asked, r.Provider, r.Subagent)
		}
	}
	if got := token.requests(); len(got) != 3 || !strings.Contains(got[2], "read the README") {
		t.Fatalf("token was asked %d times", len(got))
	}
	// the lead's own turn stays as asked
	if rec := codexSubTurn(s, "token/deepseek-v4.1-flash", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d", rec.Code)
	}
	if r := lastRoute(s); r.Subagent != nil {
		t.Fatalf("lead traced %+v", r.Subagent)
	}
}

// Set to the lead's model with the lead on one of Codex's own: a subagent
// the lead named one of magpie's models for goes to the ChatGPT sign-in on
// the lead's model. With no lead turn seen, a subagent goes as asked.
func TestCodexSubagentFollowsALeadOnCodexsOwnModel(t *testing.T) {
	up := codexSubagentSetup(t)
	token := chatRecordedOn(t, "token", "deepseek-v4.1-flash")
	if err := provider.SetCodexSubagentModel(provider.CodexSubagentLead); err != nil {
		t.Fatal(err)
	}
	s := New()
	if rec := codexAsks(s, "token/deepseek-v4.1-flash", "collab_spawn", plainTask); rec.Code != 200 {
		t.Fatalf("no lead: %d %s", rec.Code, rec.Body.String())
	}
	if r := lastRoute(s); r.Subagent != nil || len(token.requests()) != 1 {
		t.Fatalf("no lead: traced %+v, token asked %d times", r.Subagent, len(token.requests()))
	}
	if rec := codexSubTurn(s, "gpt-5.5", "lead-1", "", leadAsk); rec.Code != 200 {
		t.Fatalf("lead: %d %s", rec.Code, rec.Body.String())
	}
	if rec := codexAsksIn(s, "worker-2", "token/deepseek-v4.1-flash", "collab_spawn", plainTask); rec.Code != 200 {
		t.Fatalf("subagent: %d %s", rec.Code, rec.Body.String())
	}
	if got := up.asked(); len(got) != 2 || got[1] != "gpt-5.5" {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	if n := len(token.requests()); n != 1 {
		t.Fatalf("token was asked %d times", n)
	}
	if r := lastRoute(s); r.Subagent == nil || *r.Subagent != (SubagentPick{Asked: "token/deepseek-v4.1-flash", To: "gpt-5.5", Lead: true}) {
		t.Fatalf("trace: %+v", r.Subagent)
	}
}

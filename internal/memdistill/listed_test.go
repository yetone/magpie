package memdistill

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// A conversation the Sessions page lists only from magpie's gateway (an
// agent such as WorkBuddy, whose own files magpie doesn't read) is read
// from what the gateway recorded of it, as the agents' own files are
// (bxgz233 on X: 1000+ WorkBuddy sessions listed, none read). One the
// gateway counted but recorded nothing of is passed over.
func TestGatewayOnlySessionsRead(t *testing.T) {
	sb := newSandbox(t)
	list, read = listed, transcriptOf
	sb.at = time.Now()
	on(t, "")
	if err := sessions.SetGatewayRecording(true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessions.SetGatewayRecording(false, true) })
	at := sb.at.Add(-2 * time.Hour)
	first := []sessions.Part{{Role: "user", Kind: "text", Text: "wb: we release with make ship"}}
	answer := []sessions.Part{{Role: "assistant", Kind: "text", Text: "Done."}}
	second := append(slices.Concat(first, answer), sessions.Part{Role: "user", Kind: "text", Text: "never use npm here, pnpm only"})
	for i, in := range [][]sessions.Part{first, second} {
		turn := sessions.GatewayTurn{Agent: "workbuddy", Session: "wb-1", Time: at.Add(time.Duration(i) * time.Minute), Status: 200,
			Input: append([]sessions.Part{{Role: "system", Kind: "text", Text: "You are WorkBuddy."}}, in...), Output: answer}
		if err := sessions.SaveGatewayTurn(turn); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"wb-1", "wb-1", "unrecorded"} {
		usage.Append(usage.Record{Time: at.Add(time.Duration(i) * time.Minute), Agent: "workbuddy", Session: id, Provider: "p", Model: "m", Input: 10, Output: 2, Status: 200})
	}
	ss, _ := due(State{Since: sb.at.Add(-Back)}, sb.at)
	if len(ss) != 2 || !ss[0].Gateway || !ss[1].Gateway {
		t.Fatalf("due: %+v", ss)
	}
	res, err := Run(context.Background(), "auto/m", sb.complete)
	if err != nil || res.Read != 1 || len(sb.asked) != 1 || sb.asked[0] != "auto/m User: wb: we release with make ship" {
		t.Fatalf("%+v %v %q", res, err, sb.asked)
	}
	st, _ := Load()
	if _, ok := st.Done["workbuddy/wb-1"]; !ok {
		t.Fatalf("not marked read: %+v", st.Done)
	}
	if _, ok := st.Done["workbuddy/unrecorded"]; !ok {
		t.Fatalf("an unrecorded one is passed over and marked: %+v", st.Done)
	}
}

// A conversation magpie lists but can't resume or delete (the Antigravity
// app's, Cursor's editor's, Hermes') is the user's conversation all the
// same, and read.
func TestReadOnlySessionsRead(t *testing.T) {
	sb := newSandbox(t)
	on(t, "")
	sb.session("ro", time.Hour, "ro: one", "two").ReadOnly = true
	res, err := Run(context.Background(), "auto/m", sb.complete)
	if err != nil || res.Read != 1 || len(sb.asked) != 1 || sb.asked[0] != "auto/m User: ro: one" {
		t.Fatalf("%+v %v %q", res, err, sb.asked)
	}
}

package sessions

import (
	"testing"
	"time"
)

// zcodeCalls are the ZCode calls Calls gives, by message id.
func zcodeCalls(t *testing.T) map[string]Call {
	t.Helper()
	out := map[string]Call{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "zcode" {
			if _, dup := out[c.Msg]; dup {
				t.Fatalf("message %s counted twice", c.Msg)
			}
			out[c.Msg] = c
		}
	}
	return out
}

// ZCode's calls reach the usage ledger too: the Sessions page read its
// database all along, and the Usage page showed none of it.
func TestZCodeCalls(t *testing.T) {
	setupAgents(t)
	cs := zcodeCalls(t)
	// ses_zcA's own reply went through magpie's gateway (the route magpie
	// writes names it "magpie"), so it is the gateway's record to keep; the
	// others are read here: ses_zcB's (a subagent, counted under the session
	// it ran in) and ses_zcC's
	if len(cs) != 2 {
		t.Fatalf("want 2 ZCode calls, got %d: %+v", len(cs), cs)
	}
	if _, dup := cs["msg_z3"]; dup {
		t.Fatalf("a call made to magpie's gateway was read from the database too: %+v", cs["msg_z3"])
	}
	if b := cs["msg_z4"]; b.Model != "glm-5.1-air" || b.Tokens != (Tokens{50, 5, 0, 0, 0}) {
		t.Fatalf("the subagent's reply: %+v", b)
	}
	if c := cs["msg_z5"]; c.Tokens != (Tokens{10, 1, 0, 0, 0}) {
		t.Fatalf("ses_zcC's reply: %+v", c)
	}
}

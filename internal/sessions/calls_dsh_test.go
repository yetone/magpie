package sessions

import (
	"testing"
	"time"
)

// dshCalls are the DeepSeek Harness calls Calls gives.
func dshCalls(t *testing.T) []Call {
	t.Helper()
	var out []Call
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "dsh" {
			out = append(out, c)
		}
	}
	return out
}

// The Sessions page has read dsh's session files all along; the usage ledger
// had not, so dsh's tokens never reached the Usage page — which showed only
// what magpie's own gateway served. Every assistant/message is one call, with
// the model and the usage of the request that answered with it.
func TestDshCalls(t *testing.T) {
	setupAgents(t)
	cs := dshCalls(t)
	by := map[string][]Call{}
	for _, c := range cs {
		by[c.Session] = append(by[c.Session], c)
	}
	// dsh-a's subagent (dsh-b) counts in the session it ran under, as
	// OpenCode's does; dsh-c and dsh-d are forks, whose seeded events are
	// the session they came from and are not spent again here
	if len(cs) != 6 {
		t.Fatalf("want 6 dsh calls, got %d: %+v", len(cs), cs)
	}
	// a call dsh sent through magpie's own gateway is the gateway's record
	// to keep, as OpenCode's and ZCode's are: dsh sends the gateway no
	// session header, so the two records cannot be paired afterwards (the
	// token counts differ call to call) and the call would be counted
	// twice — once by the gateway with the provider and account that
	// really answered, once here as a nameless local session. dsh-gw holds
	// one such call and one of its own.
	// dsh-gw's own call is kept; only its gateway one is left out
	if gw := by["dsh-gw"]; len(gw) != 1 || gw[0].Upstream != "jieyue" || gw[0].Tokens != (Tokens{30, 7, 0, 0, 0}) {
		t.Fatalf("dsh-gw's own call: %+v", gw)
	}
	for _, c := range cs {
		if c.Upstream == "magpie" || c.Upstream == "dial" {
			t.Fatalf("a call through magpie's gateway was counted here: %+v", c)
		}
		if c.Tokens == (Tokens{700, 90, 5000, 0, 0}) {
			t.Fatalf("the gateway's own call was counted here: %+v", c)
		}
	}
	if len(by["dsh-a"]) != 3 || len(by["dsh-c"]) != 1 || len(by["dsh-d"]) != 1 {
		t.Fatalf("by session: %d a, %d c, %d d (want 3, 1, 1)", len(by["dsh-a"]), len(by["dsh-c"]), len(by["dsh-d"]))
	}
	// the 999s are the older session.jsonl beside dsh-a's newest, not read
	for _, c := range cs {
		if c.Input == 999 {
			t.Fatalf("the older file beside the newest was read: %+v", c)
		}
	}
	// the seeded copy of dsh-a's first reply, which dsh-c and dsh-d each
	// carry: spent once, in dsh-a
	seeded := 0
	for _, c := range cs {
		if c.Tokens == (Tokens{100, 20, 1000, 0, 0}) {
			seeded++
		}
	}
	if seeded != 1 {
		t.Fatalf("a seeded reply counted %d times, want once", seeded)
	}

	a := by["dsh-a"]
	// newest first. dsh names the model it was told to use, and its reply's
	// replay state names the one the vendor answered with when the two
	// differ: Call.Model is what answered (as Claude Code's file has it),
	// Call.Requested what was asked for, and Call.Upstream the provider the
	// file recorded — all three are what the Requests table's columns read.
	if a[0].Model != "deepseek-v4-flash-sg" || a[0].Requested != "deepseek-v4-flash" ||
		a[0].Upstream != "workbuddy-ai" || a[0].Tokens != (Tokens{10, 5, 200, 0, 0}) {
		t.Fatalf("dsh-a's newest call (asked deepseek-v4-flash, answered deepseek-v4-flash-sg): %+v", a[0])
	}
	if a[1].Tokens != (Tokens{7, 3, 0, 0, 0}) {
		t.Fatalf("the subagent's call: %+v", a[1])
	}
	// answered with the very model asked for: not a swap, and the two agree
	if a[2].Model != "deepseek-v4-pro" || a[2].Requested != "deepseek-v4-pro" ||
		a[2].Upstream != "deepseek" || a[2].Tokens != (Tokens{100, 20, 1000, 0, 0}) ||
		!a[2].Time.Equal(ms(1790503205000)) {
		t.Fatalf("dsh-a's first call: %+v", a[2])
	}
	if c := by["dsh-c"][0]; c.Tokens != (Tokens{50, 10, 0, 0, 0}) {
		t.Fatalf("dsh-c's own call: %+v", c)
	}
	if d := by["dsh-d"][0]; d.Tokens != (Tokens{1, 2, 0, 0, 0}) {
		t.Fatalf("dsh-d's own call: %+v", d)
	}
}

// A session that has not changed is not read again: what was read is kept,
// and a restart keeps it too.
func TestDshCallsKept(t *testing.T) {
	setupAgents(t)
	first := dshCalls(t)
	if len(first) == 0 {
		t.Fatal("no dsh calls read")
	}
	resetCalls()
	again := dshCalls(t)
	if len(again) != len(first) {
		t.Fatalf("after a restart: %d calls, want %d", len(again), len(first))
	}
}

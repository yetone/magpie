package sessions

import (
	"path/filepath"
	"testing"
	"time"
)

// Qoder's and Qoder CN's own calls reach the usage ledger (eating_dream on
// X: Qoder's sessions were on the Sessions page and nowhere in Usage). The
// files in testdata/qoder-1.1.89 are lines as qodercli 1.1.89 writes them
// (its transcript writer, Xj and vUl, read from the npm bundle; the ids are
// made up): its own model is "auto", and a reply's usage has every count 0
// with what it cost in Qoder's credits, as QODER_EXPOSE_TOKEN_USAGE unset
// leaves it. A reply whose usage named no credits is written with none at
// all, and is no call here. The fixture in testdata/qoder, with counts, is
// what a model of the user's own writes.
func TestQoderCalls(t *testing.T) {
	home := setupAgents(t)
	copyTree(t, "testdata/qoder-1.1.89/qoder", filepath.Join(home, ".qoder"))
	copyTree(t, "testdata/qoder-1.1.89/qoder-cn", filepath.Join(home, ".qoder-cn"))
	const qoderAuto, qoderCNAuto = "5f0c9a3e-2b7d-4c1a-9e8f-3a6b1d2c4e51", "7a2e4c6b-8d1f-4a3e-b5c7-9e0d2f4a6b83"
	got := map[string][]Call{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "qoder" || c.Agent == "qoder-cn" {
			got[c.Agent+" "+c.Session] = append(got[c.Agent+" "+c.Session], c)
		}
	}
	at := func(s string) time.Time {
		v, _ := time.Parse(time.RFC3339Nano, s)
		return v
	}
	for _, w := range []struct {
		key     string
		msgs    []string
		times   []string
		credits []float64
	}{
		// newest first
		{"qoder " + qoderAuto, []string{"msg_q2", "msg_q1"}, []string{"2026-11-15T08:12:09.330Z", "2026-11-15T08:12:05.871Z"}, []float64{0.18, 0.42}},
		{"qoder-cn " + qoderCNAuto, []string{"msg_c1"}, []string{"2026-11-15T09:00:03.250Z"}, []float64{1.5}},
	} {
		cs := got[w.key]
		if len(cs) != len(w.msgs) {
			t.Fatalf("%s: want %d calls, got %d: %+v", w.key, len(w.msgs), len(cs), cs)
		}
		for i, c := range cs {
			if c.Msg != w.msgs[i] || !c.Time.Equal(at(w.times[i])) || c.Model != "auto" || c.Tokens != (Tokens{}) || c.Credits == nil || *c.Credits != w.credits[i] {
				t.Fatalf("%s call %d: %+v (credits %v)", w.key, i, c, c.Credits)
			}
		}
	}
	// a model of the user's own, its counts written: Qoder's subagent's
	// reply is a call too
	var tokens Tokens
	for _, c := range got["qoder "+qoderID] {
		tokens.Input += c.Input
		tokens.Output += c.Output
		tokens.CacheRead += c.CacheRead
		if c.Model != "qwen3-coder-plus" || c.Credits != nil {
			t.Fatalf("qoder %s: %+v", qoderID, c)
		}
	}
	if tokens.Input == 0 || tokens.Output == 0 {
		t.Fatalf("qoder %s: no tokens read from its counts: %+v", qoderID, got["qoder "+qoderID])
	}
}

// A count that isn't a number loses only itself, not the reply's line.
func TestQoderCreditsOfAnotherType(t *testing.T) {
	var l ccCall
	if err := ccDecode([]byte(`{"type":"assistant","timestamp":"2026-11-15T08:12:05.871Z","message":{"id":"m","model":"auto","content":[],"usage":{"input_tokens":3,"credits":"0.4"}}}`), &l); err != nil {
		t.Fatal(err)
	}
	if l.Message.Usage == nil || l.Message.Usage.Input != 3 || l.Message.Usage.Credits.Set {
		t.Fatalf("%+v", l.Message.Usage)
	}
}

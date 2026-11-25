package sessions

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
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

// magpie writes a route per protocol into dsh (agent.dshKinds): a call on
// any of them went through the gateway and is the gateway's to count, while
// a route of the user's named like one is not taken for it.
func TestDshGatewayProviderTakesEveryRouteOfMagpies(t *testing.T) {
	for p, want := range map[string]bool{
		"magpie": true, "magpie-responses": true, "magpie-anthropic": true, "magpie-gemini": true, "dial": true,
		"magpie-mine": false, "jieyue": false, "": false,
	} {
		if got := dshGatewayProvider(dshSource{Provider: p}); got != want {
			t.Errorf("%q: %v, want %v", p, got, want)
		}
	}
}

// dshCallReader reads the calls of the dsh session file at path as the
// ledger does, and says where each scan of it started.
func dshCallReader(t *testing.T, path string) func() ([]int, []int64) {
	var mu sync.Mutex
	var starts []int64
	scanHook = func(p string, off int64) {
		if p == path {
			mu.Lock()
			starts = append(starts, off)
			mu.Unlock()
		}
	}
	t.Cleanup(func() { scanHook = nil })
	return func() ([]int, []int64) {
		t.Helper()
		mu.Lock()
		starts = nil
		mu.Unlock()
		var in []int
		found := false
		for _, s := range CallSources() {
			if s.Path == path {
				found = true
				for _, c := range ReadCallSource(s) {
					in = append(in, c.Input)
				}
			}
		}
		if !found {
			t.Fatalf("%s is no call source", path)
		}
		slices.Sort(in)
		mu.Lock()
		defer mu.Unlock()
		return in, append([]int64(nil), starts...)
	}
}

// A dsh session read for calls before is read on from where it was left,
// packed or not: a frame appended is read alone, a frame or line not yet
// written whole waits, the read goes on from the disk shard after the
// in-memory one is let go, and a file cut short is read whole again.
func TestDshCallsReadOnlyWhatWasAppended(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	plain := filepath.Join(home, ".dsh", "sessions", "work-dsh-3f2a", "dsh-a", "session.v3.jsonl")
	b, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(plain)
	enc, _ := zstd.NewWriter(nil)
	defer enc.Close()
	frame := func(s string) string { return string(enc.EncodeAll([]byte(s), nil)) }
	p := plain + ".zstd"
	half := bytes.IndexByte(b[len(b)/2:], '\n') + len(b)/2 + 1
	orig := frame(string(b[:half])) + frame(string(b[half:]))
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	reply := func(n int) string {
		return fmt.Sprintf(`{"type":"assistant/message","seq":%d,"time":%d,"data":{"message":{"source":{"model":"deepseek-v4-flash"}},"usage":{"inputTokens":%d,"outputTokens":1,"cacheReadTokens":0}}}`+"\n", n, 1790503290000+int64(n)*1000, n)
	}
	size := func() int64 {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	read := dshCallReader(t, p)
	check := func(what string, from int64, want ...int) {
		t.Helper()
		in, starts := read()
		if len(starts) == 0 {
			t.Fatalf("%s: not read", what)
		}
		for _, at := range starts {
			if at != from {
				t.Fatalf("%s: read from %d, want %d (%v)", what, at, from, starts)
			}
		}
		if !slices.Equal(in, want) {
			t.Fatalf("%s: calls %v, want %v", what, in, want)
		}
	}
	check("first read", 0, 10, 100)

	was := size()
	appendText(t, p, frame(reply(6)))
	check("a frame appended", was, 6, 10, 100)

	was = size()
	f := frame(reply(7))
	appendText(t, p, f[:len(f)/2])
	check("half a frame", was, 6, 10, 100)
	appendText(t, p, f[len(f)/2:])
	check("the half frame finished", was, 6, 7, 10, 100)

	// a frame ending inside a line: the line is read once whole, from
	// the frame it starts in
	was = size()
	r9 := reply(9)
	appendText(t, p, frame(reply(8)+r9[:20]))
	check("a frame ending in a line", was, 6, 7, 8, 10, 100)
	appendText(t, p, frame(r9[20:]))
	check("the line finished in the next frame", was, 6, 7, 8, 9, 10, 100)

	resetCalls()
	was = size()
	appendText(t, p, frame(reply(11)))
	check("from the disk shard", was, 6, 7, 8, 9, 10, 11, 100)

	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	check("cut short", 0, 10, 100)
}

// A fork's end-seed that comes in a later read still ends what it was
// seeded with: what was counted before it is its source's, in memory and
// in the disk shard.
func TestDshCallsEndSeedAcrossReads(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	p := filepath.Join(home, ".dsh", "sessions", "work-dsh-3f2a", "dsh-c", "session.v3.jsonl")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.Index(b, []byte(`{"type":"session/end-seed"`))
	rest := bytes.IndexByte(b[cut:], '\n') + cut + 1
	if err := os.WriteFile(p, b[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	read := dshCallReader(t, p)
	if in, _ := read(); !slices.Equal(in, []int{100}) {
		t.Fatalf("before its end-seed: %v", in)
	}
	appendText(t, p, string(b[cut:rest]))
	if in, starts := read(); len(in) != 0 || !slices.Equal(starts, []int64{int64(cut)}) {
		t.Fatalf("its end-seed read on from %v: %v, want none", starts, in)
	}
	appendText(t, p, string(b[rest:]))
	if in, starts := read(); !slices.Equal(in, []int{50}) || !slices.Equal(starts, []int64{int64(rest)}) {
		t.Fatalf("the fork's own, read on from %v: %v", starts, in)
	}
	resetCalls()
	if in, starts := read(); !slices.Equal(in, []int{50}) || len(starts) != 0 {
		t.Fatalf("from the disk shard (read from %v): %v", starts, in)
	}
}

package sessions

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// #2097 (parkavenue9639): Antigravity CLI's own calls reach the usage
// ledger. testdata/agy/brain holds three conversations: one agy 1.2.16
// wrote in a sandbox against a fake Gemini endpoint (three model steps,
// one with 40 thought tokens in its output, and a turn that failed to
// connect five times), one made of the lines the issue quotes, and one an
// older agy wrote, whose model step has no token fields.
func TestAgyCalls(t *testing.T) {
	claude, _ := setup(t)
	copyTree(t, "testdata/agy", filepath.Join(filepath.Dir(claude), "home", ".gemini", "antigravity-cli"))

	var got []Call
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "agy" {
			got = append(got, c)
		}
	}
	slices.SortFunc(got, func(a, b Call) int { return a.Time.Compare(b.Time) })
	want := []struct {
		at      string
		session string
		tokens  Tokens
	}{
		{"2026-10-14T03:03:17Z", "2b0e6c1a-1d3e-4f6a-9a51-0f1c2d3e4f5a", Tokens{Input: 14559, Output: 2116}},
		{"2026-10-14T03:04:02Z", "2b0e6c1a-1d3e-4f6a-9a51-0f1c2d3e4f5a", Tokens{Input: 3013, CacheRead: 14973, Output: 1518}},
		{"2026-10-14T06:12:41Z", "8ac63d53-9162-4a29-af29-bda3428931ae", Tokens{Input: 702, CacheRead: 300, Output: 9}},
		{"2026-10-14T06:15:01Z", "8ac63d53-9162-4a29-af29-bda3428931ae", Tokens{Input: 701, CacheRead: 300, Output: 8}},
		{"2026-10-14T06:16:06Z", "8ac63d53-9162-4a29-af29-bda3428931ae", Tokens{Input: 701, CacheRead: 300, Output: 48}},
	}
	if len(got) != len(want) {
		t.Fatalf("want %d agy calls (none from the transcript without token fields), got %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		c := got[i]
		at, _ := time.Parse(time.RFC3339, w.at)
		if !c.Time.Equal(at) || c.Session != w.session || c.Tokens != w.tokens {
			t.Errorf("call %d: %v %s %+v, want %s %s %+v", i, c.Time, c.Session, c.Tokens, w.at, w.session, w.tokens)
		}
		if c.Model != "" {
			t.Errorf("call %d names model %q, with no database of its conversation", i, c.Model)
		}
		if c.Msg == "" {
			t.Errorf("call %d: no step id to count it once by", i)
		}
	}
}

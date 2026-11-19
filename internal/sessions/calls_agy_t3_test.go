package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// zzzkl on X: agy's calls made in T3 Code weren't on Usage → Requests,
// while a plain agy's were. T3 Code runs agy as an ACP server in a profile
// of its own per Antigravity instance, and agy keeps that instance's
// conversations there. testdata/agy-t3 is what agy 1.5.10 wrote under
// antigravity-acp when started as T3 Code starts it (`agy acp
// --gemini-dir <profile> --app-data-dir antigravity-acp`) in a sandbox
// against a fake Gemini endpoint, one turn of one model step.
func TestAgyCallsInT3Code(t *testing.T) {
	sum := sha256.Sum256([]byte("antigravity")) // T3 Code's own instance's id
	profile := hex.EncodeToString(sum[:])
	for _, c := range []struct {
		name string
		base func(home, dir string) string // $T3CODE_HOME; nil leaves it unset
		at   func(home, dir string) string
	}{
		{"default folder", nil, func(home, _ string) string { return filepath.Join(home, ".t3") }},
		{"T3CODE_HOME", func(_, dir string) string { return filepath.Join(dir, "t3home") }, func(_, dir string) string { return filepath.Join(dir, "t3home") }},
		{"T3CODE_HOME under ~", func(_, _ string) string { return "~/t3elsewhere" }, func(home, _ string) string { return filepath.Join(home, "t3elsewhere") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			claude, _ := setup(t)
			dir := filepath.Dir(claude)
			home := filepath.Join(dir, "home")
			if c.base != nil {
				t.Setenv("T3CODE_HOME", c.base(home, dir))
			}
			copyTree(t, "testdata/agy-t3", filepath.Join(c.at(home, dir), "userdata", "providers", "antigravity", profile, "antigravity-acp"))

			var got []Call
			for _, call := range Calls(time.Time{}) {
				if call.Agent == "agy" {
					got = append(got, call)
				}
			}
			if len(got) != 1 {
				t.Fatalf("want the one model step of T3 Code's agy conversation, got %d calls: %+v", len(got), got)
			}
			at, _ := time.Parse(time.RFC3339, "2026-11-19T01:06:24Z")
			want := Tokens{Input: 1034, CacheRead: 200, Output: 56}
			if g := got[0]; !g.Time.Equal(at) || g.Session != "f8dd5efd-4ce8-499e-8359-ac13b3b2dd03" || g.Tokens != want {
				t.Errorf("call %v %s %+v, want %v f8dd5efd-… %+v", g.Time, g.Session, g.Tokens, at, want)
			}
			// the consent lists agy for these files alone
			if !slices.Contains(Found(), "agy") {
				t.Errorf("Found() = %v, want agy for T3 Code's agy conversations", Found())
			}
		})
	}
}

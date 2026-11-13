package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// nzvzbmz78932 on X: a Pi subagent's DeepSeek calls were on no page. The
// session is Pi 1.4.0's with pi-subagents 0.88.1, run in a sandbox against
// a provider of its own (relay, deepseek/deepseek-flash): the lead called
// the subagent tool once, and pi-subagents ran its scout as a Pi of its own
// with its session in the folder named for the lead's file
// (testdata/pi-subagents; the system prompts left out, the sandbox's
// folders written /work/sub and /home/u/.pi/agent). The lead's tool result
// carries the scout's usage summed. The scout's two calls are the lead
// session's, once, on the scout's model and provider, on Usage and on
// Sessions, whatever the lead runs on: magpie's own entry (the reporter's
// case, its calls the gateway's to count) included.
func TestPiSubagentCalls(t *testing.T) {
	const lead = "01a1d480-175f-7297-b482-4707b3e6e44c"
	leadFile := filepath.Join("sessions", "--work-sub--", "2026-11-13T10-42-06-815Z_"+lead+".jsonl")
	type call struct {
		at     string
		tokens Tokens
	}
	scout := []call{
		{"2026-11-13T10:42:13.418Z", Tokens{Input: 2592, Output: 515}},
		{"2026-11-13T10:42:15.393Z", Tokens{Input: 261, Output: 320, CacheRead: 2944}},
	}
	leads := []call{
		{"2026-11-13T10:42:09.509Z", Tokens{Input: 6574, Output: 92}},
		{"2026-11-13T10:42:16.967Z", Tokens{Input: 405, Output: 201, CacheRead: 6528}},
		{"2026-11-13T10:42:18.859Z", Tokens{Input: 215, Output: 184, CacheRead: 7040}},
	}
	for _, own := range []bool{false, true} {
		for _, gateway := range []bool{false, true} {
			name := "beside its lead"
			if own {
				name = "in defaultSessionDir"
			}
			if gateway {
				name += ", the lead on magpie"
			}
			t.Run(name, func(t *testing.T) {
				claude, _ := setup(t)
				resetCalls()
				pi := filepath.Join(filepath.Dir(claude), "pi")
				copyTree(t, "testdata/pi-subagents", pi)
				if gateway {
					// the lead on magpie's entry, the scout on a provider
					// of its own
					p := filepath.Join(pi, leadFile)
					b, err := os.ReadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					s := strings.ReplaceAll(string(b), `"provider":"relay","model":"deepseek/deepseek-flash"`, `"provider":"magpie","model":"gpt-6-astra"`)
					s = strings.ReplaceAll(s, `"provider":"relay","modelId":"deepseek/deepseek-flash"`, `"provider":"magpie","modelId":"gpt-6-astra"`)
					if strings.Contains(s, `"relay"`) {
						t.Fatal("the lead's file still names relay")
					}
					if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if own {
					// pi-subagents told to keep every subagent's session
					// in a folder of its own: <run id>/run-0/session.jsonl
					// there, its header naming the lead's file
					runs := filepath.Join(filepath.Dir(claude), "subs", "x")
					if err := os.MkdirAll(filepath.Dir(runs), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(filepath.Join(pi, strings.TrimSuffix(leadFile, ".jsonl")), runs); err != nil {
						t.Fatal(err)
					}
					cfg := filepath.Join(pi, "extensions", "subagent", "config.json")
					os.MkdirAll(filepath.Dir(cfg), 0o755)
					if err := os.WriteFile(cfg, []byte(`{"defaultSessionDir": "`+filepath.ToSlash(runs)+`"}`), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want := append([]call{}, scout...)
				if !gateway {
					want = append(want, leads...)
				}
				sort.Slice(want, func(i, j int) bool { return want[i].at < want[j].at })
				var got []Call
				for _, c := range Calls(time.Time{}) {
					if c.Agent == "pi" {
						got = append(got, c)
					}
				}
				sort.Slice(got, func(i, j int) bool { return got[i].Time.Before(got[j].Time) })
				if len(got) != len(want) {
					t.Fatalf("%d calls, want %d: %+v", len(got), len(want), got)
				}
				for i, w := range want {
					c := got[i]
					at, _ := time.Parse(time.RFC3339Nano, w.at)
					if c.Session != lead || !c.Time.Equal(at) || c.Model != "deepseek/deepseek-flash" || c.Upstream != "relay" ||
						c.Cwd != "/work/sub" || c.Tokens != w.tokens {
						t.Errorf("call %d: %+v, want %s %+v", i, c, w.at, w.tokens)
					}
				}
				var deepseek Tokens
				for _, c := range want {
					deepseek.add(c.tokens)
				}
				var n int
				for _, s := range List(0) {
					if s.Agent != "pi" {
						continue
					}
					n++
					if s.ID != lead || s.Cwd != "/work/sub" {
						t.Errorf("session %s %q", s.ID, s.Cwd)
					}
					var seen bool
					for _, m := range s.Models {
						if m.Model == "deepseek/deepseek-flash" {
							seen = true
							if m.Tokens != deepseek {
								t.Errorf("deepseek on Sessions: %+v, want %+v", m.Tokens, deepseek)
							}
						}
					}
					if !seen {
						t.Errorf("no deepseek on Sessions: %+v", s.Models)
					}
					// a scout on magpie's entry sends its header's id; the gateway's
					// pi|<that id> is the lead's row (GatewayKeys)
					if !slices.Contains(s.Subs, "01a1d480-2447-77e7-b4d4-1747dc3bd17c") {
						t.Errorf("Subs %v, want the scout's session id", s.Subs)
					}
					if !gateway && s.Tokens != deepseek {
						t.Errorf("session tokens %+v, want %+v", s.Tokens, deepseek)
					}
				}
				if n != 1 {
					t.Errorf("%d Pi sessions, want the lead's alone", n)
				}
			})
		}
	}
}

package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// Toby5080 on X, Maxxxxx-Wang in #7107: Qoder CN's calls through magpie were
// listed a second time under "Not through magpie" as "Local session" rows,
// their model a magpie routing group (group/cheap-model) and their cache hit
// ~50%. Qoder writes a reply from its OpenAI provider (magpie's own, id
// "magpie") with input_tokens counting the cache read too (29,351 = 1,703
// + 27,648 in the report), where the gateway records the uncached part, so
// the call never paired with the gateway's record by its tokens. The lines
// below are qodercli 1.1.89's (testdata/qoder-1.1.89) with the top-level
// fields and usage of the reporter's Qoder CN 1.1.86-hotfix.5.3 line.
func TestQoderCallsThroughMagpieCountOnce(t *testing.T) {
	now := holdClock(t, time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local))
	type call struct {
		id, model, provider string
		in, read, out       int
		gateway             time.Duration // the gateway record's end, from the line's; -1 for none
	}
	calls := []call{
		// a routing group's call, the gateway's record ending 29 ms before the line
		{"chatcmpl-a", "group/deepseek-flash-group", "magpie", 29351, 27648, 470, -29 * time.Millisecond},
		// a Codex subscription's model through magpie
		{"chatcmpl-b", "gpt-6.2-sol", "magpie", 412870, 206336, 1210, -40 * time.Millisecond},
		// a group's call whose record ended long before the line was written:
		// only magpie serves group/…, so it is magpie's whatever its pairing
		{"chatcmpl-c", "group/cheap-model", "magpie", 780112, 389120, 96, -9 * time.Second},
		// a provider of the user's own, never through magpie
		{"chatcmpl-d", "glm-5", "zhipu", 400, 300, 50, -1},
	}
	for _, agent := range []string{"qoder-cn", "qoder"} {
		t.Run(agent, func(t *testing.T) {
			pageHome(t)
			sid := "7a2e4c6b-8d1f-4a3e-b5c7-9e0d2f4a6b83"
			var lines, recs []string
			for i, c := range calls {
				at := now.Add(time.Duration(i-10) * time.Minute).UTC()
				lines = append(lines,
					fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"继续"},"uuid":"u%d","timestamp":%q,"parentUuid":null,"isSidechain":false,"cwd":"/Users/me/proj","sessionId":%q,"userType":"external","entrypoint":"cli","version":"1.1.86-hotfix.5.3","gitBranch":"main"}`,
						i, at.Add(-15*time.Second).Format(time.RFC3339Nano), sid),
					fmt.Sprintf(`{"type":"assistant","uuid":"a%d","timestamp":%q,"customProviderId":%q,"modelSource":"custom","message":{"id":%q,"type":"message","role":"assistant","model":%q,"stop_reason":"end_turn","stop_sequence":null,"content":[{"type":"text","text":"好的"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0}}},"parentUuid":"u%d","isSidechain":false,"cwd":"/Users/me/proj","sessionId":%q,"userType":"external","entrypoint":"cli","version":"1.1.86-hotfix.5.3","gitBranch":"main"}`,
						i, at.Format(time.RFC3339Nano), c.provider, c.id, c.model, c.in, c.out, c.read, i, sid))
				if c.gateway == -1 {
					continue
				}
				end := at.Add(c.gateway)
				r := Record{Time: end.Add(-14788 * time.Millisecond), Millis: 14788, Agent: agent, Provider: "deepseek", Model: "deepseek-v4-flash", Served: "deepseek-v4-flash", Requested: c.model, Status: 200,
					Input: c.in - c.read, Output: c.out, CacheRead: c.read, Session: sid, NativeSession: sid, ResponseID: c.id, Endpoint: "/v1/chat/completions"}
				j, _ := json.Marshal(r)
				recs = append(recs, string(j))
			}
			dir := filepath.Join(sessions.QoderDir(agent), "projects", "-Users-me-proj")
			os.MkdirAll(dir, 0700)
			file := filepath.Join(dir, sid+".jsonl")
			if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Qoder last wrote the file with its last line, on the held day:
			// a file not written since the period began is never read, and
			// the machine's own clock may be on another day (red on a Mac in
			// PST, where it was still 11-25).
			last := now.Add(time.Duration(len(calls)-11) * time.Minute)
			if err := os.Chtimes(file, last, last); err != nil {
				t.Fatal(err)
			}
			os.MkdirAll(filepath.Dir(Path()), 0700)
			os.WriteFile(Path(), []byte(strings.Join(recs, "\n")+"\n"), 0600)
			sessions.Reset()

			local := func(where string, rows []Row) {
				t.Helper()
				n := 0
				for _, r := range rows {
					if r.Source != "log" {
						continue
					}
					n++
					if r.Model != "glm-5" {
						t.Errorf("%s: a call through magpie listed again as a local session's: %+v", where, r)
					}
				}
				if n != 1 {
					t.Errorf("%s: %d local rows, want the one call on the user's own provider", where, n)
				}
			}
			p := QueryPage(Today, Filter{}, 0, 100)
			if p.Total != 4 || p.Sum.Calls != 4 {
				t.Errorf("Requests: %d rows, %d calls, want the gateway's 3 and the one direct", p.Total, p.Sum.Calls)
			}
			local("Requests", p.Rows)
			l := LedgerOf(Today, Filter{})
			if len(l.Rows) != 4 {
				t.Errorf("ledger: %d rows, want 4", len(l.Rows))
			}
			local("ledger", l.Rows)
			if d := Direct(Today); d.Totals.Calls != 1 {
				t.Errorf("Direct counts %d calls, want the one on the user's own provider", d.Totals.Calls)
			}
		})
	}
}

// A routing group's id names a model only magpie serves, as the agent
// wrote it (with magpie's provider id before it, too); a model that merely
// has "group" in it is not one.
func TestMagpieOnlyModels(t *testing.T) {
	for id, want := range map[string]bool{
		"group/cheap-model":          true,
		"group/deepseek-flash-group": true,
		"Group/Cheap":                true,
		"magpie/group/cheap-model":   true,
		"group/":                     false,
		"groupthink/x":               false,
		"my-group/x":                 false,
		"openrouter/group/x":         false,
		"glm-5":                      false,
		"":                           false,
	} {
		if got := magpieOnly(id); got != want {
			t.Errorf("magpieOnly(%q) = %v, want %v", id, got, want)
		}
	}
}

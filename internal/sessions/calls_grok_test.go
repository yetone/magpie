package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #4423 (liubing0806): Grok Build's own calls reach Usage, read from the
// same updates.jsonl the Sessions page reads (parseGrok). The four sessions
// in testdata/grok:
//
//   - a: two turns, the first spent by grok-4.7-build and grok-4.7-mini
//     together, the second by grok-4.7-build alone
//   - b: a subagent's own folder (session_kind subagent), its one turn its
//     own, not its parent a's
//   - c: a fork of a, whose first turn is the copy of a's first one —
//     counted where it was first written, so only its own turn (written
//     after its created_at) is a call of its own
//   - d: no turn completed, no calls
type grokTotal struct{ n, input, cacheRead, cacheWrite, output int }

func grokCallsBy(f func(Call) string) map[string]grokTotal {
	out := map[string]grokTotal{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent != "grok" {
			continue
		}
		t := out[f(c)]
		t.n, t.input, t.cacheRead, t.cacheWrite, t.output =
			t.n+1, t.input+c.Input, t.cacheRead+c.CacheRead, t.cacheWrite+c.CacheWrite, t.output+c.Output
		out[f(c)] = t
	}
	return out
}

func TestGrokCalls(t *testing.T) {
	for _, viaEnv := range []bool{false, true} {
		name := "~/.grok"
		if viaEnv {
			name = "GROK_HOME"
		}
		t.Run(name, func(t *testing.T) {
			claude, _ := setup(t)
			dir := filepath.Join(filepath.Dir(claude), "home", ".grok")
			copyTree(t, "testdata/grok", dir)
			if viaEnv {
				t.Setenv("GROK_HOME", dir)
			}
			want := map[string]grokTotal{
				"0190aaaa-0000-7000-8000-00000000000a": {3, 800, 2800, 0, 110},
				"0190aaaa-0000-7000-8000-00000000000b": {1, 200, 100, 0, 20},
				"0190aaaa-0000-7000-8000-00000000000c": {1, 100, 300, 0, 30},
			}
			got := grokCallsBy(func(c Call) string { return c.Session })
			if len(got) != len(want) {
				t.Fatalf("sessions with calls %v, want %v", got, want)
			}
			for s, w := range want {
				if got[s] != w {
					t.Fatalf("session %s calls %+v, want %+v", s, got[s], w)
				}
			}
			// the Usage ledger lists them by model
			byModel := grokCallsBy(func(c Call) string { return c.Model })
			wantModels := map[string]grokTotal{
				"grok-4.7-build": {3, 800, 3100, 0, 130},
				"grok-4.7-mini":  {2, 300, 100, 0, 30},
			}
			for m, w := range wantModels {
				if byModel[m] != w {
					t.Fatalf("model %s calls %+v, want %+v", m, byModel[m], w)
				}
			}
			if len(byModel) != len(wantModels) {
				t.Fatalf("models %v, want %v", byModel, wantModels)
			}
		})
	}
}

// A fork counts only its own turns: c's call is its turn of 09:00:30, not
// the copied one of 08:00:20.
func TestGrokCallsForkKeepsItsOwnTurns(t *testing.T) {
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".grok")
	copyTree(t, "testdata/grok", dir)
	var got []int64
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "grok" && c.Session == "0190aaaa-0000-7000-8000-00000000000c" {
			got = append(got, c.Time.Unix())
		}
	}
	if len(got) != 1 || got[0] != 1790499630 {
		t.Fatalf("fork c call times %v, want [1790499630]", got)
	}
}

// A turn asked through magpie's gateway is the gateway's to count, so it
// is not a Local session call too: its turn_completed sums every request
// of the turn per model, keyed by the model the reply named, which no
// gateway record matches one to one (liubing0806's modelCalls 7 and 17).
// The model it was asked on is in its prompt's _meta.modelId, one of the
// [model."magpie/…"] tables magpie writes in config.toml. The lines are a
// real session's of each kind, the prompt text cut: e asked on magpie's
// group/auto-kimi-k3 and answered as moonshotai/Kimi-K3; f asked on Grok's
// own grok-4.7, four calls in one turn.
func TestGrokCallsLeaveTheGatewaysTurnsOut(t *testing.T) {
	claude, _ := setup(t)
	dir := filepath.Join(filepath.Dir(claude), "home", ".grok")
	copyTree(t, "testdata/grok", dir)
	write := func(path, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "config.toml"), "[models]\ndefault = \"magpie/group/auto-kimi-k3\"\n\n[model.\"magpie/group/auto-kimi-k3\"]\nmodel = \"group/auto-kimi-k3\"\nbase_url = \"http://127.0.0.1:3425/v1\"\napi_key = \"magpie\"\n")
	sessions := filepath.Join(dir, "sessions", "%2Fwork%2Fgrok")
	const e, f = "01a10341-1c84-7bc0-935e-a35c28f59817", "01a19751-b6a2-7172-b233-ebb9aaac02ba"
	write(filepath.Join(sessions, e, "summary.json"), `{"info":{"id":"`+e+`","cwd":"/work/grok"},"created_at":"2026-10-03T19:32:41.254395Z","current_model_id":"group/auto-kimi-k3"}`)
	write(filepath.Join(sessions, e, "updates.jsonl"), `{"timestamp":1791056007,"method":"session/update","params":{"sessionId":"`+e+`","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"},"_meta":{"modelId":"group/auto-kimi-k3","promptIndex":0}},"_meta":{"eventId":"`+e+`-4","agentTimestampMs":1791056006956}}}
{"timestamp":1791056019,"method":"_x.ai/session/update","params":{"sessionId":"`+e+`","update":{"sessionUpdate":"turn_completed","prompt_id":"8aeb9078-f6d6-44ce-9264-8119e6360964","stop_reason":"end_turn","usage":{"inputTokens":26124,"outputTokens":92,"totalTokens":26216,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":45,"modelCalls":1,"apiDurationMs":11887,"modelUsage":{"moonshotai/Kimi-K3":{"inputTokens":26124,"outputTokens":92,"totalTokens":26216,"cachedReadTokens":0,"cacheCreationTokens":0,"reasoningTokens":45,"modelCalls":1,"apiDurationMs":11887}},"numTurns":1},"elapsed_ms":12182},"_meta":{"eventId":"`+e+`-46","agentTimestampMs":1791056019060}}}
`)
	write(filepath.Join(sessions, f, "summary.json"), `{"info":{"id":"`+f+`","cwd":"/work/grok"},"created_at":"2026-11-01T13:34:37.231999Z","current_model_id":"grok-4.7","session_kind":"headless"}`)
	write(filepath.Join(sessions, f, "updates.jsonl"), `{"timestamp":1793540077,"method":"session/update","params":{"sessionId":"`+f+`","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"},"_meta":{"modelId":"grok-4.7","promptIndex":0}},"_meta":{"eventId":"`+f+`-4","agentTimestampMs":1793540077642}}}
{"timestamp":1793540091,"method":"_x.ai/session/update","params":{"sessionId":"`+f+`","update":{"sessionUpdate":"turn_completed","prompt_id":"aae30f85-b907-4557-82d7-5ac8fe6363db","stop_reason":"end_turn","usage":{"inputTokens":97708,"outputTokens":435,"totalTokens":98143,"cachedReadTokens":82304,"cacheCreationTokens":0,"reasoningTokens":223,"modelCalls":4,"apiDurationMs":12347,"costUsdTicks":745700000,"modelUsage":{"grok-4.7-build":{"inputTokens":97708,"outputTokens":435,"totalTokens":98143,"cachedReadTokens":82304,"cacheCreationTokens":0,"reasoningTokens":223,"modelCalls":4,"apiDurationMs":12347,"costUsdTicks":745700000}},"numTurns":4},"elapsed_ms":14206},"_meta":{"eventId":"`+f+`-102","agentTimestampMs":1793540091654}}}
`)
	// g: e's turn with no prompt line before it, so the session's own
	// model (summary.json) tells; h: asked on a magpie/ table name that
	// config.toml doesn't list (written by an older magpie, or edited).
	turn := `{"timestamp":1791056019,"method":"_x.ai/session/update","params":{"sessionId":"ID","update":{"sessionUpdate":"turn_completed","stop_reason":"end_turn","usage":{"inputTokens":26124,"outputTokens":92,"cachedReadTokens":0,"modelCalls":1,"modelUsage":{"moonshotai/Kimi-K3":{"inputTokens":26124,"outputTokens":92,"cachedReadTokens":0,"modelCalls":1}}}}}}` + "\n"
	const g, h = "01a10341-1c84-7bc0-935e-a35c28f5981a", "01a10341-1c84-7bc0-935e-a35c28f5981b"
	write(filepath.Join(sessions, g, "summary.json"), `{"info":{"id":"`+g+`","cwd":"/work/grok"},"created_at":"2026-10-03T19:32:41Z","current_model_id":"group/auto-kimi-k3"}`)
	write(filepath.Join(sessions, g, "updates.jsonl"), strings.ReplaceAll(turn, "ID", g))
	write(filepath.Join(sessions, h, "summary.json"), `{"info":{"id":"`+h+`","cwd":"/work/grok"},"created_at":"2026-10-03T19:32:41Z","current_model_id":"grok-4.7"}`)
	write(filepath.Join(sessions, h, "updates.jsonl"), `{"timestamp":1791056007,"method":"session/update","params":{"sessionId":"`+h+`","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"},"_meta":{"modelId":"magpie/deepseek/pro","promptIndex":0}}}}`+"\n"+strings.ReplaceAll(turn, "ID", h))
	got := grokCallsBy(func(c Call) string { return c.Session })
	for _, s := range []string{e, g, h} {
		if _, ok := got[s]; ok {
			t.Fatalf("%s: a turn asked through magpie's gateway is a Local session call too: %+v", s, got[s])
		}
	}
	if w := (grokTotal{1, 97708 - 82304, 82304, 0, 435}); got[f] != w {
		t.Fatalf("Grok's own turn: %+v, want %+v", got[f], w)
	}
	// the three of testdata/grok are still counted
	if len(got) != 4 {
		t.Fatalf("sessions with calls %v", got)
	}
}

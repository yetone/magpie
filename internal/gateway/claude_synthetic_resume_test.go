package gateway

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Code adds this local reply on native resume. Its model is
// <synthetic> in the transcript, but only role/content reach Messages.
const resumeLocalReply = "No response requested."

func TestClaudeSyntheticResumeKeepsTheRun(t *testing.T) {
	for _, tui := range []bool{false, true} {
		for _, shelved := range []bool{false, true} {
			for _, changedSchema := range []bool{false, true} {
				t.Run(fmt.Sprintf("tui=%t/shelved=%t/schemaChanged=%t", tui, shelved, changedSchema), func(t *testing.T) {
					var h *resumeHarness
					if tui {
						h, _ = tuiHelperHarness(t, "")
					} else {
						h = newResumeHarness(t)
					}
					send := func(schema string, msgs ...string) {
						t.Helper()
						body := `{"model":"claude-sonnet-5","max_tokens":100,"system":"rules A","tools":[{"name":"read","input_schema":` + schema + `}],"messages":[` + strings.Join(msgs, ",") + `]}`
						var u Usage
						if code, msg := h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
							t.Fatalf("%d %s", code, msg)
						}
					}
					schema := `{"type":"object"}`
					send(schema, user("original ask"))
					var pid, sid string
					for p, l := range h.runs() {
						if strings.Contains(l, "\ntold ") {
							pid, sid = p, "s"+p
						}
					}
					if pid == "" {
						t.Fatalf("original run not found in %v", h.runs())
					}
					if shelved {
						for i := range idleMost {
							h.ask(fmt.Sprintf("other rules %d", i), "other ask")
						}
						eventually(t, "the original session was not shelved", func() bool {
							h.s.subscription.mu.Lock()
							defer h.s.subscription.mu.Unlock()
							return len(h.s.subscription.shelf) == 1
						})
					}
					before := h.runs()
					if changedSchema {
						schema = `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`
					}
					send(schema, user("original ask"), assistant("ok"), assistant(resumeLocalReply), user("more after resume"))
					after := h.runs()
					var log string
					selectedPID := pid
					if !shelved && !changedSchema {
						log = after[pid]
						if len(after) != len(before) {
							t.Errorf("synthetic resume started a new run: %d -> %d", len(before), len(after))
							for p, l := range after {
								if _, had := before[p]; !had {
									selectedPID, log = p, l
								}
							}
						}
					} else {
						for p, l := range after {
							if _, had := before[p]; !had {
								selectedPID, log = p, l
							}
						}
						resumed := strings.Contains(log, "--resume "+sid)
						if resumed != (shelved && !changedSchema) {
							t.Errorf("resumed original session: %t (want sid=%q, selectedPID=%q); shelved=%t schemaChanged=%t\n%s", resumed, sid, selectedPID, shelved, changedSchema, log)
						}
					}
					i := strings.LastIndex(log, "\ntold ")
					if i < 0 {
						t.Fatalf("no input sent to the selected run: %s", log)
					}
					told := log[i:]
					observed := told
					if tui && changedSchema {
						args := strings.Fields(strings.SplitN(log, "\n", 2)[0])
						for j := 0; j+1 < len(args); j++ {
							if args[j] != "--resume" {
								continue
							}
							files, err := filepath.Glob(filepath.Join(h.config, "projects", "*", args[j+1]+".jsonl"))
							if err != nil || len(files) != 1 {
								t.Fatalf("replay transcript: %v, %v", files, err)
							}
							b, err := os.ReadFile(files[0])
							if err != nil {
								t.Fatal(err)
							}
							observed += string(b)
						}
					}
					if !strings.Contains(observed, resumeLocalReply) || !strings.Contains(told, "more after resume") {
						t.Errorf("resume lost local reply or new input: %s", told)
					}
					if changedSchema {
						var toolsPath string
						h.s.subscription.mu.Lock()
						for _, r := range h.s.subscription.idle {
							if fmt.Sprint(r.cmd.Process.Pid) == selectedPID {
								toolsPath = filepath.Join(r.tmp, "tools.json")
							}
						}
						h.s.subscription.mu.Unlock()
						tools, err := os.ReadFile(toolsPath)
						if err != nil || !strings.Contains(string(tools), `"required":["path"]`) {
							t.Errorf("new run was not given the changed schema: %s, %v", tools, err)
						}
						if len(after) != len(before)+1 || !strings.Contains(observed, "original ask") {
							t.Errorf("schema change did not start a run with full history: %s", told)
						}
					} else if strings.Contains(told, "original ask") || strings.Contains(told, "conversation_history") {
						t.Errorf("synthetic resume rendered full history instead of the increment: %s", told)
					}
					if !changedSchema {
						send(schema, user("original ask"), assistant("ok"), assistant(resumeLocalReply), user("more after resume"), assistant("ok"), user("final ask"))
						last := h.runs()
						log = last[selectedPID]
						i = strings.LastIndex(log, "\ntold ")
						if len(last) != len(after) || i < 0 || !strings.Contains(log[i:], "final ask") || strings.Contains(log[i:], resumeLocalReply) || strings.Contains(log[i:], "original ask") {
							t.Errorf("turn after synthetic resume did not keep the run with only its new input: %v", last)
						}
					}
				})
			}
		}
	}
}

func TestClaudeResumePrefixRequiresTheSameConversation(t *testing.T) {
	for _, c := range []struct {
		name      string
		change    func(*Request, *string)
		want      bool
		wantSince int
	}{
		{"unchanged", func(*Request, *string) {}, true, 2},
		{"cancelled turn before local reply", func(r *Request, _ *string) {
			r.Messages = []Message{
				r.Messages[0],
				r.Messages[1],
				{Role: "user", Parts: []Part{{Kind: Text, Text: "cancelled ask"}}},
				r.Messages[2],
				r.Messages[3],
			}
		}, true, 3},
		{"other account", func(_ *Request, owner *string) { *owner = "other" }, false, 0},
		{"other model", func(r *Request, _ *string) { r.Model = "other" }, false, 0},
		{"other system", func(r *Request, _ *string) { r.System = "other" }, false, 0},
		{"other tool schema", func(r *Request, _ *string) {
			r.Tools = []Tool{{Name: "read", Schema: []byte(`{"type":"object","required":["path"]}`)}}
		}, false, 0},
		{"other settings", func(r *Request, _ *string) { r.ThinkOff = true }, false, 0},
		{"changed prefix", func(r *Request, _ *string) { r.Messages[0].Parts[0].Text = "other" }, false, 0},
		{"another provider replied", func(r *Request, _ *string) { r.Messages[2].Parts[0].Text = "an answer from another provider" }, false, 0},
		{"unmatched result", func(r *Request, _ *string) {
			r.Messages[2].Parts = []Part{{Kind: ToolResult, CallID: "unknown", Text: "result"}}
		}, false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := &Request{
				Model:  "claude-sonnet-5",
				System: "rules",
				Tools:  []Tool{{Name: "read", Schema: []byte(`{"type":"object"}`)}},
				Messages: []Message{
					{Role: "user", Parts: []Part{{Kind: Text, Text: "original ask"}}},
					{Role: "assistant", Parts: []Part{{Kind: Text, Text: "ok"}}},
					{Role: "assistant", Parts: []Part{{Kind: Text, Text: resumeLocalReply}}},
					{Role: "user", Parts: []Part{{Kind: Text, Text: "more"}}},
				},
			}
			key := turnKey("owner", req, req.Messages[:2])
			b := newSubscriptionBridge()
			saved := &savedSession{key: key, timer: time.NewTimer(time.Hour)}
			t.Cleanup(func() { saved.timer.Stop() })
			b.shelf[key] = saved
			owner := "owner"
			c.change(req, &owner)
			got := b.unshelve(req, owner)
			if (got == saved) != c.want {
				t.Fatalf("found original conversation: %t, want %t", got == saved, c.want)
			}
			if c.want {
				if len(got.since) != c.wantSince {
					t.Fatalf("increment length = %d, want %d: %+v", len(got.since), c.wantSince, got.since)
				}
				foundLocal := false
				for _, m := range got.since {
					if m.Role == "assistant" && len(m.Parts) == 1 && m.Parts[0].Kind == Text && m.Parts[0].Text == resumeLocalReply {
						foundLocal = true
					}
				}
				if !foundLocal {
					t.Fatalf("lost the local reply in increment: %+v", got.since)
				}
			}
		})
	}
}

// A turn after Claude Code's local "No response requested." that goes on in
// the run waiting before it (waitingPrefix), refused for the account's limit
// (429), then asked again after the reset (including a second 429 on the
// unshelved run). The run asked again must still be told the cancelled ask.
func TestClaudeSyntheticResumeRefusedForLimitKeepsCancelledAsk(t *testing.T) {
	for _, mode := range []string{"print", "interactive"} {
		t.Run(mode, func(t *testing.T) {
			h := newQuotaHarness(t, mode, time.Now().Add(3*time.Hour).Unix())
			logs := keepLog(t)
			first := `the conversation so far ALPHA`
			code, answer := h.try(`[` + rmsg("user", first) + `]`)
			if code != 200 || !strings.Contains(answer, "turn 1") {
				t.Fatalf("first: %d %s", code, answer)
			}
			said := strings.TrimSpace(answer[strings.Index(answer, "pid "):])
			said = said[:strings.Index(said, `"`)]
			conv := `[` + rmsg("user", first) + `,` + rmsg("assistant", said) + `,` + rmsg("user", "cancelled ask GAMMA") + `,` + rmsg("assistant", resumeLocalReply) + `,` + rmsg("user", "and run the tests BETA") + `]`
			os.WriteFile(h.limited, nil, 0o600)
			before429 := h.toldBy()
			if code, msg := h.try(conv); code != 429 {
				t.Fatalf("while limited (first): %d %s\n%s", code, msg, logs.String())
			}
			newRuns := 0
			for pid := range h.toldBy() {
				if _, ok := before429[pid]; !ok {
					newRuns++
				}
			}
			if newRuns != 0 || h.shelved() == nil {
				t.Fatalf("expected resumed run shelved after first 429: newRuns=%d shelved=%v", newRuns, h.shelved() != nil)
			}
			// A second 429 on the unshelved run must also keep the prefix key and cancelled ask.
			if code, msg := h.try(conv); code != 429 {
				t.Fatalf("while limited (second): %d %s\n%s", code, msg, logs.String())
			}
			if h.shelved() == nil {
				t.Fatalf("expected session re-shelved after second 429:\n%s", logs.String())
			}
			os.Remove(h.limited)
			before := h.toldBy()
			code, answer = h.try(conv)
			if code != 200 {
				t.Fatalf("after reset: %d %s\n%s", code, answer, logs.String())
			}
			var run string
			for pid, l := range h.toldBy() {
				if old, ok := before[pid]; !ok || l != old {
					run = l
				}
			}
			i := strings.LastIndex(run, "\ntold ")
			told := run
			if i >= 0 {
				told = run[i:]
			}
			if !strings.Contains(told, "BETA") {
				t.Fatalf("new ask not told: %s", run)
			}
			seen := run
			args := strings.Fields(strings.SplitN(run, "\n", 2)[0])
			for j := 0; j+1 < len(args); j++ {
				if args[j] == "--resume" {
					seen += h.sessionFile(args[j+1])
				}
			}
			if !strings.Contains(seen, "GAMMA") {
				t.Errorf("CONTEXT LOST: the run asked again after the reset was never told the cancelled ask GAMMA:\n%s\nmagpie log:\n%s", run, logs.String())
			}
		})
	}
}

// A turn after Claude Code's local "No response requested." goes on in the
// run waiting before it (waitingPrefix); the client's connection drops
// mid-reply and it sends the same request again (#5405). The retry's run
// must still be told the cancelled ask GAMMA.
func TestClaudeSyntheticResumeBusyRetryKeepsCancelledAsk(t *testing.T) {
	for _, mode := range []string{"print", "interactive"} {
		t.Run(mode, func(t *testing.T) {
			h := newBusyRetryHarness(t, mode)
			logs := keepLog(t)
			first := h.ask(`[` + rmsg("user", "the conversation so far ALPHA") + `]`)
			if !strings.Contains(first, "turn 1") {
				t.Fatalf("first: %q", first)
			}
			conv := `[` + rmsg("user", "the conversation so far ALPHA") + `,` + rmsg("assistant", first) + `,` + rmsg("user", "cancelled ask GAMMA") + `,` + rmsg("assistant", resumeLocalReply) + `,` + rmsg("user", "and run the tests BETA") + `]`
			firstTold := h.heard()
			done := h.send(conv)
			t.Cleanup(func() { h.s.subscription.abortAll(); <-done })
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(h.read(), "BETA") || h.heard() == firstTold {
				if time.Now().After(deadline) {
					t.Fatalf("the first attempt never held in a resumed run: %s", logs.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			before := h.toldBy()
			_ = h.ask(conv)
			foundRetry := false
			for name, told := range h.toldBy() {
				if _, ok := before[name]; ok {
					continue
				}
				foundRetry = true
				seen := told
				args := strings.Fields(strings.SplitN(told, "\n", 2)[0])
				for j := 0; j+1 < len(args); j++ {
					if args[j] == "--resume" {
						seen += (&quotaHarness{config: os.Getenv("CLAUDE_CONFIG_DIR")}).sessionFile(args[j+1])
					}
				}
				if !strings.Contains(seen, "GAMMA") {
					t.Errorf("CONTEXT LOST: the retry's run %s was never told the cancelled ask GAMMA\nmagpie log:\n%s", name, logs.String())
				}
			}
			if !foundRetry {
				t.Fatalf("expected retry branch run to start:\n%s", logs.String())
			}
		})
	}
}

// A turn taken past the local reply that went on to a tool call, its
// result refused for the limit: the turn is the one the run took, the
// cancelled ask and the call in it, at the key it took it at; a key no run
// took is nextTurn's.
func TestClaudeTurnFromKeepsTheTurnTaken(t *testing.T) {
	text := func(role, s string) Message { return Message{Role: role, Parts: []Part{{Kind: Text, Text: s}}} }
	req := &Request{Model: "claude-sonnet-5", System: "rules", Messages: []Message{
		text("user", "original ask"),
		text("assistant", "ok"),
		text("user", "cancelled ask GAMMA"),
		text("assistant", resumeLocalReply),
		text("user", "more BETA"),
		{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: "c1", Name: "read"}}},
		{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: "c1", Text: "result"}}},
	}}
	took := turnKey("owner", req, req.Messages[:2])
	key, since := turnFrom(req, "owner", took)
	if key != took || len(since) != 5 || since[0].Parts[0].Text != "cancelled ask GAMMA" {
		t.Fatalf("turn taken past the local reply: key %t, since %+v", key == took, since)
	}
	next, nextSince := nextTurn(req, "owner")
	for _, other := range []string{"", next, turnKey("other", req, req.Messages[:2])} {
		if key, since := turnFrom(req, "owner", other); key != next || len(since) != len(nextSince) {
			t.Fatalf("a key no run took past the local reply (%q) gave %d messages", other, len(since))
		}
	}
	if key, _ := pastLocalReply(&Request{Model: "m", Messages: []Message{req.Messages[0], req.Messages[1], req.Messages[4]}}, "owner"); key != "" {
		t.Fatal("no local reply passed, but a key")
	}
}

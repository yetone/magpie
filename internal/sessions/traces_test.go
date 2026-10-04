package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func traceLine(t *testing.T, c *traceCursor, at time.Time, typ string, payload any, bodies bool) []TraceSpan {
	t.Helper()
	line, err := json.Marshal(map[string]any{"timestamp": at, "type": typ, "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	return c.line(line, bodies)
}
func traceTestCursor(agent string) *traceCursor {
	return &traceCursor{agent: agent, turns: map[string]*traceTurn{}, tools: map[string]TraceSpan{}, parents: map[string]string{}}
}

func TestCodexTraceRootWaitsForCompleteImageFirstTurn(t *testing.T) {
	for _, bodies := range []bool{false, true} {
		c := traceTestCursor("codex")
		at := time.Now().UTC()
		traceLine(t, c, at, "session_meta", map[string]any{"id": "session"}, bodies)
		root := traceLine(t, c, at, "event_msg", map[string]any{"type": "task_started", "turn_id": "turn"}, bodies)[0]
		if !root.Pending {
			t.Fatal("empty start root must not be exported")
		}
		input := json.RawMessage(`[{"type":"local_image","path":"/tmp/image.png"},{"type":"text","text":"Describe this image"}]`)
		traceLine(t, c, at.Add(time.Second), "event_msg", map[string]any{"type": "item_completed", "turn_id": "turn", "item": map[string]any{"type": "UserMessage", "content": input}}, bodies)
		model := traceLine(t, c, at.Add(2*time.Second), "token_usage_record", map[string]any{"turn_id": "turn", "response_id": "response"}, bodies)[0]
		finished := traceLine(t, c, at.Add(3*time.Second), "event_msg", map[string]any{"type": "task_complete", "turn_id": "turn", "last_agent_message": "An image"}, bodies)[0]
		if finished.Pending || finished.ID != root.ID || model.Parent != root.ID {
			t.Fatal("completion or child ancestry lost")
		}
		if bodies && (finished.Input != string(input) || model.Input != string(input) || finished.Output != "An image") {
			t.Fatal("image-first input or final output lost")
		}
		if !bodies && (finished.Input != "" || finished.Output != "" || model.Input != "") {
			t.Fatal("bodies exported when disabled")
		}
	}
}

func TestCodexTraceRecordedToolsAndInterruptedTurns(t *testing.T) {
	c := traceTestCursor("codex")
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	traceLine(t, c, base, "session_meta", map[string]any{"id": "session"}, false)
	root := traceLine(t, c, base, "event_msg", map[string]any{"type": "task_started", "turn_id": "turn", "started_at": base.Unix()}, false)[0]
	traceLine(t, c, base, "turn_context", map[string]any{"turn_id": "turn", "model": "model"}, false)
	tools := traceLine(t, c, base.Add(3*time.Second), "event_msg", map[string]any{"type": "item_completed", "turn_id": "turn", "started_at_ms": base.Add(time.Second).UnixMilli(), "completed_at_ms": base.Add(3 * time.Second).UnixMilli(), "item": map[string]any{"type": "CommandExecution", "id": "tool", "command": "PRIVATE-COMMAND", "aggregated_output": "PRIVATE-OUTPUT", "exit_code": 1}}, false)
	if len(tools) != 1 || tools[0].Inferred || !tools[0].Error || tools[0].Parent != root.ID || tools[0].End.Sub(tools[0].Start) != 2*time.Second || tools[0].Input != "" || tools[0].Output != "" {
		t.Fatalf("tool: %+v", tools)
	}
	model := traceLine(t, c, base.Add(4*time.Second), "token_usage_record", map[string]any{"turn_id": "turn", "response_id": "response", "usage": map[string]int{"input_tokens": 100, "cached_input_tokens": 40, "output_tokens": 10}}, false)[0]
	if model.Parent != root.ID || model.Tokens.Input != 60 || model.Tokens.CacheRead != 40 || model.Model != "model" || !model.Inferred {
		t.Fatalf("model: %+v", model)
	}
	end := base.Add(5*time.Second + 100*time.Millisecond)
	finished := traceLine(t, c, end, "event_msg", map[string]any{"type": "turn_aborted", "turn_id": "turn", "duration_ms": 5100, "completed_at": end.Unix()}, false)[0]
	if !finished.Error || finished.ID != root.ID || finished.Start != base || finished.End != end {
		t.Fatalf("finished: %+v", finished)
	}
	// The next interaction must have its own root, even in the same session.
	next := traceLine(t, c, end, "event_msg", map[string]any{"type": "task_started", "turn_id": "next"}, false)[0]
	if next.ID == root.ID || TraceID(next.Agent, next.Session, next.Turn) == TraceID(root.Agent, root.Session, root.Turn) {
		t.Fatal("two turns share a trace")
	}
}

func TestCodexTraceNewTurnClosesMissingCompletion(t *testing.T) {
	for _, first := range []string{"task_started", "turn_context"} {
		t.Run(first, func(t *testing.T) {
			c := traceTestCursor("codex")
			at := time.Now().UTC()
			traceLine(t, c, at, "session_meta", map[string]any{"id": "session"}, true)
			root := traceLine(t, c, at, "event_msg", map[string]any{"type": "task_started", "turn_id": "old"}, true)[0]
			traceLine(t, c, at.Add(time.Second), "event_msg", map[string]any{"type": "item_completed", "turn_id": "old", "item": map[string]any{"type": "UserMessage", "content": "question"}}, true)
			traceLine(t, c, at.Add(2*time.Second), "response_item", map[string]any{"type": "function_call", "call_id": "tool", "name": "exec", "arguments": "command"}, true)
			typ := "event_msg"
			if first == "turn_context" {
				typ = "turn_context"
			}
			end := at.Add(3 * time.Second)
			spans := traceLine(t, c, end, typ, map[string]any{"type": first, "turn_id": "new", "model": "model"}, true)
			var roots, tools int
			for _, s := range spans {
				if s.Turn != "old" {
					continue
				}
				if !s.Error || s.Pending || s.End != end {
					t.Fatalf("unfinished observation: %+v", s)
				}
				if s.Kind == "span" {
					roots++
					if s.ID != root.ID || !s.Inferred || s.Input != `"question"` {
						t.Fatalf("interrupted root: %+v", s)
					}
				} else if s.Kind == "tool" {
					tools++
					if s.Parent != root.ID {
						t.Fatal("tool ancestry lost")
					}
				}
			}
			if roots != 1 || tools != 1 || c.turns["old"] != nil || len(c.tools) != 0 {
				t.Fatal("interrupted turn or tool not closed")
			}
			if spans := traceLine(t, c, end, "event_msg", map[string]any{"type": "task_started", "turn_id": "new"}, true); len(spans) != 1 || !spans[0].Pending || spans[0].ID == root.ID {
				t.Fatal("next turn duplicated the interrupted root")
			}
		})
	}
}

func TestPiAndOMPTraceStartRootIsPending(t *testing.T) {
	for _, agent := range []string{"pi", "omp"} {
		t.Run(agent, func(t *testing.T) {
			c := traceTestCursor(agent)
			c.session = "session"
			at := time.Now().UTC()
			line, err := json.Marshal(map[string]any{"type": "message", "id": "user", "timestamp": at, "message": map[string]any{"role": "user", "content": "question"}})
			if err != nil {
				t.Fatal(err)
			}
			spans := c.line(line, true)
			if len(spans) != 1 || !spans[0].Pending || spans[0].Input != `"question"` {
				t.Fatalf("start root must stay pending: %+v", spans)
			}
		})
	}
}

func TestPiTracePairsToolsAndUsesFinalTiming(t *testing.T) {
	c := traceTestCursor("pi")
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	feed := func(at time.Time, o map[string]any) []TraceSpan {
		o["timestamp"] = at
		b, _ := json.Marshal(o)
		return c.line(b, true)
	}
	feed(base, map[string]any{"type": "session", "id": "session"})
	root := feed(base, map[string]any{"type": "message", "id": "user", "message": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "question"}}}})[0]
	model := feed(base.Add(4*time.Second), map[string]any{"type": "message", "id": "assistant", "parentId": "user", "message": map[string]any{"role": "assistant", "timestamp": base.Add(time.Second).UnixMilli(), "responseId": "response", "model": "model", "provider": "magpie", "stopReason": "toolUse", "usage": map[string]int{"input": 100, "output": 10, "cacheRead": 20}, "content": []any{map[string]any{"type": "toolCall", "id": "tool", "name": "bash", "arguments": map[string]string{"command": "hello"}}}}})[0]
	tool := feed(base.Add(5*time.Second), map[string]any{"type": "message", "id": "result", "parentId": "assistant", "message": map[string]any{"role": "toolResult", "toolCallId": "tool", "toolName": "bash", "isError": false, "content": []any{map[string]string{"type": "text", "text": "result"}}}})[0]
	if model.Parent != root.ID || model.Start != base.Add(time.Second) || model.Tokens.Input != 100 || tool.Parent != root.ID || !tool.Inferred || tool.End.Sub(tool.Start) != time.Second || tool.Input == "" || tool.Output == "" {
		t.Fatalf("model %+v tool %+v", model, tool)
	}
	spans := feed(base.Add(8*time.Second), map[string]any{"type": "message", "id": "final", "parentId": "result", "message": map[string]any{"role": "assistant", "timestamp": base.Add(6 * time.Second).UnixMilli(), "model": "model", "stopReason": "stop", "content": []any{map[string]string{"type": "text", "text": "answer"}}}})
	if len(spans) != 1 || spans[0].Kind != "generation" || spans[0].Output == "" {
		t.Fatalf("final: %+v", spans)
	}
	finished := feed(base.Add(8*time.Second), map[string]any{"type": "custom", "customType": "timing-final", "data": map[string]any{"totalMs": 8000, "endAt": base.Add(8 * time.Second).UnixMilli()}})[0]
	if finished.ID != root.ID || finished.Start != base || finished.End != base.Add(8*time.Second) {
		t.Fatalf("timing: %+v", finished)
	}
	if spans := c.finishPi(time.Now().Add(piTimingGrace)); len(spans) != 0 {
		t.Fatalf("timed root exported again: %+v", spans)
	}
}

func TestPiCompletedRootWithoutTimingExportsOnce(t *testing.T) {
	for _, agent := range []string{"pi", "omp"} {
		t.Run(agent, func(t *testing.T) {
			c := traceTestCursor(agent)
			c.session = "session"
			at := time.Now().UTC()
			feed := func(o map[string]any) []TraceSpan {
				o["timestamp"] = at
				b, _ := json.Marshal(o)
				return c.line(b, true)
			}
			root := feed(map[string]any{"type": "message", "id": "user", "message": map[string]any{"role": "user", "content": "question"}})[0]
			feed(map[string]any{"type": "message", "id": "final", "parentId": "user", "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": "answer"}})
			if spans := c.finishPi(time.Now()); len(spans) != 0 {
				t.Fatalf("root finished before timing grace: %+v", spans)
			}
			finished := c.finishPi(time.Now().Add(piTimingGrace))
			if len(finished) != 1 || finished[0].ID != root.ID || finished[0].Input != `"question"` || finished[0].Output != `"answer"` {
				t.Fatalf("fallback: %+v", finished)
			}
			if len(c.finishPi(time.Now().Add(piTimingGrace))) != 0 {
				t.Fatal("fallback exported twice")
			}
			if spans := feed(map[string]any{"type": "custom", "parentId": "final", "customType": "timing-final", "data": map[string]any{"totalMs": 1000, "endAt": at.Add(time.Second).UnixMilli()}}); len(spans) != 0 {
				t.Fatalf("late timing exported duplicate: %+v", spans)
			}
		})
	}
}

func TestPiTimingAcrossPollsAndFallback(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	dir := filepath.Join(PiDir(), "sessions", "work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, timing := range []bool{true, false} {
		t.Run(fmt.Sprint(timing), func(t *testing.T) {
			at := time.Now().UTC().Truncate(time.Millisecond)
			path := filepath.Join(dir, "2026-10-04T00-00-00_session.jsonl")
			appendEvent := func(o map[string]any) {
				t.Helper()
				b, err := json.Marshal(o)
				if err != nil {
					t.Fatal(err)
				}
				f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if _, err := f.Write(append(b, '\n')); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			appendEvent(map[string]any{"type": "session", "id": "session"})
			appendEvent(map[string]any{"type": "message", "id": "user", "timestamp": at, "message": map[string]any{"role": "user", "content": "question"}})
			appendEvent(map[string]any{"type": "message", "id": "final", "parentId": "user", "timestamp": at.Add(100 * time.Millisecond), "message": map[string]any{"role": "assistant", "timestamp": at.UnixMilli(), "stopReason": "stop", "content": "answer"}})
			r := NewTraceReader(at.Add(-time.Second))
			spans := r.Poll(true)
			if len(spans) != 2 || !spans[0].Pending || spans[1].Kind != "generation" {
				t.Fatalf("root exported without waiting: %+v", spans)
			}
			event := map[string]any{"type": "custom", "parentId": "final", "customType": "timing-final", "timestamp": at.Add(300 * time.Millisecond), "data": map[string]any{"totalMs": 350, "endAt": at.Add(300 * time.Millisecond).UnixMilli()}}
			// Model the next poll without sleeping. Expiry must not flush the
			// fallback before reading newly appended timing data.
			r.files[path].turns["user"].finishAfter = time.Now().Add(-time.Second)
			if timing {
				appendEvent(event)
			}
			finished := r.Poll(true)
			wantStart, wantEnd := at, at.Add(100*time.Millisecond)
			if timing {
				wantStart, wantEnd = at.Add(-50*time.Millisecond), at.Add(300*time.Millisecond)
			}
			if len(finished) != 1 || finished[0].ID != spans[0].ID || finished[0].Start != wantStart || finished[0].End != wantEnd || finished[0].Output != `"answer"` {
				t.Fatalf("completed root: %+v", finished)
			}
			if !timing {
				appendEvent(event)
			}
			if again := r.Poll(true); len(again) != 0 {
				t.Fatalf("root duplicated: %+v", again)
			}
		})
	}
}

func TestTraceReaderDoesNotReplayHistoryOrPartialLines(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", root)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	dir := filepath.Join(root, "sessions", "work")
	os.MkdirAll(dir, 0700)
	path := filepath.Join(dir, "2026-10-02T00-00-00-000Z_session.jsonl")
	old := time.Now().Add(-time.Minute)
	since := time.Now().Add(-time.Second)
	now := time.Now()
	header, _ := json.Marshal(map[string]any{"type": "session", "id": "session", "timestamp": old})
	historical, _ := json.Marshal(map[string]any{"type": "message", "id": "old-user", "timestamp": old, "message": map[string]any{"role": "user", "content": "old"}})
	live, _ := json.Marshal(map[string]any{"type": "message", "id": "live-user", "timestamp": now, "message": map[string]any{"role": "user", "content": "PRIVATE-PROMPT"}})
	completed, _ := json.Marshal(map[string]any{"type": "message", "id": "old-final", "parentId": "old-user", "timestamp": old.Add(time.Second), "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": "old answer"}})
	data := append(append(append(header, '\n'), historical...), '\n')
	data = append(data, append(completed, '\n')...)
	data = append(data, live...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	r := NewTraceReader(since)
	if spans := r.Poll(false); len(spans) != 0 {
		t.Fatalf("replayed history or partial line: %+v", spans)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("\n")
	f.Close()
	spans := r.Poll(false)
	if len(spans) != 1 || spans[0].Turn != "live-user" || spans[0].Input != "" {
		t.Fatalf("live: %+v", spans)
	}
	if len(r.Poll(false)) != 0 {
		t.Fatal("unchanged file emitted twice")
	}
}

func TestTraceReaderNewEventsWithCoarseFileMtime(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", root)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	dir := filepath.Join(root, "sessions", "work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-10-03T00-00-00-000Z_session.jsonl")
	since := time.Now().UTC()
	var data []byte
	for _, o := range []map[string]any{
		{"type": "session", "id": "session"},
		{"type": "message", "id": "old", "timestamp": since.Add(-time.Minute), "message": map[string]any{"role": "user", "content": "old"}},
		{"type": "message", "id": "old-final", "parentId": "old", "timestamp": since.Add(-30 * time.Second), "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": "old reply"}},
		{"type": "message", "id": "new", "timestamp": since.Add(time.Millisecond), "message": map[string]any{"role": "user", "content": "new"}},
	} {
		b, _ := json.Marshal(o)
		data = append(data, append(b, '\n')...)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	coarse := since.Add(-time.Second)
	if err := os.Chtimes(path, coarse, coarse); err != nil {
		t.Fatal(err)
	}
	r := NewTraceReader(since)
	spans := r.Poll(false)
	if len(spans) != 1 || spans[0].Turn != "new" {
		t.Fatalf("new event lost or history replayed: %+v", spans)
	}
	if len(r.Poll(false)) != 0 {
		t.Fatal("unchanged file replayed")
	}
}

func TestPiTraceInterruptedInteractionClosesPendingTool(t *testing.T) {
	c := traceTestCursor("pi")
	base := time.Now().UTC()
	feed := func(at time.Time, o map[string]any) []TraceSpan {
		o["timestamp"] = at
		b, _ := json.Marshal(o)
		return c.line(b, false)
	}
	feed(base, map[string]any{"type": "session", "id": "session"})
	root := feed(base, map[string]any{"type": "message", "id": "user", "message": map[string]any{"role": "user"}})[0]
	feed(base.Add(time.Second), map[string]any{"type": "message", "id": "assistant", "parentId": "user", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "id": "tool", "name": "bash"}}}})
	spans := feed(base.Add(2*time.Second), map[string]any{"type": "message", "id": "next", "message": map[string]any{"role": "user"}})
	if len(spans) != 3 || spans[0].ID != root.ID || !spans[0].Error || !spans[0].Inferred || spans[1].Parent != root.ID || !spans[1].Error || len(c.tools) != 0 {
		t.Fatalf("interruption: %+v", spans)
	}
}

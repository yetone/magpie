package sessions

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func clientTraceFeed(t *testing.T, c *traceCursor, bodies bool, lines ...string) []TraceSpan {
	t.Helper()
	var out []TraceSpan
	for _, line := range lines {
		out = append(out, c.line([]byte(line), bodies)...)
	}
	return out
}
func spanKinds(spans []TraceSpan) (roots, models, tools []TraceSpan) {
	for _, s := range spans {
		switch s.Kind {
		case "generation":
			models = append(models, s)
		case "tool":
			tools = append(tools, s)
		default:
			roots = append(roots, s)
		}
	}
	return
}
func TestClaudeTraceRepeatedBlocksAndToolResult(t *testing.T) {
	for _, bodies := range []bool{false, true} {
		c := traceTestCursor("claude")
		spans := clientTraceFeed(t, c, bodies,
			`{"type":"user","sessionId":"session","uuid":"u","timestamp":"2026-10-02T12:00:00Z","message":{"content":"question"}}`,
			`{"type":"assistant","uuid":"a","timestamp":"2026-10-02T12:00:02Z","message":{"id":"m","model":"claude-model","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"private"}],"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":50}}}`,
			`{"type":"assistant","uuid":"b","timestamp":"2026-10-02T12:00:03Z","message":{"id":"m","model":"claude-model","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool","name":"Read","input":{"file":"private"}}],"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":50}}}`,
			`{"type":"user","uuid":"result","timestamp":"2026-10-02T12:00:05Z","message":{"content":[{"type":"tool_result","tool_use_id":"tool","content":"result","is_error":true}]}}`,
			`{"type":"assistant","uuid":"final","timestamp":"2026-10-02T12:00:08Z","message":{"id":"m2","model":"claude-model","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}],"usage":{"input_tokens":120,"output_tokens":30}}}`,
			`{"type":"system","subtype":"turn_duration","timestamp":"2026-10-02T12:00:09Z","durationMs":8000}`)
		roots, models, tools := spanKinds(spans)
		if len(models) != 2 || models[0].Tokens.Output != 20 || models[0].Tokens.Input != 100 || len(tools) != 1 || len(roots) != 2 {
			t.Fatalf("counts: %+v", spans)
		}
		if tools[0].Parent != roots[0].ID || !tools[0].Error || tools[0].End.Sub(tools[0].Start) != 2*time.Second || !tools[0].Inferred {
			t.Fatalf("tool %+v", tools)
		}
		if roots[1].ID != roots[0].ID || !roots[1].Error || roots[1].End.Sub(roots[1].Start) != 9*time.Second {
			t.Fatalf("root %+v", roots)
		}
		for _, s := range spans {
			if !bodies && (s.Input != "" || s.Output != "") {
				t.Fatal("body leaked")
			}
		}
		if bodies && (!strings.Contains(models[0].Output, "tool_use") || !strings.Contains(roots[1].Output, "answer")) {
			t.Fatal("missing content")
		}
	}
}

func TestClaudeTraceFinalWithoutTurnDuration(t *testing.T) {
	c := traceTestCursor("claude-desktop")
	clientTraceFeed(t, c, false,
		`{"type":"user","sessionId":"s","uuid":"u","timestamp":"2026-10-02T12:00:00Z","message":{"content":"question"}}`,
		`{"type":"assistant","timestamp":"2026-10-02T12:00:05Z","message":{"id":"m","model":"claude-model","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}],"usage":{"output_tokens":3}}}`)
	spans := c.finishClaude(false)
	if len(spans) != 2 || spans[0].Agent != "claude-desktop" || !spans[1].Inferred || len(c.finishClaude(false)) != 0 {
		t.Fatalf("final %+v", spans)
	}
}
func TestOmpTraceMainAndAuxiliaryModels(t *testing.T) {
	c := traceTestCursor("omp")
	spans := clientTraceFeed(t, c, false,
		`{"type":"title","title":"private"}`,
		`{"type":"session","id":"s"}`,
		`{"type":"message","id":"u","timestamp":"2026-10-02T12:00:00Z","message":{"role":"user","content":"question"}}`,
		`{"type":"message","id":"a","parentId":"u","timestamp":"2026-10-02T12:00:04Z","message":{"role":"assistant","model":"model","usage":{"input":100,"output":5},"content":[{"type":"toolCall","id":"tool","name":"bash"}],"stopReason":"toolUse"}}`,
		`{"type":"model_usage","id":"aux","parentId":"a","timestamp":"2026-10-02T12:00:05Z","model":"small","provider":"p","purpose":"title","usage":{"input":10,"output":2}}`,
		`{"type":"message","id":"result","parentId":"a","timestamp":"2026-10-02T12:00:06Z","message":{"role":"toolResult","toolCallId":"tool","content":"result"}}`,
		`{"type":"message","id":"final","parentId":"result","timestamp":"2026-10-02T12:00:08Z","message":{"role":"assistant","model":"model","usage":{"input":120,"output":3},"content":[],"stopReason":"stop"}}`)
	spans = append(spans, c.finishPi(time.Now().Add(piTimingGrace))...)
	roots, models, tools := spanKinds(spans)
	if len(roots) != 2 || len(models) != 3 || len(tools) != 1 || models[1].Tokens.Input != 10 || models[1].Parent != roots[0].ID {
		t.Fatalf("omp %+v", spans)
	}
}

func TestOpenCodeTraceSQLiteIDsAndUpdates(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "v1", true: "v2"}[v2], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "opencode.db")
			t.Setenv("OPENCODE_DB", path)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			queries := []string{
				`CREATE TABLE session (id TEXT PRIMARY KEY,parent_id TEXT,directory TEXT,title TEXT,time_created INTEGER,time_updated INTEGER)`,
				`CREATE TABLE message (id TEXT PRIMARY KEY,session_id TEXT,time_created INTEGER,time_updated INTEGER,data TEXT)`,
				`CREATE TABLE part (id TEXT PRIMARY KEY,message_id TEXT,session_id TEXT,time_updated INTEGER,data TEXT)`,
				`CREATE TABLE session_v2 (id TEXT PRIMARY KEY,parent_id TEXT,directory TEXT,title TEXT,time_created INTEGER,time_updated INTEGER)`,
				`CREATE TABLE session_message (id TEXT PRIMARY KEY,session_id TEXT,type TEXT,seq INTEGER,time_created INTEGER,time_updated INTEGER,data TEXT)`,
				`CREATE TABLE kv (key TEXT PRIMARY KEY,value TEXT)`,
			}
			for _, q := range queries {
				if _, err = db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().Add(-5 * time.Second).UnixMilli()
			table := "session"
			if v2 {
				table = "session_v2"
			}
			if _, err = db.Exec("INSERT INTO "+table+" VALUES ('s',NULL,'private','private',?,?)", now, now+5000); err != nil {
				t.Fatal(err)
			}
			if v2 {
				db.Exec(`INSERT INTO kv VALUES ('migration.v1-v2','{"phase":"completed"}')`)
			}
			tool := map[string]any{"id": "tool", "type": "tool", "name": "bash", "tool": "bash", "time": map[string]int64{"created": now + 2000, "ran": now + 2000, "completed": now + 3000}, "state": map[string]any{"status": "completed", "input": map[string]string{"command": "private"}, "output": "result", "time": map[string]int64{"start": now + 2000, "end": now + 3000}}}
			insert := func(id, role string, seq int, doc map[string]any) {
				b, _ := json.Marshal(doc)
				var err error
				if v2 {
					_, err = db.Exec(`INSERT INTO session_message VALUES (?,'s',?,?,?, ?,?)`, id, role, seq, now+int64(seq)*1000, now+5000, string(b))
				} else {
					_, err = db.Exec(`INSERT INTO message VALUES (?,'s',?,?,?)`, id, now+int64(seq)*1000, now+5000, string(b))
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			insert("u", "user", 0, map[string]any{"role": "user", "text": "question", "time": map[string]int64{"created": now}})
			a := map[string]any{"role": "assistant", "parentID": "u", "modelID": "model", "model": map[string]string{"id": "model", "providerID": "p"}, "providerID": "p", "time": map[string]int64{"created": now + 1000, "completed": now + 4000}, "tokens": map[string]any{"input": 100, "output": 10, "reasoning": 5, "cache": map[string]int{"read": 20, "write": 0}}}
			if v2 {
				a["content"] = []any{tool}
			}
			insert("a", "assistant", 1, a)
			if !v2 {
				b, _ := json.Marshal(tool)
				db.Exec(`INSERT INTO part VALUES ('tool','a','s',?,?)`, now+5000, string(b))
			}
			r := NewTraceReader(ms(now).Add(-time.Second))
			budget := int64(8 << 20)
			spans := r.pollOpenCode(false, &budget)
			roots, models, tools := spanKinds(spans)
			if len(roots) != 1 || len(models) != 1 || len(tools) != 1 {
				t.Fatalf("sqlite %+v", spans)
			}
			if tools[0].Parent != roots[0].ID || tools[0].Inferred || models[0].Tokens.Output != 15 || models[0].Provider != "p" {
				t.Fatalf("sqlite %+v", spans)
			}
			if len(r.pollOpenCode(false, &budget)) != 0 {
				t.Fatal("unchanged snapshot replayed")
			}
			if !v2 {
				db.Exec(`UPDATE part SET time_updated=?,data=json_set(data,'$.state.status','error','$.state.error','failed')`, now+6000)
				updated := r.pollOpenCode(false, &budget)
				// A terminal observation can be patched without recounting its metrics.
				for _, s := range updated {
					if s.Parent != "" && !s.Update {
						t.Fatal("terminal observation recounted")
					}
				}
			}
		})
	}
}

func TestGeminiTracePatchesRewindsAndPrivacy(t *testing.T) {
	for _, bodies := range []bool{false, true} {
		s := &geminiTraceState{messages: map[string]*geminiTraceMessage{}}
		lines := []string{
			`{"sessionId":"s","startTime":"2026-10-02T12:00:00Z"}`,
			`{"type":"user","id":"u","timestamp":"2026-10-02T12:00:00Z","content":[{"text":"private"}]}`,
			`{"type":"gemini","id":"a","timestamp":"2026-10-02T12:00:02Z","model":"gemini-model","content":"thinking","tokens":{"input":100,"output":10,"thoughts":5,"cached":40},"toolCalls":[{"id":"tool","name":"read_file","status":"success","timestamp":"2026-10-02T12:00:03Z","args":{"path":"private"}}]}`,
			`{"$patch":{"id":"a","toolCalls":[{"id":"tool","result":[{"functionResponse":{"id":"tool","response":{"result":"private"}}}]}]}}`,
			`{"type":"user","id":"fr","timestamp":"2026-10-02T12:00:03Z","content":[{"functionResponse":{"id":"tool","response":{"result":"private"}}}]}`,
			`{"type":"gemini","id":"final","timestamp":"2026-10-02T12:00:05Z","model":"gemini-model","content":"answer","tokens":{"input":120,"output":20,"cached":50}}`,
		}
		for _, line := range lines {
			s.line([]byte(line), bodies)
		}
		r := NewTraceReader(at("2026-10-02T11:59:59Z"))
		snap := r.snapshot("test")
		spans := r.observations(snap, s.spans(bodies, true))
		roots, models, tools := spanKinds(spans)
		if len(roots) != 1 || len(models) != 2 || len(tools) != 1 || models[0].Tokens.Input != 60 || models[0].Tokens.Output != 15 || tools[0].Parent != roots[0].ID {
			t.Fatalf("gemini %+v", spans)
		}
		for _, span := range spans {
			if !bodies && (span.Input != "" || span.Output != "") {
				t.Fatal("privacy leak")
			}
		}
		if bodies && !strings.Contains(tools[0].Output, "private") {
			t.Fatal("tool patch lost")
		}
		if len(r.observations(snap, s.spans(bodies, true))) != 0 {
			t.Fatal("snapshot duplicated")
		}
		s.line([]byte(`{"$rewindTo":"a"}`), bodies)
		if len(s.order) != 2 || s.messages["final"] != nil {
			t.Fatal("rewind ignored")
		}
	}
}

func TestGeminiReaderHistoryPartialAndSettledTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-test.jsonl")
	old := time.Now().Add(-time.Minute)
	now := time.Now().Add(-3 * time.Second)
	msg := func(id, typ string, at time.Time) string {
		b, _ := json.Marshal(map[string]any{"id": id, "type": typ, "timestamp": at, "content": "private", "model": "model", "tokens": map[string]int{"input": 10, "output": 2}})
		return string(b) + "\n"
	}
	initial := `{"sessionId":"s"}` + "\n" + msg("old", "user", old) + msg("old-a", "gemini", old.Add(time.Second)) + msg("new", "user", now)
	full := msg("new-a", "gemini", now.Add(time.Second))
	os.WriteFile(path, []byte(initial+full[:len(full)/2]), 0600)
	f := file{agent: "gemini", path: path}
	stat(&f)
	r := NewTraceReader(now.Add(-time.Second))
	r.files[path] = traceTestCursor("gemini")
	budget := int64(8 << 20)
	first := r.readGemini(f, false, &budget)
	for _, s := range first {
		if s.Turn != "new" {
			t.Fatal("history replayed")
		}
	}
	h, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	h.WriteString(full[len(full)/2:])
	h.Close()
	stat(&f)
	spans := r.readGemini(f, false, &budget)
	_, models, _ := spanKinds(spans)
	if len(models) != 0 {
		t.Fatal("unsettled final counted")
	}
	snap := r.snapshot(path)
	final := r.observations(snap, snap.gemini.spans(false, true))
	_, models, _ = spanKinds(final)
	if len(models) != 1 || models[0].Turn != "new" {
		t.Fatalf("final %+v", final)
	}
	if len(r.observations(snap, snap.gemini.spans(false, true))) != 0 {
		t.Fatal("settled replayed")
	}
}

func TestGeminiLegacySnapshotAndLaterPatch(t *testing.T) {
	s := &geminiTraceState{messages: map[string]*geminiTraceMessage{}}
	s.line([]byte(`{"sessionId":"s","messages":[{"id":"u","type":"user","timestamp":"2026-10-02T12:00:00Z","content":"question"},{"id":"a","type":"gemini","timestamp":"2026-10-02T12:00:02Z","model":"model","tokens":{"input":5,"output":1},"toolCalls":[{"id":"tool","name":"read","status":"success","timestamp":"2026-10-02T12:00:03Z"}]}]}`), true)
	r := NewTraceReader(at("2026-10-02T11:59:59Z"))
	snap := r.snapshot("legacy")
	first := r.observations(snap, s.spans(true, true))
	_, models, tools := spanKinds(first)
	if len(models) != 1 || len(tools) != 1 {
		t.Fatalf("legacy %+v", first)
	}
	s.line([]byte(`{"$patch":{"updates":[{"id":"a","toolCalls":[{"id":"tool","result":"late result"}]}]}}`), true)
	updated := r.observations(snap, s.spans(true, true))
	_, models, tools = spanKinds(updated)
	if len(models) != 0 || len(tools) != 1 || !tools[0].Update || tools[0].Output != `"late result"` {
		t.Fatalf("late patch %+v", updated)
	}
}

func TestOpenCodeLegacyJSONToolUpdates(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "opencode", "storage")
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("OPENCODE_DB", "")
	write := func(path string, value any) {
		t.Helper()
		os.MkdirAll(filepath.Dir(path), 0700)
		b, _ := json.Marshal(value)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(-time.Second).UnixMilli()
	write(filepath.Join(root, "session", "project", "s.json"), map[string]any{"id": "s", "time": map[string]int64{"created": now, "updated": now}})
	write(filepath.Join(root, "message", "s", "u.json"), map[string]any{"id": "u", "role": "user", "time": map[string]int64{"created": now}})
	write(filepath.Join(root, "message", "s", "a.json"), map[string]any{"id": "a", "role": "assistant", "parentID": "u", "modelID": "model", "time": map[string]int64{"created": now + 100, "completed": now + 500}, "tokens": map[string]int{"input": 10, "output": 2}})
	part := filepath.Join(root, "part", "a", "tool.json")
	tool := map[string]any{"id": "tool", "type": "tool", "tool": "bash", "state": map[string]any{"status": "running", "time": map[string]int64{"start": now + 200}}}
	write(part, tool)
	r := NewTraceReader(ms(now).Add(-time.Second))
	budget := int64(8 << 20)
	initial := r.pollOpenCode(true, &budget)
	_, models, tools := spanKinds(initial)
	if len(models) != 1 || len(tools) != 0 {
		t.Fatalf("legacy initial %+v", initial)
	}
	tool["state"] = map[string]any{"status": "completed", "output": "result", "time": map[string]int64{"start": now + 200, "end": now + 600}}
	write(part, tool)
	// Touch only the part. A message's metadata need not change with it.
	os.Chtimes(part, time.Now().Add(time.Second), time.Now().Add(time.Second))
	updated := r.pollOpenCode(true, &budget)
	_, models, tools = spanKinds(updated)
	if len(tools) != 1 || tools[0].Name != "bash" || tools[0].End.Sub(tools[0].Start) != 400*time.Millisecond {
		t.Fatalf("legacy update %+v", updated)
	}
	for _, m := range models {
		if !m.Update {
			t.Fatal("model usage recounted")
		}
	}
}

func TestClaudeTraceInterruptedToolDoesNotBlockNextInteraction(t *testing.T) {
	c := traceTestCursor("claude")
	clientTraceFeed(t, c, false,
		`{"type":"user","sessionId":"s","uuid":"u","timestamp":"2026-10-02T12:00:00Z","message":{"content":"question"}}`,
		`{"type":"assistant","timestamp":"2026-10-02T12:00:02Z","message":{"id":"m","model":"model","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool","name":"bash"}]}}`)
	interrupted := clientTraceFeed(t, c, false, `{"type":"user","uuid":"next","timestamp":"2026-10-02T12:00:05Z","message":{"content":"next"}}`)
	_, _, tools := spanKinds(interrupted)
	if len(tools) != 1 || !tools[0].Error || len(c.tools) != 0 {
		t.Fatalf("interrupted %+v", interrupted)
	}
	clientTraceFeed(t, c, false, `{"type":"assistant","timestamp":"2026-10-02T12:00:08Z","message":{"id":"m2","model":"model","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}}`)
	final := c.finishClaude(false)
	if len(final) != 2 || final[1].Turn != "next" || final[1].Error {
		t.Fatalf("next %+v", final)
	}
}

func TestTraceDiscoveryIncludesClaudeChildren(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	testenv.SetHome(t, home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "pi"))
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	t.Setenv("GEMINI_CLI_HOME", filepath.Join(home, "gemini-home"))
	files := map[string]string{
		"claude": filepath.Join(home, "claude", "projects", "p", "s.jsonl"),
		"omp":    filepath.Join(home, ".omp", "agent", "sessions", "p", "now_s.jsonl"),
		"gemini": filepath.Join(home, "gemini-home", ".gemini", "tmp", "p", "chats", "session-s.jsonl"),
	}
	for _, path := range files {
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte("{}\n"), 0600)
	}
	for _, path := range []string{filepath.Join(home, "claude", "projects", "p", "s", "subagents", "agent-a.jsonl"), filepath.Join(home, ".omp", "agent", "sessions", "p", "now_s", "agent-a.jsonl")} {
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte("{}\n"), 0600)
	}
	got := traceLineFiles()
	if len(got) != 4 {
		t.Fatalf("discovery %+v", got)
	}
	for _, f := range got {
		if f.agent == "claude" && !f.main {
			if sessionOfPath(f.path) != "s" {
				t.Fatalf("child identity %+v", f)
			}
			continue
		}
		if f.path != files[f.agent] {
			t.Fatalf("unexpected child discovered %+v", f)
		}
	}
}

func TestGeminiParallelToolResultsStayWithTheirCalls(t *testing.T) {
	raw := json.RawMessage(`[{"functionResponse":{"id":"one","name":"read","response":{"result":"first"}}},{"functionResponse":{"id":"two","name":"read","response":{"result":"second"}}}]`)
	got := geminiTraceResult(raw, "one", "read")
	if !strings.Contains(got, "first") || strings.Contains(got, "second") {
		t.Fatalf("parallel results mixed: %s", got)
	}
}

func TestClaudeTraceSubagentsShareSessionWithoutIDCollisions(t *testing.T) {
	for _, agent := range []string{"claude", "claude-desktop"} {
		t.Run(agent, func(t *testing.T) {
			testenv.SetHome(t, t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
			t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("GEMINI_CLI_HOME", t.TempDir())
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			t.Setenv("OPENCODE_DB", "")
			since := time.Now().Add(-10 * time.Second)
			root := filepath.Join(ClaudeDir(), "projects", "p")
			paths := []string{filepath.Join(root, "parent.jsonl"), filepath.Join(root, "parent", "subagents", "agent-a.jsonl"), filepath.Join(root, "parent", "subagents", "agent-b.jsonl")}
			for _, path := range paths {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				// Both children deliberately repeat the parent's user/message UUIDs.
				// No sessionId is required: child paths provide the parent session ID.
				var lines []string
				for _, event := range []map[string]any{
					{"type": "user", "uuid": "old", "entrypoint": agent, "timestamp": since.Add(-time.Minute), "message": map[string]any{"content": "PRIVATE-OLD"}},
					{"type": "user", "uuid": "same-user", "entrypoint": agent, "timestamp": since, "message": map[string]any{"content": "PRIVATE-NEW"}},
					{"type": "assistant", "timestamp": since.Add(time.Second), "message": map[string]any{"id": "same-message", "model": "claude-sonnet", "stop_reason": "end_turn", "content": []map[string]string{{"type": "text", "text": "PRIVATE-ANSWER"}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}},
					{"type": "system", "subtype": "turn_duration", "timestamp": since.Add(2 * time.Second)},
				} {
					// Real child transcripts end at assistant/end_turn, without turn_duration.
					if event["type"] == "system" && filepath.Base(filepath.Dir(path)) == "subagents" {
						continue
					}
					b, err := json.Marshal(event)
					if err != nil {
						t.Fatal(err)
					}
					lines = append(lines, string(b))
				}
				if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := NewTraceReader(since)
			spans := r.Poll(false)
			for _, path := range paths {
				settleClaudeTestFile(t, path)
			}
			r.Poll(false) // Observe the changed mtime before idle completion.
			spans = append(spans, r.Poll(false)...)
			_, models, _ := spanKinds(spans)
			if len(models) != 3 {
				t.Fatalf("child model coverage: %+v", spans)
			}
			ids := map[string]bool{}
			traces := map[string]bool{}
			for _, model := range models {
				if model.Agent != agent || model.Session != "parent" || model.Tokens.Input != 10 || model.Tokens.Output != 5 || model.Input != "" || model.Output != "" {
					t.Fatalf("child export: %+v", model)
				}
				if ids[model.ID] {
					t.Fatalf("copied UUID collision: %s", model.ID)
				}
				ids[model.ID] = true
				traces[TraceID(model.Agent, model.Session, model.Turn)] = true
			}
			if len(traces) != 3 {
				t.Fatal("child trace collision")
			}
			if next := r.Poll(true); len(next) != 0 {
				t.Fatalf("unchanged child replayed: %s", fmt.Sprint(next))
			}
		})
	}
}

func TestClaudeSubagentEndTurnAfterToolResult(t *testing.T) {
	for _, bodies := range []bool{false, true} {
		t.Run(fmt.Sprint(bodies), func(t *testing.T) {
			testenv.SetHome(t, t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
			t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("GEMINI_CLI_HOME", t.TempDir())
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			t.Setenv("OPENCODE_DB", "")
			path := filepath.Join(ClaudeDir(), "projects", "p", "parent", "subagents", "agent-a.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			since := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
			initial := `{"type":"user","uuid":"user","timestamp":"2026-10-03T00:00:00Z","message":{"content":"PRIVATE-question"}}
{"type":"assistant","timestamp":"2026-10-03T00:00:01Z","message":{"id":"m1","model":"claude-model","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool","name":"Read","input":{"path":"PRIVATE-path"}}],"usage":{"input_tokens":10,"output_tokens":3}}}
`
			if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
				t.Fatal(err)
			}
			r := NewTraceReader(since)
			spans := r.Poll(bodies)
			_, models, _ := spanKinds(spans)
			if len(models) != 0 {
				t.Fatal("tool-use model flushed before tool result")
			}
			final := `{"type":"user","timestamp":"2026-10-03T00:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"tool","content":"PRIVATE-tool"}]}}
{"type":"assistant","timestamp":"2026-10-03T00:00:03Z","message":{"id":"m2","model":"claude-model","stop_reason":"end_turn","content":[{"type":"text","text":"PRIVATE-answer"}],"usage":{"input_tokens":20,"output_tokens":7}}}
`
			h, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.WriteString(final)
			closeErr := h.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			spans = append(spans, r.Poll(bodies)...)
			settleClaudeTestFile(t, path)
			spans = append(spans, r.Poll(bodies)...)
			spans = append(spans, r.Poll(bodies)...)
			roots, models, tools := spanKinds(spans)
			if len(models) != 2 || len(tools) != 1 || len(roots) != 2 {
				t.Fatalf("terminal child coverage: %+v", spans)
			}
			if models[0].Tokens.Input != 10 || models[1].Tokens.Input != 20 || models[1].Tokens.Output != 7 {
				t.Fatalf("usage: %+v", models)
			}
			if roots[1].ID != roots[0].ID || !roots[1].End.Equal(since.Add(3*time.Second)) || models[1].Parent != roots[0].ID {
				t.Fatalf("unclosed child interaction: %+v", roots)
			}
			if bodies && !strings.Contains(roots[1].Output, "PRIVATE-answer") {
				t.Fatal("final answer missing")
			}
			for _, span := range spans {
				if !bodies && (span.Input != "" || span.Output != "") {
					t.Fatal("body consent bypassed")
				}
			}
			if next := r.Poll(bodies); len(next) != 0 {
				t.Fatalf("third poll duplicated final answer: %+v", next)
			}
			c := r.files[path]
			if c == nil || c.claudeMessage != nil || !c.turns[c.current].completed {
				t.Fatal("terminal child state left pending")
			}
		})
	}
}

func settleClaudeTestFile(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-3 * time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeSubagentRepeatedEndTurnBlocks(t *testing.T) {
	for _, bodies := range []bool{false, true} {
		for _, split := range []bool{false, true} {
			t.Run(fmt.Sprintf("bodies=%t/split=%t", bodies, split), func(t *testing.T) {
				testenv.SetHome(t, t.TempDir())
				t.Setenv("CODEX_HOME", t.TempDir())
				t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
				t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
				t.Setenv("XDG_DATA_HOME", t.TempDir())
				t.Setenv("GEMINI_CLI_HOME", t.TempDir())
				t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
				t.Setenv("OPENCODE_DB", "")
				path := filepath.Join(ClaudeDir(), "projects", "p", "parent", "subagents", "agent-a.jsonl")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				user := `{"type":"user","uuid":"user","timestamp":"2026-10-03T00:00:00Z","message":{"content":"PRIVATE-question"}}` + "\n"
				thinking := `{"type":"assistant","timestamp":"2026-10-03T00:00:01Z","message":{"id":"m","model":"claude-model","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"PRIVATE-thinking"}],"usage":{"input_tokens":20,"output_tokens":7}}}` + "\n"
				text := `{"type":"assistant","timestamp":"2026-10-03T00:00:02Z","message":{"id":"m","model":"claude-model","stop_reason":"end_turn","content":[{"type":"text","text":"PRIVATE-answer"}],"usage":{"input_tokens":20,"output_tokens":7}}}` + "\n"
				initial := user + thinking
				if !split {
					initial += text
				}
				if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
					t.Fatal(err)
				}
				r := NewTraceReader(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
				spans := r.Poll(bodies)
				if split {
					h, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					_, err = h.WriteString(text)
					closeErr := h.Close()
					if err != nil {
						t.Fatal(err)
					}
					if closeErr != nil {
						t.Fatal(closeErr)
					}
					spans = append(spans, r.Poll(bodies)...)
				}
				_, pending, _ := spanKinds(spans)
				if len(pending) != 0 {
					t.Fatal("end_turn flushed before content blocks settled")
				}
				settleClaudeTestFile(t, path)
				spans = append(spans, r.Poll(bodies)...)
				spans = append(spans, r.Poll(bodies)...)
				roots, models, _ := spanKinds(spans)
				if len(models) != 1 || len(roots) != 2 {
					t.Fatalf("duplicate terminal observations: %+v", spans)
				}
				if models[0].Tokens.Input != 20 || models[0].Tokens.Output != 7 {
					t.Fatalf("usage counted incorrectly: %+v", models)
				}
				if bodies && (!strings.Contains(models[0].Output, "PRIVATE-thinking") || !strings.Contains(models[0].Output, "PRIVATE-answer") || roots[1].Output != models[0].Output) {
					t.Fatal("split response content lost")
				}
				if !bodies {
					for _, span := range spans {
						if span.Input != "" || span.Output != "" {
							t.Fatal("body consent bypassed")
						}
					}
				}
				if roots[0].ID != roots[1].ID || !roots[1].End.Equal(time.Date(2026, 10, 3, 0, 0, 2, 0, time.UTC)) {
					t.Fatal("root not closed at final block")
				}
				if next := r.Poll(bodies); len(next) != 0 {
					t.Fatal("settled response replayed")
				}
			})
		}
	}
}

package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// putLiveAgent writes a K-brain session.jsonl under root's sessions, at
// rel (<project>/<id>), its lines as they are.
func putLiveAgent(t *testing.T, root, rel string, lines ...string) string {
	t.Helper()
	p := filepath.Join(root, "sessions", filepath.FromSlash(rel), "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// laIssueMeta is #6741's session_meta, word for word but for its
// placeholders: a session that ran on through magpie, its messages
// compacted away, so the totals are all there is.
const laIssueMeta = `{"type":"session_meta","payload":{"id":"67e678d0","title":"Port the reader","model":"glm-5.1","provider":"magpie","cwd":"/work/kb","usage_in":12786068,"usage_cached":12455808,"usage_out":103572,"model_usage":{"glm-5.1 @ magpie":{"prompt_tokens":12786068,"completion_tokens":103572,"prompt_tokens_details":{"cached_tokens":12455808}}},"created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-02T12:00:00Z","forked_from":"","fork_seq":0,"effort":""}}`

// laParser is a session that ran: a prompt, a reply that read a skill
// through SkillsManager, its result, a runtime notice and a reply to it.
// Its totals hold the replies, a compaction's call (prompt 1000,
// completion 100) and, in sub_usage, its subagent's.
var laParser = []string{
	`{"type":"session_meta","payload":{"id":"abc123","title":"Parser fix","model":"glm-5.1","provider":"magpie","cwd":"/work/parser","usage_in":3000,"usage_cached":1000,"usage_out":300,"model_usage":{"glm-5.1 @ magpie":{"prompt_tokens":3000,"completion_tokens":300,"prompt_tokens_details":{"cached_tokens":1000}}},"sub_usage":{"glm-5.1 @ magpie":{"prompt_tokens":500,"completion_tokens":50}},"created_at":"2026-11-01T08:59:00Z","updated_at":"2026-11-02T08:00:06Z","forked_from":"","fork_seq":0,"effort":"","version":3}}`,
	`{"type":"message","payload":{"seq":1,"message":{"id":"m1","role":"user","content":"Fix the parser","authored":true,"sent_at":"2026-11-01T09:00:00Z"}}}`,
	`{"type":"message","payload":{"seq":2,"message":{"id":"m2","role":"assistant","content":"Reading the skill.","reasoning":"Need the skill.","tool_calls":[{"id":"c1","type":"function","function":{"name":"SkillsManager","arguments":"{\"action\":\"read\",\"path\":\"skill://greet-user/SKILL.md\"}"}}],"sent_at":"2026-11-01T09:00:10Z","model":"glm-5.1 @ magpie","usage":{"prompt_tokens":1200,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":1000}}}}}`,
	`{"type":"message","payload":{"seq":3,"message":{"id":"m3","role":"tool","content":"# greet-user","tool_call_id":"c1","sent_at":"2026-11-01T09:00:11Z"}}}`,
	`{"type":"snapshot","payload":{"seq":3,"ref":"s1"}}`,
	`{"type":"message","payload":{"seq":4,"message":{"id":"m4","role":"user","content":[{"type":"text","text":"Task t1 finished"}],"origin":"background","sent_at":"2026-11-02T08:00:00Z"}}}`,
	`{"type":"message","payload":{"seq":5,"message":{"id":"m5","role":"assistant","content":"Done.","sent_at":"2026-11-02T08:00:05Z","model":"glm-5.1 @ magpie","usage":{"prompt_tokens":800,"completion_tokens":100}}}}`,
	`{"type":"compaction","payload":{"seq":5,"summary":"…"}}`,
}

// TestLiveAgentSessions lists LiveAgent's sessions as K-brain keeps them
// (#6741): each project's, an older one right under sessions, and not
// .runs or .deleted-history; a reply's usage on its own day, what else
// the totals hold on the last; a fork without its source's copies, and a
// subagent's session under its parent's, its spend there already.
func TestLiveAgentSessions(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	t.Setenv("LIVEAGENT_HOME", "")
	t.Setenv("K_BRAIN_HOME", "")
	root := filepath.Join(home, ".liveagent")
	if LiveAgentDir() != root {
		t.Fatalf("LiveAgentDir() = %q, want %q", LiveAgentDir(), root)
	}
	issue := putLiveAgent(t, root, "kb/67e678d0", laIssueMeta)
	putLiveAgent(t, root, "parser/abc123", laParser...)
	putLiveAgent(t, root, "parser/task-abc123-t1",
		`{"type":"session_meta","payload":{"id":"task-abc123-t1","title":"Check the tests","model":"glm-5.1","provider":"magpie","cwd":"/work/parser","usage_in":500,"usage_out":50,"model_usage":{"glm-5.1 @ magpie":{"prompt_tokens":500,"completion_tokens":50}},"created_at":"2026-11-01T09:05:00Z","updated_at":"2026-11-01T09:06:00Z","forked_from":"abc123","fork_seq":0,"task_id":"t1"}}`,
		`{"type":"message","payload":{"seq":1,"message":{"id":"t1","role":"user","content":"Check the tests","authored":true,"sent_at":"2026-11-01T09:05:00Z"}}}`,
		`{"type":"message","payload":{"seq":2,"message":{"id":"t2","role":"assistant","tool_calls":[{"id":"r1","type":"function","function":{"name":"Read","arguments":"{\"path\":\"parser_test.go\"}"}}],"sent_at":"2026-11-01T09:06:00Z","model":"glm-5.1 @ magpie","usage":{"prompt_tokens":500,"completion_tokens":50}}}}`,
	)
	// a fork of abc123 after its first reply: the copies' spend is its
	// source's, in its totals too
	putLiveAgent(t, root, "parser/fork1",
		`{"type":"session_meta","payload":{"id":"fork1","title":"Parser fix (fork)","model":"glm-5.1","provider":"magpie","cwd":"/work/parser","usage_in":1600,"usage_cached":1000,"usage_out":140,"model_usage":{"glm-5.1 @ magpie":{"prompt_tokens":1600,"completion_tokens":140,"prompt_tokens_details":{"cached_tokens":1000}}},"created_at":"2026-11-03T10:00:00Z","updated_at":"2026-11-03T10:00:05Z","forked_from":"abc123","fork_seq":2,"effort":""}}`,
		laParser[1], laParser[2],
		`{"type":"message","payload":{"seq":3,"message":{"id":"f3","role":"user","content":"Try another way","authored":true,"sent_at":"2026-11-03T10:00:00Z"}}}`,
		`{"type":"message","payload":{"seq":4,"message":{"id":"f4","role":"assistant","content":"OK.","sent_at":"2026-11-03T10:00:05Z","model":"glm-5.1 @ magpie","usage":{"prompt_tokens":400,"completion_tokens":40}}}}`,
	)
	putLiveAgent(t, root, "old1",
		`{"type":"session_meta","payload":{"id":"old1","title":"","model":"glm-5.1","provider":"magpie","cwd":"/work/old","created_at":"2026-10-20T10:00:00Z","updated_at":"2026-10-20T10:00:09Z"}}`,
		`{"type":"message","payload":{"seq":1,"message":{"id":"o1","role":"user","content":"An older one","authored":true,"sent_at":"2026-10-20T10:00:00Z"}}}`,
	)
	putLiveAgent(t, root, ".runs/r1", laIssueMeta)
	putLiveAgent(t, root, ".deleted-history/gone1", laIssueMeta)
	Reset()

	ss := List(0)
	if n := count(ss, "liveagent"); n != 4 {
		t.Fatalf("want 4 LiveAgent sessions (not .runs, .deleted-history or the subagent's), got %d", n)
	}

	a := find(t, ss, "liveagent", "67e678d0")
	if a.Title != "Port the reader" || a.Cwd != "/work/kb" || a.Resume != "" || a.Path != issue {
		t.Errorf("the issue's session: %+v", a)
	}
	if !a.Start.Equal(at("2026-11-01T10:00:00Z")) || !a.Last.Equal(at("2026-11-02T12:00:00Z")) {
		t.Errorf("the issue's session ran %v to %v", a.Start, a.Last)
	}
	if got := model(a, "glm-5.1").Tokens; got != (Tokens{Input: 12786068 - 12455808, Output: 103572, CacheRead: 12455808}) {
		t.Errorf("the issue's tokens = %+v", got)
	}
	if !a.Transcript {
		t.Errorf("the issue's session has no transcript")
	}

	b := find(t, ss, "liveagent", "abc123")
	if b.Title != "Parser fix" || b.Cwd != "/work/parser" || b.Resume != "" {
		t.Errorf("abc123: %+v", b)
	}
	if !b.Start.Equal(at("2026-11-01T08:59:00Z")) || !b.Last.Equal(at("2026-11-02T08:00:05Z")) {
		t.Errorf("abc123 ran %v to %v", b.Start, b.Last)
	}
	// replies 200+800 in, 100+100 out, 1000 read; the compaction's call
	// 1000/100 and the subagent's 500/50 on top
	if got := model(b, "glm-5.1").Tokens; got != (Tokens{Input: 2500, Output: 350, CacheRead: 1000}) {
		t.Errorf("abc123 tokens = %+v", got)
	}

	f := find(t, ss, "liveagent", "fork1")
	if f.Title != "Parser fix (fork)" || !f.Start.Equal(at("2026-11-03T10:00:00Z")) {
		t.Errorf("fork1: %+v", f)
	}
	if got := model(f, "glm-5.1").Tokens; got != (Tokens{Input: 400, Output: 40}) {
		t.Errorf("fork1 tokens = %+v, want only its own reply's", got)
	}

	o := find(t, ss, "liveagent", "old1")
	if o.Title != "An older one" || len(o.Models) != 0 {
		t.Errorf("old1: %+v", o)
	}

	// each reply on its day, the rest on the last message's
	st := statsAt(0, time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	var sum *Summary
	for i := range st.Sessions {
		if st.Sessions[i].Agent == "liveagent" && st.Sessions[i].ID == "abc123" {
			sum = &st.Sessions[i]
		}
	}
	if sum == nil {
		t.Fatal("abc123 not in the stats")
	}
	if sum.Prompts != 1 || sum.Replies != 2 {
		t.Errorf("abc123 prompts %d replies %d, want 1 and 2 (the notice isn't a prompt, the subagent's aren't its)", sum.Prompts, sum.Replies)
	}
	if sum.ToolCalls != 2 || sum.tools.of("SkillsManager") != 1 || sum.tools.of("Read") != 1 {
		t.Errorf("abc123 tools %d %v, want its SkillsManager and its subagent's Read", sum.ToolCalls, sum.tools)
	}
	if sum.skills.of("greet-user") != 1 || len(sum.skills) != 1 {
		t.Errorf("abc123 skills %v", sum.skills)
	}
	if len(sum.Days) != 2 {
		t.Errorf("abc123 days %v, want 11-01 and 11-02", sum.Days)
	}
	if sum.Tokens != (Tokens{Input: 2500, Output: 350, CacheRead: 1000}) {
		t.Errorf("abc123 stats tokens %+v", sum.Tokens)
	}
	day1 := statsAt(1, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	for _, x := range day1.Sessions {
		if x.Agent == "liveagent" && x.ID == "abc123" && x.Tokens != (Tokens{Input: 200, Output: 100, CacheRead: 1000}) {
			t.Errorf("abc123 on 11-01: %+v, want its first reply's", x.Tokens)
		}
	}
}

// TestLiveAgentTranscript reads a session as it went: the prompt, the
// reply's thinking, words and tool call, the tool's answer, LiveAgent's
// own notice as context.
func TestLiveAgentTranscript(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	t.Setenv("LIVEAGENT_HOME", "")
	t.Setenv("K_BRAIN_HOME", "")
	putLiveAgent(t, filepath.Join(home, ".liveagent"), "parser/abc123", laParser...)
	Reset()
	s := find(t, List(0), "liveagent", "abc123")
	tr, err := TranscriptOf(s)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range tr.Parts {
		got = append(got, p.Role+"/"+p.Kind+"/"+p.Name+": "+p.Text)
	}
	want := []string{
		"user/text/: Fix the parser",
		"assistant/thinking/: Need the skill.",
		"assistant/text/: Reading the skill.",
		"assistant/tool_use/SkillsManager: {\n  \"action\": \"read\",\n  \"path\": \"skill://greet-user/SKILL.md\"\n}",
		"tool/tool_result/SkillsManager: # greet-user",
		"user/context/: Task t1 finished",
		"assistant/text/: Done.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("transcript:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// LiveAgent's root is K-brain's: $LIVEAGENT_HOME, then $K_BRAIN_HOME
// (absolute only), then the folder ~/.liveagent/data-dir.json names.
func TestLiveAgentRoot(t *testing.T) {
	home := setupAgents(t)
	moved := filepath.Join(home, "elsewhere")
	t.Setenv("LIVEAGENT_HOME", "")
	t.Setenv("K_BRAIN_HOME", "")
	os.MkdirAll(filepath.Join(home, ".liveagent"), 0o755)
	if err := os.WriteFile(filepath.Join(home, ".liveagent", "data-dir.json"), []byte(`{"path":`+strconv.Quote(moved)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if LiveAgentDir() != moved {
		t.Errorf("data-dir.json: %q", LiveAgentDir())
	}
	putLiveAgent(t, moved, "kb/67e678d0", laIssueMeta)
	Reset()
	if s := find(t, List(0), "liveagent", "67e678d0"); s.Path != filepath.Join(moved, "sessions", "kb", "67e678d0", "session.jsonl") {
		t.Errorf("read from %q", s.Path)
	}
	kb := filepath.Join(home, "kb")
	t.Setenv("K_BRAIN_HOME", kb)
	if LiveAgentDir() != kb {
		t.Errorf("$K_BRAIN_HOME: %q", LiveAgentDir())
	}
	live := filepath.Join(home, "live")
	t.Setenv("LIVEAGENT_HOME", live)
	if LiveAgentDir() != live {
		t.Errorf("$LIVEAGENT_HOME: %q", LiveAgentDir())
	}
	t.Setenv("LIVEAGENT_HOME", "relative/dir")
	if LiveAgentDir() != kb {
		t.Errorf("a relative $LIVEAGENT_HOME is taken: %q", LiveAgentDir())
	}
}

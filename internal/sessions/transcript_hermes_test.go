package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// hermesRealSchema is the three tables magpie reads as Hermes Agent's
// state.db (schema_version 31) creates them, copied from a store Hermes
// itself made, so a reader written against a column it lacks shows here.
const hermesRealSchema = `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY, source TEXT NOT NULL, created_source TEXT, user_id TEXT,
    session_key TEXT, chat_id TEXT, chat_type TEXT, thread_id TEXT, display_name TEXT,
    origin_json TEXT, expiry_finalized INTEGER DEFAULT 0, model TEXT, model_config TEXT,
    system_prompt TEXT, system_prompt_hash TEXT, parent_session_id TEXT,
    started_at REAL NOT NULL, ended_at REAL, end_reason TEXT,
    message_count INTEGER DEFAULT 0, tool_call_count INTEGER DEFAULT 0,
    input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0,
    cache_read_tokens INTEGER DEFAULT 0, cache_write_tokens INTEGER DEFAULT 0,
    reasoning_tokens INTEGER DEFAULT 0, cwd TEXT, git_branch TEXT, git_repo_root TEXT,
    git_metadata_generation INTEGER NOT NULL DEFAULT 0, billing_provider TEXT,
    billing_base_url TEXT, billing_mode TEXT, estimated_cost_usd REAL, actual_cost_usd REAL,
    cost_status TEXT, cost_source TEXT, pricing_version TEXT, title TEXT, title_source TEXT,
    last_activity_at REAL, last_activity_description TEXT, last_activity_provenance TEXT,
    api_call_count INTEGER DEFAULT 0, handoff_state TEXT, handoff_platform TEXT,
    handoff_error TEXT, compression_failure_cooldown_until REAL,
    compression_failure_error TEXT, compression_fallback_streak INTEGER NOT NULL DEFAULT 0,
    compression_ineffective_count INTEGER NOT NULL DEFAULT 0,
    compression_recovery_deadline REAL, compression_overload_streak INTEGER NOT NULL DEFAULT 0,
    profile_name TEXT, transport_profile TEXT, rewind_count INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0, auto_archived INTEGER NOT NULL DEFAULT 0,
    pinned INTEGER NOT NULL DEFAULT 0, hidden INTEGER NOT NULL DEFAULT 0,
    last_read_at REAL, tool_names TEXT,
    FOREIGN KEY (parent_session_id) REFERENCES sessions(id),
    FOREIGN KEY (system_prompt_hash) REFERENCES system_prompts(hash)
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL, content TEXT, tool_call_id TEXT, tool_calls TEXT, tool_name TEXT,
    effect_disposition TEXT, timestamp REAL NOT NULL, token_count INTEGER,
    finish_reason TEXT, reasoning TEXT, reasoning_content TEXT, reasoning_details TEXT,
    codex_reasoning_items TEXT, codex_message_items TEXT, platform_message_id TEXT,
    observed INTEGER DEFAULT 0, _compressed_summary INTEGER NOT NULL DEFAULT 0,
    active INTEGER NOT NULL DEFAULT 1, compacted INTEGER NOT NULL DEFAULT 0,
    api_content TEXT, display_kind TEXT, display_metadata TEXT, display_identity BLOB,
    display_order INTEGER, message_uid TEXT, absorbed_message_uids TEXT,
    tool_call_uids TEXT, tool_call_uid TEXT
);
CREATE TABLE session_model_usage (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    model TEXT NOT NULL, billing_provider TEXT NOT NULL DEFAULT '',
    billing_base_url TEXT NOT NULL DEFAULT '', billing_mode TEXT NOT NULL DEFAULT '',
    task TEXT NOT NULL DEFAULT '', api_call_count INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0, estimated_cost_usd REAL NOT NULL DEFAULT 0,
    actual_cost_usd REAL NOT NULL DEFAULT 0, cost_status TEXT, cost_source TEXT,
    first_seen REAL, last_seen REAL,
    PRIMARY KEY (session_id, model, billing_provider, billing_base_url, billing_mode, task)
);`

// hermesRow is one messages row as Hermes's append_message and
// archive_and_compact write it.
type hermesRow struct {
	role, content, toolCallID, toolCalls, toolName, reasoning, kind, meta string
	active, compacted                                                     int
}

// HermesStore writes a profile's state.db (root, or root/profiles/<profile>)
// holding one session, sid, of rows.
func hermesStore(t *testing.T, root, profile, sid string, at time.Time, rows []hermesRow) {
	t.Helper()
	dir := root
	if profile != "" {
		dir = filepath.Join(root, "profiles", profile)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(hermesRealSchema); err != nil {
		t.Fatal(err)
	}
	base := float64(at.Unix())
	if _, err := db.Exec(`INSERT INTO sessions (id, source, model, started_at, last_activity_at, title, cwd, profile_name)
		VALUES (?, 'cli', 'gpt-5.5', ?, ?, 'Go tests', '/work/app', ?)`, sid, base, base+float64(len(rows)), profile); err != nil {
		t.Fatal(err)
	}
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	for i, r := range rows {
		if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_call_id, tool_calls, tool_name,
			timestamp, reasoning, active, compacted, display_kind, display_metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sid, r.role, null(r.content), null(r.toolCallID), null(r.toolCalls), null(r.toolName),
			base+float64(i+1), null(r.reasoning), r.active, r.compacted, null(r.kind), null(r.meta)); err != nil {
			t.Fatal(err)
		}
	}
}

// A Hermes session's conversation is read from its rows: Hermes's encoded
// parts, an assistant's reasoning and tool calls (OpenAI's, arguments a
// JSON string), a tool's answer by its tool_name, a compaction's archived
// turns once and its handoff as context. What Hermes never shows (a hidden
// row, model-only scaffolding) is left out. Before this, Hermes had no
// transcript at all, so memory learning passed every Hermes session over
// (CFK on Discord, 11-26).
func TestHermesTranscript(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	at := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	summary := hermesCurrentSummary + "\nThe user asked how they like Go tests run.\n" + hermesHandoffEnd
	hermesStore(t, root, "", "20261126_101500_ab12cd", at, []hermesRow{
		{role: "user", content: "[System: the session resumed]", kind: "hidden", active: 1},
		{role: "user", content: "\x00json:" + `[{"type": "text", "text": "How do I like Go tests run?"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}]`, compacted: 1},
		{role: "assistant", reasoning: "Check the Makefile first.", toolCalls: `[{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\": \"Makefile\"}"}}]`, compacted: 1},
		{role: "tool", content: "test:\n\tgo test -race ./...", toolCallID: "call_1", toolName: "read_file", compacted: 1},
		{role: "assistant", content: "You run them with -race.", compacted: 1},
		{role: "user", content: "Remember the plan", meta: `{"model_only": true}`, active: 1},
		{role: "user", content: summary + "\n\nAlways add -count=1 too.", active: 1},
		{role: "assistant", content: "Noted: -race and -count=1.", active: 1},
	})
	hermesStore(t, root, "work", "20261126_111500_ef34ab", at, []hermesRow{
		{role: "user", content: "Use pnpm, never npm.", active: 1},
		{role: "assistant", content: "Got it.", active: 1},
	})

	var main, work Session
	for _, s := range List(0) {
		if s.Agent != "hermes" {
			continue
		}
		if !s.Transcript {
			t.Errorf("a Hermes session has no transcript: %+v", s)
		}
		if strings.Contains(s.Path, filepath.Join("profiles", "work", "state.db")) {
			work = s
		} else {
			main = s
		}
	}
	if main.Path == "" || work.Path == "" {
		t.Fatalf("want the default and the work profile's sessions, got %+v and %+v", main, work)
	}

	got, err := TranscriptOf(main)
	if err != nil {
		t.Fatal(err)
	}
	want := []Part{
		{Role: "user", Kind: "text", Text: "How do I like Go tests run?"},
		{Role: "user", Kind: "image", Text: "[image]"},
		{Role: "assistant", Kind: "thinking", Text: "Check the Makefile first."},
		{Role: "assistant", Kind: "tool_use", Name: "read_file", Text: "{\n  \"path\": \"Makefile\"\n}"},
		{Role: "tool", Kind: "tool_result", Name: "read_file", Text: "test:\n\tgo test -race ./..."},
		{Role: "assistant", Kind: "text", Text: "You run them with -race."},
		{Role: "user", Kind: "context", Text: summary},
		{Role: "user", Kind: "text", Text: "Always add -count=1 too."},
		{Role: "assistant", Kind: "text", Text: "Noted: -race and -count=1."},
	}
	if !reflect.DeepEqual(got.Parts, want) {
		t.Errorf("default profile's transcript:\n got %#v\nwant %#v", got.Parts, want)
	}

	got, err = TranscriptOf(work)
	if err != nil {
		t.Fatal(err)
	}
	want = []Part{
		{Role: "user", Kind: "text", Text: "Use pnpm, never npm."},
		{Role: "assistant", Kind: "text", Text: "Got it."},
	}
	if !reflect.DeepEqual(got.Parts, want) {
		t.Errorf("work profile's transcript:\n got %#v\nwant %#v", got.Parts, want)
	}
}

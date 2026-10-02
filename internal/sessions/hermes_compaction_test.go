package sessions

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHermesCompactionLogicalMessages(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "compaction", false)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`ALTER TABLE messages ADD COLUMN active INTEGER DEFAULT 1;
	 ALTER TABLE messages ADD COLUMN compacted INTEGER DEFAULT 0;
	 ALTER TABLE messages ADD COLUMN tool_call_id TEXT;
	 ALTER TABLE messages ADD COLUMN tool_name TEXT;
	 DELETE FROM messages;
	 INSERT INTO messages (id, session_id, role, content, tool_calls, timestamp, active, compacted, tool_call_id, tool_name) VALUES
	 (1, 'compaction', 'user', 'archived only', NULL, 100, 0, 1, NULL, NULL),
	 (2, 'compaction', 'user', 'repeat', NULL, 200, 0, 1, NULL, NULL),
	 (3, 'compaction', 'assistant', 'answer', '[]', 300, 0, 1, NULL, NULL),
	 (4, 'compaction', 'user', 'repeat', NULL, 400, 1, 0, NULL, NULL),
	 (5, 'compaction', 'tool', 'same result', NULL, 500, 0, 1, 'call-a', 'read'),
	 (6, 'compaction', 'tool', 'same result', NULL, 500, 1, 0, 'call-b', 'read'),
	 (7, 'compaction', 'tool', 'same result', NULL, 500, 1, 0, 'call-a', 'write'),
	 (8, 'compaction', 'assistant', 'answer', '[{"function":{"name":"read","arguments":{}}}]', 300, 1, 0, NULL, NULL),
	 (9, 'compaction', 'user', 'repeat', NULL, 200, 0, 1, NULL, NULL),
	 (10, 'compaction', 'assistant', 'answer', '[]', 300, 1, 0, NULL, NULL),
	 (11, 'compaction', 'user', 'repeat', NULL, 200, 1, 0, NULL, NULL),
	 (12, 'compaction', 'tool', 'same result', NULL, 500, 1, 0, 'call-a', 'read'),
	 (13, 'compaction', 'user', 'discarded', NULL, 600, 0, 0, NULL, NULL),
	 (14, 'compaction', 'user', 'older timestamp later turn', NULL, 50, 1, 0, NULL, NULL),
	 (15, 'compaction', 'user', 'repeat', NULL, 200, 0, 1, NULL, NULL),
	 (16, 'compaction', 'assistant', 'answer', '[]', 300, 1, 0, NULL, NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	var ids []int64
	if err := newHermesDB(db).eachMessage(db, "compaction", func(m hermesMessage) {
		texts = append(texts, m.content)
		ids = append(ids, m.id)
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"archived only", "repeat", "answer", "repeat", "same result", "same result", "same result", "answer", "older timestamp later turn"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("logical transcript = %q, want %q", texts, want)
	}
	if wantIDs := []int64{1, 11, 16, 4, 12, 6, 7, 8, 14}; !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("active/newest representatives in first-row order = %v, want %v", ids, wantIDs)
	}
	s := hermesSessionByID(t, hermesID(path, "compaction"))
	assertHermesMessageStats(t, s.ID, 4, 2, 1)
}

func TestHermesModelOnlyMessages(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "model-only", false)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`ALTER TABLE messages ADD COLUMN display_metadata TEXT;
	 UPDATE messages SET display_metadata='{"model_only":true}' WHERE id IN (1, 2);
	 UPDATE messages SET display_metadata='{invalid' WHERE id=3;
	 UPDATE messages SET display_metadata='{"model_only":false}' WHERE id=4;`)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	if err := newHermesDB(db).eachMessage(db, "model-only", func(m hermesMessage) {
		texts = append(texts, m.content)
	}); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 {
		t.Fatalf("model-only rows entered transcript: %q", texts)
	}
	s := hermesSessionByID(t, hermesID(path, "model-only"))
	assertHermesMessageStats(t, s.ID, 0, 1, 0)
}

func assertHermesMessageStats(t *testing.T, sid string, prompts, replies, tools int) {
	t.Helper()
	for _, summary := range StatsFor(0).Sessions {
		if summary.Agent == "hermes" && summary.ID == sid {
			if summary.Prompts != prompts || summary.Replies != replies || summary.ToolCalls != tools {
				t.Fatalf("logical message statistics: %+v", summary)
			}
			return
		}
	}
	t.Fatal("missing Hermes summary")
}

func TestHermesCompactionUserHandoffIdentity(t *testing.T) {
	root := t.TempDir()
	path := hermesFixture(t, root, "", "handoff", false)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`ALTER TABLE messages ADD COLUMN active INTEGER DEFAULT 1;
	 ALTER TABLE messages ADD COLUMN compacted INTEGER DEFAULT 0;
	 ALTER TABLE messages ADD COLUMN display_kind TEXT;
	 DELETE FROM messages;`)
	if err != nil {
		t.Fatal(err)
	}
	const boundary = "--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---"
	carrier := "[CONTEXT SUMMARY]: summary\n" + boundary + "\ncurrent ask"
	standalone := "[CONTEXT SUMMARY]: summary\n" + boundary
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, content, timestamp, active, compacted, display_kind) VALUES
	 (1, 'handoff', 'user', 'current ask', 100, 0, 1, NULL),
	 (2, 'handoff', 'user', 'later turn', 200, 1, 0, NULL),
	 (3, 'handoff', 'user', ?, 100, 1, 0, 'hidden'),
	 (4, 'handoff', 'user', ?, 100, 0, 1, 'hidden'),
	 (5, 'handoff', 'user', ?, 300, 1, 0, 'hidden')`, carrier, carrier, standalone)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if err := newHermesDB(db).eachMessage(db, "handoff", func(m hermesMessage) {
		ids = append(ids, m.id)
	}); err != nil {
		t.Fatal(err)
	}
	if want := []int64{3, 2, 5}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("handoff representatives = %v, want %v", ids, want)
	}
}

func TestHermesCompactionEncodedHandoffIdentity(t *testing.T) {
	path := hermesFixture(t, t.TempDir(), "", "encoded-handoff", false)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`ALTER TABLE messages ADD COLUMN active INTEGER DEFAULT 1;
	 ALTER TABLE messages ADD COLUMN compacted INTEGER DEFAULT 0;
	 DELETE FROM messages;`)
	if err != nil {
		t.Fatal(err)
	}
	// Native _encode_content uses Python json.dumps defaults: separators,
	// field order and escaped Unicode all participate in display identity.
	plain := "\x00json:" + `[{"type": "text", "text": "\u5f53\u524d\u95ee\u9898"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,AA=="}}]`
	carrier := "\x00json:" + `[{"type": "text", "text": "[PRIOR CONTEXT \u2014 for reference only; not a new message]\n\u5f53\u524d\u95ee\u9898"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,AA=="}}, {"type": "text", "text": "\n[END OF PRIOR CONTEXT \u2014 COMPACTION SUMMARY BELOW]\n[CONTEXT SUMMARY]: summary\n--- END OF CONTEXT SUMMARY \u2014 respond to the message below, not the summary above ---"}]`
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, content, timestamp, active, compacted) VALUES
	 (1, 'encoded-handoff', 'user', ?, 100, 0, 1),
	 (2, 'encoded-handoff', 'user', ?, 100, 1, 0)`, plain, carrier)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if err := newHermesDB(db).eachMessage(db, "encoded-handoff", func(m hermesMessage) {
		ids = append(ids, m.id)
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []int64{2}) {
		t.Fatalf("encoded handoff representatives = %v, want [2]", ids)
	}
}

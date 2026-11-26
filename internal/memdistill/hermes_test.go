package memdistill

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// A Hermes Agent conversation, rows in a profile's state.db, is read for
// memory like any other agent's (CFK on Discord: "从对话中学习读不到hermes
// 的对话"). Before, Hermes had no transcript, so due passed every one of
// its sessions over, in every profile.
func TestHermesSessionsRead(t *testing.T) {
	sb := newSandbox(t)
	list, read = listed, transcriptOf
	sb.at = time.Now()
	on(t, "")
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	dir := filepath.Join(root, "profiles", "coder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := float64(sb.at.Add(-2 * time.Hour).Unix())
	for _, q := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, model TEXT, started_at REAL NOT NULL,
			ended_at REAL, last_activity_at REAL, title TEXT, cwd TEXT)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL,
			content TEXT, tool_call_id TEXT, tool_calls TEXT, tool_name TEXT, timestamp REAL NOT NULL, reasoning TEXT,
			active INTEGER NOT NULL DEFAULT 1, compacted INTEGER NOT NULL DEFAULT 0, display_kind TEXT, display_metadata TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions VALUES ('20261126_130300_c0ffee', 'cli', 'm', ?, NULL, ?, 'Releases', ?)`, at, at+4, filepath.Join(sb.dir, "proj")); err != nil {
		t.Fatal(err)
	}
	for i, m := range [][2]string{
		{"user", "\x00json:" + `[{"type": "text", "text": "hermes: we release with make ship"}]`},
		{"assistant", "Done."},
		{"user", "never use npm here, pnpm only"},
		{"assistant", "Noted."},
	} {
		if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, timestamp) VALUES ('20261126_130300_c0ffee', ?, ?, ?)`, m[0], m[1], at+float64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Run(context.Background(), "auto/m", sb.complete)
	if err != nil || res.Read != 1 || len(sb.asked) != 1 || sb.asked[0] != "auto/m User: hermes: we release with make ship" {
		t.Fatalf("%+v %v %q", res, err, sb.asked)
	}
}

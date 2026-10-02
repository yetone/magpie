package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	_ "modernc.org/sqlite"
)

const hermesSchema = `
CREATE TABLE sessions (
 id TEXT PRIMARY KEY, title TEXT, cwd TEXT, model TEXT,
 started_at REAL, ended_at REAL, last_activity_at REAL,
 input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER,
 cache_write_tokens INTEGER, reasoning_tokens INTEGER, api_call_count INTEGER
);
CREATE TABLE messages (
 id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT,
 tool_calls TEXT, timestamp REAL
);
CREATE TABLE session_model_usage (
 session_id TEXT, model TEXT, billing_provider TEXT,
 input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER,
 cache_write_tokens INTEGER, reasoning_tokens INTEGER, api_call_count INTEGER,
 last_seen REAL
);`

func hermesFixture(t *testing.T, root, profile, sid string, withUsage bool) string {
	t.Helper()
	dir := root
	if profile != "" {
		dir = filepath.Join(root, "profiles", profile)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(hermesSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC).Unix()
	_, err = db.Exec(`INSERT INTO sessions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sid, "A Hermes task", "/work/hermes", "fallback-model", float64(base), nil, float64(base+12), 90, 30, 40, 10, 5, 2)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO messages VALUES
	 (1, ?, 'user', ?, '[]', ?),
	 (2, ?, 'assistant', ?, ?, ?),
	 (3, ?, 'tool', ?, '[]', ?),
	 (4, ?, 'assistant', ?, '[]', ?)`,
		sid, "\x00json:"+`[{"type":"text","text":"Please inspect this"}]`, float64(base+1),
		sid, `[{"type":"text","text":"I will inspect it"}]`, `[{"function":{"name":"read_file","arguments":{"path":"a.txt"}}}]`, float64(base+4),
		sid, `[{"type":"text","text":"file contents"}]`, float64(base+7),
		sid, `[{"type":"text","text":"Done"}]`, float64(base+12))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if withUsage {
		_, err = db.Exec(`INSERT INTO session_model_usage VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, sid, "actual-model", "router", 100, 50, 25, 15, 8, 3, float64(base+12))
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func hermesSessionByID(t *testing.T, sid string) Session {
	t.Helper()
	for _, s := range List(0) {
		if s.Agent == "hermes" && s.ID == sid {
			return s
		}
	}
	t.Fatalf("Hermes session %q not found in %+v", sid, List(0))
	return Session{}
}

func TestHermesListStatsCallsAndContent(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "same-id", true)
	profilePath := hermesFixture(t, root, "work", "same-id", true)
	_ = path
	_ = profilePath

	listed := List(0)
	var hs []Session
	for _, s := range listed {
		if s.Agent == "hermes" {
			hs = append(hs, s)
		}
	}
	if len(hs) != 2 {
		t.Fatalf("want default and profile sessions, got %+v", hs)
	}
	if hs[0].ID == hs[1].ID {
		t.Fatalf("duplicate database IDs were not namespaced: %q", hs[0].ID)
	}
	for _, s := range hs {
		if !s.ReadOnly || s.Resume != "" || s.Title != "A Hermes task" || s.Cwd != "/work/hermes" {
			t.Errorf("metadata/read-only state: %+v", s)
		}
		if m := model(s, "actual-model"); m.Tokens != (Tokens{100, 50, 25, 15}) {
			t.Errorf("usage aggregate: %+v", m)
		}
		if model(s, "fallback-model").Model != "" {
			t.Errorf("session fallback was added to authoritative usage rows: %+v", s.Models)
		}
	}
	for _, managed := range ListAgent("hermes") {
		if managed.Deletable || managed.Resume != "" {
			t.Errorf("Hermes must not expose mutation or resume: %+v", managed)
		}
	}

	// Each DB has the same session id and usage; both are included exactly once.
	day := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	stats := StatsAt(0, day)
	var total Tokens
	for _, d := range stats.Days {
		for _, u := range d.Usage {
			if u.Agent == "hermes" {
				total.add(u.Tokens)
			}
		}
	}
	if total != (Tokens{200, 100, 50, 30}) {
		t.Errorf("Hermes stats total = %+v", total)
	}

	// hermesCalls is intentionally absent: Hermes usage is session-level
	// and has no production consumer in callsFor or FindCall.
	calls := Calls(time.Time{})
	for _, c := range calls {
		if c.Agent == "hermes" {
			t.Errorf("Hermes should not appear in Calls: %+v", c)
		}
	}
}

func TestHermesUsageFallbackAndLiveRefresh(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "fallback", false)

	first := hermesSessionByID(t, hermesID(path, "fallback"))
	if got := model(first, "fallback-model").Tokens; got != (Tokens{90, 30, 40, 10}) {
		t.Fatalf("session aggregate fallback = %+v", got)
	}

	// Repeated discovery must see SQLite WAL changes to aggregate usage and
	// the title even when the main database file's mtime has not moved, without
	// resetting Magpie's cache.
	oldInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_model_usage VALUES ('fallback', 'new-model', NULL, 11, 7, 3, 2, 1, 1, ?)`, float64(time.Date(2026, 9, 28, 10, 1, 0, 0, time.UTC).Unix())); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET title = 'Updated title' WHERE id = 'fallback'`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := os.Chtimes(path, oldInfo.ModTime(), oldInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	updated := hermesSessionByID(t, hermesID(path, "fallback"))
	if updated.Title != "Updated title" {
		t.Errorf("title not refreshed: %q", updated.Title)
	}
	if got := model(updated, "new-model").Tokens; got != (Tokens{11, 7, 3, 2}) {
		t.Errorf("WAL usage not refreshed: %+v", got)
	}
	// The bucket timestamp intentionally stays fixed. Newly observed usage
	// must still be visible on the next scan, and not be treated as a second
	// synthetic request or merged with the session-level fallback.
	if _, err := db.Exec(`UPDATE session_model_usage SET input_tokens=13, output_tokens=9, cache_read_tokens=4, cache_write_tokens=3 WHERE session_id='fallback'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET title='Changed again' WHERE id='fallback'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	updated = hermesSessionByID(t, hermesID(path, "fallback"))
	if updated.Title != "Changed again" || model(updated, "new-model").Tokens != (Tokens{13, 9, 4, 3}) {
		t.Errorf("same-mtime usage/title refresh: %+v", updated)
	}
}

func TestHermesOlderSchemaAndNullFields(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	dir := filepath.Join(root, "profiles", "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (id TEXT, title TEXT, cwd TEXT, model TEXT, started_at REAL, ended_at REAL, last_activity_at REAL);
	 INSERT INTO sessions VALUES ('old', NULL, NULL, NULL, NULL, NULL, NULL);
	 CREATE TABLE messages (session_id TEXT, role TEXT, content TEXT, tool_calls TEXT, timestamp REAL);
	 INSERT INTO messages VALUES ('old', 'user', 'hello', '[]', NULL);`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	// Older schemas can omit usage columns. Nullable metadata and times must
	// coalesce safely while the available message content remains readable.
	defer closeDBs()
	found := false
	for _, f := range hermesFiles() {
		if f.sid == "old" {
			found = true
			st := parseHermes(f)
			if st == nil || st.Title != "hello" || !st.Start.IsZero() {
				t.Errorf("old schema parse: %+v", st)
			}
		}
	}
	if !found {
		t.Fatal("old profile session not discovered")
	}
}

func TestHermesStatsIncludesRecentUsageForOldTranscript(t *testing.T) {
	setup(t)
	inZone(t, 0)
	// The background cache writer reads time.Local; drain it before inZone
	// restores the zone. Cleanups run LIFO, so this runs before inZone's.
	t.Cleanup(func() {
		saving.Lock()
		for saving.running {
			saving.Wait()
		}
		saving.Unlock()
	})
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "old-transcript", true)
	now := time.Date(2026, 10, 2, 18, 0, 0, 0, time.UTC)
	old := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC).Unix()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET started_at=?, ended_at=?, last_activity_at=? WHERE id='old-transcript'`, float64(old), float64(old), float64(old)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messages SET timestamp=? WHERE session_id='old-transcript'`, float64(old)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE session_model_usage SET last_seen=?, input_tokens=17 WHERE session_id='old-transcript'`, float64(now.Unix())); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	stats := StatsAt(1, now)
	if len(stats.Days) != 1 || stats.Days[0].Date != "2026-10-02" {
		t.Fatalf("today's stats should include recent auxiliary usage for an old transcript: %+v", stats)
	}
	found := false
	for _, u := range stats.Days[0].Usage {
		if u.Agent == "hermes" && u.Model == "actual-model" && u.Input == 17 {
			found = true
		}
	}
	if !found {
		t.Errorf("recent Hermes usage missing from today's stats: %+v", stats.Days[0].Usage)
	}
}

func TestHermesOpensStateDatabaseReadOnly(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "read-only", true)

	db, err := provider.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE sessions SET title='should not persist' WHERE id='read-only'`); err == nil {
		t.Fatal("Hermes database accepted a write through read-only connection")
	}
	var title string
	if err := db.QueryRow(`SELECT title FROM sessions WHERE id='read-only'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "A Hermes task" {
		t.Fatalf("read-only probe changed fixture title to %q", title)
	}
}

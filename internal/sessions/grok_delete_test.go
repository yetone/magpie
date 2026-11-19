package sessions

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	// grokSub is the subagent the fixture's main session ran: a session of
	// its own, in a folder of its own, which grokFiles folds into its
	// parent's. grokFork is a fork of the same session, a session of its own.
	grokSub = "0190aaaa-0000-7000-8000-00000000000b"
	// grokWord names each session's text in the index, so a search for it
	// tells whose text is still there.
	grokWordMain = "narwhal"
	grokWordSub  = "pangolin"
	grokWordFork = "axolotl"
)

// grokSearchMake makes Grok Build's search index at path, with the schema a
// real one has (session_search.sqlite, session_search_schema_version 4):
// session_docs, a row a session, over session_docs_fts, an FTS5 index of it
// that the table's own triggers keep in step. Each doc's text carries its
// own word, so a search can tell whose text is still in the index.
func grokSearchMake(t *testing.T, path string, docs [][2]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteURL(path, "_pragma=journal_mode(WAL)"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE session_docs (
			session_id TEXT PRIMARY KEY,
			cwd TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			content_hash TEXT NOT NULL
		, last_indexed_offset INTEGER NOT NULL DEFAULT 0)`,
		`CREATE VIRTUAL TABLE session_docs_fts USING fts5(
			title,
			content,
			content='session_docs',
			content_rowid='rowid'
		)`,
		`CREATE TRIGGER session_docs_ai AFTER INSERT ON session_docs BEGIN
			INSERT INTO session_docs_fts(rowid, title, content)
			VALUES (new.rowid, new.title, new.content);
		END`,
		`CREATE TRIGGER session_docs_ad AFTER DELETE ON session_docs BEGIN
			INSERT INTO session_docs_fts(session_docs_fts, rowid, title, content)
			VALUES ('delete', old.rowid, old.title, old.content);
		END`,
		`INSERT INTO meta (key, value) VALUES ('session_search_schema_version', '4'), ('last_bootstrap_at', '1795097116')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, d := range docs {
		if _, err := db.Exec(`INSERT INTO session_docs (session_id, cwd, updated_at, title, content, content_hash)
			VALUES (?, '/work', 1793938408, 'a talk', ?, 'hash')`, d[0], "we talked of the "+d[1]); err != nil {
			t.Fatal(err)
		}
	}
}

// grokSearchCount is what the index at path holds of a session: its rows in
// session_docs, and how many rows its own FTS index finds for word.
func grokSearchCount(t *testing.T, path, id, word string) (rows, found int) {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteURL(path, "mode=ro&_pragma=busy_timeout(2000)"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT count(*) FROM session_docs WHERE session_id = ?`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM session_docs_fts WHERE session_docs_fts MATCH ?`, word).Scan(&found); err != nil {
		t.Fatal(err)
	}
	return rows, found
}

// putGrok is setup with Grok Build's fixture sessions where a real ~/.grok
// keeps them, in the temporary home, and $GROK_HOME pointing there too:
// nothing of the user's own ~/.grok is read or written.
func putGrok(t *testing.T) (home, dir string) {
	t.Helper()
	claude, _ := setup(t)
	home = filepath.Join(filepath.Dir(claude), "home")
	dir = filepath.Join(home, ".grok")
	copyTree(t, "testdata/grok", dir)
	t.Setenv("GROK_HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	return home, dir
}

// grokCwd is the folder the fixture's sessions ran in, named as Grok Build
// names it.
func grokCwd(dir string) string { return filepath.Join(dir, "sessions", "%2Fwork%2Fgrok") }

// #3003 (and #4595): Grok Build's sessions couldn't be deleted on the
// Sessions page. A session is its folder, so it goes to the trash whole:
// updates.jsonl, summary.json, the locks and the terminal output Grok Build
// keeps beside them, and its subagents' folders, which are folders of their
// own beside it. The one thing outside them is the copy of its text in
// Grok Build's search index, session_search.sqlite: those rows go before a
// file does, so Grok Build's search doesn't go on finding a session that is
// gone. Restore puts the folders back.
func TestGrokDeleteRestore(t *testing.T) {
	_, dir := putGrok(t)
	cwd := grokCwd(dir)
	index := grokSearchDB()
	grokSearchMake(t, index, [][2]string{
		{grokMain, grokWordMain}, {grokSub, grokWordSub}, {grokFork, grokWordFork},
	})
	ageTree(dir)
	Reset()

	if !Deletable("grok") {
		t.Fatal("Grok Build's sessions are not deletable")
	}
	m, ok := findManaged(ListAgent("grok"), grokMain)
	if !ok || !m.Deletable || m.Files != 2 {
		t.Fatalf("listed as %+v", m)
	}

	// one being written to is left alone, as every other agent's is
	now := time.Now()
	if err := os.Chtimes(filepath.Join(cwd, grokMain, "updates.jsonl.lock"), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete("grok", grokMain); !errors.Is(err, ErrActive) {
		t.Fatalf("a session just written: %v", err)
	}
	if rows, found := grokSearchCount(t, index, grokMain, grokWordMain); rows != 1 || found != 1 {
		t.Fatalf("its index row went with a delete that was refused: %d rows, %d found", rows, found)
	}
	ageTree(dir)

	// what it is, and what is not its own: the other session's folder, the
	// prompts typed in that cwd, which every session there shares, and the
	// index itself
	mine := []string{filepath.Join(cwd, grokMain), filepath.Join(cwd, grokSub)}
	others := []string{filepath.Join(cwd, grokFork), filepath.Join(cwd, "prompt_history.jsonl"), index}

	tr, err := Delete("grok", grokMain)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Items) != len(mine) {
		t.Fatalf("trashed %+v, want both folders", tr.Items)
	}
	for _, p := range mine {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s is still there", p)
		}
	}
	for _, p := range others {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("another's %s: %v", p, err)
		}
	}
	// the search has nothing of either folder's session left, and the other
	// session's text is where it was
	for id, word := range map[string]string{grokMain: grokWordMain, grokSub: grokWordSub} {
		if rows, found := grokSearchCount(t, index, id, word); rows != 0 || found != 0 {
			t.Fatalf("%s: %d rows and %d found after the delete", id, rows, found)
		}
	}
	if rows, found := grokSearchCount(t, index, grokFork, grokWordFork); rows != 1 || found != 1 {
		t.Fatalf("another session's index row: %d rows, %d found", rows, found)
	}
	Reset()
	if _, ok := findManaged(ListAgent("grok"), grokMain); ok {
		t.Fatal("still listed after the delete")
	}
	if _, ok := findManaged(ListAgent("grok"), grokSub); ok {
		t.Fatal("its subagent is listed as a session of its own")
	}
	if _, ok := findManaged(ListAgent("grok"), grokFork); !ok {
		t.Fatal("another session went with it")
	}

	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(cwd, grokMain, "updates.jsonl"),
		filepath.Join(cwd, grokMain, "summary.json"),
		filepath.Join(cwd, grokMain, "prompt_context.json"),
		filepath.Join(cwd, grokMain, "terminal", "1-call.txt"),
		filepath.Join(cwd, grokMain, "subagents", grokSub, "meta.json"),
		filepath.Join(cwd, grokSub, "updates.jsonl"),
		filepath.Join(cwd, grokSub, "updates.jsonl.lock"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s isn't back: %v", p, err)
		}
	}
	// the index has its rows again, its search finds them, and the other
	// session's is not doubled
	for id, word := range map[string]string{grokMain: grokWordMain, grokSub: grokWordSub, grokFork: grokWordFork} {
		if rows, found := grokSearchCount(t, index, id, word); rows != 1 || found != 1 {
			t.Fatalf("%s after the restore: %d rows and %d found, want 1 and 1", id, rows, found)
		}
	}
	Reset()
	if back, ok := findManaged(ListAgent("grok"), grokMain); !ok || back.Title != m.Title || !back.Deletable {
		t.Fatalf("the restored session: %+v", back)
	}

	// and its trash entry is erased for good
	if _, err := Delete("grok", grokMain); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, tr := range Trash() {
		keys = append(keys, tr.Key)
	}
	if len(keys) != 1 {
		t.Fatalf("trash: %v", keys)
	}
	if err := Purge(keys[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(TrashDir(), keys[0])); err == nil {
		t.Fatalf("the trash entry %s is still there", keys[0])
	}
	Reset()
	if _, ok := findManaged(ListAgent("grok"), grokMain); ok {
		t.Fatal("listed after its trash entry was purged")
	}
}

// An index that Grok Build's search can't give up the row of stops the
// delete: the session stays where it is, whole, rather than half gone.
func TestGrokDeleteLeavesTheSessionWhenItsIndexRowCantGo(t *testing.T) {
	_, dir := putGrok(t)
	cwd := grokCwd(dir)
	// not a database at all: its row can't be taken out
	if err := os.WriteFile(grokSearchDB(), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	ageTree(dir)
	Reset()

	if _, err := Delete("grok", grokMain); err == nil {
		t.Fatal("a session whose index row can't go was deleted")
	}
	if _, err := os.Stat(filepath.Join(cwd, grokMain, "updates.jsonl")); err != nil {
		t.Fatalf("the session went anyway: %v", err)
	}
	Reset()
	if _, ok := findManaged(ListAgent("grok"), grokMain); !ok {
		t.Fatal("it is not listed after a delete that failed")
	}
	if len(Trash()) != 0 {
		t.Fatalf("a refused delete left trash behind: %+v", Trash())
	}
}

// grokSearchRowsDrop takes a session's row out of the index, its FTS entry
// with it, and does nothing at all to a Grok Build that keeps no index.
func TestGrokSearchRowsDrop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	if err := os.MkdirAll(filepath.Join(home, ".grok", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	// no index: a Grok Build that never searched deletes as before
	if _, err := grokSearchRowsDrop([]string{grokMain}); err != nil {
		t.Fatalf("no index: %v", err)
	}

	index := grokSearchDB()
	grokSearchMake(t, index, [][2]string{{grokMain, grokWordMain}, {grokFork, grokWordFork}})
	if _, err := grokSearchRowsDrop([]string{grokMain}); err != nil {
		t.Fatal(err)
	}
	if rows, found := grokSearchCount(t, index, grokMain, grokWordMain); rows != 0 || found != 0 {
		t.Fatalf("after the drop: %d rows of it, %d found", rows, found)
	}
	if rows, found := grokSearchCount(t, index, grokFork, grokWordFork); rows != 1 || found != 1 {
		t.Fatalf("the other session: %d rows, %d found", rows, found)
	}
	// nothing of it left to take out is not an error
	if _, err := grokSearchRowsDrop([]string{grokMain}); err != nil {
		t.Fatalf("already gone: %v", err)
	}

	// a database without Grok Build's tables is left alone
	bare := filepath.Join(home, "bare.sqlite")
	db, err := sql.Open("sqlite", bare)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Rename(bare, index); err != nil {
		t.Fatal(err)
	}
	if _, err := grokSearchRowsDrop([]string{grokMain}); err != nil {
		t.Fatalf("no tables: %v", err)
	}
}

// A session's index rows come back whole with it: every column, the offset
// Grok Build keeps too, and a row Grok Build wrote again meanwhile is not
// written over.
func TestGrokSearchRowsComeBackWhole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	index := grokSearchDB()
	grokSearchMake(t, index, [][2]string{{grokMain, grokWordMain}, {grokFork, grokWordFork}})
	db, err := sql.Open("sqlite", sqliteURL(index, "_pragma=busy_timeout(2000)"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE session_docs SET last_indexed_offset = 4242, content_hash = 'h1' WHERE session_id = ?`, grokMain); err != nil {
		t.Fatal(err)
	}
	rows, err := grokSearchRowsDrop([]string{grokMain})
	if err != nil || len(rows) != 1 {
		t.Fatalf("dropped %v, %v", rows, err)
	}
	dir := t.TempDir()
	if err := grokSearchKeep(dir, rows); err != nil {
		t.Fatal(err)
	}
	if err := grokSearchPut(grokSearchKept(dir)); err != nil {
		t.Fatal(err)
	}
	var off int
	var hash string
	if err := db.QueryRow(`SELECT last_indexed_offset, content_hash FROM session_docs WHERE session_id = ?`, grokMain).Scan(&off, &hash); err != nil || off != 4242 || hash != "h1" {
		t.Fatalf("row back as %d %q, %v", off, hash, err)
	}
	if r, f := grokSearchCount(t, index, grokMain, grokWordMain); r != 1 || f != 1 {
		t.Fatalf("%d rows, %d found", r, f)
	}
	// Grok Build indexed it again first: its row stays
	if _, err := db.Exec(`UPDATE session_docs SET content_hash = 'newer' WHERE session_id = ?`, grokMain); err != nil {
		t.Fatal(err)
	}
	if err := grokSearchPut(grokSearchKept(dir)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT content_hash FROM session_docs WHERE session_id = ?`, grokMain).Scan(&hash); err != nil || hash != "newer" {
		t.Fatalf("restore wrote over %q, %v", hash, err)
	}
}

package sessions

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
)

// Grok Build keeps a search index of its own beside the sessions it reads:
// <GROK_HOME>/sessions/session_search.sqlite copies each session's text
// into session_docs (one row a session: its id, cwd, title, the whole
// conversation as content, a hash and the offset it was indexed to), with
// session_docs_fts an FTS5 index over it kept in step by the table's own
// triggers (session_docs_ai, -ad and -au; Grok's search reads that index).
// A session's folder is therefore not the only copy of its text: deleting
// the folder alone would leave its row behind, and Grok Build's search
// would go on finding a session Grok Build no longer lists (upstream #4595).
//
// Only the deleted session's rows go. A Grok Build that has never searched
// (no index file, or one an older version made without these tables) has
// nothing to take out, and deletes as before. An index that can't be read
// is not nothing: the delete stops rather than moving files out from under
// a row it couldn't see.

// grokSearchDB is Grok Build's search index, whether or not it is there.
func grokSearchDB() string {
	return filepath.Join(GrokDir(), "sessions", "session_search.sqlite")
}

// grokSearchRowsDrop takes the given sessions' rows out of Grok Build's
// search index, their FTS entries going with them through session_docs_ad.
// It runs before any of a session's files are moved, and its error stops
// the delete: a session still listed with its text still searchable is
// whole, where a folder moved out from under its row is not.
func grokSearchRowsDrop(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	path := grokSearchDB()
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	db, err := sql.Open("sqlite", sqliteURL(path, "_pragma=busy_timeout(5000)"))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// a Grok Build that keeps no such tables: nothing to do
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('session_docs', 'session_docs_fts')`).Scan(&n); err != nil || n != 2 {
		return err
	}
	// Grok Build may be indexing a session while the page deletes it: the
	// write lock is taken first, so this waits for its own write (up to the
	// busy timeout) instead of failing at once, and the rows taken are the
	// rows the index has when the delete goes through
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := conn.ExecContext(ctx, `DELETE FROM session_docs WHERE session_id = ?`, id); err != nil {
			conn.ExecContext(ctx, `ROLLBACK`)
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// grokSearchRow is one dropped row of session_docs as the index had it, every
// column by name, so Restore puts back what was there whatever columns this
// Grok Build's version keeps (1.0.67 has six; a later one may add more).
type grokSearchRow struct {
	Cols []string `json:"cols"`
	Vals []any    `json:"vals"`
}

// grokSearchFile is where a trashed session's dropped rows are kept, beside
// its note. Their text can be large, so they are not in the note the Trash
// page lists.
const grokSearchFile = "grok_search.json"

// grokSearchRowsDrop takes the given sessions' rows out of Grok Build's
// search index, their FTS entries going with them through session_docs_ad,
// and returns them so a restore can put them back. It runs before any of a
// session's files are moved, and its error stops the delete: a session still
// listed with its text still searchable is whole, where a folder moved out
// from under its row is not.
func grokSearchRowsDrop(ids []string) ([]grokSearchRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	path := grokSearchDB()
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return nil, nil
	}
	conn, closeDB, err := grokSearchOpen(path)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	ctx := context.Background()
	// a Grok Build that keeps no such tables: nothing to do
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('session_docs', 'session_docs_fts')`).Scan(&n); err != nil || n != 2 {
		return nil, err
	}
	// Grok Build may be indexing a session while the page deletes it: the
	// write lock is taken first, so this waits for its own write (up to the
	// busy timeout) instead of failing at once, and the rows taken are the
	// rows the index has when the delete goes through
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	var took []grokSearchRow
	for _, id := range ids {
		rows, err := grokSearchRead(ctx, conn, id)
		if err == nil {
			_, err = conn.ExecContext(ctx, `DELETE FROM session_docs WHERE session_id = ?`, id)
		}
		if err != nil {
			conn.ExecContext(ctx, `ROLLBACK`)
			return nil, err
		}
		took = append(took, rows...)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		conn.ExecContext(ctx, `ROLLBACK`)
		return nil, err
	}
	return took, nil
}

func grokSearchOpen(path string) (*sql.Conn, func(), error) {
	db, err := sql.Open("sqlite", sqliteURL(path, "_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, nil, err
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return conn, func() { conn.Close(); db.Close() }, nil
}

// grokSearchRead is the rows session_docs has of one session, whole.
func grokSearchRead(ctx context.Context, conn *sql.Conn, id string) ([]grokSearchRow, error) {
	r, err := conn.QueryContext(ctx, `SELECT * FROM session_docs WHERE session_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	cols, err := r.Columns()
	if err != nil {
		return nil, err
	}
	var out []grokSearchRow
	for r.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := r.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, grokSearchRow{Cols: cols, Vals: vals})
	}
	return out, r.Err()
}

// grokSearchPut puts dropped rows back into the index, the FTS entries
// following through session_docs_ai. A row the index has again by then
// (Grok Build indexed the session in the meantime) is left as it is, and
// the index being gone, unreadable or of another shape is not an error a
// restore stops on: the session is whole either way.
func grokSearchPut(rows []grokSearchRow) error {
	if len(rows) == 0 {
		return nil
	}
	path := grokSearchDB()
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	conn, closeDB, err := grokSearchOpen(path)
	if err != nil {
		return err
	}
	defer closeDB()
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	for _, r := range rows {
		if len(r.Cols) == 0 || len(r.Cols) != len(r.Vals) {
			continue
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(r.Cols)), ",")
		quoted := make([]string, len(r.Cols))
		for i, c := range r.Cols {
			quoted[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
		}
		q := `INSERT OR IGNORE INTO session_docs (` + strings.Join(quoted, ", ") + `) VALUES (` + marks + `)`
		if _, err := conn.ExecContext(ctx, q, r.Vals...); err != nil {
			conn.ExecContext(ctx, `ROLLBACK`)
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}
	return nil
}

// grokSearchKeep writes the dropped rows into the trash folder dir.
func grokSearchKeep(dir string, rows []grokSearchRow) error {
	if len(rows) == 0 {
		return nil
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, grokSearchFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, grokSearchFile))
}

// grokSearchKept reads them again; none kept (or unreadable) is none.
func grokSearchKept(dir string) []grokSearchRow {
	b, err := os.ReadFile(filepath.Join(dir, grokSearchFile))
	if err != nil {
		return nil
	}
	var rows []grokSearchRow
	if json.Unmarshal(b, &rows) != nil {
		return nil
	}
	return rows
}

package sessions

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// A ZCode request carries its session's id without the prefix its database
// gives it (sess_…), so the Routing page asks for the title by that id. The
// title is found under the database's key and handed back under the id asked
// for, as it is for the prefixed id.
func TestZCodeTitlesByRequestID(t *testing.T) {
	home := setupAgents(t)
	const id = "578bd3d1-834b-43fb-b40c-ed9d17ad7731"
	const name = "A ZCode session's title"
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', NULL, 's', '/work/zc', ?, 'generated', '0.16.9', 1790496000000, 1790496000000)`, "sess_"+id, name); err != nil {
		t.Fatal(err)
	}
	got := Titles([]string{"zcode:" + id, "zcode:sess_" + id})
	for _, k := range []string{"zcode:" + id, "zcode:sess_" + id} {
		if got[k] != name {
			t.Fatalf("%s: title %q, want %q (all %v)", k, got[k], name, got)
		}
	}
}

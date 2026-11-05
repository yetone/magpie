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

// A ZCode subagent is filed as sess_subagent_agent_<id>, with its
// conversation under the parent session's key (ocRoots). A request names it
// by the bare id, so its title is read from its own file, not the parent's
// and not the folder the Routing page falls back to.
func TestZCodeSubagentTitlesByRequestID(t *testing.T) {
	home := setupAgents(t)
	const parent = "67186573-3a8c-429f-a1eb-c7a53c195415"
	const child = "fb968629-4765-4241-9458-26e351aaffea"
	const parentName = "The parent session's title"
	const childName = "The subagent's title"
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range []struct {
		id, parent, title, source string
	}{
		{"sess_" + parent, "", parentName, "generated"},
		{"sess_subagent_agent_" + child, "sess_" + parent, childName, "first_input"},
	} {
		var par any
		if s.parent != "" {
			par = s.parent
		}
		if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', ?, 's', '/work/zc', ?, ?, '0.16.9', 1790496200000, 1790496200000)`, s.id, par, s.title, s.source); err != nil {
			t.Fatal(err)
		}
	}
	got := Titles([]string{"zcode:" + child, "zcode:sess_subagent_agent_" + child, "zcode:" + parent})
	if got["zcode:"+child] != childName {
		t.Fatalf("subagent by request id: %q, want %q (all %v)", got["zcode:"+child], childName, got)
	}
	if got["zcode:sess_subagent_agent_"+child] != childName {
		t.Fatalf("subagent by database id: %q, want %q (all %v)", got["zcode:sess_subagent_agent_"+child], childName, got)
	}
	if got["zcode:"+parent] != parentName {
		t.Fatalf("parent: %q, want %q (all %v)", got["zcode:"+parent], parentName, got)
	}
}

// A name poll for ZCode reads its session rows, not the message join
// zcodeFiles makes for a session's size: that join over a large database
// took ~20s on the Routing page's 15s refresh. A database without its
// message table still names a session whose row has a title.
func TestZCodeTitlesWithoutMessageTable(t *testing.T) {
	home := setupAgents(t)
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("DROP TABLE message"); err != nil {
		t.Fatal(err)
	}
	got := Titles([]string{"zcode:ses_zcA"})
	if got["zcode:ses_zcA"] != "Port the parser to Go" {
		t.Fatalf("title %q, want %q (all %v)", got["zcode:ses_zcA"], "Port the parser to Go", got)
	}
}

// A ZCode session whose database title is still the default one is named by
// its first prompt, as assembleListed names it.
func TestZCodeDefaultTitleFallsBackToFirstPrompt(t *testing.T) {
	home := setupAgents(t)
	const id = "0f6c7b1e-3d2a-4c8e-9f10-2b3c4d5e6f70"
	const prompt = "The first typed prompt"
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', NULL, 's', '/work/zc', 'New session - default', 'default', '0.16.9', 1790496300000, 1790496300000)`, "sess_"+id); err != nil {
		t.Fatal(err)
	}
	ocInsert(t, db, "sess_"+id, "msg_default", 1790496301000, 1790496301000, `{"id":"msg_default","role":"user","time":{"created":1790496301000},"semantics":{"origin":"real_user"}}`)
	if _, err := db.Exec("INSERT INTO part VALUES (?, ?, ?, ?, ?, ?)", "prt_default", "msg_default", "sess_"+id, 1790496301000, 1790496301000, `{"type":"text","text":"`+prompt+`"}`); err != nil {
		t.Fatal(err)
	}
	got := Titles([]string{"zcode:" + id})
	if got["zcode:"+id] != prompt {
		t.Fatalf("title %q, want %q (all %v)", got["zcode:"+id], prompt, got)
	}
}

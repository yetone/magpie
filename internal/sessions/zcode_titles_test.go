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

// A ZCode fork is a child session with a normal sess_<id>, which ocRoots
// files under its parent's key. A request names it by that id, so its own
// title is read from its file, not the parent's title and not the folder.
func TestZCodeForkTitlesByRequestID(t *testing.T) {
	home := setupAgents(t)
	const parent = "af74e6e0-c3c4-4535-82b5-3aba9a1db963"
	const fork = "8588528f-76cd-4810-9d01-9fcc2b02e22c"
	const parentName = "The parent session's title"
	const forkName = "Fork of The parent session's title"
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range []struct {
		id, parent, title, source string
	}{
		{"sess_" + parent, "", parentName, "generated"},
		{"sess_" + fork, "sess_" + parent, forkName, "generated"},
	} {
		var par any
		if s.parent != "" {
			par = s.parent
		}
		if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', ?, 's', '/work/zc', ?, ?, '0.16.9', 1790496400000, 1790496400000)`, s.id, par, s.title, s.source); err != nil {
			t.Fatal(err)
		}
	}
	got := Titles([]string{"zcode:" + fork, "zcode:sess_" + fork, "zcode:" + parent})
	if got["zcode:"+fork] != forkName {
		t.Fatalf("fork by request id: %q, want %q (all %v)", got["zcode:"+fork], forkName, got)
	}
	if got["zcode:sess_"+fork] != forkName {
		t.Fatalf("fork by database id: %q, want %q (all %v)", got["zcode:sess_"+fork], forkName, got)
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

// A name poll sees a ZCode session's new messages, as the full listing
// does: ZCode writes a message without touching its session's row, so a
// session still under its default title is named by its first prompt once
// that prompt is in, not when the row next changes.
func TestZCodeTitlesSeeNewMessages(t *testing.T) {
	home := setupAgents(t)
	const id = "9a1d2c3b-4e5f-4a6b-8c7d-0e1f2a3b4c5d"
	const prompt = "The prompt typed after the first poll"
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', NULL, 's', '/work/zc', 'New session - default', 'default', '0.16.9', 1790496400000, 1790496400000)`, "sess_"+id); err != nil {
		t.Fatal(err)
	}
	if got := Titles([]string{"zcode:" + id}); got["zcode:"+id] != "" {
		t.Fatalf("before any prompt: %q, want none", got["zcode:"+id])
	}
	ocInsert(t, db, "sess_"+id, "msg_late", 1790496401000, 1790496401000, `{"id":"msg_late","role":"user","time":{"created":1790496401000},"semantics":{"origin":"real_user"}}`)
	if _, err := db.Exec("INSERT INTO part VALUES (?, ?, ?, ?, ?, ?)", "prt_late", "msg_late", "sess_"+id, 1790496401000, 1790496401000, `{"type":"text","text":"`+prompt+`"}`); err != nil {
		t.Fatal(err)
	}
	if got := Titles([]string{"zcode:" + id}); got["zcode:"+id] != prompt {
		t.Fatalf("after the prompt: %q, want %q (all %v)", got["zcode:"+id], prompt, got)
	}
}

// What a name poll keeps of a ZCode session is what the Sessions page's
// listing would keep: the same size and last activity, so neither reads
// the session again only because the other read it last.
func TestZCodeTitlesKeepTheListingsStamp(t *testing.T) {
	setupAgents(t)
	if got := Titles([]string{"zcode:ses_zcA"}); got["zcode:ses_zcA"] == "" {
		t.Fatalf("no title for ses_zcA")
	}
	mu.Lock()
	defer mu.Unlock()
	release := useDBs()
	defer release()
	var checked bool
	for _, f := range zcodeFiles() {
		if f.m().sid != "ses_zcA" {
			continue
		}
		checked = true
		st := cache[f.path]
		if st == nil {
			t.Fatalf("%s not kept", f.path)
		}
		if st.Size != f.size || st.Mod != f.mod.UnixNano() {
			t.Fatalf("kept size %d mod %d, listing has size %d mod %d", st.Size, st.Mod, f.size, f.mod.UnixNano())
		}
	}
	if !checked {
		t.Fatal("ses_zcA not listed")
	}
}

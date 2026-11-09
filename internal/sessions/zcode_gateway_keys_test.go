package sessions

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
)

// A ZCode request names its session by the id its database gives it with a
// prefix (sess_…, a subagent's sess_subagent_agent_…), so a session whose
// files are on this computer was listed twice: once under its folder, and
// again as a gateway-only row: the same session under its bare
// id. Its own id, its fork's and its subagent's bare ids are keys of it
// (eachGatewayKey), and those of neither of the two are rows of their own.
func TestZCodeGatewayRowIsTheSessionItself(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	zcForkDB(t, home)
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// a fork of sess_main and a subagent's own session, filed under it
	// (ocRoots) and no rows of their own
	for _, s := range []struct{ id, title string }{
		{"sess_fork_of_main", "Fork of Port the parser"},
		{"sess_subagent_agent_abc", "Subagent"},
	} {
		if _, err := db.Exec("INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', 'sess_main', 's', '/work/zcf', ?, 'generated', '3.17.2', 1791000000000, 1791000000000)",
			s.id, s.title); err != nil {
			t.Fatal(err)
		}
		ocInsert(t, db, s.id, s.id+"_m", 1791000000000, 1791000010000,
			`{"id":"`+s.id+`_m","role":"assistant","modelId":"glm-5.3","tokens":{"input":10,"output":2,"cache":{"read":0,"write":0}},"time":{"created":1791000000000,"completed":1791000010000}}`)
	}
	list, _, _ := ListUnless(All, -1)
	var main Session
	for _, s := range list {
		if s.Agent == "zcode" && s.ID == "sess_main" {
			main = s
		}
	}
	if main.ID == "" {
		t.Fatal("sess_main not listed")
	}
	keys := GatewayKeys(main)
	for _, want := range []string{"zcode|sess_main", "zcode|main", "zcode|fork_of_main", "zcode|abc"} {
		if !slices.Contains(keys, want) {
			t.Errorf("keys %v lack %q", keys, want)
		}
	}
	// the management page's ids (NativeIDs) are the same: its count of
	// gateway-only sessions and its delete leave these records alone
	native := NativeIDs(map[string]bool{"zcode": true})["zcode"]
	for _, want := range []string{"sess_main", "main", "fork_of_main", "abc"} {
		if !native[want] {
			t.Errorf("NativeIDs lack %q: %v", want, native)
		}
	}
	// what the gateway saw of them is that session's record: no row added
	ext := []ExternalSession{
		{Agent: "zcode", ID: "main"},
		{Agent: "zcode", ID: "fork_of_main"},
		{Agent: "zcode", ID: "abc"},
	}
	if added := MergeExternal(list, ext); len(added) != len(list) {
		t.Fatalf("%d rows for %d sessions: one here was listed again as a gateway row", len(added), len(list))
	}
	// one whose files are not here is a row of its own, as before
	if added := MergeExternal(list, []ExternalSession{{Agent: "zcode", ID: "gone"}}); len(added) != len(list)+1 {
		t.Fatalf("%d rows for %d sessions: a session with no files here was left out", len(added), len(list))
	}
}

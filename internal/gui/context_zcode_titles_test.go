package gui

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
)

// A ZCode request names its session by the bare id its database gives with
// a prefix (sess_, a subagent's sess_subagent_agent_), and a subagent's own
// session is no row of its own: ocRoots files it under its parent's. The
// Context tab matched the ids it was given against the listed ones exactly,
// so it showed the first 8 characters of the id where the Sessions page
// names both (flandern1211_58540 on Discord showed the ids on ZCode's).
func TestContextZCodeSessionTitles(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, ".zcode", "cli", "db")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(path, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE TABLE session (id text PRIMARY KEY, project_id text NOT NULL, parent_id text, slug text NOT NULL, directory text NOT NULL, title text NOT NULL, title_source text, version text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL)",
		"CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)",
		"CREATE TABLE part (id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	const main, sub = "9a8b7c6d-1111-4222-8333-444455556666", "0f0e0d0c-aaaa-4bbb-8ccc-ddddeeeeffff"
	for _, s := range []struct{ id, parent, title string }{
		{"sess_" + main, "", "Port the parser"},
		{"sess_subagent_agent_" + sub, "sess_" + main, "Subagent's own"},
	} {
		var parent any
		if s.parent != "" {
			parent = s.parent
		}
		if _, err := db.Exec("INSERT INTO session VALUES (?, 'p', ?, 's', '/work/zcf', ?, 'ai', '3.17.2', 1790496000000, 1790496000000)", s.id, parent, s.title); err != nil {
			t.Fatal(err)
		}
	}
	msg := func(sid, id string) {
		if _, err := db.Exec("INSERT INTO message VALUES (?, ?, 1790496003000, 1790496030000, ?)", id, sid,
			`{"id":"`+id+`","role":"assistant","modelId":"glm-5.3","providerID":"magpie","tokens":{"total":1540,"input":1500,"output":40,"cache":{"read":1000,"write":200}},"time":{"created":1790496003000,"completed":1790496030000}}`); err != nil {
			t.Fatal(err)
		}
	}
	msg("sess_"+main, "msg_main")
	msg("sess_subagent_agent_"+sub, "msg_sub")
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	out := contextOf([]gateway.Route{
		ctxRoute(1, "zcode", main, 20000, 1000, 200000, 0),
		ctxRoute(2, "zcode", sub, 20000, 1000, 200000, 0),
		ctxRoute(3, "zcode", "not-a-session", 20000, 1000, 200000, 0),
	}, 7)
	titles := map[string]string{}
	for _, s := range out.Sessions {
		titles[s.Key] = s.Title
	}
	if titles[main] != "Port the parser" || titles[sub] != "Subagent's own" || titles["not-a-session"] != "" {
		t.Fatalf("titles %v", titles)
	}
}

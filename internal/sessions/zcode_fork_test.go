package sessions

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// zcForkDB adds to the ZCode database setupAgents made a session on
// 2026-10-31, a selection side chat opened from it on 11-02, a side chat
// opened from that one, and a fork whose parent is gone, each message as
// ZCode 3.17's CLI copies it (gce): a new id and session, the original's
// time, model and tokens, and metadata.forkOrigin {sessionId, messageId}.
func zcForkDB(t *testing.T, home string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	day := func(d, h int) int64 { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC).UnixMilli() }
	for _, s := range []struct {
		id, title string
		at        int64
	}{
		{"sess_main", "Port the parser", day(31, 0)},
		{"sess_side", "Selection side chat", day(33, 9)}, // 11-02
		{"sess_side2", "Selection side chat", day(33, 10)},
		{"sess_orphan", "Fork", day(30, 0)},
	} {
		if _, err := db.Exec("INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', NULL, 's', '/work/zcf', ?, 'ai', '3.17.2', ?, ?)",
			s.id, s.title, s.at, s.at); err != nil {
			t.Fatal(err)
		}
	}
	reply := func(id, parent string, created int64, in, out, read int, meta string) string {
		return fmt.Sprintf(`{"id":%q,"role":"assistant","parentID":%q,"modelId":"glm-5.3","providerID":"zai","tokens":{"total":%d,"input":%d,"output":%d,"reasoning":0,"cache":{"read":%d,"write":0}},"time":{"created":%d,"completed":%d}%s}`,
			id, parent, in+out, in, out, read, created, created+30000, meta)
	}
	origin := func(sid, mid string) string {
		return fmt.Sprintf(`,"metadata":{"forkOrigin":{"sessionId":%q,"messageId":%q}}`, sid, mid)
	}
	m1 := day(31, 1)
	for _, m := range []struct {
		sid, id string
		at      int64
		data    string
	}{
		{"sess_main", "msg_m0", m1 - 1000, `{"id":"msg_m0","role":"user","time":{"created":` + fmt.Sprint(m1-1000) + `},"semantics":{"origin":"real_user"}}`},
		{"sess_main", "msg_m1", m1, reply("msg_m1", "msg_m0", m1, 1000, 100, 600, "")},
		// the side chat: the main session copied in, then its own reply
		{"sess_side", "msg_c0", m1 - 1000, `{"id":"msg_c0","role":"user","time":{"created":` + fmt.Sprint(m1-1000) + `},"semantics":{"origin":"real_user"}` + origin("sess_main", "msg_m0") + `}`},
		{"sess_side", "msg_c1", m1, reply("msg_c1", "msg_c0", m1, 1000, 100, 600, origin("sess_main", "msg_m1"))},
		{"sess_side", "msg_cn", day(33, 9), `{"id":"msg_cn","role":"assistant","time":{"created":` + fmt.Sprint(day(33, 9)) + `},"metadata":{"forkOrigin":{"parentSessionId":"sess_main","targetMessageId":"msg_m1"}}}`},
		{"sess_side", "msg_c2", day(33, 9) + 5000, reply("msg_c2", "msg_c0", day(33, 9)+5000, 20, 2, 0, "")},
		// a side chat of the side chat: a copy of a copy
		{"sess_side2", "msg_d1", m1, reply("msg_d1", "", m1, 1000, 100, 600, origin("sess_side", "msg_c1"))},
		// a fork whose parent session was deleted: its copy is all there is
		{"sess_orphan", "msg_o1", day(30, 1), reply("msg_o1", "", day(30, 1), 300, 30, 0, origin("sess_gone", "msg_gone"))},
	} {
		ocInsert(t, db, m.sid, m.id, m.at, m.at+30000, m.data)
	}
}

// A message ZCode copied into a side chat, a fork or a copy of one made no
// call: the session it was copied from counts it, once (Sky, skyhuang1994:
// a side chat opened on 11-02 put a 10-31 session's 0.42B tokens on 10-31
// a second time).
func TestZCodeCopiesCountedOnce(t *testing.T) {
	inZone(t, 0)
	zcForkDB(t, setupAgents(t))
	cs := zcodeCalls(t)
	for _, id := range []string{"msg_c1", "msg_d1"} {
		if c, ok := cs[id]; ok {
			t.Errorf("the copy %s was counted as a call: %+v", id, c)
		}
	}
	if c := cs["msg_m1"]; c.Session != "sess_main" || c.Tokens != (Tokens{400, 100, 600, 0, 0}) {
		t.Errorf("the original: %+v", c)
	}
	if c := cs["msg_c2"]; c.Session != "sess_side" || c.Tokens != (Tokens{20, 2, 0, 0, 0}) {
		t.Errorf("the side chat's own reply: %+v", c)
	}
	if c := cs["msg_o1"]; c.Session != "sess_orphan" || c.Tokens != (Tokens{300, 30, 0, 0, 0}) {
		t.Errorf("a copy whose original is gone: %+v", c)
	}

	s := statsAt(0, time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	for _, d := range []struct {
		date string
		want Tokens
	}{
		{"2026-10-31", Tokens{400, 100, 600, 0, 0}},
		{"2026-11-02", Tokens{20, 2, 0, 0, 0}},
		{"2026-10-30", Tokens{300, 30, 0, 0, 0}},
	} {
		if got, _ := usageOn(s, d.date, "/work/zcf"); got != d.want {
			t.Errorf("the Sessions page's %s: %+v, want %+v", d.date, got, d.want)
		}
	}
}

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
	if c := cs["msg_m1"]; c.Session != "main" || c.Tokens != (Tokens{400, 100, 600, 0, 0}) {
		t.Errorf("the original: %+v", c)
	}
	if c := cs["msg_c2"]; c.Session != "side" || c.Tokens != (Tokens{20, 2, 0, 0, 0}) {
		t.Errorf("the side chat's own reply: %+v", c)
	}
	if c := cs["msg_o1"]; c.Session != "orphan" || c.Tokens != (Tokens{300, 30, 0, 0, 0}) {
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

// A fork that went on working: ZCode copied an August session's replies
// into it, then it made calls of its own through September. ZCode keeps a
// message's id in its row, not in its data, as OpenCode does, so the rows
// here carry none in data; the provider is a custom one, named by a UUID.
// Only the copies are the parent's. Every reply of the fork's own is a
// call (Sky, skyhuang1994: the "msc" fork's 11,409 September calls, 3.49B
// tokens, none with a forkOrigin, went missing from the Usage page once
// the fork held one copy).
func TestZCodeForkThatWentOnWorkingKeepsItsOwnCalls(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	db, err := sql.Open("sqlite", filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const provider = "6f1c2b9e-4a7d-4c1e-9b2a-3d5e8f0a1b2c"
	at := func(m time.Month, d, h, min int) int64 {
		return time.Date(2026, m, d, h, min, 0, 0, time.UTC).UnixMilli()
	}
	for _, s := range []struct {
		id string
		at int64
	}{{"ses_aug", at(8, 30, 0, 0)}, {"ses_msc", at(9, 1, 0, 0)}} {
		if _, err := db.Exec("INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', NULL, 's', '/work/msc', ?, 'ai', '3.17.2', ?, ?)",
			s.id, s.id, s.at, s.at); err != nil {
			t.Fatal(err)
		}
	}
	reply := func(created int64, in, out, read int, meta string) string {
		return fmt.Sprintf(`{"role":"assistant","parentID":"msg_u","modelId":"glm-5.3","providerID":%q,"tokens":{"total":%d,"input":%d,"output":%d,"reasoning":0,"cache":{"read":%d,"write":0}},"time":{"created":%d,"completed":%d}%s}`,
			provider, in+out, in, out, read, created, created+30000, meta)
	}
	// the parent's two replies on 08-30, and their copies in the fork
	for i := range 2 {
		c := at(8, 30, 1, i)
		ocInsert(t, db, "ses_aug", fmt.Sprintf("msg_a%d", i), c, c+30000, reply(c, 1000, 100, 600, ""))
		ocInsert(t, db, "ses_msc", fmt.Sprintf("msg_k%d", i), c, c+30000,
			reply(c, 1000, 100, 600, fmt.Sprintf(`,"metadata":{"forkOrigin":{"sessionId":"ses_aug","messageId":"msg_a%d"}}`, i)))
	}
	// ZCode's notice of the fork
	ocInsert(t, db, "ses_msc", "msg_kn", at(9, 1, 0, 0), at(9, 1, 0, 0),
		fmt.Sprintf(`{"role":"assistant","time":{"created":%d},"metadata":{"forkOrigin":{"parentSessionId":"ses_aug","targetMessageId":"msg_a1"}}}`, at(9, 1, 0, 0)))
	// the fork's own work in September: no forkOrigin
	const own = 40
	for i := range own {
		c := at(9, 2+i%20, 10, i)
		ocInsert(t, db, "ses_msc", fmt.Sprintf("msg_w%02d", i), c, c+30000, reply(c, 500, 50, 200, ""))
	}

	bySession := map[string]Tokens{}
	n := map[string]int{}
	for _, c := range Calls(time.Time{}) {
		if c.Agent == "zcode" {
			v := bySession[c.Session]
			v.add(c.Tokens)
			bySession[c.Session] = v
			n[c.Session]++
		}
	}
	var wantOwn Tokens
	for range own {
		wantOwn.add(Tokens{300, 50, 200, 0, 0})
	}
	if n["ses_msc"] != own || bySession["ses_msc"] != wantOwn {
		t.Errorf("the fork's calls: %d %+v, want its %d own %+v", n["ses_msc"], bySession["ses_msc"], own, wantOwn)
	}
	if n["ses_aug"] != 2 || bySession["ses_aug"] != (Tokens{800, 200, 1200, 0, 0}) {
		t.Errorf("the parent's calls: %d %+v", n["ses_aug"], bySession["ses_aug"])
	}

	s := statsAt(0, time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	if got, _ := usageOn(s, "2026-08-30", "/work/msc"); got != (Tokens{800, 200, 1200, 0, 0}) {
		t.Errorf("the Sessions page's 08-30: %+v, want the parent's alone", got)
	}
	var sept Tokens
	for d := 2; d <= 21; d++ {
		got, _ := usageOn(s, fmt.Sprintf("2026-09-%02d", d), "/work/msc")
		sept.add(got)
	}
	if sept != wantOwn {
		t.Errorf("the Sessions page's September: %+v, want %+v", sept, wantOwn)
	}
}

package sessions

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const (
	ocMain  = "ses_1a2b3c4d5e6fAAAAAAAAAAAAAA"
	ocChild = "ses_1a2b3c4d5e6fBBBBBBBBBBBBBB"
	piFirst = "0199aaaa-1111-7222-8333-444455556666"
	piFork  = "0199bbbb-1111-7222-8333-444455556666"
)

// setupMore is setup with OpenCode's JSON files and Pi's sessions put
// where the two keep them.
func setupMore(t *testing.T) (data, pi string) {
	claude, _ := setup(t)
	dir := filepath.Dir(claude)
	data, pi = filepath.Join(dir, "data"), filepath.Join(dir, "pi")
	copyTree(t, "testdata/opencode", filepath.Join(data, "opencode"))
	copyTree(t, "testdata/pi", pi)
	return
}

func find(t *testing.T, ss []Session, agent, id string) Session {
	t.Helper()
	for _, s := range ss {
		if s.Agent == agent && s.ID == id {
			return s
		}
	}
	t.Fatalf("no %s session %s in %+v", agent, id, ss)
	return Session{}
}

func at(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// checkOpenCode checks the OpenCode fixtures' one session with anything in
// it: its subagent's usage in, the reasoning in the output, the typed words
// of its first prompt its title.
func checkOpenCode(t *testing.T, ss []Session) {
	t.Helper()
	n := 0
	for _, s := range ss {
		if s.Agent == "opencode" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want 1 OpenCode session (the child in its parent, the empty one out), got %d: %+v", n, ss)
	}
	oc := find(t, ss, "opencode", ocMain)
	if oc.Cwd != "/work/oc" || oc.Title != "Why does the login test flake?" {
		t.Fatalf("opencode: %+v", oc)
	}
	if a := model(oc, "gpt-6-astra"); a.Tokens != (Tokens{1000, 120, 5000, 0}) || !a.Priced {
		t.Fatalf("astra: %+v", a)
	}
	if l := model(oc, "codex/gpt-6-luna"); l.Tokens != (Tokens{300, 60, 2000, 0}) || l.Priced {
		t.Fatalf("luna: %+v", l)
	}
	if o := model(oc, "claude-opus-5-5"); o.Tokens != (Tokens{40, 60, 700, 300}) {
		t.Fatalf("the subagent's opus: %+v", o)
	}
	if !oc.Start.Equal(at("2026-09-26T10:00:00Z")) || !oc.Last.Equal(at("2026-09-26T10:03:00Z")) {
		t.Fatalf("opencode times %s %s", oc.Start, oc.Last)
	}
	if runtime.GOOS != "windows" {
		if want := "cd '/work/oc' && opencode --session " + ocMain; oc.Resume != want {
			t.Fatalf("resume %q", oc.Resume)
		}
	}
}

func TestOpenCodeFiles(t *testing.T) {
	inZone(t, 0)
	setupMore(t)
	checkOpenCode(t, List(0))

	// the work of the main session only, 10:00:00–10:03:00, every pause short
	s := statsAt(0, statsNow)
	if got, sec := usageOn(s, "2026-09-26", "/work/oc"); got != (Tokens{1340, 240, 7700, 300}) || sec != 180 {
		t.Fatalf("opencode on the 26th: %+v, %ds active", got, sec)
	}
}

// ocDB builds an OpenCode database from the JSON fixtures, as OpenCode's
// own migration does.
func ocMakeDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db := ocMakeEmpty(t, path, "")
	root := "testdata/opencode/storage"
	infos, _ := filepath.Glob(filepath.Join(root, "session", "*", "*.json"))
	for _, p := range infos {
		var i ocInfo
		b, _ := os.ReadFile(p)
		if err := json.Unmarshal(b, &i); err != nil {
			t.Fatal(err)
		}
		var parent any
		if i.ParentID != "" {
			parent = i.ParentID
		}
		// the times OpenCode's migration gave them: not the latest message's
		if _, err := db.Exec("INSERT INTO session (id, project_id, parent_id, slug, directory, title, version, time_created, time_updated) VALUES (?, '0f3a', ?, 's', ?, ?, '1.1.53', ?, ?)",
			i.ID, parent, i.Directory, i.Title, i.Time.Created, i.Time.Created); err != nil {
			t.Fatal(err)
		}
		msgs, _ := filepath.Glob(filepath.Join(root, "message", i.ID, "*.json"))
		for _, mp := range msgs {
			b, _ := os.ReadFile(mp)
			var m ocMessage
			json.Unmarshal(b, &m)
			ocInsert(t, db, i.ID, m.ID, m.Time.Created, max(m.Time.Created, m.Time.Completed), string(b))
			parts, _ := filepath.Glob(filepath.Join(root, "part", m.ID, "*.json"))
			for _, pp := range parts {
				b, _ := os.ReadFile(pp)
				if _, err := db.Exec("INSERT INTO part VALUES (?, ?, ?, 0, 0, ?)", filepath.Base(pp), m.ID, i.ID, string(b)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return db
}

// ocMakeEmpty makes OpenCode's tables, the session's with more columns.
func ocMakeEmpty(t *testing.T, path, more string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if more != "" {
		more = ", " + more
	}
	for _, q := range []string{
		"PRAGMA journal_mode=WAL",
		"CREATE TABLE session (id text PRIMARY KEY, project_id text NOT NULL, parent_id text, slug text NOT NULL, directory text NOT NULL, title text NOT NULL, version text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, time_archived integer, tokens_input integer DEFAULT 0 NOT NULL" + more + ")",
		"CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)",
		"CREATE TABLE part (id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func ocInsert(t *testing.T, db *sql.DB, sid, mid string, created, updated int64, data string) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO message VALUES (?, ?, ?, ?, ?)", mid, sid, created, updated, data); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeDB(t *testing.T) {
	inZone(t, 0)
	data, _ := setupMore(t)
	// the JSON files stay behind beside the database made from them: only
	// the database is read
	db := ocMakeDB(t, filepath.Join(data, "opencode", "opencode.db"))
	ss := List(0)
	checkOpenCode(t, ss)
	oc := find(t, ss, "opencode", ocMain)
	if filepath.Base(oc.Path) != "opencode.db#"+ocMain {
		t.Fatalf("path %s", oc.Path)
	}
	s := statsAt(0, statsNow)
	if got, sec := usageOn(s, "2026-09-26", "/work/oc"); got != (Tokens{1340, 240, 7700, 300}) || sec != 180 {
		t.Fatalf("opencode on the 26th: %+v, %ds active", got, sec)
	}

	// a reply later in the subagent: its session is read again
	ocInsert(t, db, ocChild, "msg_0005", 1790416990000, 1790417000000,
		`{"id":"msg_0005","role":"assistant","modelID":"claude-opus-5-5","tokens":{"input":1,"output":2,"reasoning":0,"cache":{"read":3,"write":4}},"time":{"created":1790416990000,"completed":1790417000000}}`)
	oc = find(t, List(0), "opencode", ocMain)
	if o := model(oc, "claude-opus-5-5"); o.Tokens != (Tokens{41, 62, 703, 304}) {
		t.Fatalf("after a new reply: %+v", o)
	}
	if !oc.Last.Equal(at("2026-09-26T10:03:20Z")) {
		t.Fatalf("last %s", oc.Last)
	}

	// $OPENCODE_DB names another database, in the data folder; with none
	// made there yet, the JSON files are what there is
	t.Setenv("OPENCODE_DB", "other.db")
	if oc := find(t, List(0), "opencode", ocMain); filepath.Ext(oc.Path) != ".json" {
		t.Fatalf("read a database $OPENCODE_DB doesn't name: %s", oc.Path)
	}
	if len(dbs) != 0 {
		t.Fatalf("databases left open: %v", dbs)
	}
}

func TestPi(t *testing.T) {
	inZone(t, 0)
	setupMore(t)
	ss := List(0)
	p := find(t, ss, "pi", piFirst)
	if p.Cwd != "/work/pi" || p.Title != "Refactor the parser" {
		t.Fatalf("pi: %+v", p)
	}
	// a tool's model work and a compaction on the model in use, a usage
	// entry on its own
	if o := model(p, "claude-opus-5-5"); o.Tokens != (Tokens{110, 55, 1300, 200}) || !o.Priced {
		t.Fatalf("opus: %+v", o)
	}
	if o := model(p, "claude/claude-opus-5-5"); o.Tokens != (Tokens{60, 40, 500, 0}) {
		t.Fatalf("magpie's opus: %+v", o)
	}
	if !p.Start.Equal(at("2026-09-24T08:00:00Z")) || !p.Last.Equal(at("2026-09-24T08:04:00Z")) {
		t.Fatalf("pi times %s %s", p.Start, p.Last)
	}
	if runtime.GOOS != "windows" {
		if want := "cd '/work/pi' && pi --session " + piFirst; p.Resume != want {
			t.Fatalf("resume %q", p.Resume)
		}
	}

	// the fork: what it copied counted in the session it came from
	f := find(t, ss, "pi", piFork)
	if f.Tokens != (Tokens{7, 3, 0, 0}) || f.Title != "Refactor the parser" {
		t.Fatalf("fork: %+v", f)
	}
	if !f.Start.Equal(at("2026-09-25T09:00:00Z")) || !f.Last.Equal(at("2026-09-25T09:00:20Z")) {
		t.Fatalf("fork times %s %s", f.Start, f.Last)
	}

	s := statsAt(0, statsNow)
	if got, sec := usageOn(s, "2026-09-24", "/work/pi"); got != (Tokens{170, 95, 1800, 200}) || sec != 240 {
		t.Fatalf("pi on the 24th: %+v, %ds active", got, sec)
	}
	if got, sec := usageOn(s, "2026-09-25", "/work/pi"); got != (Tokens{7, 3, 0, 0}) || sec != 20 {
		t.Fatalf("pi on the 25th: %+v, %ds active", got, sec)
	}
}

// Pi's sessions in a folder of the user's choosing, all in one, as the
// sessionDir setting or $PI_CODING_AGENT_SESSION_DIR puts them.
func TestPiSessionDir(t *testing.T) {
	_, pi := setupMore(t)
	flat := filepath.Join(filepath.Dir(pi), "flat")
	os.MkdirAll(flat, 0o755)
	from := filepath.Join(pi, "sessions", "--work-pi--")
	name := "2026-09-24T08-00-00-000Z_" + piFirst + ".jsonl"
	if err := os.Rename(filepath.Join(from, name), filepath.Join(flat, name)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(pi, "settings.json"), []byte("{\n  // where the sessions go\n  \"sessionDir\": "+jsonString(flat)+",\n}\n"), 0o644)
	find(t, List(0), "pi", piFirst)

	t.Setenv("PI_CODING_AGENT_SESSION_DIR", filepath.Join(filepath.Dir(pi), "none"))
	Reset()
	for _, s := range List(0) {
		if s.ID == piFirst {
			t.Fatal("$PI_CODING_AGENT_SESSION_DIR comes before the setting")
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDirs(t *testing.T) {
	data, pi := setupMore(t)
	d := Dirs()
	if len(d) != 4 || d[2] != filepath.Join(data, "opencode") || d[3] != pi {
		t.Fatalf("dirs %v", d)
	}
	os.RemoveAll(pi)
	if d := Dirs(); len(d) != 3 {
		t.Fatalf("a Pi folder that isn't there listed: %v", d)
	}
}

// ocMakeV2 makes OpenCode 2's tables (packages/core/src/session/sql.ts,
// the columns read and those beside them) beside the old ones, with the
// fixtures' sessions as its migration copies them over: a prompt's words
// in the prompt, what OpenCode put in beside them a synthetic message of
// its own, a reply's model {id, providerID, variant}.
func ocMakeV2(t *testing.T, path string) *sql.DB {
	t.Helper()
	db := ocMakeEmpty(t, path, "")
	for _, q := range []string{
		"CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text NOT NULL, workspace_id text, parent_id text, slug text NOT NULL, directory text NOT NULL, path text, title text, version text NOT NULL, cost real DEFAULT 0 NOT NULL, tokens_input integer DEFAULT 0 NOT NULL, tokens_output integer DEFAULT 0 NOT NULL, tokens_reasoning integer DEFAULT 0 NOT NULL, tokens_cache_read integer DEFAULT 0 NOT NULL, tokens_cache_write integer DEFAULT 0 NOT NULL, agent text, model text, time_created integer NOT NULL, time_updated integer NOT NULL, time_archived integer)",
		"CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, seq integer NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)",
		"CREATE TABLE kv (key text PRIMARY KEY, value text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL)",
		`INSERT INTO kv VALUES ('migration.v1-v2', '{"phase":"completed"}', 0, 0)`,
		`INSERT INTO session_v2 (id, project_id, slug, directory, title, version, model, time_created, time_updated) VALUES
			('` + ocMain + `', 'global', 'brave-otter', '/work/oc', 'Flaky test hunt', '2.0.18', '{"id":"codex/gpt-6-luna","providerID":"magpie","variant":"default"}', 1790416800000, 1790416800000),
			('ses_1a2b3c4d5e6fCCCCCCCCCCCCCC', 'global', 'idle-crow', '/work/oc', NULL, '2.0.18', NULL, 1790413200000, 1790413200000)`,
		`INSERT INTO session_v2 (id, project_id, parent_id, slug, directory, title, version, time_created, time_updated) VALUES
			('` + ocChild + `', 'global', '` + ocMain + `', 'calm-heron', '/work/oc', NULL, '2.0.18', 1790416860000, 1790416860000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i, m := range []struct{ sid, id, typ, data string }{
		{ocMain, "msg_0001", "user", `{"text":"Why does  the login test\nflake?","time":{"created":1790416805000}}`},
		{ocMain, "msg_0001s", "synthetic", `{"text":"Called the Read tool with the following input","time":{"created":1790416805000}}`},
		{ocMain, "msg_0002", "assistant", `{"agent":"build","model":{"id":"gpt-6-astra","providerID":"openai","variant":"default"},"content":[{"type":"reasoning","text":"…"},{"type":"text","text":"Let me look."}],"cost":0.01,"tokens":{"input":1000,"output":100,"reasoning":20,"cache":{"read":5000,"write":0}},"time":{"created":1790416806000,"completed":1790416830000}}`},
		{ocMain, "msg_0003", "assistant", `{"agent":"build","model":{"id":"codex/gpt-6-luna","providerID":"magpie","variant":"default"},"content":[{"type":"text","text":"It races the clock."}],"cost":0,"tokens":{"input":300,"output":50,"reasoning":10,"cache":{"read":2000,"write":0}},"time":{"created":1790416890000,"completed":1790416980000}}`},
		{ocMain, "msg_0003i", "idle", `{"outcome":"succeeded","time":{"created":1790416980000}}`},
		{ocChild, "msg_0004", "assistant", `{"agent":"general","model":{"id":"claude-opus-5-5","providerID":"anthropic"},"content":[],"cost":0,"tokens":{"input":40,"output":60,"reasoning":0,"cache":{"read":700,"write":300}},"time":{"created":1790416860000,"completed":1790416890000}}`},
	} {
		var d struct {
			Time struct{ Created, Completed int64 }
		}
		json.Unmarshal([]byte(m.data), &d)
		if _, err := db.Exec("INSERT INTO session_message VALUES (?, ?, ?, ?, ?, ?, ?)", m.id, m.sid, m.typ, i, d.Time.Created, max(d.Time.Created, d.Time.Completed), m.data); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestOpenCodeV2(t *testing.T) {
	inZone(t, 0)
	data, _ := setupMore(t)
	db := ocMakeV2(t, filepath.Join(data, "opencode", "opencode.db"))
	// an old session left behind in the old tables that OpenCode 2 no
	// longer has (deleted since it was copied over)
	const gone = "ses_1a2b3c4d5e6fDDDDDDDDDDDDDD"
	if _, err := db.Exec("INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) VALUES (?, '0f3a', 's', '/work/gone', 'Gone', '1.1.53', 1790416800000, 1790416800000)", gone); err != nil {
		t.Fatal(err)
	}
	ocInsert(t, db, gone, "msg_0009", 1790416800000, 1790416810000,
		`{"id":"msg_0009","role":"assistant","modelID":"claude-opus-5-5","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1790416800000,"completed":1790416810000}}`)

	ss := List(0)
	checkOpenCode(t, ss)
	oc := find(t, ss, "opencode", ocMain)
	if filepath.Base(oc.Path) != "opencode.db#"+ocMain {
		t.Fatalf("v2 session: %+v", oc)
	}
	s := statsAt(0, statsNow)
	if got, sec := usageOn(s, "2026-09-26", "/work/oc"); got != (Tokens{1340, 240, 7700, 300}) || sec != 180 {
		t.Fatalf("opencode 2 on the 26th: %+v, %ds active", got, sec)
	}

	// a compaction names no model: it counts on the one last replied with
	if _, err := db.Exec(`INSERT INTO session_message VALUES ('msg_0005', ?, 'compaction', 9, 1790417000000, 1790417010000, ?)`, ocMain,
		`{"status":"failed","reason":"auto","error":{"type":"unknown","message":"x"},"cost":0,"tokens":{"input":5,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1790417000000}}`); err != nil {
		t.Fatal(err)
	}
	oc = find(t, List(0), "opencode", ocMain)
	if l := model(oc, "codex/gpt-6-luna"); l.Tokens != (Tokens{305, 61, 2000, 0}) {
		t.Fatalf("luna after a compaction: %+v", l)
	}

	// while the old sessions are still being copied over, one not copied
	// yet is read from the old tables, one copied only from the new
	if _, err := db.Exec(`UPDATE kv SET value = '{"phase":"sessions","cursor":"x"}'`); err != nil {
		t.Fatal(err)
	}
	ss = List(0)
	if g := find(t, ss, "opencode", gone); g.Cwd != "/work/gone" {
		t.Fatalf("an old session not copied yet: %+v", g)
	}
	n := 0
	for _, s := range ss {
		if s.ID == ocMain {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a copied session read %d times", n)
	}
	if len(dbs) != 0 {
		t.Fatalf("databases left open: %v", dbs)
	}
}

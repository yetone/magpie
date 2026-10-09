package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/klauspost/compress/zstd"
)

const (
	clineMain = "1790503200000_ab12c"
	clineFork = "1790510400000_cd34e"
	qoderID   = "11111111-aaaa-4bbb-8ccc-000000000001"
	qoderCNID = "11111111-aaaa-4bbb-8ccc-000000000002"
)

// setupAgents is setup with ZCode's database made and dsh's, Cline's,
// Qoder's, Grok Build's and WorkBuddy's sessions put in the home folder,
// where each keeps them.
func setupAgents(t *testing.T) (home string) {
	claude, _ := setup(t)
	home = filepath.Join(filepath.Dir(claude), "home")
	copyTree(t, "testdata/dsh", filepath.Join(home, ".dsh"))
	copyTree(t, "testdata/cline", filepath.Join(home, ".cline", "data"))
	copyTree(t, "testdata/qoder", filepath.Join(home, ".qoder"))
	copyTree(t, "testdata/qoder-cn", filepath.Join(home, ".qoder-cn"))
	copyTree(t, "testdata/grok", filepath.Join(home, ".grok"))
	copyTree(t, "testdata/workbuddy", filepath.Join(home, ".workbuddy"))
	copyTree(t, "testdata/omp", filepath.Join(home, ".omp"))
	zcMakeDB(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	return
}

// zcMakeDB makes a ZCode database: OpenCode's tables, the session's with
// title_source, a message's model modelId or modelID, its tokens the AI
// SDK's.
func zcMakeDB(t *testing.T, path string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	db := ocMakeEmpty(t, path, "title_source text")
	for _, s := range []struct{ id, parent, title, source string }{
		{"ses_zcA", "", "Port the parser to Go", "ai"},
		{"ses_zcB", "ses_zcA", "Subagent", "ai"},
		{"ses_zcC", "", "New session", "default"},
	} {
		var parent any
		if s.parent != "" {
			parent = s.parent
		}
		if _, err := db.Exec("INSERT INTO session (id, project_id, parent_id, slug, directory, title, title_source, version, time_created, time_updated) VALUES (?, 'p', ?, 's', '/work/zc', ?, ?, '0.16.9', 1790496000000, 1790496000000)",
			s.id, parent, s.title, s.source); err != nil {
			t.Fatal(err)
		}
	}
	// the first user message ZCode's own, a reminder; the second typed
	ocInsert(t, db, "ses_zcA", "msg_z1", 1790496001000, 1790496001000, `{"id":"msg_z1","role":"user","time":{"created":1790496001000},"semantics":{"origin":"system_reminder"}}`)
	ocInsert(t, db, "ses_zcA", "msg_z2", 1790496002000, 1790496002000, `{"id":"msg_z2","role":"user","time":{"created":1790496002000},"semantics":{"origin":"real_user"}}`)
	ocInsert(t, db, "ses_zcA", "msg_z3", 1790496003000, 1790496030000, `{"id":"msg_z3","role":"assistant","modelId":"glm-5.1","providerID":"magpie","tokens":{"total":1540,"input":1500,"output":40,"reasoning":10,"cache":{"read":1000,"write":200}},"time":{"created":1790496003000,"completed":1790496030000}}`)
	ocInsert(t, db, "ses_zcB", "msg_z4", 1790496010000, 1790496020000, `{"id":"msg_z4","role":"assistant","modelID":"glm-5.1-air","tokens":{"input":50,"output":5,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1790496010000,"completed":1790496020000}}`)
	ocInsert(t, db, "ses_zcC", "msg_z5", 1790496100000, 1790496110000, `{"id":"msg_z5","role":"assistant","modelId":"glm-5.1","tokens":{"input":10,"output":1,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1790496100000,"completed":1790496110000}}`)
	for _, p := range [][3]string{
		{"prt_z1", "msg_z1", "Remember the house rules"},
		{"prt_z2", "msg_z2", "Port the   parser"},
	} {
		if _, err := db.Exec("INSERT INTO part VALUES (?, ?, 'ses_zcA', 0, 0, ?)", p[0], p[1], `{"type":"text","text":"`+p[2]+`"}`); err != nil {
			t.Fatal(err)
		}
	}
}

func count(ss []Session, agent string) int {
	n := 0
	for _, s := range ss {
		if s.Agent == agent {
			n++
		}
	}
	return n
}

func TestZCode(t *testing.T) {
	inZone(t, 0)
	setupAgents(t)
	ss := List(0)
	if n := count(ss, "zcode"); n != 2 {
		t.Fatalf("want 2 ZCode sessions (the subagent in its parent), got %d", n)
	}
	z := find(t, ss, "zcode", "ses_zcA")
	if z.Cwd != "/work/zc" || z.Title != "Port the parser to Go" || z.Resume != "" {
		t.Fatalf("zcode: %+v", z)
	}
	// input without the cache, output with its reasoning already
	if m := model(z, "glm-5.1"); m.Tokens != (Tokens{300, 40, 1000, 200, 0}) {
		t.Fatalf("glm-5.1: %+v", m)
	}
	if m := model(z, "glm-5.1-air"); m.Tokens != (Tokens{50, 5, 0, 0, 0}) {
		t.Fatalf("the subagent's: %+v", m)
	}
	if !z.Start.Equal(at("2026-09-27T08:00:00Z")) || !z.Last.Equal(at("2026-09-27T08:00:30Z")) {
		t.Fatalf("zcode times %s %s", z.Start, z.Last)
	}
	// a default title is no title
	if c := find(t, ss, "zcode", "ses_zcC"); c.Title != "" {
		t.Fatalf("default title kept: %q", c.Title)
	}
	if len(dbs) != 0 {
		t.Fatalf("databases left open: %v", dbs)
	}
}

func checkDsh(t *testing.T, ss []Session) {
	t.Helper()
	if n := count(ss, "dsh"); n != 4 {
		t.Fatalf("want 4 dsh sessions (the subagent in its parent), got %d", n)
	}
	a := find(t, ss, "dsh", "dsh-a")
	if a.Cwd != "/work/dsh" || a.Title != "Build script tidy" || a.Resume != "" {
		t.Fatalf("dsh: %+v", a)
	}
	// the format 3 file, not the one left behind
	if m := model(a, "deepseek-v4-pro"); m.Tokens != (Tokens{100, 20, 1000, 0, 0}) {
		t.Fatalf("pro: %+v", m)
	}
	if m := model(a, "deepseek-v4-flash"); m.Tokens != (Tokens{17, 8, 200, 0, 0}) {
		t.Fatalf("flash, the subagent's in: %+v", m)
	}
	if !a.Start.Equal(at("2026-09-27T10:00:00Z")) || !a.Last.Equal(at("2026-09-27T10:01:00Z")) {
		t.Fatalf("dsh times %s %s", a.Start, a.Last)
	}
	// the forks: what they were seeded with counted where it came from
	c := find(t, ss, "dsh", "dsh-c")
	if c.Tokens != (Tokens{50, 10, 0, 0, 0}) || c.Title != "Tidy the build script" {
		t.Fatalf("format 3 fork: %+v", c)
	}
	if !c.Start.Equal(at("2026-09-27T11:00:00Z")) || !c.Last.Equal(at("2026-09-27T11:00:20Z")) {
		t.Fatalf("fork times %s %s", c.Start, c.Last)
	}
	if d := find(t, ss, "dsh", "dsh-d"); d.Tokens != (Tokens{1, 2, 0, 0, 0}) || !d.Start.Equal(at("2026-09-27T12:00:00Z")) {
		t.Fatalf("format 0 fork: %+v", d)
	}
	s := statsAt(0, statsNow)
	if got, _ := usageOn(s, "2026-09-27", "/work/dsh"); got != (Tokens{898, 137, 6200, 0, 0}) {
		t.Fatalf("dsh on the 27th: %+v", got)
	}
}

func TestDsh(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	checkDsh(t, List(0))

	// a reply more: the session is read again
	p := filepath.Join(home, ".dsh", "sessions", "work-dsh-3f2a", "dsh-a", "session.v3.jsonl")
	more := []byte(`{"type":"assistant/message","seq":6,"time":1790503290000,"data":{"message":{"source":{"model":"deepseek-v4-flash"}},"usage":{"inputTokens":1,"outputTokens":1,"cacheReadTokens":0}}}` + "\n")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, append(b, more...), 0o644)
	a := find(t, List(0), "dsh", "dsh-a")
	if m := model(a, "deepseek-v4-flash"); m.Tokens != (Tokens{18, 9, 200, 0, 0}) || !a.Last.Equal(at("2026-09-27T10:01:30Z")) {
		t.Fatalf("after a new reply: %+v %s", m, a.Last)
	}
	os.WriteFile(p, b, 0o644)

	// compressed, in frames appended a batch at a time
	for _, id := range []string{"dsh-a", "dsh-b", "dsh-c"} {
		p := filepath.Join(home, ".dsh", "sessions", "work-dsh-3f2a", id, "session.v3.jsonl")
		b, _ := os.ReadFile(p)
		enc, _ := zstd.NewWriter(nil)
		half := bytes.IndexByte(b[len(b)/2:], '\n') + len(b)/2 + 1
		z := enc.EncodeAll(b[half:], enc.EncodeAll(b[:half], nil))
		enc.Close()
		os.Remove(p)
		os.WriteFile(p+".zstd", z, 0o644)
	}
	Reset()
	checkDsh(t, List(0))

	// $DSH_HOME, elsewhere
	other := filepath.Join(filepath.Dir(home), "dsh-home")
	if err := os.Rename(filepath.Join(home, ".dsh"), other); err != nil {
		t.Fatal(err)
	}
	Reset()
	if n := count(List(0), "dsh"); n != 0 {
		t.Fatalf("%d dsh sessions with no ~/.dsh", n)
	}
	t.Setenv("DSH_HOME", other)
	Reset()
	checkDsh(t, List(0))
}

func TestCline(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	ss := List(0)
	if n := count(ss, "cline"); n != 2 {
		t.Fatalf("want 2 Cline sessions (the subagent in its session), got %d", n)
	}
	c := find(t, ss, "cline", clineMain)
	// the prompt typed, out of Cline's tags, before its title
	if c.Cwd != "/work/cline" || c.Title != "Importer speed-up" {
		t.Fatalf("cline: %+v", c)
	}
	if m := model(c, "claude-opus-5-5"); m.Tokens != (Tokens{100, 50, 1000, 500, 0}) || !m.Priced {
		t.Fatalf("opus: %+v", m)
	}
	if m := model(c, "claude-haiku-4-5"); m.Tokens != (Tokens{130, 30, 100, 0, 0}) {
		t.Fatalf("haiku, the subagent's in: %+v", m)
	}
	if !c.Start.Equal(at("2026-09-27T10:00:00Z")) || !c.Last.Equal(at("2026-09-27T10:02:00Z")) {
		t.Fatalf("cline times %s %s", c.Start, c.Last)
	}
	if runtime.GOOS != "windows" {
		if want := "cd '/work/cline' && cline --id " + clineMain; c.Resume != want {
			t.Fatalf("resume %q", c.Resume)
		}
	}
	// the fork: what it copied counted in the session it came from
	f := find(t, ss, "cline", clineFork)
	if f.Tokens != (Tokens{100, 30, 300, 0, 0}) || f.Title != "Speed up the importer" {
		t.Fatalf("fork: %+v", f)
	}
	if !f.Start.Equal(at("2026-09-27T12:00:00Z")) || !f.Last.Equal(at("2026-09-27T12:01:00Z")) {
		t.Fatalf("fork times %s %s", f.Start, f.Last)
	}

	// the sessions folder of $CLINE_DIR, and of $CLINE_SESSION_DATA_DIR
	moved := filepath.Join(filepath.Dir(home), "cline-dir")
	if err := os.Rename(filepath.Join(home, ".cline"), moved); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLINE_DIR", moved)
	Reset()
	find(t, List(0), "cline", clineMain)
	t.Setenv("CLINE_SESSION_DATA_DIR", filepath.Join(moved, "none"))
	Reset()
	if n := count(List(0), "cline"); n != 0 {
		t.Fatalf("$CLINE_SESSION_DATA_DIR comes first: %d", n)
	}
}

func TestQoder(t *testing.T) {
	inZone(t, 0)
	home := setupAgents(t)
	ss := List(0)
	for _, c := range []struct{ agent, id, model, run string }{
		{"qoder", qoderID, "qwen3-coder-plus", "qodercli"},
		{"qoder-cn", qoderCNID, "glm-5", "qoderclicn"},
	} {
		q := find(t, ss, c.agent, c.id)
		if q.Cwd != "/work/q" || q.Title != "Explain the cache" {
			t.Fatalf("%s: %+v", c.agent, q)
		}
		if m := model(q, c.model); m.Tokens != (Tokens{45, 30, 300, 0, 0}) {
			t.Fatalf("%s, the subagent's in: %+v", c.agent, m)
		}
		if runtime.GOOS != "windows" {
			if want := "cd '/work/q' && " + c.run + " --resume " + c.id; q.Resume != want {
				t.Fatalf("resume %q", q.Resume)
			}
		}
	}
	moved := filepath.Join(filepath.Dir(home), "qcn")
	os.Rename(filepath.Join(home, ".qoder-cn"), moved)
	t.Setenv("QODERCN_CONFIG_DIR", moved)
	Reset()
	find(t, List(0), "qoder-cn", qoderCNID)
}

func TestDirsMore(t *testing.T) {
	home := setupAgents(t)
	d := Dirs()
	want := []string{filepath.Join(home, ".zcode"), filepath.Join(home, ".dsh"), filepath.Join(home, ".cline", "data", "sessions"), filepath.Join(home, ".qoder"), filepath.Join(home, ".qoder-cn"),
		filepath.Join(home, ".grok"), filepath.Join(home, ".workbuddy"), filepath.Join(home, ".omp", "agent")}
	if len(d) != 2+len(want) {
		t.Fatalf("dirs %v", d)
	}
	for i, w := range want {
		if d[2+i] != w {
			t.Fatalf("dirs %v, want %v after Claude Code's and Codex's", d, want)
		}
	}
}

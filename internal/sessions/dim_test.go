package sessions

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	_ "modernc.org/sqlite"
)

// dimSchema is what of Dim alpha's store (format 53) magpie reads, each
// table as dim-app-server's storage/sqlite.rs makes it, word for word.
var dimSchema = []string{
	"CREATE TABLE agent_store_format (version INTEGER NOT NULL PRIMARY KEY CHECK (version = 53))",
	"CREATE TABLE agent_sessions (session_id TEXT NOT NULL PRIMARY KEY, config TEXT NOT NULL, created_at_ms INTEGER NOT NULL CHECK(created_at_ms >= 0), leader_session_id TEXT REFERENCES agent_sessions(session_id), origin TEXT NOT NULL DEFAULT 'unknown' CHECK (origin IN ('client', 'acp', 'internal', 'unknown', 'automation')))",
	"CREATE INDEX agent_sessions_by_leader ON agent_sessions(leader_session_id)",
	"CREATE TABLE timeline_branches (session_id TEXT NOT NULL, branch_id TEXT NOT NULL, info TEXT NOT NULL, sequence INTEGER NOT NULL CHECK (sequence >= 0), PRIMARY KEY (session_id, branch_id), FOREIGN KEY (session_id) REFERENCES agent_sessions(session_id))",
	"CREATE TABLE timeline_content (session_id TEXT NOT NULL, hash TEXT NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY (session_id, hash), FOREIGN KEY (session_id) REFERENCES agent_sessions(session_id))",
	"CREATE TABLE timeline_events (session_id TEXT NOT NULL, branch_id TEXT NOT NULL, sequence INTEGER NOT NULL CHECK (sequence > 0), last_sequence INTEGER NOT NULL CHECK (last_sequence >= sequence), kind TEXT NOT NULL, body TEXT NOT NULL, event_id TEXT, context_render INTEGER NOT NULL DEFAULT 0, context_state INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (session_id, branch_id, sequence), FOREIGN KEY (session_id, branch_id) REFERENCES timeline_branches(session_id, branch_id))",
	"CREATE INDEX timeline_events_users ON timeline_events(session_id, branch_id, kind, json_type(body,'$.record.event.data.user'), sequence) WHERE json_type(body,'$.record.event.data.user') IS NOT NULL",
	"CREATE TABLE session_metadata (session_id TEXT NOT NULL PRIMARY KEY REFERENCES agent_sessions(session_id), revision INTEGER NOT NULL CHECK (revision > 0), title TEXT, title_source TEXT CHECK (title_source IN ('auto', 'manual')), CHECK ((title IS NULL) = (title_source IS NULL)))",
	"CREATE TABLE session_catalog (session_id TEXT NOT NULL PRIMARY KEY REFERENCES agent_sessions(session_id), updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms >= 0), preview TEXT, cwd TEXT NOT NULL)",
	"CREATE INDEX session_catalog_by_activity ON session_catalog(updated_at_ms DESC, session_id)",
}

const (
	dimShop   = "ses_01JDIMSHOP0000000000000001"  // two prompts, tools, a member
	dimMember = "ses_01JDIMMEMBER000000000000002" // the shop's member (collaboration)
	dimFork   = "ses_01JDIMFORK0000000000000003"  // forked from the shop after its first turn
	dimIdle   = "ses_01JDIMIDLE0000000000000004"  // opened, a message from an agent, no prompt
	dimBase   = int64(1795000000000)              // 2026-11-18 11:06:40 UTC
)

// dimWriter writes a Dim store as Dim's daemon writes it.
type dimWriter struct {
	t   *testing.T
	db  *sql.DB
	seq map[string]int64 // session\x00branch → its head
}

func (w *dimWriter) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.db.Exec(q, args...); err != nil {
		w.t.Fatal(err)
	}
}

// session is agent_sessions' row, its main line, and its catalog row
// (cwd, and a preview when not "").
func (w *dimWriter) session(id, leader, origin, model string, at int64, cwd, preview string) {
	w.t.Helper()
	var l, p any
	if leader != "" {
		l = leader
	}
	if preview != "" {
		p = preview
	}
	config := `{"model":{"providerId":"magpie","modelId":"` + model + `"},"cwd":"` + cwd + `","permissionMode":"default"}`
	w.exec(`INSERT INTO agent_sessions (session_id, config, created_at_ms, leader_session_id, origin) VALUES (?, ?, ?, ?, ?)`, id, config, at, l, origin)
	w.exec(`INSERT INTO timeline_branches (session_id, branch_id, info, sequence) VALUES (?, 'main', '{}', 0)`, id)
	w.exec(`INSERT INTO session_catalog (session_id, updated_at_ms, preview, cwd) VALUES (?, ?, ?, ?)`, id, at, p, cwd)
}

func (w *dimWriter) next(id, branch string) int64 {
	k := id + "\x00" + branch
	w.seq[k]++
	w.exec(`UPDATE timeline_branches SET sequence = ? WHERE session_id = ? AND branch_id = ?`, w.seq[k], id, branch)
	return w.seq[k]
}

func dimEvent(seq int64, kind string, at int64, data string, extra string) string {
	typ, name, _ := strings.Cut(kind, "/")
	return `{"id":"evt_` + dimItoa(seq) + `","sequence":` + dimItoa(seq) + `,"create_at":` + dimItoa(at) +
		extra + `,"type":"` + typ + `","name":"` + name + `","data":` + data + `}`
}

func dimItoa(n int64) string { return strconv.FormatInt(n, 10) }

// event writes one event row on id's main line.
func (w *dimWriter) event(id, kind string, at int64, data string) {
	w.eventOn(id, "main", kind, at, data, "")
}

func (w *dimWriter) eventOn(id, branch, kind string, at int64, data, extra string) {
	w.t.Helper()
	seq := w.next(id, branch)
	body := `{"encoding":"event","record":{"event":` + dimEvent(seq, kind, at, data, extra) + `,"refs":null,"trace":null,"batch_end":` + dimItoa(seq) + `}}`
	w.exec(`INSERT INTO timeline_events (session_id, branch_id, sequence, last_sequence, kind, body, event_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, branch, seq, seq, kind, body, "evt_"+dimItoa(seq))
	w.exec(`UPDATE session_catalog SET updated_at_ms = MAX(updated_at_ms, ?) WHERE session_id = ?`, at, id)
}

// output writes the deltas of one request as Dim keeps them: one row,
// its sequence the first delta's and last_sequence the last's.
func (w *dimWriter) output(id, kind, turn, req string, at int64, deltas ...string) {
	w.t.Helper()
	first := w.seq[id+"\x00main"] + 1
	var recs []string
	for i, d := range deltas {
		seq := w.next(id, "main")
		data, _ := json.Marshal(map[string]string{"turn_id": turn, "request_id": req, "delta": d})
		recs = append(recs, `{"event":`+dimEvent(seq, kind, at+int64(i)*10, string(data), "")+`,"refs":null,"trace":null,"batch_end":`+dimItoa(seq)+`}`)
	}
	last := w.seq[id+"\x00main"]
	body := `{"encoding":"output","turn_id":"` + turn + `","request_id":"` + req + `","started_at":` + dimItoa(at) + `,"records":[` + strings.Join(recs, ",") + `]}`
	w.exec(`INSERT INTO timeline_events (session_id, branch_id, sequence, last_sequence, kind, body, event_id) VALUES (?, 'main', ?, ?, ?, ?, NULL)`,
		id, first, last, kind, body)
}

func dimUser(texts ...string) string {
	var blocks []map[string]string
	for _, t := range texts {
		blocks = append(blocks, map[string]string{"type": "text", "text": t})
	}
	b, _ := json.Marshal(map[string]any{"submission_id": "sub_1", "user": map[string]any{"content": blocks}})
	return string(b)
}

func dimCall(turn, req, model string) string {
	return `{"turn_id":"` + turn + `","request_id":"` + req + `","request":{"providerId":"magpie","modelId":"` + model + `","messages":[],"tools":[]}}`
}

func dimUsage(turn, req string, prompt, completion, read, write int) string {
	u := map[string]any{"promptTokens": prompt, "completionTokens": completion, "totalTokens": prompt + completion}
	if read > 0 {
		u["cacheReadTokens"] = read
	}
	if write > 0 {
		u["cacheWriteTokens"] = write
	}
	b, _ := json.Marshal(map[string]any{"turn_id": turn, "request_id": req, "usage": u})
	return string(b)
}

// dimFixture is a Dim store under the test's home, written as Dim's
// daemon writes it, in WAL mode (journal "wal" for it, "delete" for a
// rollback journal), and the writer, for a test to go on writing.
// No Dim store was on a test machine, so the rows are made from Dim's
// source: the stored row (storage/timeline.rs' Row, codec.rs' WireEvent),
// the events (dim-agent-types' events.rs: InputMessageData's user
// content, a model call's request, ModelUsage's camelCase tokens with
// the cache inside promptTokens, a tool-call's calls, a
// tool-completed's result) and the catalog and metadata rows
// (session_store.rs, catalog.rs).
func dimFixture(t *testing.T, journal string) (string, *dimWriter) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".dimcode-alpha", "rust")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dimcode.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	w := &dimWriter{t: t, db: db, seq: map[string]int64{}}
	w.exec("PRAGMA journal_mode=" + journal)
	w.exec("PRAGMA busy_timeout=3000")
	for _, q := range dimSchema {
		w.exec(q)
	}
	w.exec(`INSERT INTO agent_store_format (version) VALUES (53)`)

	b := dimBase
	w.session(dimShop, "", "client", "claude-opus-5-5", b, "/Users/dev/work/shop", "列出这个项目里的 Go 文件")
	w.exec(`INSERT INTO session_metadata (session_id, revision, title, title_source) VALUES (?, 1, 'Go 文件清单', 'auto')`, dimShop)
	// a line the agent opened and left: no events of its own
	w.exec(`INSERT INTO timeline_branches (session_id, branch_id, info, sequence) VALUES (?, 'br_side', '{}', 0)`, dimShop)
	w.event(dimShop, "input/message", b+1000, dimUser("列出这个项目里的 Go 文件", `<dim-skill name="go">Use go list.</dim-skill>`))
	w.event(dimShop, "model/call", b+1100, dimCall("turn_1", "req_1", "claude-opus-5-5"))
	w.output(dimShop, "step/thinking", "turn_1", "req_1", b+1200, "I should ", "list the folder.")
	w.output(dimShop, "step/text", "turn_1", "req_1", b+2000, "我先看看", "目录。")
	w.event(dimShop, "step/tool-use", b+2100, `{"turn_id":"turn_1","request_id":"req_1","call":{"id":"call_1","name":"list_dir","input":{"path":"."}}}`)
	w.event(dimShop, "model/usage", b+2150, dimUsage("turn_1", "req_1", 1500, 60, 800, 100))
	w.event(dimShop, "step/tool-call", b+2200, `{"turn_id":"turn_1","calls":[{"id":"call_1","name":"list_dir","input":{"path":"."}}]}`)
	w.event(dimShop, "step/tool-completed", b+2300, `{"turn_id":"turn_1","call_event_id":"evt_x","call_id":"call_1","result":{"content":[{"type":"text","text":"main.go\nshop.go\nREADME.md"}],"isError":false}}`)
	w.event(dimShop, "step/tool-result", b+2400, `{"turn_id":"turn_1","results":[{"call_id":"call_1","result":{"content":[{"type":"text","text":"main.go\nshop.go\nREADME.md"}],"isError":false}}]}`)
	w.event(dimShop, "model/call", b+3000, dimCall("turn_1", "req_2", "claude-opus-5-5"))
	w.output(dimShop, "step/text", "turn_1", "req_2", b+3500, "两个 Go 文件：", "main.go 和 shop.go。")
	w.event(dimShop, "model/usage", b+3600, dimUsage("turn_1", "req_2", 2000, 40, 1500, 0))
	// the fork's cut, after the first turn: its rows are copied up to here
	cut := w.seq[dimShop+"\x00main"]

	w.event(dimShop, "input/message", b+60_000, dimUser("再派一个成员数行数"))
	w.event(dimShop, "model/call", b+60_100, dimCall("turn_2", "req_3", "claude-opus-5-5"))
	w.event(dimShop, "step/tool-call", b+60_200, `{"turn_id":"turn_2","calls":[{"id":"call_2","name":"agent","input":{"action":"spawn","task":"Count the lines of main.go and shop.go"}}]}`)
	w.event(dimShop, "model/usage", b+60_300, dimUsage("turn_2", "req_3", 100, 10, 0, 0))
	// a call's own events (its parent_call_id): no words of the user's
	w.eventOn(dimShop, "main", "input/message", b+60_210, dimUser("a nested run's task"), `,"parent_call_id":"call_2"`)
	w.eventOn(dimShop, "main", "step/text", b+60_220, `{"turn_id":"turn_n","request_id":"req_n","delta":"a nested run's words"}`, `,"parent_call_id":"call_2"`)
	// the member's report comes back as a raw block: not the user's words
	w.event(dimShop, "input/message", b+90_000, `{"blocks":[{"type":"raw","text":"<dim-agent-report agent=\"counter\" session=\"`+dimMember+`\">\nStatus: completed.\n</dim-agent-report>"}],"mode":"steer"}`)
	w.event(dimShop, "model/call", b+90_100, dimCall("turn_2", "req_4", "claude-opus-5-5"))
	w.output(dimShop, "step/text", "turn_2", "req_4", b+90_200, "main.go 有 12 行，shop.go 有 30 行。")
	w.event(dimShop, "model/usage", b+90_300, dimUsage("turn_2", "req_4", 400, 25, 0, 0))
	// the same usage said again: counted once
	w.event(dimShop, "model/usage", b+90_310, dimUsage("turn_2", "req_4", 400, 25, 0, 0))

	// the member: the leader's task is its input, its tools and tokens
	// the leader's session's; its request ids are its own (req_1 again)
	w.session(dimMember, dimShop, "internal", "claude-haiku-5", b+60_250, "/Users/dev/work/shop", "Count the lines of main.go and shop.go")
	w.event(dimMember, "input/message", b+60_300, dimUser("Count the lines of main.go and shop.go"))
	w.event(dimMember, "model/call", b+60_400, dimCall("turn_m", "req_1", "claude-haiku-5"))
	w.event(dimMember, "step/tool-call", b+60_500, `{"turn_id":"turn_m","calls":[{"id":"call_m","name":"read_file","input":{"path":"main.go"}}]}`)
	w.event(dimMember, "model/usage", b+60_600, dimUsage("turn_m", "req_1", 300, 20, 0, 0))
	w.output(dimMember, "step/text", "turn_m", "req_1", b+61_000, "main.go: 12 lines, shop.go: 30 lines")

	// the fork: the shop's main line copied through the cut, then its
	// session/forked, then its own turn; no title of its own yet
	w.session(dimFork, "", "client", "claude-opus-5-5", b+120_000, "/Users/dev/work/shop", "")
	rows, err := db.Query(`SELECT sequence, last_sequence, kind, body, event_id FROM timeline_events WHERE session_id = ? AND branch_id = 'main' AND sequence <= ? ORDER BY sequence`, dimShop, cut)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		seq, last  int64
		kind, body string
		id         sql.NullString
	}
	var copied []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.seq, &r.last, &r.kind, &r.body, &r.id); err != nil {
			t.Fatal(err)
		}
		copied = append(copied, r)
	}
	rows.Close()
	for _, r := range copied {
		w.exec(`INSERT INTO timeline_events (session_id, branch_id, sequence, last_sequence, kind, body, event_id) VALUES (?, 'main', ?, ?, ?, ?, ?)`, dimFork, r.seq, r.last, r.kind, r.body, r.id)
	}
	w.seq[dimFork+"\x00main"] = cut
	w.event(dimFork, "session/forked", b+120_000, `{}`)
	w.event(dimFork, "input/message", b+121_000, dimUser("换个方式：用 find 列"))
	w.event(dimFork, "model/call", b+121_100, dimCall("turn_f", "req_9", "claude-opus-5-5"))
	w.output(dimFork, "step/text", "turn_f", "req_9", b+121_200, "好的。")
	w.event(dimFork, "model/usage", b+121_300, dimUsage("turn_f", "req_9", 50, 5, 0, 0))

	// opened, and only an agent's message came: no prompt, not listed
	w.session(dimIdle, "", "client", "claude-opus-5-5", b+200_000, "/Users/dev/work/idle", "")
	w.event(dimIdle, "input/message", b+200_100, `{"blocks":[{"type":"raw","text":"<dim-agent-message agent=\"leader\">\nhi\n</dim-agent-message>"}],"mode":"steer"}`)
	return path, w
}

func dimListed(t *testing.T, list []Session, id string) Session {
	t.Helper()
	for _, s := range list {
		if s.Agent == "dim" && s.ID == id {
			return s
		}
	}
	t.Fatalf("Dim session %q not listed: %+v", id, list)
	return Session{}
}

func TestDimSessions(t *testing.T) {
	setup(t)
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".dimcode-alpha")
	if DimDir() != dir {
		t.Fatalf("DimDir() = %q, want %q", DimDir(), dir)
	}
	path, w := dimFixture(t, "wal")
	before := catpawBytes(t, path)

	list := List(0)
	a := dimListed(t, list, dimShop)
	if !a.ReadOnly || a.Title != "Go 文件清单" || a.Cwd != "/Users/dev/work/shop" {
		t.Errorf("shop: %+v", a)
	}
	// promptTokens holds the cache: 1500-800-100, 2000-1500, 100, 400 once
	if got, want := model(a, "claude-opus-5-5").Tokens, (Tokens{Input: 600 + 500 + 100 + 400, Output: 60 + 40 + 10 + 25, CacheRead: 800 + 1500, CacheWrite: 100}); got != want {
		t.Errorf("shop tokens = %+v, want %+v", got, want)
	}
	// the member's, under its leader, by its own model
	if got := model(a, "claude-haiku-5").Tokens; got != (Tokens{Input: 300, Output: 20}) {
		t.Errorf("shop's member tokens = %+v (%+v)", got, a.Models)
	}
	if !a.Start.Equal(time.UnixMilli(dimBase+1000)) || !a.Last.Equal(time.UnixMilli(dimBase+90_310)) {
		t.Errorf("shop times %v – %v", a.Start, a.Last)
	}
	f := dimListed(t, list, dimFork)
	if f.Title != "换个方式：用 find 列" {
		t.Errorf("fork: %+v", f)
	}
	// only the fork's own turn: the copied first turn is the shop's
	if len(f.Models) != 1 || model(f, "claude-opus-5-5").Tokens != (Tokens{Input: 50, Output: 5}) {
		t.Errorf("fork tokens: %+v", f.Models)
	}
	for _, s := range list {
		if s.Agent == "dim" && (s.ID == dimMember || s.ID == dimIdle) {
			t.Errorf("a member or a session with no prompt was listed: %+v", s)
		}
	}

	managed := ListAgent("dim")
	if len(managed) != 2 {
		t.Fatalf("ListAgent(dim) = %+v", managed)
	}
	for _, x := range managed {
		if x.Deletable || !x.Transcript || x.Size == 0 {
			t.Errorf("a Dim session on the page: %+v", x)
		}
	}
	if _, err := Delete("dim", dimShop); err == nil {
		t.Error("Delete took a session out of Dim's database")
	}

	st := StatsAt(0, time.UnixMilli(dimBase).Add(24*time.Hour))
	var shop, fork Summary
	for _, s := range st.Sessions {
		switch s.Key {
		case "dim:" + dimShop:
			shop = s
		case "dim:" + dimFork:
			fork = s
		}
	}
	if shop.Prompts != 2 || shop.Replies != 2 || shop.ToolCalls != 3 || shop.tools.of("list_dir") != 1 || shop.tools.of("agent") != 1 || shop.tools.of("read_file") != 1 {
		t.Errorf("shop stats: prompts %d replies %d tools %d %v", shop.Prompts, shop.Replies, shop.ToolCalls, shop.tools)
	}
	if fork.Prompts != 1 || fork.Replies != 1 || fork.ToolCalls != 0 {
		t.Errorf("fork stats: prompts %d replies %d tools %d", fork.Prompts, fork.Replies, fork.ToolCalls)
	}

	found := false
	for _, d := range Dirs() {
		found = found || d == dir
	}
	if !found {
		t.Errorf("Dirs() %v leaves out Dim's", Dirs())
	}
	if !slices.Contains(Found(), "dim") {
		t.Errorf("Found() %v leaves out Dim", Found())
	}
	if !bytes.Equal(before, catpawBytes(t, path)) {
		t.Error("reading Dim's sessions changed its database")
	}

	// Dim goes on: the fork's second prompt, and the title it names it by
	w.event(dimFork, "input/message", dimBase+130_000, dimUser("再试一次"))
	w.exec(`INSERT INTO session_metadata (session_id, revision, title, title_source) VALUES (?, 1, '用 find 列文件', 'auto')`, dimFork)
	f = dimListed(t, List(0), dimFork)
	if f.Title != "用 find 列文件" || !f.Last.Equal(time.UnixMilli(dimBase+130_000)) {
		t.Errorf("fork after Dim wrote on: %+v", f)
	}
}

// A Dim session is shown as Dim shows its main line: the prompts (not
// the skill a capability put after them), the thinking, the replies and
// each tool with what it was given and what it answered, once; not the
// member's own session.
func TestDimTranscript(t *testing.T) {
	setup(t)
	path, _ := dimFixture(t, "wal")
	before := catpawBytes(t, path)
	shop, ok := Find("dim", dimShop)
	if !ok {
		t.Fatal("Find(dim, shop) found nothing")
	}
	got := ombParts(t, shop)
	want := []string{
		"user/text/: 列出这个项目里的 Go 文件",
		"assistant/thinking/: I should list the folder.",
		"assistant/text/: 我先看看目录。",
		"assistant/tool_use/list_dir: {\n  \"path\": \".\"\n}",
		"tool/tool_result/list_dir: main.go\nshop.go\nREADME.md",
		"assistant/text/: 两个 Go 文件：main.go 和 shop.go。",
		"user/text/: 再派一个成员数行数",
		"assistant/tool_use/agent: {\n  \"action\": \"spawn\",\n  \"task\": \"Count the lines of main.go and shop.go\"\n}",
		"assistant/text/: main.go 有 12 行，shop.go 有 30 行。",
	}
	if !slices.Equal(got, want) {
		t.Errorf("shop transcript:\n%s", strings.Join(got, "\n"))
	}
	for q, want := range map[string]bool{"再派一个": true, "README.md": true, "go list": false} {
		if _, found, err := SearchSession(context.Background(), shop, NeedleOf(q)); err != nil || found != want {
			t.Errorf("search %q: found %v (%v), want %v", q, found, err, want)
		}
	}
	if !bytes.Equal(before, catpawBytes(t, path)) {
		t.Error("showing a Dim session changed its database")
	}
}

// Reads("dim") is the gate: with Dim's sessions not allowed, nothing of
// its database is listed.
func TestDimNotReadUnlessAllowed(t *testing.T) {
	setup(t)
	dimFixture(t, "wal")
	if len(dimFiles()) != 2 {
		t.Fatalf("dimFiles() = %+v, want two sessions", dimFiles())
	}
	if err := SetReads(nil, map[string]bool{"dim": false}); err != nil {
		t.Fatal(err)
	}
	if fs := dimFiles(); len(fs) != 0 {
		t.Errorf("Dim's database read with Dim not allowed: %+v", fs)
	}
	for _, s := range List(0) {
		if s.Agent == "dim" {
			t.Fatalf("listed with Dim not allowed: %+v", s)
		}
	}
}

// A read Dim's daemon keeps out (a write holding the database) is not
// "no sessions": the last listing stands, cold, with what was read of
// each session, and the next read that can is read again.
func TestDimBusyKeepsLastRead(t *testing.T) {
	setup(t)
	_, w := dimFixture(t, "delete")
	first := List(0)
	a := dimListed(t, first, dimShop)
	var shop file
	for _, f := range dimFiles() {
		if f.m().sid == dimShop {
			shop = f
		}
	}
	old := parseDim(shop, nil)
	if old.Size == 0 || len(old.Days) == 0 {
		t.Fatalf("parseDim(shop) = %+v", old)
	}

	// Dim's writer holds the database, mid-write
	conn, err := w.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `UPDATE session_metadata SET title = 'half', revision = 2 WHERE session_id = ?`, dimShop); err != nil {
		t.Fatal(err)
	}
	fs := dimFiles()
	if len(fs) != 2 {
		t.Fatalf("a busy database read as %d sessions", len(fs))
	}
	for _, f := range fs {
		if !f.cold {
			t.Errorf("a session listed from a busy database is not cold: %+v", f)
		}
	}
	// a session listed before Dim took the database: its last parse
	if got := parseDim(shop, old); got != old {
		t.Errorf("a parse that couldn't read gave %+v, not the last one", got)
	}
	b := dimListed(t, List(0), dimShop)
	if b.Title != a.Title || model(b, "claude-opus-5-5").Tokens != model(a, "claude-opus-5-5").Tokens {
		t.Errorf("busy: %+v, was %+v", b, a)
	}

	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	for _, f := range dimFiles() {
		if f.cold {
			t.Errorf("still cold once Dim let go: %+v", f)
		}
	}

	// the file gone: no sessions, and no listing kept for it
	conn.Close()
	w.db.Close()
	home, _ := os.UserHomeDir()
	if err := os.RemoveAll(filepath.Join(home, ".dimcode-alpha")); err != nil {
		t.Fatal(err)
	}
	if fs := dimFiles(); len(fs) != 0 {
		t.Errorf("listed with Dim's database gone: %+v", fs)
	}
}

// Dim's folder is $DIMCODE_HOME as given, else .dimcode-alpha in the home
// folder dim-cli's default_home takes: USERPROFILE on Windows, HOME
// elsewhere.
func TestDimDir(t *testing.T) {
	env := map[string]string{"HOME": "/home/dev", "USERPROFILE": `C:\Users\dev`}
	get := func(k string) string { return env[k] }
	if got, want := dimDirOn("windows", get), filepath.Join(`C:\Users\dev`, ".dimcode-alpha"); got != want {
		t.Errorf("windows: %q, want %q", got, want)
	}
	if got, want := dimDirOn("linux", get), filepath.Join("/home/dev", ".dimcode-alpha"); got != want {
		t.Errorf("linux: %q, want %q", got, want)
	}
	if got := dimDirOn("windows", func(k string) string { return map[string]string{"HOME": "/home/dev"}[k] }); got != "" {
		t.Errorf("windows with no USERPROFILE: %q", got)
	}
	if got := dimDBIn(""); got != "" {
		t.Errorf("dimDBIn(\"\") = %q", got)
	}
	env["DIMCODE_HOME"] = ` D:\dim `
	if got := dimDirOn("windows", get); got != `D:\dim` {
		t.Errorf("DIMCODE_HOME: %q", got)
	}
}

func TestDimPromptText(t *testing.T) {
	for in, want := range map[string]string{
		dimUser("hi", `<dim-skill name="go">x</dim-skill>`, "and this"): "hi\nand this",
		dimUser(`<dim-hooks kind="prompt-submit">pasted</dim-hooks>`):   `<dim-hooks kind="prompt-submit">pasted</dim-hooks>`,
		dimUser("a", "<dim-notification>\ndone\n</dim-notification>"):   "a",
		dimUser("a", "<dim-skilled>mine</dim-skilled"):                  "a\n<dim-skilled>mine</dim-skilled",
		`{"text":"legacy"}`: "legacy",
	} {
		ev := gjson.Parse(`{"data":` + in + `}`)
		if got, _ := dimPromptOf(ev); got != want {
			t.Errorf("dimPromptOf(%s) = %q, want %q", in, got, want)
		}
	}
}

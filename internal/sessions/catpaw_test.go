package sessions

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// catpawSchema is CatPaw 2.x's two tables as its HistoryServiceAdapter
// makes them (main.c98fb807.js of catpawai.dmg's mt-idekit.mt-idekit-code
// extension: the conversation and message services' initialize), word
// for word.
const catpawSchema = `
                CREATE TABLE IF NOT EXISTS t_conversations (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    conversation_id TEXT NOT NULL UNIQUE,
                    parent_conversation_id TEXT DEFAULT NULL,
                    title TEXT DEFAULT NULL,
                    mis TEXT NOT NULL,
                    version TEXT NOT NULL,
                    starred INTEGER DEFAULT 0,
                    workspace_id TEXT DEFAULT NULL,
                    ide_type TEXT DEFAULT NULL,
                    create_time INTEGER DEFAULT (strftime('%s', 'now') * 1000),
                    update_time INTEGER NOT NULL
                );

                CREATE INDEX IF NOT EXISTS idx_conversation_id ON t_conversations (conversation_id);
                CREATE INDEX IF NOT EXISTS idx_mis ON t_conversations (mis);
                CREATE INDEX IF NOT EXISTS idx_workspace_id ON t_conversations (workspace_id);
                CREATE INDEX IF NOT EXISTS idx_starred ON t_conversations (starred);
                CREATE INDEX IF NOT EXISTS idx_ide_type ON t_conversations (ide_type);
                CREATE INDEX IF NOT EXISTS idx_parent_conversation_id ON t_conversations (parent_conversation_id);
                CREATE INDEX IF NOT EXISTS idx_create_time ON t_conversations (create_time);
                CREATE INDEX IF NOT EXISTS idx_update_time ON t_conversations (update_time);

                CREATE TABLE IF NOT EXISTS t_ui_messages (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    conversation_id TEXT NOT NULL,
                    message_id TEXT NOT NULL,
                    message_type TEXT NOT NULL,
                    message_sub_type TEXT DEFAULT NULL,
                    sub_conversation_id TEXT DEFAULT NULL,
                    content TEXT DEFAULT NULL,
                    create_time INTEGER DEFAULT (strftime('%s', 'now') * 1000),
                    update_time INTEGER DEFAULT (strftime('%s', 'now') * 1000),
                    status INTEGER DEFAULT 1
                );

                CREATE INDEX IF NOT EXISTS idx_message_conversation_id ON t_ui_messages (conversation_id);
                CREATE INDEX IF NOT EXISTS idx_message_id ON t_ui_messages (message_id);
                CREATE INDEX IF NOT EXISTS idx_message_type ON t_ui_messages (message_type);
                CREATE INDEX IF NOT EXISTS idx_message_sub_type ON t_ui_messages (message_sub_type);
                CREATE INDEX IF NOT EXISTS idx_sub_conversation_id ON t_ui_messages (sub_conversation_id);
                CREATE INDEX IF NOT EXISTS idx_message_status ON t_ui_messages (status);
                CREATE INDEX IF NOT EXISTS idx_message_create_time ON t_ui_messages (create_time);
`

const (
	cpShop  = "conv-1795000000000-a1b2c3"     // two prompts, a tool, a subagent
	cpSub   = "conv-1795000060000-sub-d4e5f6" // the subagent's own conversation
	cpMulti = "conv-1795000300000-g7h8i9"     // a .code-workspace window
	cpEmpty = "conv-1795000400000-j0k1l2"     // opened and left: no prompt
	cpBase  = int64(1795000000000)            // 2026-11-18 11:06:40 UTC
)

// catpawDB is a CatPaw database written as CatPaw writes it, open in WAL
// mode as CatPaw keeps it, and the writer, for a test to go on writing.
// No conversation could be had in a signed-in CatPaw, so the rows are
// made the way the adapter makes them: ensureConversationExists's row
// (mis "unknown" signed out, the version, workspace_id the window's
// folder), and agentMessageToMessageData's, the message object's JSON
// with each class's own fields (UserPromptMessage's content and
// submitEditorState, a text's content, tokenUsage, responseId and
// actualUseModelName, ThinkingMessage's thinkingData, BaseToolMessage's
// tooluseName, blockData, toolAskStatus, toolResultData and expandContent).
func catpawFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".sankuai", "CatPawAI", "sqliteDB")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "globalCache.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=3000", catpawSchema} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	conv := func(id, parent, title, ws string, at int64) {
		t.Helper()
		var p any
		if parent != "" {
			p = parent
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO t_conversations
            (conversation_id, parent_conversation_id, title, mis, version, starred, workspace_id, ide_type, create_time, update_time)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, p, title, "unknown", "2.3.1", 0, ws, nil, at, at); err != nil {
			t.Fatal(err)
		}
	}
	conv(cpShop, "", "列出这个项目里的 Go 文件", "/Users/dev/work/shop", cpBase)
	conv(cpSub, cpShop, "Count the lines of main.go and shop.go", "/Users/dev/work/shop", cpBase+60_000)
	conv(cpMulti, "", "新对话", "/Users/dev/work/multi/multi.code-workspace", cpBase+300_000)
	conv(cpEmpty, "", "新对话", "defaultWorkspace", cpBase+400_000)

	use := func(prompt, completion int, more string) string {
		return `{"prompt_tokens":` + itoa(prompt) + `,"completion_tokens":` + itoa(completion) + `,"total_tokens":` + itoa(prompt+completion) + more + `}`
	}
	type msg struct {
		conv, typ, sub string
		at             int64
		js             string
	}
	n := 0
	add := func(m msg) {
		t.Helper()
		n++
		var obj map[string]any
		if err := json.Unmarshal([]byte(m.js), &obj); err != nil {
			t.Fatalf("message %d: %v", n, err)
		}
		id := "msg-" + itoa(n)
		obj["messageId"], obj["messageType"] = id, m.typ
		b, _ := json.Marshal(obj)
		var sub any
		if m.sub != "" {
			sub = m.sub
		}
		if _, err := db.Exec(`INSERT INTO t_ui_messages
                (conversation_id, message_id, message_type, message_sub_type, sub_conversation_id, content, create_time, update_time, status)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.conv, id, m.typ, nil, sub, string(b), m.at, m.at, 1); err != nil {
			t.Fatal(err)
		}
	}
	chat, think := "LongCat-Flash-Chat", "LongCat-Flash-Thinking"
	u1 := use(1200, 60, `,"cache_read_tokens":800`)
	for _, m := range []msg{
		{cpShop, "user_prompt", "", cpBase + 1000, `{"role":"user","content":"列出这个项目里的 Go 文件","streamStatus":"completed","isSlice":false,"userSendTime":1795000001000,
			"submitEditorState":{"contentText":"列出这个项目里的 Go 文件","selectedModelName":"LongCat-Flash-Chat","triggerMode":"agent"},
			"workingRules":[{"name":"go.md","description":"Go rules","content":"Use gofmt."}]}`},
		// the first response streams: its thinking carries the usage seen
		// so far, its text and tool the last
		{cpShop, "thinking", "", cpBase + 2000, `{"role":"assistant","thinkingData":{"content":"I should list the folder first.","signature":""},
			"tokenUsage":` + use(1200, 30, `,"cache_read_tokens":800`) + `,"actualUseModelName":"` + chat + `","responseId":"resp-1"}`},
		{cpShop, "text", "", cpBase + 3000, `{"role":"assistant","content":"我先看看目录。","tokenUsage":` + u1 + `,"actualUseModelName":"` + chat + `","responseId":"resp-1","phase":"main"}`},
		{cpShop, "tool", "", cpBase + 4000, `{"role":"user","tooluseName":"list_dir","toolAskStatus":"tool_execute_success","askMode":false,
			"blockData":{"name":"list_dir","params":{"path":"."},"toolCallId":"call_1","partial":false},
			"toolResultData":"main.go\nshop.go\nREADME.md","expandContent":"Selected tool: list_dir\n<tool_execute_result>\nmain.go\nshop.go\nREADME.md\n</tool_execute_result>",
			"tokenUsage":` + u1 + `,"actualUseModelName":"` + chat + `","responseId":"resp-1"}`},
		// an OpenAI-shaped usage: the cache read is inside prompt_tokens
		{cpShop, "text", "", cpBase + 5000, `{"role":"assistant","content":"两个 Go 文件：main.go 和 shop.go。","tokenUsage":` + use(1500, 40, `,"prompt_tokens_details":{"cached_tokens":1000}`) + `,"actualUseModelName":"` + chat + `","responseId":"resp-2"}`},
		{cpShop, "user_prompt", "", cpBase + 50_000, `{"role":"user","content":"再数一下行数","streamStatus":"completed","submitEditorState":{"contentText":"再数一下行数","selectedModelName":"LongCat-Flash-Chat"}}`},
		{cpShop, "tool", cpSub, cpBase + 55_000, `{"role":"user","tooluseName":"task","toolAskStatus":"tool_execute_success",
			"blockData":{"name":"task","params":{"description":"Count the lines of main.go and shop.go"},"toolCallId":"call_2"},
			"subAgentPayload":{"conversationId":"` + cpSub + `"},"toolResultData":"main.go: 12 lines, shop.go: 30 lines",
			"tokenUsage":` + use(300, 20, "") + `,"actualUseModelName":"` + chat + `","responseId":"resp-3"}`},
		{cpShop, "text", "", cpBase + 90_000, `{"role":"assistant","content":"main.go 有 12 行，shop.go 有 30 行。","tokenUsage":` + use(400, 25, `,"cache_read_tokens":0,"cache_write_tokens":100`) + `,"actualUseModelName":"` + chat + `","responseId":"resp-4"}`},
		// a notice of CatPaw's own: no prompt, no reply, no tokens
		{cpShop, "error", "", cpBase + 91_000, `{"role":"error","content":"网络异常，请重试"}`},

		// the subagent's conversation: its prompt is the parent's instruction
		{cpSub, "user_prompt", "", cpBase + 60_000, `{"role":"user","content":"Count the lines of main.go and shop.go","rquestExtraParams":{"subagentMode":true},"__telemetry_hidden":true}`},
		{cpSub, "tool", "", cpBase + 61_000, `{"role":"user","tooluseName":"read_file","toolAskStatus":"tool_execute_success","blockData":{"name":"read_file","params":{"path":"main.go"}},
			"toolResultData":"package main","tokenUsage":` + use(500, 10, "") + `,"actualUseModelName":"` + think + `","responseId":"resp-c1"}`},
		{cpSub, "text", "", cpBase + 62_000, `{"role":"assistant","content":"main.go: 12 lines, shop.go: 30 lines","tokenUsage":` + use(600, 15, "") + `,"actualUseModelName":"` + think + `","responseId":"resp-c2"}`},

		// a window of a .code-workspace, its title still CatPaw's own
		{cpMulti, "user_prompt", "", cpBase + 301_000, `{"role":"user","content":"","submitEditorState":{"contentText":"这个工作区有几个项目？","selectedModelName":"LongCat-Flash-Chat"}}`},
		{cpMulti, "text", "", cpBase + 302_000, `{"role":"assistant","content":"两个。","tokenUsage":` + use(90, 5, "") + `,"responseId":"resp-m1"}`},

		{cpEmpty, "error", "", cpBase + 401_000, `{"role":"error","content":"未登录"}`},
	} {
		add(m)
	}
	return path, db
}

func cpSession(t *testing.T, list []Session, id string) Session {
	t.Helper()
	for _, s := range list {
		if s.Agent == "catpaw" && s.ID == id {
			return s
		}
	}
	t.Fatalf("CatPaw conversation %q not listed: %+v", id, list)
	return Session{}
}

// catpawBytes is a database's file and its WAL, read as they are.
func catpawBytes(t *testing.T, path string) []byte {
	t.Helper()
	var out []byte
	for _, p := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out = append(out, b...)
	}
	return out
}

// CatPaw's conversations are listed with their prompts, replies, tools
// and tokens (KenyonCode on X: "只考虑提供对 catpawai 这个ide版本的会话管理功能"),
// a response's usage counted once though its thinking, text and tool
// each carry it, and a subagent's conversation inside its parent's.
func TestCatPawSessions(t *testing.T) {
	setup(t)
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".sankuai", "CatPawAI", "sqliteDB")
	if CatPawDir() != dir {
		t.Fatalf("CatPawDir() = %q, want %q", CatPawDir(), dir)
	}
	path, w := catpawFixture(t)
	before := catpawBytes(t, path)

	list := List(0)
	a := cpSession(t, list, cpShop)
	if !a.ReadOnly || a.Resume != "" || a.Title != "列出这个项目里的 Go 文件" || a.Cwd != "/Users/dev/work/shop" {
		t.Errorf("shop: %+v", a)
	}
	// resp-1 once, at its last usage; resp-2's cache read out of its
	// prompt_tokens; resp-4's cache write
	if got, want := model(a, "LongCat-Flash-Chat").Tokens, (Tokens{Input: 1200 + 500 + 300 + 400, Output: 60 + 40 + 20 + 25, CacheRead: 800 + 1000, CacheWrite: 100}); got != want {
		t.Errorf("shop tokens = %+v, want %+v", got, want)
	}
	if got := model(a, "LongCat-Flash-Thinking").Tokens; got != (Tokens{Input: 1100, Output: 25}) {
		t.Errorf("shop's subagent tokens = %+v", got)
	}
	if !a.Start.Equal(time.UnixMilli(cpBase+1000)) || !a.Last.Equal(time.UnixMilli(cpBase+91_000)) {
		t.Errorf("shop times %v – %v", a.Start, a.Last)
	}
	m := cpSession(t, list, cpMulti)
	if m.Title != "这个工作区有几个项目？" || m.Cwd != "/Users/dev/work/multi" {
		t.Errorf("multi: %+v", m)
	}
	// no model named: the one picked for the prompt
	if got := model(m, "LongCat-Flash-Chat").Tokens; got != (Tokens{Input: 90, Output: 5}) {
		t.Errorf("multi tokens = %+v (%+v)", got, m.Models)
	}
	for _, s := range list {
		if s.Agent == "catpaw" && (s.ID == cpSub || s.ID == cpEmpty) {
			t.Errorf("a subagent's or an empty conversation was listed: %+v", s)
		}
	}

	managed := ListAgent("catpaw")
	if len(managed) != 2 {
		t.Fatalf("ListAgent(catpaw) = %+v", managed)
	}
	for _, x := range managed {
		if x.Deletable || !x.Transcript {
			t.Errorf("a CatPaw conversation on the page: %+v", x)
		}
		want := map[string]int{cpShop: 4, cpMulti: 2}[x.ID]
		if x.Messages != want || x.Size == 0 {
			t.Errorf("%s on the page: %d messages (want %d), %d bytes", x.ID, x.Messages, want, x.Size)
		}
	}
	if _, err := Delete("catpaw", cpShop); err == nil {
		t.Error("Delete took a conversation out of CatPaw's database")
	}

	st := StatsAt(0, time.UnixMilli(cpBase).Add(24*time.Hour))
	var sum Summary
	for _, s := range st.Sessions {
		if s.Key == "catpaw:"+cpShop {
			sum = s
		}
	}
	if sum.Prompts != 2 || sum.Replies != 2 || sum.ToolCalls != 3 || sum.tools.of("list_dir") != 1 || sum.tools.of("task") != 1 || sum.tools.of("read_file") != 1 {
		t.Errorf("shop stats: prompts %d replies %d tools %d %v", sum.Prompts, sum.Replies, sum.ToolCalls, sum.tools)
	}

	found := false
	for _, d := range Dirs() {
		found = found || d == dir
	}
	if !found {
		t.Errorf("Dirs() %v leaves out CatPaw's", Dirs())
	}
	if !slices.Contains(Found(), "catpaw") {
		t.Errorf("Found() %v leaves out CatPaw", Found())
	}
	if !bytes.Equal(before, catpawBytes(t, path)) {
		t.Error("reading CatPaw's conversations changed its database")
	}

	// CatPaw goes on: a third prompt, and the title it names it by
	if _, err := w.Exec(`INSERT INTO t_ui_messages (conversation_id, message_id, message_type, content, create_time, update_time) VALUES (?, 'msg-x', 'user_prompt', ?, ?, ?)`,
		cpMulti, `{"messageId":"msg-x","messageType":"user_prompt","role":"user","content":"分别叫什么？"}`, cpBase+303_000, cpBase+303_000); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`UPDATE t_conversations SET title = '工作区里的项目', update_time = ? WHERE conversation_id = ?`, cpBase+303_000, cpMulti); err != nil {
		t.Fatal(err)
	}
	m = cpSession(t, List(0), cpMulti)
	if m.Title != "工作区里的项目" || !m.Last.Equal(time.UnixMilli(cpBase+303_000)) {
		t.Errorf("multi after CatPaw wrote on: %+v", m)
	}
}

// A CatPaw conversation is shown as CatPaw shows it: the prompts, the
// thinking, the replies and each tool with what it was given and what it
// answered; not its subagent's own conversation, nor CatPaw's notices.
func TestCatPawTranscript(t *testing.T) {
	setup(t)
	path, _ := catpawFixture(t)
	before := catpawBytes(t, path)
	shop, ok := Find("catpaw", cpShop)
	if !ok {
		t.Fatal("Find(catpaw, shop) found nothing")
	}
	got := ombParts(t, shop)
	want := []string{
		"user/text/: 列出这个项目里的 Go 文件",
		"assistant/thinking/: I should list the folder first.",
		"assistant/text/: 我先看看目录。",
		"assistant/tool_use/list_dir: {\n  \"path\": \".\"\n}",
		"tool/tool_result/list_dir: main.go\nshop.go\nREADME.md",
		"assistant/text/: 两个 Go 文件：main.go 和 shop.go。",
		"user/text/: 再数一下行数",
		"assistant/tool_use/task: {\n  \"description\": \"Count the lines of main.go and shop.go\"\n}",
		"tool/tool_result/task: main.go: 12 lines, shop.go: 30 lines",
		"assistant/text/: main.go 有 12 行，shop.go 有 30 行。",
	}
	if !slices.Equal(got, want) {
		t.Errorf("shop transcript:\n%s", strings.Join(got, "\n"))
	}
	multi, ok := Find("catpaw", cpMulti)
	if !ok {
		t.Fatal("Find(catpaw, multi) found nothing")
	}
	if got := ombParts(t, multi); !slices.Equal(got, []string{"user/text/: 这个工作区有几个项目？", "assistant/text/: 两个。"}) {
		t.Errorf("multi transcript:\n%s", strings.Join(got, "\n"))
	}
	for q, want := range map[string]bool{"再数一下": true, "README.md": true, "未登录": false} {
		if _, found, err := SearchSession(context.Background(), shop, NeedleOf(q)); err != nil || found != want {
			t.Errorf("search %q: found %v (%v), want %v", q, found, err, want)
		}
	}
	if !bytes.Equal(before, catpawBytes(t, path)) {
		t.Error("showing a CatPaw conversation changed its database")
	}
}

// Reads("catpaw") is the gate: with CatPaw's sessions not allowed,
// nothing of its database is listed.
func TestCatPawNotReadUnlessAllowed(t *testing.T) {
	setup(t)
	catpawFixture(t)
	if len(catpawFiles()) != 2 {
		t.Fatalf("catpawFiles() = %+v, want two conversations", catpawFiles())
	}
	if err := SetReads(nil, map[string]bool{"catpaw": false}); err != nil {
		t.Fatal(err)
	}
	if fs := catpawFiles(); len(fs) != 0 {
		t.Errorf("CatPaw's database read with CatPaw not allowed: %+v", fs)
	}
	for _, s := range List(0) {
		if s.Agent == "catpaw" {
			t.Fatalf("listed with CatPaw not allowed: %+v", s)
		}
	}
}

func TestCatPawTitleAndCwd(t *testing.T) {
	for in, want := range map[string]string{
		"新对话":                           "",
		"对话 2026/11/18 19:06:40":        "",
		strings.Repeat("字", 47) + "...": "",
		"修一下登录页":                        "修一下登录页",
	} {
		if got := catpawTitle(in); got != want {
			t.Errorf("catpawTitle(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"defaultWorkspace":                      "",
		"":                                      "",
		`c:\Users\dev\shop`:                     `c:\Users\dev\shop`,
		`c:\Users\dev\all.code-workspace`:       `c:\Users\dev`,
		"/Users/dev/multi/multi.code-workspace": "/Users/dev/multi",
	} {
		if got := catpawCwd(in); got != want {
			t.Errorf("catpawCwd(%q) = %q, want %q", in, got, want)
		}
	}
}

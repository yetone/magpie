package sessions

import (
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

const (
	curMain  = "6f0c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"
	curSub   = "7a1b2c3d-4e5f-4a6b-9c7d-8e9f0a1b2c3d"
	curOther = "8b2c3d4e-5f6a-4b7c-8d9e-0f1a2b3c4d5e"
	curEmpty = "9c3d4e5f-6a7b-4c8d-9e0f-1a2b3c4d5e6f"
	curCwd   = "/work/cur"
)

// pbBytes and pbVarint write a protobuf field.
func pbBytes(b []byte, field int, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3|2)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

func pbVarint(b []byte, field int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(field)<<3)
	return binary.AppendUvarint(b, v)
}

// curChat is a chat as Cursor's CLI writes it.
type curChat struct {
	id, cwd   string // the cwd its folder is named by
	meta      map[string]any
	metaJSON  map[string]any // meta.json beside store.db, when not nil
	archived  []string       // messages a compaction summed up
	messages  []string       // the conversation's own, as JSON
	started   int64          // conversation_started_timestamp_ms
	plainMeta bool           // row "0" as JSON, not in hex
	live      bool           // kept open, as by a Cursor at work, so it's all in -wal
}

// makeCursorChat writes a chat's store.db (in WAL, as Cursor's run store
// does), and its meta.json, under chats/<md5(cwd)>/<id>/ in dir, and
// returns its folder.
func makeCursorChat(t *testing.T, dir string, c curChat) string {
	t.Helper()
	sum := md5.Sum([]byte(c.cwd))
	folder := filepath.Join(dir, "chats", hex.EncodeToString(sum[:]), c.id)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(folder, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if c.live {
		t.Cleanup(func() { db.Close() })
	} else {
		defer db.Close()
	}
	for _, q := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA wal_autocheckpoint = 0",
		"CREATE TABLE IF NOT EXISTS blobs (id TEXT PRIMARY KEY, data BLOB)",
		"CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	put := func(data []byte) []byte {
		id := sha256.Sum256(data)
		if _, err := db.Exec("INSERT OR REPLACE INTO blobs (id, data) VALUES (?, ?)", hex.EncodeToString(id[:]), data); err != nil {
			t.Fatal(err)
		}
		return id[:]
	}
	meta := map[string]any{"agentId": c.id, "name": "New Agent", "createdAt": 0, "mode": "default", "latestRootBlobId": ""}
	for k, v := range c.meta {
		meta[k] = v
	}
	if len(c.messages) > 0 || len(c.archived) > 0 {
		var root []byte
		if len(c.archived) > 0 {
			var archive []byte
			for _, m := range c.archived {
				archive = pbBytes(archive, 1, put([]byte(m)))
			}
			archive = pbBytes(archive, 2, []byte("what came before"))
			root = pbBytes(root, 13, put(archive))
		}
		for _, m := range c.messages {
			root = pbBytes(root, 1, put([]byte(m)))
		}
		// a field the reader passes over, before the start time
		root = pbVarint(root, 17, 2)
		if c.started > 0 {
			root = pbVarint(root, 26, uint64(c.started))
		}
		meta["latestRootBlobId"] = hex.EncodeToString(put(root))
	}
	b, _ := json.Marshal(meta)
	v := hex.EncodeToString(b)
	if c.plainMeta {
		v = string(b)
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES ('0', ?)", v); err != nil {
		t.Fatal(err)
	}
	if c.metaJSON != nil {
		b, _ := json.Marshal(c.metaJSON)
		if err := os.WriteFile(filepath.Join(folder, "meta.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return folder
}

const (
	curT0 = 1790500000000 // createdAt
	curT1 = 1790503600000 // updatedAt, an hour on
)

// makeCursorChats are a chat with a compaction behind it and a subagent's
// chat under it, a chat with no meta.json in another folder, and a chat
// nothing was said in.
func makeCursorChats(t *testing.T, dir string) (main, sub, other, empty string) {
	main = makeCursorChat(t, dir, curChat{
		id: curMain, cwd: curCwd,
		meta:     map[string]any{"name": "Port the parser", "createdAt": curT0, "lastUsedModel": "claude-4.6-sonnet"},
		metaJSON: map[string]any{"schemaVersion": 1, "title": "Port the parser", "createdAtMs": curT0, "updatedAtMs": curT1, "hasConversation": true, "cwd": curCwd},
		started:  curT0,
		archived: []string{
			`{"role":"system","content":"You are a coding agent."}`,
			`{"role":"user","content":[{"type":"text","text":"<user_info>OS: darwin</user_info>\n<user_query>Start the   port\nof the parser</user_query>"}]}`,
			`{"role":"assistant","content":[{"type":"text","text":"Looking."},{"type":"tool-call","toolCallId":"c1","toolName":"Read","args":{"path":"a.go"}}]}`,
			`{"role":"tool","content":[{"type":"tool-result","toolCallId":"c1","toolName":"Read","result":"package a"}]}`,
		},
		messages: []string{
			`{"role":"user","content":[{"type":"text","text":"<system_reminder>be brief</system_reminder>"}]}`,
			`{"role":"user","content":[{"type":"text","text":"<user_query>Now the tests</user_query>"}]}`,
			`{"role":"assistant","content":[{"type":"reasoning","text":"hm"},{"type":"tool-call","toolCallId":"c2","toolName":"Shell","input":{"command":"go test"}},{"type":"tool-call","toolCallId":"c3","toolName":"Read","input":{"path":"a_test.go"}}]}`,
			`{"role":"tool","content":[{"type":"tool-result","toolCallId":"c2","toolName":"Shell","result":"ok"}]}`,
			`{"role":"assistant","content":"Done."}`,
		},
	})
	sub = makeCursorChat(t, dir, curChat{
		id: curSub, cwd: curCwd,
		meta: map[string]any{"name": "Explore", "createdAt": curT0 + 60000,
			"subagentInfo": map[string]any{"parentAgentId": curMain, "rootParentAgentId": curMain, "toolCallId": "c9", "typeName": "explore"}},
		messages: []string{
			`{"role":"user","content":"<user_query>Find the lexer</user_query>"}`,
			`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"s1","toolName":"Grep","args":{"pattern":"lex"}}]}`,
		},
	})
	other = makeCursorChat(t, dir, curChat{
		id: curOther, cwd: "/work/other", plainMeta: true, live: true,
		meta:     map[string]any{"name": "Tidy imports", "createdAt": curT0},
		messages: []string{`{"role":"user","content":"tidy the   imports"}`, `{"role":"assistant","content":"Tidied."}`},
	})
	empty = makeCursorChat(t, dir, curChat{id: curEmpty, cwd: curCwd, meta: map[string]any{"createdAt": curT0}})
	return
}

// curOld is a time between the chat's start and its last word, days ago.
var curOld = time.UnixMilli(curT0 + 1800000)

// ageCursor dates every file and folder in roots curOld.
func ageCursor(roots ...string) {
	for _, root := range roots {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil {
				os.Chtimes(p, curOld, curOld)
			}
			return nil
		})
	}
}

// snapshot is every file under root, with its size and time.
func snapshot(root string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if fi, err := os.Lstat(p); err == nil {
			out[p] = fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return out
}

func cursorSetup(t *testing.T) (dir string) {
	setup(t)
	dir = filepath.Join(t.TempDir(), "cursor")
	t.Setenv("CURSOR_CONFIG_DIR", dir)
	return dir
}

func findSession(ss []Session, agent, id string) (Session, bool) {
	for _, s := range ss {
		if s.Agent == agent && s.ID == id {
			return s, true
		}
	}
	return Session{}, false
}

func TestCursor(t *testing.T) {
	dir := cursorSetup(t)
	_, _, other, _ := makeCursorChats(t, dir)
	if fi, err := os.Stat(filepath.Join(other, "store.db-wal")); err != nil || fi.Size() == 0 {
		t.Fatalf("the live chat has no log: %v", err)
	}
	ageCursor(dir)
	before := snapshot(dir)
	ss := List(0)
	// reading wrote nothing in Cursor's folder: no -wal, no -shm, no time
	if after := snapshot(dir); !maps.Equal(before, after) {
		t.Fatalf("Cursor's folder changed:\n%v\n%v", before, after)
	}
	if n := count(ss, "cursor"); n != 2 {
		t.Fatalf("cursor sessions %d, want 2 (the subagent's in its chat, the empty one left out): %+v", n, ss)
	}
	s, ok := findSession(ss, "cursor", curMain)
	if !ok {
		t.Fatal("the chat isn't listed")
	}
	if s.Title != "Start the port of the parser" || s.Cwd != curCwd {
		t.Fatalf("title %q cwd %q", s.Title, s.Cwd)
	}
	if !s.Start.Equal(time.UnixMilli(curT0)) || !s.Last.Equal(time.UnixMilli(curT1)) {
		t.Fatalf("start %v last %v", s.Start, s.Last)
	}
	if !s.Tokens.zero() || len(s.Models) != 0 {
		t.Fatalf("Cursor's chats say nothing of tokens: %+v", s)
	}
	want := "cursor-agent --resume " + curMain
	if runtime.GOOS != "windows" {
		want = "cd '/work/cur' && " + want
	}
	if s.Resume != want {
		t.Fatalf("resume %q, want %q", s.Resume, want)
	}
	o, ok := findSession(ss, "cursor", curOther)
	if !ok || o.Title != "tidy the imports" || o.Cwd != "" || o.Resume != "" {
		t.Fatalf("the chat with no meta.json: %+v (its folder unknown, it has no resume)", o)
	}
	if !o.Last.Equal(curOld) {
		t.Fatalf("with no meta.json the store's time is its last: %v", o.Last)
	}
	if !slices.Contains(Dirs(), dir) {
		t.Fatalf("dirs %v", Dirs())
	}

	m, ok := findManaged(ListAgent("cursor"), curMain)
	// 2 prompts typed (the reminder alone isn't one), 3 replies; the
	// subagent's are its own work, not the user's
	if !ok || m.Files != 2 || m.Messages != 5 || !m.Deletable {
		t.Fatalf("managed %+v", m)
	}
	if e, ok := findManaged(ListAgent("cursor"), curEmpty); !ok || e.Title != "" {
		t.Fatalf("the empty chat is listed to be cleared away: %+v", e)
	}

	// the tools called, the subagent's with them, on the day each was last at work
	date := dateOf(time.UnixMilli(curT1))
	tools := map[string]int{}
	for _, f := range cursorFiles() {
		if st := cache[f.path]; st != nil && f.key == "cursor:"+curMain {
			if st.Days[date] == nil {
				t.Fatalf("%s counts no day %s: %v", f.path, date, st.Days)
			}
			for name, n := range st.Days[date].Tools {
				tools[name] += n
			}
		}
	}
	if tools["Read"] != 2 || tools["Shell"] != 1 || tools["Grep"] != 1 {
		t.Fatalf("tools %v", tools)
	}
}

func TestCursorDelete(t *testing.T) {
	dir := cursorSetup(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	main, sub, _, _ := makeCursorChats(t, dir)
	home, _ := os.UserHomeDir()
	transcripts := filepath.Join(home, ".cursor", "projects", "work-cur", "agent-transcripts", curMain)
	os.MkdirAll(transcripts, 0o755)
	os.WriteFile(filepath.Join(transcripts, curMain+".jsonl"), []byte(`{"role":"user","message":{"content":[]}}`+"\n"), 0o644)
	// a chat being written to is left alone
	if _, err := Delete("cursor", curMain); !errors.Is(err, ErrActive) {
		t.Fatalf("a chat just written: %v", err)
	}
	ageCursor(dir, filepath.Join(home, ".cursor"))
	Reset()
	tr, err := Delete("cursor", curMain)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Items) != 3 || tr.Cwd != curCwd {
		t.Fatalf("trashed %+v, want the chat's folder, its subagent's and its transcripts", tr)
	}
	for _, p := range []string{main, sub, transcripts} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s is still there", p)
		}
	}
	if _, ok := findSession(List(0), "cursor", curMain); ok {
		t.Fatal("the deleted chat is listed")
	}
	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(main, "store.db"), filepath.Join(sub, "store.db"), filepath.Join(transcripts, curMain+".jsonl")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s isn't back: %v", p, err)
		}
	}
	Reset()
	if s, ok := findSession(List(0), "cursor", curMain); !ok || s.Title != "Start the port of the parser" {
		t.Fatalf("the restored chat: %+v", s)
	}
}

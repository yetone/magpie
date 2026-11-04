package sessions

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCursorEditorMultipartMessages(t *testing.T) {
	dir := cursorSetup(t)
	t.Setenv("CURSOR_DATA_DIR", dir)
	p := filepath.Join(dir, "projects", "example", "agent-transcripts", curMain, curMain+".jsonl")
	writeLines(t, p, `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>Explain "},{"type":"text","text":"the example.</user_query>"}]}}`, `{"role":"assistant","message":{"content":[{"type":"text","text":"First paragraph."},{"type":"text","text":"Second paragraph."}]}}`)
	m, ok := findManaged(ListAgent("cursor"), curMain)
	if !ok || m.Messages != 2 || m.Title != "Explain the example." {
		t.Fatalf("multipart messages counted incorrectly: %+v", m)
	}
}

func TestCursorEditorPathWithHash(t *testing.T) {
	dir := cursorSetup(t) + "#backup"
	t.Setenv("CURSOR_DATA_DIR", dir)
	editorTranscript(t, dir, curMain, time.Now())
	s, ok := findSession(List(All), "cursor", curMain)
	if !ok {
		t.Fatal("transcript below a folder containing # is missing")
	}
	if tr, err := TranscriptOf(s); err != nil || len(tr.Parts) != 2 {
		t.Fatalf("transcript %+v: %v", tr, err)
	}
}

func TestCursorCLITranscriptIncludesTools(t *testing.T) {
	dir := cursorSetup(t)
	makeCursorChats(t, dir)
	s := find(t, List(All), "cursor", curMain)
	tr, err := TranscriptOf(s)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, p := range tr.Parts {
		kinds[p.Kind]++
	}
	if kinds["thinking"] != 1 || kinds["tool_use"] != 3 || kinds["tool_result"] != 2 {
		t.Fatalf("CLI conversation lost parts: %v", kinds)
	}
}

func TestCursorEditorLiveDatabaseAndMetadataRefresh(t *testing.T) {
	dir := cursorSetup(t)
	t.Setenv("CURSOR_DATA_DIR", dir)
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().Add(-time.Hour)
	editorTranscript(t, dir, curMain, at)
	put := func(name string, updated time.Time) {
		b, _ := json.Marshal(map[string]any{"composerId": curMain, "name": name, "createdAt": at.UnixMilli(), "lastUpdatedAt": updated.UnixMilli()})
		if _, err := db.Exec("INSERT OR REPLACE INTO cursorDiskKV VALUES (?,?)", "composerData:"+curMain, b); err != nil {
			t.Fatal(err)
		}
	}
	put("Alpha", at)
	before, _ := os.ReadFile(p + "-wal")
	s := find(t, List(All), "cursor", curMain)
	if s.Title != "Alpha" {
		t.Fatalf("WAL metadata not read: %q", s.Title)
	}
	after, _ := os.ReadFile(p + "-wal")
	if !bytes.Equal(before, after) {
		t.Fatal("source WAL changed")
	}
	put("Bravo", at.Add(time.Minute))
	s = find(t, List(All), "cursor", curMain)
	if s.Title != "Bravo" {
		t.Fatalf("metadata rename not refreshed: %q", s.Title)
	}
}

func editorTranscript(t *testing.T, root, id string, at time.Time) string {
	t.Helper()
	p := filepath.Join(root, "projects", "home-test-project", "agent-transcripts", id, id+".jsonl")
	writeLines(t, p, `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>Fix editor history</user_query>"}]}}`, `{"role":"assistant","message":{"content":[{"type":"text","text":"History is visible."}]}}`)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCursorEditorTranscript(t *testing.T) {
	dir := cursorSetup(t)
	t.Setenv("CURSOR_DATA_DIR", dir)
	at := time.Date(2026, 10, 12, 10, 0, 0, 0, time.Local)
	editorTranscript(t, dir, curMain, at)
	before := snapshot(dir)
	s, ok := findSession(List(All), "cursor", curMain)
	if !ok {
		t.Fatal("Cursor editor transcript is absent from session list")
	}
	if s.Title != "Fix editor history" || !s.Last.Equal(at) || !s.ReadOnly || s.Resume != "" || !s.Transcript {
		t.Fatalf("editor session: %+v", s)
	}
	tr, err := TranscriptOf(s)
	if err != nil || len(tr.Parts) != 2 || tr.Parts[0].Text != "Fix editor history" {
		t.Fatalf("transcript %+v: %v", tr, err)
	}
	if n := StatsAt(1, at).Overview("cursor", "", "", 8).Count; n != 1 {
		t.Fatalf("today count %d, want 1", n)
	}
	if n := StatsAt(1, at.AddDate(0, 0, 1)).Overview("cursor", "", "", 8).Count; n != 0 {
		t.Fatalf("tomorrow count %d, want 0", n)
	}
	if !maps.Equal(before, snapshot(dir)) {
		t.Fatal("source files changed")
	}
}

func TestCursorEditorDoesNotDuplicateCLI(t *testing.T) {
	dir := cursorSetup(t)
	t.Setenv("CURSOR_DATA_DIR", dir)
	makeCursorChat(t, dir, curChat{id: curMain, cwd: curCwd, meta: map[string]any{"name": "CLI chat", "createdAt": curT0}, messages: []string{`{"role":"user","content":"hello"}`}})
	editorTranscript(t, dir, curMain, time.Now())
	ss := List(All)
	if n := count(ss, "cursor"); n != 1 {
		t.Fatalf("count %d, want 1", n)
	}
	s, _ := findSession(ss, "cursor", curMain)
	if s.Title != "CLI chat" || s.ReadOnly {
		t.Fatalf("CLI ownership lost: %+v", s)
	}
}

func TestCursorEditorDatabase(t *testing.T) {
	cursorSetup(t)
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value BLOB)")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 9, 10, 0, 0, 0, time.Local)
	put := func(k string, v any) {
		b, _ := json.Marshal(v)
		if _, err := db.Exec("INSERT INTO cursorDiskKV VALUES (?,?)", k, b); err != nil {
			t.Fatal(err)
		}
	}
	put("composerData:"+curMain, map[string]any{"composerId": curMain, "name": "Older editor chat", "createdAt": at.UnixMilli(), "lastUpdatedAt": at.Add(time.Minute).UnixMilli(), "fullConversationHeadersOnly": []any{map[string]any{"bubbleId": "u", "type": 1}, map[string]any{"bubbleId": "a", "type": 2}}, "workspaceIdentifier": map[string]any{"uri": map[string]any{"fsPath": "/work/editor"}}})
	put("bubbleId:"+curMain+":u", map[string]any{"type": 1, "text": "Old question", "createdAt": at.Format(time.RFC3339Nano)})
	put("bubbleId:"+curMain+":a", map[string]any{"type": 2, "text": "Old answer", "createdAt": at.Add(time.Minute).Format(time.RFC3339Nano)})
	put("composerData:"+curEmpty, map[string]any{"composerId": curEmpty, "name": "Failed export", "createdAt": at.UnixMilli()})
	writeLines(t, filepath.Join(cursorDataDir(), "projects", "project", "agent-transcripts", curEmpty, curEmpty+".jsonl"), `{"type":"error","status":"failed","error":"No conversation found"}`)
	db.Close()
	before := snapshot(filepath.Dir(p))
	s, ok := findSession(List(All), "cursor", curMain)
	if _, found := findSession(List(All), "cursor", curEmpty); found {
		t.Fatal("failed export with no messages listed as a conversation")
	}
	for _, a := range Agents() {
		if a.Agent == "cursor" && a.Count != 1 {
			t.Fatalf("management count %d, want 1", a.Count)
		}
	}
	if !ok {
		t.Fatal("Cursor editor database chat is absent from session list")
	}
	if s.Title != "Older editor chat" || s.Cwd != "/work/editor" || !s.ReadOnly || !s.Last.Equal(at.Add(time.Minute)) {
		t.Fatalf("session: %+v", s)
	}
	tr, err := TranscriptOf(s)
	if err != nil || len(tr.Parts) != 2 || tr.Parts[1].Text != "Old answer" {
		t.Fatalf("transcript %+v: %v", tr, err)
	}
	if !maps.Equal(before, snapshot(filepath.Dir(p))) {
		t.Fatal("editor database changed")
	}
}

// #1988: the chat list must come off the key's index. A LIKE over the key
// scans every bubble in a store of several GB, from a copy that is cold in
// the page cache each time Cursor has written, and Sessions never loaded.
func TestCursorComposersQueryUsesTheKeyIndex(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Cursor's own schema
	if _, err := db.Exec("CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bubbleId:a:1", "checkpointId:a:1", "composerData", "composerData:a", "composerData:b:c", "composerData;x", "composerDataZ", "composerdata:lower", "inlineDiffs-a"} {
		if _, err := db.Exec("INSERT INTO cursorDiskKV VALUES (?,?)", k, k); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query("EXPLAIN QUERY PLAN " + cursorComposersQuery)
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	rows.Close()
	if p := strings.Join(plan, "; "); !strings.Contains(p, "USING INDEX") || strings.Contains(p, "SCAN") {
		t.Fatalf("the chat list reads %q, want a search on the key's index", p)
	}
	rows, err = db.Query(cursorComposersQuery)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var v string
		rows.Scan(&v)
		got = append(got, v)
	}
	rows.Close()
	if strings.Join(got, ",") != "composerData:a,composerData:b:c" {
		t.Fatalf("listed %v, want only the composerData: keys", got)
	}
}

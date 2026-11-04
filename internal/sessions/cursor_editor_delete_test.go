package sessions

import (
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ysm119 on Discord: on the Sessions page some Cursor rows had a delete
// and some had none. A CLI chat can be deleted; a chat of Cursor's editor
// was read only, so its row had no control at all. Every row of an agent
// whose sessions can be deleted now says whether it can be and, if not,
// why; an editor transcript whose chat the editor no longer keeps is files
// of its own and is deleted as a CLI chat's transcripts are.

// editorStore writes Cursor's editor's state.vscdb, in the sandbox HOME,
// with a chat of each id given.
func editorStore(t *testing.T, ids ...string) string {
	t.Helper()
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	for _, id := range ids {
		b, _ := json.Marshal(map[string]any{"composerId": id, "name": "Debugging access page", "createdAt": at.UnixMilli(), "lastUpdatedAt": at.UnixMilli(),
			"fullConversationHeadersOnly": []any{map[string]any{"bubbleId": "u", "type": 1}}, "workspaceIdentifier": map[string]any{"uri": map[string]any{"fsPath": "/work/editor"}}})
		if _, err := db.Exec("INSERT INTO cursorDiskKV VALUES (?,?)", "composerData:"+id, b); err != nil {
			t.Fatal(err)
		}
		u, _ := json.Marshal(map[string]any{"type": 1, "text": "Why is the access page blank?", "createdAt": at.Format(time.RFC3339Nano)})
		if _, err := db.Exec("INSERT INTO cursorDiskKV VALUES (?,?)", "bubbleId:"+id+":u", u); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

const curLoose = "3eb00720-1b2c-4d3e-8f40-5a6b7c8d9e0f"

func TestCursorRowsSayWhetherTheyCanBeDeleted(t *testing.T) {
	dir := cursorSetup(t)
	t.Setenv("CURSOR_DATA_DIR", dir)
	makeCursorChat(t, dir, curChat{id: curOther, cwd: curCwd, meta: map[string]any{"name": "CLI chat", "createdAt": curT0}, messages: []string{`{"role":"user","content":"hello"}`}})
	old := time.Now().Add(-time.Hour)
	held := editorTranscript(t, dir, curMain, old)   // a chat the editor keeps, with its transcript
	loose := editorTranscript(t, dir, curLoose, old) // a transcript whose chat the editor no longer keeps
	store := editorStore(t, curMain, curEmpty)       // curEmpty: kept in the store alone
	ageCursor(dir)
	rows := map[string]Managed{}
	for _, m := range ListAgent("cursor") {
		rows[m.ID] = m
	}
	if len(rows) != 4 {
		t.Fatalf("rows %d, want the CLI chat, two transcripts and the store's chat: %+v", len(rows), rows)
	}
	for id, want := range map[string]struct {
		ok  bool
		why string
	}{curOther: {true, ""}, curLoose: {true, ""}, curMain: {false, "cursor-editor"}, curEmpty: {false, "cursor-editor"}} {
		if m := rows[id]; m.Deletable != want.ok || m.Undeletable != want.why {
			t.Errorf("%s: deletable %v (%q), want %v (%q)", id, m.Deletable, m.Undeletable, want.ok, want.why)
		}
	}

	// the editor's chats are left as they are, and say why
	before := snapshot(filepath.Dir(store))
	for _, id := range []string{curMain, curEmpty} {
		if _, err := Delete("cursor", id); err == nil || !strings.Contains(err.Error(), "Cursor's editor") {
			t.Fatalf("%s: deleting a chat the editor keeps: %v", id, err)
		}
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("the editor's chat's transcript was moved: %v", err)
	}
	if !maps.Equal(before, snapshot(filepath.Dir(store))) {
		t.Fatal("Cursor's editor store changed")
	}

	// the loose transcript goes to the trash, its folder and nothing beside it
	tr, err := Delete("cursor", curLoose)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Items) != 1 || tr.Items[0].From != filepath.Dir(loose) {
		t.Fatalf("trashed %+v, want only %s", tr.Items, filepath.Dir(loose))
	}
	if _, err := os.Stat(filepath.Dir(loose)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the transcript is still there: %v", err)
	}
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("the folder beside it went too: %v", err)
	}
	if _, ok := findManaged(ListAgent("cursor"), curLoose); ok {
		t.Fatal("the deleted transcript is listed")
	}
	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	Reset()
	if m, ok := findManaged(ListAgent("cursor"), curLoose); !ok || !m.Deletable {
		t.Fatalf("the restored transcript: %+v %v", m, ok)
	}
}

// A transcript lying straight in agent-transcripts/ (<id>.jsonl, older
// builds) is moved with its .txt, never the folder it is in.
func TestCursorLooseFlatTranscript(t *testing.T) {
	dir := cursorSetup(t)
	t.Setenv("CURSOR_DATA_DIR", dir)
	folder := filepath.Join(dir, "projects", "home-test-project", "agent-transcripts")
	p := filepath.Join(folder, curLoose+".jsonl")
	writeLines(t, p, `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>Component changes review</user_query>"}]}}`, `{"role":"assistant","message":{"content":[{"type":"text","text":"Reviewed."}]}}`)
	os.WriteFile(filepath.Join(folder, curLoose+".txt"), []byte("user: Component changes review\n"), 0o644)
	other := filepath.Join(folder, "keep-me.jsonl")
	os.WriteFile(other, []byte("{}\n"), 0o644)
	ageCursor(dir)
	tr, err := Delete("cursor", curLoose)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Items) != 2 {
		t.Fatalf("trashed %+v, want the .jsonl and the .txt", tr.Items)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a file beside it went: %v", err)
	}
}

// A store that is there but can't be read says nothing of which chats the
// editor keeps: no transcript is taken for loose then.
func TestCursorUnreadEditorStoreDeletesNothing(t *testing.T) {
	dir := cursorSetup(t)
	t.Setenv("CURSOR_DATA_DIR", dir)
	editorTranscript(t, dir, curLoose, time.Now().Add(-time.Hour))
	config, _ := os.UserConfigDir()
	p := filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb")
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, ok := findManaged(ListAgent("cursor"), curLoose)
	if !ok || m.Deletable || m.Undeletable != "cursor-editor" {
		t.Fatalf("row %+v %v: want listed, not deletable, kept by the editor", m, ok)
	}
	if _, err := Delete("cursor", curLoose); err == nil {
		t.Fatal("deleted a transcript whose chat the editor may keep")
	}
}

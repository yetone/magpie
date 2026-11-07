package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testdata/agy-sessions is agy's data folder after three conversations
// agy had in a sandbox HOME against a fake Gemini endpoint (paths written
// /work): one of two turns, its first by agy 1.3.5 and the second by 1.4.7
// picked up again with --conversation from another folder; one that read
// a file (1.4.7); and one whose
// row isn't in conversation_summaries.db (as before agy writes it).
const (
	agyTwoTurns = "9c462bce-9709-47a1-9655-4ddd39f54270"
	agyRead     = "2c228855-9d7d-4fba-b2a6-9c9af1d279f7"
	agyNoRow    = "1fcab41f-d57c-4ef4-b4d3-6bcdb84853e5"
)

func putAgy(t *testing.T) (dir string) {
	t.Helper()
	claude, _ := setup(t)
	dir = filepath.Join(filepath.Dir(claude), "home", ".gemini", "antigravity-cli")
	copyTree(t, "testdata/agy-sessions", dir)
	AgyStart = nil
	t.Cleanup(func() { AgyStart = nil })
	Reset()
	return dir
}

func agyResume(cwd, run string) string {
	if cwd == "" {
		return run
	}
	if runtime.GOOS == "windows" {
		return "Set-Location -LiteralPath '" + cwd + "'; " + run
	}
	return "cd '" + cwd + "' && " + run
}

// RickyWei (Discord): agy's conversations were on no Sessions page.
func TestAgySessions(t *testing.T) {
	inZone(t, 0)
	putAgy(t)
	ss := List(0)
	if n := count(ss, "agy"); n != 3 {
		t.Fatalf("want 3 agy sessions, got %d", n)
	}
	two := find(t, ss, "agy", agyTwoTurns)
	if two.Title != "Hello from the fake." || two.Cwd != "/work/proj" {
		t.Fatalf("two turns: title %q cwd %q", two.Title, two.Cwd)
	}
	if !two.Start.Equal(at("2026-10-25T09:46:59Z")) || !two.Last.Equal(at("2026-10-25T09:48:56Z")) {
		t.Fatalf("two turns: %s to %s", two.Start, two.Last)
	}
	if m := model(two, "agy"); m.Tokens != (Tokens{Input: 1404, Output: 18, CacheRead: 600}) {
		t.Fatalf("two turns' tokens: %+v", two.Models)
	}
	if want := agyResume("/work/proj", "agy --conversation "+agyTwoTurns); two.Resume != want {
		t.Fatalf("resume %q, want %q", two.Resume, want)
	}
	if !two.Transcript {
		t.Fatal("its conversation can't be read")
	}
	// no row of its own yet: its first prompt, out of agy's metadata
	nr := find(t, ss, "agy", agyNoRow)
	if nr.Title != "what is in this folder" || nr.Cwd != "" {
		t.Fatalf("no row: title %q cwd %q", nr.Title, nr.Cwd)
	}
	if want := "agy --conversation " + agyNoRow; nr.Resume != want {
		t.Fatalf("no row resume %q", nr.Resume)
	}

	// started on magpie's gateway: picked up there again
	AgyStart = func() string {
		return "GEMINI_API_KEY=k GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:1 agy --model 'magpie/x'"
	}
	r := find(t, List(0), "agy", agyRead)
	if want := agyResume("/work/proj2", "GEMINI_API_KEY=k GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:1 agy --model 'magpie/x' --conversation "+agyRead); r.Resume != want {
		t.Fatalf("wired resume %q, want %q", r.Resume, want)
	}
}

// The day's prompts and replies, and the tools called.
func TestAgyCounts(t *testing.T) {
	inZone(t, 0)
	dir := putAgy(t)
	f := file{agent: "agy", path: filepath.Join(dir, "brain", agyRead, ".system_generated", "logs", "transcript_full.jsonl"), main: true}
	if !stat(&f) {
		t.Fatal("no transcript")
	}
	s := parseAgy(f, nil)
	d := s.Days.of("2026-10-25")
	if d == nil || d.Prompts != 1 || d.Replies != 1 || d.Tools.of("read_file") != 1 {
		t.Fatalf("day: %+v", d)
	}
	if s.Title != "read a.txt" {
		t.Fatalf("title %q", s.Title)
	}
}

// agy writes the title once the first reply is in, after the transcript:
// the conversation is read again for it.
func TestAgyTitleLater(t *testing.T) {
	dir := putAgy(t)
	if s := find(t, List(0), "agy", agyRead); s.Title != "Listing the folder" {
		t.Fatalf("title %q", s.Title)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE conversation_summaries SET title = 'Reading a.txt' WHERE conversation_id = ?`, agyRead); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if s := find(t, List(0), "agy", agyRead); s.Title != "Reading a.txt" {
		t.Fatalf("title after agy renamed it: %q", s.Title)
	}
}

// A conversation picked up again grows its transcript; a step written
// again counts once, read on or read whole.
func TestAgyGrows(t *testing.T) {
	dir := putAgy(t)
	path := filepath.Join(dir, "brain", agyRead, ".system_generated", "logs", "transcript_full.jsonl")
	f := file{agent: "agy", path: path, main: true}
	stat(&f)
	first := parseAgy(f, nil)
	more := `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-25T09:49:47Z","input_tokens":1500,"cache_read_tokens":500,"output_tokens":60,"content":"The folder has a.txt."}` + "\n" +
		`{"step_index":4,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-25T10:00:00Z","content":"<USER_REQUEST>\nthanks\n</USER_REQUEST>"}` + "\n" +
		`{"step_index":5,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-25T10:00:01Z","input_tokens":10,"cache_read_tokens":0,"output_tokens":2,"content":"ok"}` + "\n"
	w, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(more)
	w.Close()
	stat(&f)
	on := parseAgy(f, first)
	whole := parseAgy(f, nil)
	want := Tokens{Input: 3010, CacheRead: 1000, Output: 112}
	for name, s := range map[string]*state{"read on": on, "read whole": whole} {
		if got := s.Models.of("agy"); got != want {
			t.Errorf("%s: %+v, want %+v", name, got, want)
		}
		if d := s.Days.of("2026-10-25"); d == nil || d.Prompts != 2 || d.Replies != 2 {
			t.Errorf("%s: day %+v", name, d)
		}
		if !s.Last.Equal(at("2026-10-25T10:00:01Z")) {
			t.Errorf("%s: last %s", name, s.Last)
		}
	}
	if first.Models.of("agy") != (Tokens{Input: 3000, CacheRead: 1000, Output: 100}) {
		t.Errorf("the read before was changed: %+v", first.Models)
	}
}

func TestAgyTranscript(t *testing.T) {
	putAgy(t)
	tr, err := TranscriptOf(find(t, List(0), "agy", agyRead))
	if err != nil {
		t.Fatal(err)
	}
	want := []Part{
		{Role: "user", Kind: "text", Text: "read a.txt"},
		{Role: "assistant", Kind: "thinking", Text: "I should list it."},
		{Role: "assistant", Kind: "tool_use", Name: "read_file", Text: `{"Path":"/work/proj2/a.txt"}`},
		{Role: "tool", Kind: "tool_result", Text: "Showing lines 1-1 of 1\n1: hi"},
		{Role: "assistant", Kind: "text", Text: "The folder has a.txt."},
	}
	if len(tr.Parts) != len(want) {
		t.Fatalf("parts %+v", tr.Parts)
	}
	for i, p := range want {
		if tr.Parts[i] != p {
			t.Errorf("part %d %+v, want %+v", i, tr.Parts[i], p)
		}
	}
	// agy's system notices aren't said
	tr, _ = TranscriptOf(find(t, List(0), "agy", agyTwoTurns))
	for _, p := range tr.Parts {
		if strings.Contains(p.Text, "SYSTEM_MESSAGE") {
			t.Fatalf("a system notice: %+v", p)
		}
	}
}

func TestAgyFolder(t *testing.T) {
	for _, c := range []struct{ uri, goos, want string }{
		{"file:///work/proj", "darwin", "/work/proj"},
		{"file:///home/a/my%20proj", "linux", "/home/a/my proj"},
		{"file:///C:/Users/a/proj", "windows", `C:\Users\a\proj`},
		{"file:///c%3A/Users/a/proj", "windows", `C:\Users\a\proj`},
		{"file://server/share/proj", "windows", `\\server\share\proj`},
		{"file://server/share/proj", "linux", ""},
		{"https://example.com/x", "darwin", ""},
		{"", "windows", ""},
	} {
		if got := agyFolder(c.uri, c.goos); got != c.want {
			t.Errorf("agyFolder(%q, %s) = %q, want %q", c.uri, c.goos, got, c.want)
		}
	}
}

// #1718 (zk020106): a conversation agy has a row and its own database for,
// and no transcript, is listed from its row: title, folder, time, resume.
func TestAgyRowOnly(t *testing.T) {
	dir := putAgy(t)
	const id = "7d1e2f3a-4b5c-4d6e-8f90-a1b2c3d4e5f6"
	db, err := sql.Open("sqlite", filepath.Join(dir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time, workspace_uris, last_user_input_time) VALUES (?, 'Older talk', '2026-10-12 04:41:53.5+00:00', '["file:///work/old"]', '2026-10-12 04:41:53.5+00:00')`, id); err != nil {
		t.Fatal(err)
	}
	// a row with neither is a conversation gone
	if _, err := db.Exec(`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time, workspace_uris, last_user_input_time) VALUES ('0a0b0c0d-0000-4000-8000-000000000000', 'Gone', '2026-10-12 04:00:00+00:00', '[]', '2026-10-12 04:00:00+00:00')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	os.MkdirAll(filepath.Join(dir, "conversations"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "conversations", id+".db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ss := List(0)
	if n := count(ss, "agy"); n != 4 {
		t.Fatalf("want 4 agy sessions, got %d", n)
	}
	s := find(t, ss, "agy", id)
	if s.Title != "Older talk" || s.Cwd != "/work/old" || !s.Last.Equal(at("2026-10-12T04:41:53.5Z")) || !s.Tokens.zero() {
		t.Fatalf("row only: %+v", s)
	}
	if want := agyResume("/work/old", "agy --conversation "+id); s.Resume != want {
		t.Fatalf("resume %q", s.Resume)
	}
	if s.Transcript {
		t.Fatal("offers a conversation it can't read")
	}
}

// agy's real row before a title (11-07, agy 1.4.12): title "", preview
// "hi". A conversation listed from its row alone shows that, as there is
// no transcript to read the prompt from.
func TestAgyRowOnlyPreview(t *testing.T) {
	dir := putAgy(t)
	const id = "93668250-5f30-4171-86c1-8a3692824333"
	db, err := sql.Open("sqlite", filepath.Join(dir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversation_summaries (conversation_id, title, preview, step_count, last_modified_time, workspace_uris, status, project_id, last_user_input_time, app_data_dir) VALUES (?, '', 'hi', 2, '2026-09-25 08:50:46.60116+00:00', '["file:///work/proj"]', 'CASCADE_RUN_STATUS_IDLE', 'default-cli-project', '2026-09-25 08:50:43.784481+00:00', 'antigravity-cli')`, id); err != nil {
		t.Fatal(err)
	}
	db.Close()
	os.MkdirAll(filepath.Join(dir, "conversations"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "conversations", id+".db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := find(t, List(0), "agy", id); s.Title != "hi" {
		t.Fatalf("row only, no title yet: %q, want its preview", s.Title)
	}
}

// A conversation_summaries.db made before preview was a column is still
// read, its titles and folders with it.
func TestAgyRowsWithoutPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation_summaries.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE `conversation_summaries` (`conversation_id` text,`title` text NOT NULL DEFAULT \"\",`last_modified_time` datetime NOT NULL,`workspace_uris` text NOT NULL,PRIMARY KEY (`conversation_id`))"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conversation_summaries VALUES ('a', 'Old one', '2026-10-12 04:00:00+00:00', '["file:///work/old"]')`); err != nil {
		t.Fatal(err)
	}
	rows, ok := agyRows(db)
	if !ok || rows["a"].Title != "Old one" || rows["a"].Cwd == "" {
		t.Fatalf("rows %+v ok %v", rows, ok)
	}
}

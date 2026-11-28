package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const (
	agyChild      = "5e6f7a8b-1c2d-4e3f-9a0b-c1d2e3f4a5b6"
	agyGrandchild = "6f7a8b9c-2d3e-4f40-8b1c-d2e3f4a5b6c7"
	agyOrphan     = "7a8b9c0d-3e4f-4051-9c2d-e3f4a5b6c7d8"
)

// putAgySubagents adds to the fixture what agy keeps of the subagents a
// conversation started: each its own brain/<id>/ transcript (a copy of
// agyRead's real lines) and conversations/<id>.db, and its row of
// conversation_summaries.db (agy 1.4's schema) naming the conversation
// that started it in parent_conversation_id, nesting_depth deep. A
// subagent's subagent has its row alone; one whose parent agy no longer
// has is kept too.
func putAgySubagents(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "brain", agyRead, ".system_generated", "logs", "transcript_full.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{agyChild, agyOrphan} {
		p := filepath.Join(dir, "brain", id, ".system_generated", "logs", "transcript_full.jsonl")
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(dir, "conversations"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "conversations", agyGrandchild+".db"), []byte("SQLite format 3\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, r := range []struct {
		id, title, parent, agent string
		depth                    int
	}{
		{agyChild, "Reading a.txt for the parent", agyTwoTurns, "explorer", 1},
		{agyGrandchild, "Checking a.txt", agyChild, "explorer", 2},
		{agyOrphan, "Left by a deleted parent", "0a0b0c0d-1111-4000-8000-000000000000", "explorer", 1},
	} {
		if _, err := db.Exec(`INSERT INTO conversation_summaries (conversation_id, title, last_modified_time, workspace_uris, last_user_input_time, agent_name, parent_conversation_id, nesting_depth) VALUES (?, ?, '2026-10-25 09:49:50+00:00', '["file:///work/proj"]', '2026-10-25 09:49:47+00:00', ?, ?, ?)`, r.id, r.title, r.agent, r.parent, r.depth); err != nil {
			t.Fatal(err)
		}
	}
}

// JohnRee97129809 on X: every subagent agy ran was listed as a
// conversation of its own, and agy's conversations on the Sessions page
// were all subagents'. A subagent's conversation is filed under the one
// at the top of it, its tokens counted there, and listed as its
// subagent; one whose parent is gone stays listed.
func TestAgySubagentFolded(t *testing.T) {
	inZone(t, 0)
	dir := putAgy(t)
	before := find(t, List(0), "agy", agyTwoTurns)
	read := find(t, List(0), "agy", agyRead)
	putAgySubagents(t, dir)
	Reset()

	// read first as a conversation of its own (a magpie before this
	// one), then as its parent's subagent
	db, err := sql.Open("sqlite", filepath.Join(dir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE conversation_summaries SET parent_conversation_id = '' WHERE conversation_id = ?`, agyChild); err != nil {
		t.Fatal(err)
	}
	find(t, List(0), "agy", agyChild)
	if _, err := db.Exec(`UPDATE conversation_summaries SET parent_conversation_id = ? WHERE conversation_id = ?`, agyTwoTurns, agyChild); err != nil {
		t.Fatal(err)
	}

	ss := List(0)
	if n := count(ss, "agy"); n != 4 {
		t.Fatalf("want 4 agy sessions (3 and the orphan), got %d", n)
	}
	for _, s := range ss {
		if s.Agent == "agy" && (s.ID == agyChild || s.ID == agyGrandchild) {
			t.Fatalf("a subagent listed on its own: %+v", s)
		}
	}
	find(t, ss, "agy", agyOrphan)
	two := find(t, ss, "agy", agyTwoTurns)
	if two.Title != before.Title || two.Cwd != before.Cwd || two.Path != before.Path || two.Resume != before.Resume {
		t.Fatalf("the parent changed: %+v, was %+v", two, before)
	}
	want := before.Tokens
	want.add(read.Tokens)
	if two.Tokens != want {
		t.Fatalf("the parent's tokens %+v, want its own and its subagent's %+v", two.Tokens, want)
	}
	if len(two.Subagents) != 2 {
		t.Fatalf("subagents %+v", two.Subagents)
	}
	byID := map[string]Subagent{}
	for _, sa := range two.Subagents {
		byID[sa.ID] = sa
	}
	if c := byID[agyChild]; c.Title != "Reading a.txt for the parent" || c.Kind != "explorer" || c.Parent != "" {
		t.Fatalf("the subagent %+v", c)
	}
	if g := byID[agyGrandchild]; g.Title != "Checking a.txt" || g.Parent != agyChild {
		t.Fatalf("the subagent's subagent %+v", g)
	}

	child := filepath.Join(dir, "brain", agyChild, ".system_generated", "logs", "transcript_full.jsonl")
	if d := cache[child].Days.of("2026-10-25"); d == nil || d.Prompts != 0 {
		t.Fatalf("the subagent's day, read before as its own: %+v", d)
	}

	tr, err := SubagentTranscript(two, agyChild)
	if err != nil || len(tr.Parts) == 0 || tr.Parts[0].Text != "read a.txt" {
		t.Fatalf("the subagent's transcript %+v, %v", tr.Parts, err)
	}
	if tr, _ := TranscriptOf(two); len(tr.Parts) == 0 || tr.Parts[0].Text == "read a.txt" {
		t.Fatalf("the parent's transcript is the subagent's: %+v", tr.Parts)
	}
}

// What agy asked its subagent is no prompt of the user's, and its time
// runs beside its parent's: read as a subagent's, a conversation counts
// its tokens and tools but no prompts, replies or active time. A
// conversation read before as one of its own is read again as one.
func TestAgySubagentCounts(t *testing.T) {
	inZone(t, 0)
	dir := putAgy(t)
	f := file{agent: "agy", path: filepath.Join(dir, "brain", agyRead, ".system_generated", "logs", "transcript_full.jsonl"), main: true}
	if !stat(&f) {
		t.Fatal("no transcript")
	}
	own := parseAgy(f, nil)
	f.main = false
	for name, s := range map[string]*state{"afresh": parseAgy(f, nil), "after a read as its own": parseAgy(f, own)} {
		d := s.Days.of("2026-10-25")
		if d == nil || d.Prompts != 0 || d.Replies != 0 || d.Active != 0 || d.Tools.of("read_file") != 1 {
			t.Fatalf("%s: day %+v", name, d)
		}
		if s.Title != own.Title || !reflect.DeepEqual(s.Models, own.Models) {
			t.Fatalf("%s: title %q models %+v, want %q %+v", name, s.Title, s.Models, own.Title, own.Models)
		}
	}
}

// A conversation's delete takes its subagents' conversations with it, as
// they are listed in it, and leaves another conversation's alone.
func TestAgyDeleteTakesSubagents(t *testing.T) {
	dir := putAgy(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	putAgySubagents(t, dir)
	agyConversation(t, dir, agyTwoTurns)
	agyConversation(t, dir, agyChild)
	others := agyConversation(t, dir, agyOrphan)
	others = append(others, filepath.Join(dir, "brain", agyOrphan), filepath.Join(dir, "brain", agyRead))
	ageTree(dir)
	Reset()

	if _, ok := findManaged(ListAgent("agy"), agyTwoTurns); !ok {
		t.Fatal("not listed")
	}
	tr, err := Delete("agy", agyTwoTurns)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(dir, "brain", agyTwoTurns), filepath.Join(dir, "brain", agyChild),
		filepath.Join(dir, "conversations", agyChild+".db"), filepath.Join(dir, "annotations", agyChild+".pbtxt"),
		filepath.Join(dir, "conversations", agyGrandchild+".db"),
	} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	for _, p := range others {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("another's %s: %v", p, err)
		}
	}
	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	Reset()
	if s := find(t, List(0), "agy", agyTwoTurns); len(s.Subagents) != 2 {
		t.Fatalf("restored: %+v", s.Subagents)
	}
}

// Rows that name each other as parents (no agy writes them, but a
// database is the user's) fold nothing: both stay listed.
func TestAgyFoldCycle(t *testing.T) {
	row := func(id, parent string) file {
		f := file{agent: "agy", key: "agy:" + id, main: true}
		f.w().agy = &agySummary{Parent: parent}
		return f
	}
	fs := agyFold([]file{row(agyChild, agyGrandchild), row(agyGrandchild, agyChild), row(agyOrphan, agyOrphan)})
	for _, f := range fs {
		if !f.main || f.key == "" || f.m().sid != "" {
			t.Fatalf("folded %+v", f)
		}
	}
}

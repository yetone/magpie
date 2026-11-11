package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

// Claude run with CLAUDE_CONFIG_DIR set to a folder of each account's
// (~/.claude-work beside the one magpie runs with) is counted too, a session
// kept in both folders once, and a folder with no projects/ is no Claude
// folder (@fivesmallq on X: only one Claude folder's token use counted).
func TestCallsReadEveryClaudeFolder(t *testing.T) {
	d := setupCalls(t)
	home, _ := os.UserHomeDir()
	work := filepath.Join(home, ".claude-work")
	writeLines(t, filepath.Join(d.claude, "projects", "-a", "main1.jsonl"),
		claudeMsg("m1", "claude-opus-5-5", 1, 2, 0, 0, 1))
	writeLines(t, filepath.Join(work, "projects", "-b", "work1.jsonl"),
		swap(claudeMsg("m2", "claude-opus-5-5", 3, 4, 0, 0, 2), `"sessionId":"sess1"`, `"sessionId":"work1"`))
	// the same session copied into both folders
	writeLines(t, filepath.Join(work, "projects", "-a", "main1.jsonl"),
		claudeMsg("m1", "claude-opus-5-5", 1, 2, 0, 0, 1))
	writeLines(t, filepath.Join(home, ".claude-notes", "todo.jsonl"), "{}")

	cs := Calls(time.Time{})
	got := map[string]int{}
	for _, c := range cs {
		got[c.RequestID]++
	}
	if len(cs) != 2 || got["req_m1"] != 1 || got["req_m2"] != 1 {
		t.Fatalf("calls = %+v, want req_m1 and req_m2 once each", cs)
	}
}

// A session kept in two Claude folders is one row of the list, and the
// other folder's own sessions are listed.
func TestListReadsEveryClaudeFolderOnce(t *testing.T) {
	d := setupCalls(t)
	home, _ := os.UserHomeDir()
	work := filepath.Join(home, ".claude-work")
	user := func(id string) string {
		return `{"type":"user","timestamp":"` + stamp(0) + `","message":{"role":"user","content":"hello"},"cwd":"/work/app","sessionId":"` + id + `"}`
	}
	writeLines(t, filepath.Join(d.claude, "projects", "-a", "main1.jsonl"), user("main1"))
	writeLines(t, filepath.Join(work, "projects", "-a", "main1.jsonl"), user("main1"))
	writeLines(t, filepath.Join(work, "projects", "-b", "work1.jsonl"), user("work1"))
	n := map[string]int{}
	for _, s := range List(0) {
		n[s.ID]++
	}
	if len(n) != 2 || n["main1"] != 1 || n["work1"] != 1 {
		t.Fatalf("rows = %v, want main1 and work1 once each", n)
	}
}

// Every Claude folder's files are listed into one slice made at their
// size, not grown file by file on every pass (a pass every 10 s while
// the Sessions page is open), a pass like the last gives the last one's
// list, and a session kept in two folders is still listed once.
func TestClaudeFilesMadeAtTheirSize(t *testing.T) {
	d := setupCalls(t)
	home, _ := os.UserHomeDir()
	work := filepath.Join(home, ".claude-work")
	user := func(id string) string {
		return `{"type":"user","timestamp":"` + stamp(0) + `","message":{"role":"user","content":"hello"},"cwd":"/work/app","sessionId":"` + id + `"}`
	}
	for i := range 20 {
		id := "main" + itoa(i)
		writeLines(t, filepath.Join(d.claude, "projects", "-a", id+".jsonl"), user(id))
		id = "work" + itoa(i)
		writeLines(t, filepath.Join(work, "projects", "-b", id+".jsonl"), user(id))
	}
	writeLines(t, filepath.Join(work, "projects", "-a", "main0.jsonl"), user("main0"))
	if len(ClaudeDirs()) != 2 {
		t.Fatalf("folders %v", ClaudeDirs())
	}
	fs := claudeFiles()
	if len(fs) != 40 {
		t.Fatalf("%d files, want 40", len(fs))
	}
	if cap(fs) != 40 {
		t.Fatalf("listed into room for %d, want the 40 kept", cap(fs))
	}
	if again := claudeFiles(); unsafe.SliceData(again) != unsafe.SliceData(fs) {
		t.Fatal("an unchanged pass made its list again")
	}
}

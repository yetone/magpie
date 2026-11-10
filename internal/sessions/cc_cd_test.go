package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testdata/claude-cd is Claude Code 2.1.316's own file of a session moved
// with /cd, from a sandbox run with a fake API, its paths written as /w and
// its request records, attachments and file snapshots left out. The
// session was started in /w/projA with two prompts; /cd /w/projB moved
// its file to projects/-w-projB, added a relocated line naming the new
// folder and went on with a third prompt there (moved.jsonl). Lines from
// before the move keep cwd /w/projA. moved-back.jsonl is the same session
// after a second /cd, back to /w/projA, and a fourth prompt.
const ccMoved = "da61acf5-7cf2-4bd9-9473-df20974006f4"

func ccCdFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude-cd", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ccCdWrite writes b as the session's file in the project folder named,
// over a minute old, so it isn't one still being written.
func ccCdWrite(t *testing.T, dir, project string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, "projects", project, ccMoved+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

// ccCdRows is every listed row of the moved session.
func ccCdRows(t *testing.T) []Managed {
	t.Helper()
	var out []Managed
	for _, m := range ListAgent("claude") {
		if m.ID == ccMoved {
			out = append(out, m)
		}
	}
	return out
}

// before is the session's file as it was in /w/projA, before the /cd:
// every line up to the move.
func ccCdBefore(t *testing.T) []byte {
	t.Helper()
	b := ccCdFixture(t, "moved.jsonl")
	i := strings.Index(string(b), `{"type":"relocated"`)
	if i < 0 {
		t.Fatal("moved.jsonl has no relocated line")
	}
	return b[:i]
}

// usrname1833 on Discord: after /cd the file is in the new folder, and
// magpie listed the session under the folder it was started in.
func TestClaudeCdListedUnderNewFolder(t *testing.T) {
	claude, _ := setup(t)
	old := ccCdWrite(t, claude, "-w-projA", ccCdBefore(t))
	rows := ccCdRows(t)
	if len(rows) != 1 || rows[0].Cwd != "/w/projA" {
		t.Fatalf("before the move: %+v", rows)
	}

	// what /cd does: the file goes to the new project folder and grows
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	ccCdWrite(t, claude, "-w-projB", ccCdFixture(t, "moved.jsonl"))
	rows = ccCdRows(t)
	if len(rows) != 1 {
		t.Fatalf("after the move, %d rows: %+v", len(rows), rows)
	}
	if rows[0].Cwd != "/w/projB" {
		t.Errorf("after the move the session is under %q, want /w/projB", rows[0].Cwd)
	}
	if !strings.Contains(rows[0].Resume, "/w/projB") || !strings.Contains(rows[0].Resume, "claude --resume "+ccMoved) {
		t.Errorf("resume = %q, want it run in /w/projB", rows[0].Resume)
	}
	s, ok := Find("claude", ccMoved)
	if !ok || s.Cwd != "/w/projB" {
		t.Errorf("Find = %v %q", ok, s.Cwd)
	}
	for _, l := range listSessions(0) {
		if l.ID == ccMoved && l.Cwd != "/w/projB" {
			t.Errorf("List has it under %q", l.Cwd)
		}
	}
}

// A second /cd adds another relocated line: the last one names the folder.
func TestClaudeCdLastMoveWins(t *testing.T) {
	claude, _ := setup(t)
	ccCdWrite(t, claude, "-w-projA", ccCdFixture(t, "moved-back.jsonl"))
	rows := ccCdRows(t)
	if len(rows) != 1 || rows[0].Cwd != "/w/projA" {
		t.Fatalf("rows: %+v", rows)
	}
}

// A file a magpie before this one read is kept parsed with the folder its
// first line names. Its size and time don't change, so only knowing the
// parse is from before relocated lines were read makes it read again.
func TestClaudeCdKeptParseReadAgain(t *testing.T) {
	claude, _ := setup(t)
	p := ccCdWrite(t, claude, "-w-projB", ccCdFixture(t, "moved.jsonl"))
	if rows := ccCdRows(t); len(rows) != 1 || rows[0].Cwd != "/w/projB" {
		t.Fatalf("rows: %+v", rows)
	}
	mu.Lock()
	s := cache[p]
	if s == nil {
		mu.Unlock()
		t.Fatal("not in the cache")
	}
	stale := s.clone()
	stale.Cwd, stale.CdRead = "/w/projA", false
	cache[p] = stale
	forgetKeptList()
	mu.Unlock()
	if rows := ccCdRows(t); len(rows) != 1 || rows[0].Cwd != "/w/projB" {
		t.Fatalf("kept parse from before: %+v", rows)
	}

	// one from before in the folder it names is kept, and marked so its
	// folder isn't worked out on every refresh
	mu.Lock()
	kept := cache[p].clone()
	kept.CdRead, kept.Title = false, "kept title"
	cache[p] = kept
	mu.Unlock()
	if rows := ccCdRows(t); len(rows) != 1 || rows[0].Title != "kept title" {
		t.Fatalf("an unmoved kept parse was read again: %+v", rows)
	}
	mu.Lock()
	marked := cache[p].CdRead
	mu.Unlock()
	if !marked {
		t.Error("the kept parse isn't marked as checked")
	}

	// one this magpie made is kept: a session in a folder that isn't its
	// cwd's for another reason (a project folder named by hand) is read once
	mu.Lock()
	cache[p].Cwd = "/w/elsewhere"
	mu.Unlock()
	if rows := ccCdRows(t); len(rows) != 1 || rows[0].Cwd != "/w/elsewhere" {
		t.Fatalf("a parse this magpie made was read again: %+v", rows)
	}
}

// A path whose folder name Claude Code cuts at 200 and ends with a hash.
func TestClaudeCdLongFolder(t *testing.T) {
	cwd := "/" + strings.Repeat("a", 230)
	name := claudeSlug(cwd)[:200] + "-1x2y3z"
	if !ccInProject(name, cwd) {
		t.Error("the hashed folder isn't the cwd's")
	}
	if ccInProject(name, "/"+strings.Repeat("b", 230)) {
		t.Error("another cwd's")
	}
	if !ccInProject("-w-projB", "/w/projB") || ccInProject("-w-projA", "/w/projB") {
		t.Error("short names")
	}
}

// magpie's own project move of a session /cd brought here: its first lines
// name the folder it came from, and only what is after the move is in the
// folder being moved.
func TestClaudeRelocationAfterCd(t *testing.T) {
	b := ccCdFixture(t, "moved.jsonl")
	out, changed, err := relocateClaudeJSONL(b, "/w/projB", "/w/projC", true)
	if err != nil || !changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	got := string(out)
	if !strings.Contains(got, `"relocatedCwd":"/w/projC"`) || strings.Contains(got, `"cwd":"/w/projB"`) {
		// what was said in it (the /cd command's own text) stays as written
		t.Errorf("a projB cwd left or the move not rewritten:\n%s", got)
	}
	if strings.Count(got, `"cwd":"/w/projA"`) != strings.Count(string(b), `"cwd":"/w/projA"`) {
		t.Error("the lines from before the move were rewritten")
	}
	// the session is no longer in projA: moving projA doesn't take it
	if _, _, err := relocateClaudeJSONL(b, "/w/projA", "/w/projC", true); err == nil {
		t.Error("moved a session /cd took out of the folder")
	}
}

// Before a long file's first read its row stands on a glance at its start
// and end: a /cd near the end names the folder there too.
func TestClaudeCdGlance(t *testing.T) {
	claude, _ := setup(t)
	p := ccCdWrite(t, claude, "-w-projB", ccCdFixture(t, "moved.jsonl"))
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	g := glance(file{agent: "claude", key: "claude:" + ccMoved, path: p, size: st.Size(), mod: modOf(st.ModTime()), main: true})
	if g == nil || g.Cwd != "/w/projB" {
		t.Fatalf("glance: %+v", g)
	}
}

package library

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The notes on a server and a skill (youyo.p on Discord: with many of
// them, which is which and when to use it) are the library's alone:
// nothing magpie writes for an agent holds them, whichever format the
// agent reads, through the MCP router or not.
func TestNoteNeverGiven(t *testing.T) {
	h := sandbox(t)
	const serverNote, skillNote = "备注 the shop's SQL Server, read only", "备注 for the weekly report"
	agents := []string{"claude", "codex", "opencode", "gemini"}
	ok(t)(SaveServer("", Server{Name: "db", Transport: "stdio", Command: "npx",
		Args: []string{"-y", "universal-db-mcp", "--type", "sqlserver"}, Agents: agents, Note: serverNote}))
	ok(t)(SaveServer("", Server{Name: "docs", Transport: "http", URL: "https://example.com/mcp", Agents: agents, Note: serverNote}))
	skill(t, filepath.Join(h, "src/pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(filepath.Join(h, "src"), []string{"pdf"}, agents))
	ok(t)(SkillNote("pdf", skillNote))
	// given as copies too, whose files are the agents' own
	ok(t)(SetSkillHow("claude", "copy"))

	// kept, and shown on the page
	if s := serverIn(t, "db"); s == nil || s.Note != serverNote {
		t.Fatalf("the server's note wasn't kept: %+v", s)
	}
	if v := offView(t, "pdf"); v.Note != skillNote {
		t.Fatalf("the skill's note wasn't kept: %+v", v)
	}

	given := func(when string) {
		t.Helper()
		own := filepath.Clean(settings.Dir())
		n := 0
		filepath.WalkDir(h, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && filepath.Clean(p) == own {
				return filepath.SkipDir // magpie's own: library.json, its backups
			}
			if !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			n++
			if s := string(b); strings.Contains(s, "备注") {
				t.Errorf("%s: %s has a note in it:\n%s", when, p, s)
			}
			return nil
		})
		if n == 0 {
			t.Fatalf("%s: no agent file was read", when)
		}
	}
	for _, f := range []string{".claude.json", ".codex/config.toml", ".claude/skills/pdf/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(h, f)); err != nil {
			t.Fatalf("not given to the agents: %v", err)
		}
	}
	given("each agent's own entry")

	ok(t)(SetMCPHub(true))
	given("through the router")
	for _, s := range []string{"db", "docs"} {
		b, _ := json.Marshal(hubServerOf(serverIn(t, s)))
		if strings.Contains(string(b), "备注") {
			t.Fatalf("the router tells an agent of %s with its note: %s", s, b)
		}
	}
}

// A note is carried to the other computers with the rest of the server
// or the skill, and taking it away is carried too.
func TestNoteCarried(t *testing.T) {
	a := sandbox(t)
	setUpLibrary(t, a) // the server fs and the skill pdf
	s := serverIn(t, "fs")
	s.Note = "the project's files"
	ok(t)(SaveServer("fs", *s))
	ok(t)(SkillNote("pdf", "  when a PDF\nis attached  "))
	if v := offView(t, "pdf"); v.Note != "when a PDF is attached" {
		t.Fatalf("a note is one line: %q", v.Note)
	}
	fromA := carry(t)

	b := sandbox(t)
	ok(t)(Put(&fromA))
	if s := serverIn(t, "fs"); s == nil || s.Note != "the project's files" {
		t.Fatalf("the server's note didn't come: %+v", s)
	}
	if v := offView(t, "pdf"); v.Note != "when a PDF is attached" {
		t.Fatalf("the skill's note didn't come: %+v", v)
	}

	ok(t)(SkillNote("pdf", ""))
	fromB := carry(t)
	at(t, a)
	ok(t)(Put(&fromB))
	if v := offView(t, "pdf"); v.Note != "" {
		t.Fatalf("a note taken away on B is still on A: %q", v.Note)
	}
	at(t, b)
}

// A note is a line, not a document: a longer one is turned away whole,
// rather than cut.
func TestNoteTooLong(t *testing.T) {
	h := sandbox(t)
	skill(t, filepath.Join(h, "src/pdf"), "pdf", "Read PDFs")
	ok(t)(InstallSkills(filepath.Join(h, "src"), []string{"pdf"}, []string{"claude"}))
	long := strings.Repeat("备", maxNote+1)
	if _, err := SkillNote("pdf", long); err == nil {
		t.Fatal("a skill's note over the limit was kept")
	}
	if _, err := SaveServer("", Server{Name: "x", Transport: "stdio", Command: "x", Note: long}); err == nil {
		t.Fatal("a server's note over the limit was kept")
	}
	ok(t)(SkillNote("pdf", strings.Repeat("备", maxNote)))
	if _, err := SkillNote("nope", "a"); err == nil {
		t.Fatal("a note on no skill")
	}
}

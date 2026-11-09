package library

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applyWiped is what an apply must never do to an agent's instructions
// file: a library reading empty has lost its instructions, not revoked
// them, and the files it gave out are then the only copy left (#5709: a
// start-up apply with an empty Library wiped nine agents' files to match,
// four of them deleted whole).
func applyWiped(t *testing.T, before []byte, p string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		t.Fatalf("%s was deleted whole by the apply", p)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, before) {
		t.Fatalf("%s was rewritten by the apply:\nwas %q\nnow %q", p, before, b)
	}
}

// lostLibrary is a library.json whose instructions are gone, as an
// upgrade that reset the index leaves one: no agent ticked (or none of
// the text they were to get), while the agents still hold what it wrote.
func lostLibrary(t *testing.T, agents []string, applied map[string]*Applied) {
	t.Helper()
	l := &Library{Instructions: Instructions{Agents: agents}, Applied: applied}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path(), string(b))
	if err := os.Remove(setPath(defaultSet)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// An apply — the write every start does over the whole library (Sync, the
// page's change too) — that finds the library emptied under it (no agent
// ticked, no text kept) leaves the agents' files alone: what magpie gave
// them is the only copy of instructions the library no longer has (#5709).
// The page's own take-back is another write and keeps working: it runs
// with the library as the user left it, and the recovery at the end shows
// the agents get new text the moment the library has it again.
func TestEmptyLibraryApplyLeavesAgentsFilesAlone(t *testing.T) {
	h := sandbox(t)
	own := "House rules of my own."
	write(t, filepath.Join(h, ".codex/AGENTS.md"), own)
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex", "dsh"}}))
	cx, dx := filepath.Join(h, ".codex/AGENTS.md"), filepath.Join(h, ".dsh/AGENTS.md")
	cxBefore, err := os.ReadFile(cx)
	if err != nil {
		t.Fatal(err)
	}
	dxBefore, err := os.ReadFile(dx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dxBefore), blockBegin) {
		t.Fatalf("dsh got no part:\n%s", dxBefore)
	}
	// the upgrade's reset: agents unticked and the text gone, the files
	// the agents hold untouched — then the start-up apply runs
	lostLibrary(t, nil, nil)
	ok(t)(Sync())
	applyWiped(t, cxBefore, cx)
	applyWiped(t, dxBefore, dx)

	// recovery: with the library given text again, both get it
	again := "Back again."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &again, Agents: []string{"codex", "dsh"}}))
	if s := read(t, cx); !strings.Contains(s, again) || !strings.Contains(s, own) {
		t.Errorf("codex after recovery:\n%s", s)
	}
	if s := read(t, dx); !strings.Contains(s, again) {
		t.Errorf("dsh after recovery:\n%s", s)
	}
}

// The agents still ticked but the text gone reads empty to an apply all
// the same (the part it would write hashes empty): their files stay.
func TestEmptyTextApplyLeavesTickedFilesAlone(t *testing.T) {
	h := sandbox(t)
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex", "dsh"}}))
	cx, dx := filepath.Join(h, ".codex/AGENTS.md"), filepath.Join(h, ".dsh/AGENTS.md")
	cxBefore, err := os.ReadFile(cx)
	if err != nil {
		t.Fatal(err)
	}
	dxBefore, err := os.ReadFile(dx)
	if err != nil {
		t.Fatal(err)
	}
	lostLibrary(t, []string{"codex", "dsh"}, nil)
	ok(t)(Sync())
	applyWiped(t, cxBefore, cx)
	applyWiped(t, dxBefore, dx)
}

// A library whose record says the last it wrote an agent was empty while
// the file still holds a part is one an apply already wiped once: the
// user's files came back from magpie's backups, and the next apply must
// not wipe them again. It says why it left them.
func TestApplySaysWhyItLeftAPartAlone(t *testing.T) {
	h := sandbox(t)
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex", "dsh"}}))
	cx, dx := filepath.Join(h, ".codex/AGENTS.md"), filepath.Join(h, ".dsh/AGENTS.md")
	cxBefore, err := os.ReadFile(cx)
	if err != nil {
		t.Fatal(err)
	}
	dxBefore, err := os.ReadFile(dx)
	if err != nil {
		t.Fatal(err)
	}
	lostLibrary(t, nil, map[string]*Applied{
		"codex": {Instructions: true, InstrHash: hash("")},
		"dsh":   {Instructions: true, InstrHash: hash("")},
	})
	out := &bytes.Buffer{}
	flags, w := log.Flags(), log.Writer()
	log.SetFlags(0)
	log.SetOutput(out)
	t.Cleanup(func() { log.SetFlags(flags); log.SetOutput(w) })
	ok(t)(Sync())
	applyWiped(t, cxBefore, cx)
	applyWiped(t, dxBefore, dx)
	if s := out.String(); !strings.Contains(s, "codex") || !strings.Contains(s, cx) {
		t.Errorf("the apply said nothing of leaving codex alone:\n%s", s)
	}
}

// The agent list emptied with the text still kept is the same loss to an
// apply (the reporter's library.json had every agent unticked): the files
// stay. Unticking every agent on the page still takes the parts back.
func TestUntickedLibraryApplyLeavesFilesAlone(t *testing.T) {
	h := sandbox(t)
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex", "dsh"}}))
	cx, dx := filepath.Join(h, ".codex/AGENTS.md"), filepath.Join(h, ".dsh/AGENTS.md")
	cxBefore, err := os.ReadFile(cx)
	if err != nil {
		t.Fatal(err)
	}
	dxBefore, err := os.ReadFile(dx)
	if err != nil {
		t.Fatal(err)
	}
	l := &Library{Instructions: Instructions{}}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path(), string(b))
	ok(t)(Sync())
	applyWiped(t, cxBefore, cx)
	applyWiped(t, dxBefore, dx)

	// the page's own take-back is the user's word, and goes out
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex", "dsh"}}))
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{}}))
	if s := readText(cx); strings.Contains(s, blockBegin) {
		t.Errorf("unticking codex on the page left its part:\n%s", s)
	}
}

// A shared text that can't be read is unknown, not empty: no write cuts
// the agents' parts down to what is left (#5709).
func TestUnreadableTextLeavesFilesAlone(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	h := sandbox(t)
	shared := "Be brief."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"codex", "dsh"}}))
	cx := filepath.Join(h, ".codex/AGENTS.md")
	cxBefore, err := os.ReadFile(cx)
	if err != nil {
		t.Fatal(err)
	}
	p := setPath(defaultSet)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(p, 0o644) })
	Sync()
	applyWiped(t, cxBefore, cx)
}

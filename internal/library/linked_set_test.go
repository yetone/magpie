package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A set can be kept in a file of the user's own, ~/.agents/AGENTS.md that
// Droid and omp read too, instead of a copy in the library: the agents get
// what the file says, a change made in it reaches them, the page's edits
// are written to it, and removing the set lets go of the file without
// touching it (新v学员 on Discord).
func TestLinkedSet(t *testing.T) {
	h := sandbox(t)
	cx := filepath.Join(h, ".codex/AGENTS.md")
	shared := filepath.Join(h, ".agents/AGENTS.md")
	write(t, shared, "Shared rules.\n")
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{"codex"}}))
	ok(t)(SaveInstructions(InstructionsChange{Create: &InstrSet{ID: "agents", Name: "Everyone", File: "~/.agents/AGENTS.md"}}))
	if s := read(t, cx); !strings.Contains(s, "Shared rules.") {
		t.Fatalf("codex didn't get the linked file's text (an empty set on gives way to a new one):\n%s", s)
	}
	iv, _ := ReadInstructions()
	if len(iv.Sets) != 2 || iv.Sets[1].File != shared || iv.Sets[1].Text != "Shared rules." || !iv.Sets[1].Active {
		t.Errorf("view: %+v", iv.Sets)
	}
	if _, err := os.Stat(setPath("agents")); !os.IsNotExist(err) {
		t.Error("a linked set has a copy in the library too")
	}

	// edited outside magpie: the first look notes the file, the next one
	// after a change writes it into the agents
	if res, err := SyncLinked(); err != nil || res != nil {
		t.Fatalf("first look: %v %v", res, err)
	}
	if res, err := SyncLinked(); err != nil || res != nil {
		t.Fatalf("nothing changed, yet: %v %v", res, err)
	}
	write(t, shared, "Shared rules, edited by hand.\n")
	ok(t)(SyncLinked())
	if s := read(t, cx); !strings.Contains(s, "edited by hand") {
		t.Errorf("a change in the file didn't reach codex:\n%s", s)
	}

	// the page's text goes into the file, the file kept aside first
	page := "Written on the page."
	res := ok(t)(SaveInstructions(InstructionsChange{Texts: map[string]*string{"agents": &page}}))
	if s := read(t, shared); s != page+"\n" {
		t.Errorf("the file has %q", s)
	}
	if res.Backup == "" || !strings.Contains(read(t, filepath.Join(res.Backup, "library", "AGENTS.md")), "edited by hand") {
		t.Errorf("the file wasn't kept aside before it was written: %q", res.Backup)
	}
	if s := read(t, cx); !strings.Contains(s, page) {
		t.Errorf("codex:\n%s", s)
	}
	// emptied, it stays, empty: it is the user's
	empty := ""
	ok(t)(SaveInstructions(InstructionsChange{Texts: map[string]*string{"agents": &empty}}))
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("an emptied linked file was removed: %v", err)
	}
	write(t, shared, "Back again.\n")

	// a profile taken with no text puts none back over the file
	ok(t)(SaveInstructions(InstructionsChange{Activate: "default"}))
	snap, _ := Snapshot()
	ok(t)(SaveInstructions(InstructionsChange{Activate: "agents"}))
	snap.Instructions.Set, snap.Instructions.Shared = "agents", ""
	ok(t)(Restore(snap))
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("a profile's empty text removed the linked file: %v", err)
	}

	ok(t)(SaveInstructions(InstructionsChange{Activate: "default"}))
	ok(t)(SaveInstructions(InstructionsChange{Remove: "agents"}))
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("removing the set removed the user's file: %v", err)
	}
	iv, _ = ReadInstructions()
	if len(iv.Sets) != 1 {
		t.Errorf("the set stayed: %+v", iv.Sets)
	}
}

// A set can't be kept in a file magpie writes into, or in the library's
// own, or in a file another set has.
func TestLinkRefused(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".agents/AGENTS.md"), "Mine.\n")
	ok(t)(SaveInstructions(InstructionsChange{Create: &InstrSet{ID: "a", Name: "A", File: filepath.Join(h, ".agents/AGENTS.md")}}))
	marked := filepath.Join(h, "marked.md")
	write(t, marked, "x\n"+blockBegin+"\ny\n"+blockEnd+"\n")
	write(t, filepath.Join(h, ".codex/AGENTS.md"), "codex's own\n")
	for name, file := range map[string]string{
		"relative":          "notes/AGENTS.md",
		"a folder":          filepath.Join(h, ".agents"),
		"an agent's own":    filepath.Join(h, ".codex/AGENTS.md"),
		"the library's":     filepath.Join(Dir(), "instructions.md"),
		"magpie's part":     marked,
		"another set's":     "~/.agents/AGENTS.md",
		"another set's too": filepath.Join(h, ".agents/../.agents/AGENTS.md"),
	} {
		if _, err := SaveInstructions(InstructionsChange{Create: &InstrSet{ID: "b", Name: "B", File: file}}); err == nil {
			t.Errorf("%s (%s) was linked", name, file)
		}
	}
	// a file not there yet is made when the set is first saved
	later := filepath.Join(h, "later/AGENTS.md")
	text := "Later."
	ok(t)(SaveInstructions(InstructionsChange{Create: &InstrSet{ID: "b", Name: "B", File: later}, Texts: map[string]*string{"b": &text}}))
	if s := read(t, later); s != "Later.\n" {
		t.Errorf("the new file has %q", s)
	}
}

// Carried to another computer (davsync, a backup) a linked set goes as its
// text: where the file is stays this computer's. Put back here, the set
// keeps its file, which gets the text carried and is never emptied for a
// text not carried.
func TestLinkedSetCarried(t *testing.T) {
	h := sandbox(t)
	shared := filepath.Join(h, ".agents/AGENTS.md")
	write(t, shared, "Here.\n")
	ok(t)(SaveInstructions(InstructionsChange{Create: &InstrSet{ID: "agents", Name: "Everyone", File: shared}}))
	b, err := Collect()
	if err != nil || b == nil {
		t.Fatal(b, err)
	}
	if b.Texts["agents"] != "Here." {
		t.Errorf("texts: %v", b.Texts)
	}
	for _, s := range b.Instructions.Sets {
		if s.File != "" {
			t.Errorf("the file went with the set: %+v", s)
		}
	}
	if l, _ := load(); l.instrSet("agents").File != shared {
		t.Error("Collect took the file off the library's own set")
	}

	b.Texts["agents"] = "From the other computer."
	ok(t)(Put(b))
	if s := read(t, shared); s != "From the other computer.\n" {
		t.Errorf("the file has %q", s)
	}
	iv, _ := ReadInstructions()
	if iv.Sets[1].File != shared {
		t.Errorf("Put let go of the file: %+v", iv.Sets)
	}
	if _, err := os.Stat(setPath("agents")); !os.IsNotExist(err) {
		t.Error("Put wrote a copy into the library")
	}

	delete(b.Texts, "agents")
	b.Instructions.Sets[len(b.Instructions.Sets)-1].File = filepath.Join(h, "elsewhere.md")
	ok(t)(Put(b))
	if s := read(t, shared); s != "From the other computer.\n" {
		t.Errorf("a bundle without the text emptied the file: %q", s)
	}
	if l, _ := load(); l.instrSet("agents").File != shared {
		t.Errorf("a carried file was taken: %q", l.instrSet("agents").File)
	}
}

// #4938: the copy of the app an update left finishing its requests went
// on syncing the linked sets into the agents' files every 30s, beside the
// new version, for hours. KeepLinked keeps the agents up with the linked
// file while its context lasts, and returns once it ends.
func TestKeepLinkedEndsWithItsContext(t *testing.T) {
	h := sandbox(t)
	cx := filepath.Join(h, ".codex/AGENTS.md")
	shared := filepath.Join(h, ".agents/AGENTS.md")
	write(t, shared, "Shared rules.\n")
	ok(t)(SaveInstructions(InstructionsChange{Agents: []string{"codex"}}))
	ok(t)(SaveInstructions(InstructionsChange{Create: &InstrSet{ID: "agents", Name: "Everyone", File: "~/.agents/AGENTS.md"}}))
	// the file as it is now, for the loop's first look to tell a change
	if _, err := SyncLinked(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepLinked(ctx, 10*time.Millisecond)
	}()
	// a change made in the file reaches the agents while it runs
	write(t, shared, "Shared rules, changed.\n")
	for deadline := time.Now().Add(30 * time.Second); !strings.Contains(read(t, cx), "changed"); {
		if time.Now().After(deadline) {
			t.Fatal("the linked file's change never reached codex")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("KeepLinked went on after its context ended")
	}
}

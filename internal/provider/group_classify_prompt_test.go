package provider

import (
	"strings"
	"testing"
)

// A group's own classifier instructions (sloceanxc on X) are saved
// trimmed, refused past MaxClassifyPrompt characters with the last good
// ones kept, kept by a save that changes something else, and go with the
// group when it is shared.
func TestGroupClassifyPrompt(t *testing.T) {
	fastHome(t)
	prompt := func() string {
		t.Helper()
		g, _, ok := FindGroup(GroupPrefix + "work")
		if !ok {
			t.Fatal("no group work")
		}
		return g.ClassifyPrompt
	}
	own := "用户在写小说。\n续写一段场景属于写作，即使场景里提到代码。"
	g := Group{ID: "work", Name: "Work", Members: []string{"rl/claude-opus-5-5", "an/claude-sonnet-5"}, ClassifyPrompt: "\n  " + own + "  \n"}
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if got := prompt(); got != own {
		t.Fatalf("saved %q", got)
	}

	saved, _, _ := FindGroup(GroupPrefix + "work")
	saved.Routing = Rotate
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}
	if got := prompt(); got != own {
		t.Fatalf("after a routing change %q", got)
	}

	// counted in characters, not bytes
	saved.ClassifyPrompt = strings.Repeat("写", MaxClassifyPrompt)
	if err := SaveGroup(saved); err != nil {
		t.Fatalf("at the most: %v", err)
	}
	saved.ClassifyPrompt = strings.Repeat("写", MaxClassifyPrompt+1)
	if err := SaveGroup(saved); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("past the most: %v", err)
	}
	if got := prompt(); got != strings.Repeat("写", MaxClassifyPrompt) {
		t.Fatalf("refused save changed it to %d characters", len([]rune(got)))
	}

	saved.ClassifyPrompt = own
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}
	text, err := ShareGroup("work")
	if err != nil {
		t.Fatal(err)
	}
	sg, err := ReadShared(text)
	if err != nil || sg.Group.ClassifyPrompt != own {
		t.Fatalf("shared %q, %v", sg.Group.ClassifyPrompt, err)
	}

	saved.ClassifyPrompt = ""
	if err := SaveGroup(saved); err != nil {
		t.Fatal(err)
	}
	if got := prompt(); got != "" {
		t.Fatalf("cleared %q", got)
	}
}

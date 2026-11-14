package agent

import "testing"

// @G_Z_Y on X installed cursor-agent and looked for it on the Agents page
// more than once: the row said "Cursor", which reads as the editor. The row
// is the CLI's; the files and chats it shares with the editor (SharedName)
// are Cursor's.
func TestCursorIsCursorCLI(t *testing.T) {
	a := cursor(t.TempDir())
	if a.Name != "Cursor CLI" {
		t.Errorf("Name = %q, want Cursor CLI", a.Name)
	}
	if a.ID != "cursor" || len(a.Aliases) != 1 || a.Aliases[0] != "cursor-agent" {
		t.Errorf("id or aliases changed: %q %v", a.ID, a.Aliases)
	}
	if got := a.SharedName(); got != "Cursor" {
		t.Errorf("SharedName = %q, want Cursor", got)
	}
	// Copilot CLI's ~/.copilot is GitHub Copilot's, read by VS Code and
	// the JetBrains plugin too; every other agent keeps one name
	for _, b := range All() {
		if b.ID == "copilot" {
			if b.Name != "Copilot CLI" || b.SharedName() != "GitHub Copilot" {
				t.Errorf("copilot: Name %q, SharedName %q", b.Name, b.SharedName())
			}
			continue
		}
		if b.ID != "cursor" && b.SharedName() != b.Name {
			t.Errorf("%s: SharedName %q, Name %q", b.ID, b.SharedName(), b.Name)
		}
	}
}

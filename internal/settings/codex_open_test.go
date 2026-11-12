package settings

import "testing"

// Open Codex with is this computer's own: another
// computer's sync does not carry it in.
func TestCodexOpenIsThisComputers(t *testing.T) {
	from := Settings{CodexOpen: "app"}
	from.KeepOwn(Settings{CodexOpen: "terminal"})
	if from.CodexOpen != "terminal" {
		t.Fatalf("a sync replaced the computer's own choice: %q", from.CodexOpen)
	}
}

package settings

import (
	"slices"
	"testing"
)

// The agents whose picks stay on this computer are this computer's own:
// another computer's list never comes in by sync, a Restore or a backup
// file (chenxiang0010 on Discord).
func TestAgentsHereIsThisComputers(t *testing.T) {
	from := Settings{AgentsHere: []string{"codex"}, AgentsHidden: []string{"gemini"}}
	from.KeepOwn(Settings{})
	if from.AgentsHere != nil {
		t.Fatalf("another computer's kept agents came in: %v", from.AgentsHere)
	}
	if !slices.Equal(from.AgentsHidden, []string{"gemini"}) {
		t.Fatal("the rest of the settings are synced")
	}
	from.KeepOwn(Settings{AgentsHere: []string{"claude"}})
	if !slices.Equal(from.AgentsHere, []string{"claude"}) {
		t.Fatalf("this computer's own was lost: %v", from.AgentsHere)
	}
}

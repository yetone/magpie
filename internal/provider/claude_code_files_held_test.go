package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Each Claude model's price listed the saved accounts' folders again, so
// a build of an agent's models (the window's drift poll, every minute)
// read the folder once per model. While a request holds the catalog they
// are listed once; outside one, and after it, a folder saved since is seen.
func TestClaudeCodeFilesListedOncePerHold(t *testing.T) {
	claudeHome(t)
	defer func(d time.Duration) { heldFor = d }(heldFor)
	heldFor = time.Hour // a loaded machine's pause isn't the bound
	saved := func(name string) {
		t.Helper()
		dir := filepath.Join(claudeDirsRoot(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saved("a")
	release := Hold()
	first := claudeCodeFiles()
	if len(first) != 2 {
		t.Fatalf("listed %v", first)
	}
	saved("b") // written behind magpie's back: not a write the hold sees
	if again := claudeCodeFiles(); len(again) != len(first) || &again[0] != &first[0] {
		t.Fatalf("listed again within one hold: %v then %v", first, again)
	}
	release()
	if after := claudeCodeFiles(); len(after) != 3 {
		t.Fatalf("a folder saved during the hold isn't seen after it: %v", after)
	}
}

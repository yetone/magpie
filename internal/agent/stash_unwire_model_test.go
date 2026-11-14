package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// #4258's review: unroute leaves the model stashed for its caller, so
// Claude Code's Unwire that neither puts the model back nor takes magpie's
// out (the stash kept one of magpie's, an older magpie's doing, and the
// user picked their own since) still gives it up, as unroute's unstash did
// before: no model outlives the Disconnect to come back at a later one.
// Unwire is called alone here: Disconnect reaches it only while a field is
// still magpie's, and the main model is the user's own in this case.
func TestClaudeDisconnectLeavesNoModelStashed(t *testing.T) {
	home := syncHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"model":"native-original"}`)
	a := claude(home)
	if err := a.Connect(); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"claude.model": "relay/glm-4.6"})
	if !isMagpie("relay/glm-4.6") {
		t.Fatal("relay/glm-4.6 is no model of magpie's here")
	}
	if err := edit.SetJSON(path, edit.KV{Path: "model", Value: "opus-by-hand"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Unwire(); err != nil {
		t.Fatal(err)
	}
	if got := stashShared()["claude.model"]; got != "" {
		t.Fatalf("the stash still holds a model after the Unwire: %q", got)
	}
	if got, _ := edit.GetJSON(path, "model"); got != "opus-by-hand" {
		t.Fatalf("the user's own pick went: %q", got)
	}
}

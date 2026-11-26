package settings

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/lastgood"
)

// sarosaka1023 on X: settings.json edited by hand with a slip in it was
// read from its last good generation, and magpie's next save wrote that
// over the edit. The save is refused, the file left as it was, and magpie
// runs on the last good copy until the edit is fixed.
func TestBrokenSettingsEditNotWrittenOver(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	lastgood.Reset()
	t.Cleanup(lastgood.Reset)
	for _, theme := range []string{"dark", "light"} {
		if err := Save(Settings{Theme: theme}); err != nil {
			t.Fatal(err)
		}
	}
	edit := []byte("{\n  \"theme\": \"system\",\n}\n")
	if err := os.WriteFile(Path(), edit, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load().Theme; got != "dark" {
		t.Errorf("read as %q, want the last good copy's", got)
	}
	if err := Save(Settings{Theme: "light"}); err == nil || !strings.Contains(err.Error(), Path()) {
		t.Errorf("a save over a broken edit: %v", err)
	}
	if got, _ := os.ReadFile(Path()); !bytes.Equal(got, edit) {
		t.Fatalf("the edit was written over:\n%s", got)
	}
	if err := os.WriteFile(Path(), []byte("{\n  \"theme\": \"system\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load().Theme; got != "system" {
		t.Errorf("the fixed edit read as %q", got)
	}
}

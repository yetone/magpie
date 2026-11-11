//go:build windows

package library

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
	"github.com/yetone/magpie/internal/settings"
)

// On Windows, a read-only sign-in file can't be replaced, while the
// library beside it can still be written.
func TestSaveServerRenameSignInWriteFailure(t *testing.T) {
	sandbox(t)
	f := mcpauthtest.New(t)
	alpha := Server{Name: "alpha", Transport: "http", URL: f.URL}
	ok(t)(SaveServer("", alpha))
	f.SignIn(t, "alpha")
	before, found := mcpauth.Get("alpha")
	if !found || before.Access == "" || before.Refresh == "" {
		t.Fatal("missing sign-in fixture")
	}

	library, err := os.ReadFile(path())
	if err != nil {
		t.Fatal(err)
	}
	signins := filepath.Join(settings.Dir(), "mcp-signins.json")
	signedIn, err := os.ReadFile(signins)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(signins, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(signins, 0o600); err != nil {
			t.Error(err)
		}
	})
	file, err := os.OpenFile(signins, os.O_WRONLY, 0)
	if err == nil {
		file.Close()
		t.Fatal("sign-in file is still writable")
	}
	if !os.IsPermission(err) {
		t.Fatal(err)
	}

	renamed := alpha
	renamed.Name = "gamma"
	res, err := SaveServer("alpha", renamed)
	if err == nil || res != nil {
		t.Errorf("SaveServer did not return the sign-in write failure: result=%+v error=%v", res, err)
	} else if !os.IsPermission(err) {
		t.Errorf("SaveServer returned another error: %v", err)
	}
	for p, want := range map[string][]byte{path(): library, signins: signedIn} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Error(err)
		} else if !bytes.Equal(got, want) {
			t.Errorf("file changed after failed sign-in rename: %s", p)
		}
	}
	if got, found := mcpauth.Get("alpha"); !found || !reflect.DeepEqual(got, before) {
		t.Errorf("failed rename changed the old sign-in: found=%v record=%+v", found, got)
	}
	if _, found := mcpauth.Get("gamma"); found {
		t.Error("failed rename created the new sign-in")
	}
}

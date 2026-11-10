package appdir

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// Without Developer Mode a user may not make symlinks (#973): magpie's
// folders are linked by a junction, which moves and comes back the same.
func TestMoveByJunction(t *testing.T) {
	old := symlink
	symlink = func(string, string) error {
		return &os.LinkError{Op: "symlink", Err: windows.ERROR_PRIVILEGE_NOT_HELD}
	}
	t.Cleanup(func() { symlink = old })
	moveAndBack(t)
}

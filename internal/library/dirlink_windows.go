//go:build windows

package library

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/appdir"
)

// dirLink makes link a link to the folder target. Windows lets a user make
// a symlink only with Developer Mode on or as an administrator (#973);
// without that it is a junction, which every user can make and most
// programs follow as they do a symlink to a folder; a Go program's walk
// may not, so the agents that skip one get copies (skipsJunctions). A
// junction can't point at a network share: that is still the symlink's
// error.
func dirLink(target, link string) error {
	err := os.Symlink(target, link)
	if err == nil || !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return err
	}
	if jerr := junction(target, link); jerr != nil {
		return err
	}
	return nil
}

// junction is appdir.Junction: a junction to the folder target.
func junction(target, link string) error { return appdir.Junction(target, link) }

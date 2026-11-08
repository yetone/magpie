package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// NoLinkHandlerError says that nothing on this computer opens App's links
// (Scheme://), so handing one to the system opens nothing and says nothing:
// Windows' rundll32, macOS' open and xdg-open are started, not waited on.
// With Exe set, the scheme is registered to a program that is gone.
type NoLinkHandlerError struct {
	App, Scheme, Exe string
}

func (e *NoLinkHandlerError) Error() string {
	if e.Exe != "" {
		return fmt.Sprintf("%s's links (%s://) open %s, which isn't there", e.App, e.Scheme, e.Exe)
	}
	return fmt.Sprintf("nothing on this computer opens %s's links (%s://)", e.App, e.Scheme)
}

// linkApps names the app behind each import link's scheme.
var linkApps = map[string]string{"cindy": "Cindy", "lody": "Lody", "aionui": "AionUi", "cherrystudio": "Cherry Studio"}

// linkHandler says which program opens scheme's links here: found false
// when none does, an error when that can't be read (unknown). Each system
// has its own; tests stand in for it.
var linkHandler = systemLinkHandler

// OpenAppLink hands an app's import link (Cindy's, Lody's, AionUi's, Cherry Studio's) to
// open, after checking that something here opens it. The system starts
// the opener and never says whether it found a program, so without the
// check a click on Add magpie does nothing at all when the app never
// registered its scheme or was moved since (sungt, 10-29). A check that
// can't be read is unknown, and the link is handed over anyway.
func OpenAppLink(link string, open func(string) error) error {
	scheme, _, ok := strings.Cut(link, "://")
	if !ok {
		scheme, _, _ = strings.Cut(link, ":")
	}
	scheme = strings.ToLower(scheme)
	app := linkApps[scheme]
	if app == "" {
		app = scheme
	}
	exe, found, err := linkHandler(scheme)
	if err == nil && !found {
		return &NoLinkHandlerError{App: app, Scheme: scheme}
	}
	if err == nil && exe != "" && gone(exe) {
		return &NoLinkHandlerError{App: app, Scheme: scheme, Exe: exe}
	}
	return open(link)
}

// commandProgram is the program a Windows shell\open\command value runs:
// the quoted path, or the first word of an unquoted one.
func commandProgram(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : 1+end]
		}
		return strings.TrimPrefix(cmd, `"`)
	}
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		return cmd[:i]
	}
	return cmd
}

// gone says that exe, a full path, isn't there. A bare name is found on
// PATH by the system and isn't judged; a failed look is unknown, not gone.
func gone(exe string) bool {
	if !filepath.IsAbs(exe) {
		return false
	}
	_, err := os.Stat(exe)
	return errors.Is(err, fs.ErrNotExist)
}

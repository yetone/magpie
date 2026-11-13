package uninstall

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/yetone/magpie/internal/appdir"
)

const schemeKey = `Software\Classes\magpie`

// unregisterScheme takes out the magpie:// handler the app registered
// while it opens exe, or an exe that is gone.
func unregisterScheme(exe string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, schemeKey+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	v, _, _ := k.GetStringValue("")
	k.Close()
	if run, _, ok := strings.Cut(strings.TrimPrefix(v, `"`), `"`); ok && !strings.EqualFold(filepath.Clean(run), filepath.Clean(exe)) {
		if _, err := os.Stat(run); err == nil {
			return nil // another magpie's
		}
	}
	for _, sub := range []string{`\shell\open\command`, `\shell\open`, `\shell`, `\DefaultIcon`, ``} {
		if err := registry.DeleteKey(registry.CURRENT_USER, schemeKey+sub); err != nil && err != registry.ErrNotExist {
			return err
		}
	}
	return nil
}

// webData is WebView2's profile of an installed magpie: %APPDATA%\<exe
// name>, and the one a magpie installed from a download keeps under the
// download's name (appdir.WebView, update.Settle). Portable, it is in the
// data folder already.
func webData() []string {
	if appdir.Portable() != "" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return profiles(appdir.Getenv("APPDATA"), filepath.Base(exe), appdir.WebView())
}

// profiles is webData's folders in appData: the exe's own and the noted
// one, each only where WebView2 made it (its EBWebView inside). A note
// naming a folder anywhere else is not followed.
func profiles(appData, exeName, noted string) []string {
	if appData == "" {
		return nil
	}
	var out []string
	for _, p := range []string{filepath.Join(appData, exeName), noted} {
		if p == "" || !strings.EqualFold(filepath.Clean(filepath.Dir(p)), filepath.Clean(appData)) {
			continue
		}
		if len(out) > 0 && strings.EqualFold(filepath.Clean(out[0]), filepath.Clean(p)) {
			continue
		}
		if _, err := os.Stat(filepath.Join(p, "EBWebView")); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

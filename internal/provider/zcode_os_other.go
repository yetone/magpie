//go:build !windows

package provider

// PLUGIN-SERVED (see AGENTS.md): ZCode ("zcode") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zcode-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zcode) and raise the
// mover's min in internal/provider/migrate_zcode.go.

import (
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/yetone/magpie/internal/proc"
)

// zcodeOSRelease is the kernel's release, as Node's os.release() gives it
// (25.2.0 on macOS 26), "" when it can't be told.
func zcodeOSRelease() string {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}

// zcodeSystemLocale is the Mac's region (zh_CN), the locale a terminal is
// given, for magpie opened from Finder with no LANG; "" elsewhere.
var zcodeSystemLocale = sync.OnceValue(func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := proc.Command("defaults", "read", "-g", "AppleLocale").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

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
	"strconv"
	"sync"

	"golang.org/x/sys/windows"
)

// zcodeOSRelease is Windows' version, as Node's os.release() gives it
// (10.0.26100).
func zcodeOSRelease() string {
	v := windows.RtlGetVersion()
	return strconv.Itoa(int(v.MajorVersion)) + "." + strconv.Itoa(int(v.MinorVersion)) + "." + strconv.Itoa(int(v.BuildNumber))
}

// zcodeSystemLocale is the user's first display language (zh-CN).
var zcodeSystemLocale = sync.OnceValue(func() string {
	if l, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME); err == nil && len(l) > 0 {
		return l[0]
	}
	return ""
})

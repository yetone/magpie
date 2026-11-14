package uilang

import (
	"os"

	"github.com/yetone/magpie/internal/proc"
)

// System is the first of the Mac's preferred languages, the one the
// page's navigator.language is; an app opened from Finder has no LANG.
func System() string {
	if out, err := proc.Command("defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
		if l := firstAppleLang(string(out)); l != "" {
			return l
		}
	}
	return Env(os.Getenv)
}

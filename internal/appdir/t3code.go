package appdir

import (
	"path/filepath"
	"strings"
)

// T3Code is T3 Code's folder (pingdotgg/t3code) for home: $T3CODE_HOME,
// a leading ~ being home, else ~/.t3 (resolveBaseDir in its server's
// os-jank.ts). Its settings, and each provider instance's profile, are in
// userdata in it.
func T3Code(home string) string {
	base := Getenv("T3CODE_HOME")
	if base == "" {
		return filepath.Join(home, ".t3")
	}
	if rest, ok := strings.CutPrefix(base, "~"); ok {
		return filepath.Join(home, rest)
	}
	return base
}

package gateway

import (
	"log"
	"os"

	"github.com/yetone/magpie/internal/fasttools"
	"github.com/yetone/magpie/internal/update"
)

// fastToolsExe is the magpie the fast-tools plugin is kept starting; a var
// for tests.
var fastToolsExe = func() string {
	exe, _ := update.Program()
	return exe
}

// keepFastTools writes Antigravity's fast-tools plugin again, when it is on,
// if the magpie it starts is gone or it was written by an older magpie
// (fasttools.Keep): an app moved to another folder, or a package manager's
// new path, would otherwise leave its MCP server and hook failing.
func keepFastTools() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	if err := fasttools.Keep(home, fastToolsExe()); err != nil {
		log.Printf("fast tools: %v", err)
	}
}

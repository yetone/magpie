//go:build windows && !nogui

package gui

import (
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// TestNoInstallQuestionOnCI: started with CI set, as the release
// workflow's windows-check starts dist\magpie-windows-amd64.exe, the app
// has no dialog to ask in, so update.Settle runs the copy where it is
// and the gateway comes up (v0.1.4160 to v0.1.4166 never did).
func TestNoInstallQuestionOnCI(t *testing.T) {
	was := update.AskFolder
	t.Cleanup(func() { update.AskFolder = was })
	update.AskFolder = func(string, func(string) error) string { return "" }
	t.Setenv("CI", "true")
	askInstallFolder()
	if update.AskFolder != nil {
		t.Fatal("asks where to install with CI=true")
	}
}

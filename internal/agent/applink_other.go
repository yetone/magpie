//go:build !windows && !(darwin && cgo)

package agent

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// systemLinkHandler asks xdg-mime which desktop entry opens scheme's
// links, as xdg-open does, and looks for that entry and its program. No xdg-mime, or a failed query, is unknown,
// and so is KDE's empty answer: kde-open reads KDE's own service cache,
// which can know a handler xdg-mime's lookup doesn't.
func systemLinkHandler(scheme string) (string, bool, error) {
	if runtime.GOOS == "darwin" {
		return "", false, errors.New("no launch services without cgo")
	}
	kde := strings.Contains(strings.ToUpper(appdir.Getenv("XDG_CURRENT_DESKTOP")), "KDE")
	tool := proc.FindTool("xdg-mime")
	if tool == "" {
		return "", false, errors.New("no xdg-mime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := proc.CommandContext(ctx, tool, "query", "default", "x-scheme-handler/"+scheme).Output()
	if err != nil {
		return "", false, err
	}
	if name := strings.TrimSpace(string(out)); name != "" {
		return desktopHandler(name, xdgDataDirs())
	}
	if kde {
		return "", false, errors.New("xdg-mime: no answer under KDE")
	}
	return "", false, nil
}

package netproxy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/proc"
)

// system reads the desktop's proxy (desktop.go): KDE's kioslaverc on a KDE
// session, GNOME's gsettings otherwise, then the other. A desktop with
// neither (no gsettings, or no GNOME proxy schema, and no kioslaverc) has
// none; a gsettings that is there and fails is a failed read.
func system() (Proxy, bool) {
	kde := func() Proxy {
		b, err := os.ReadFile(filepath.Join(configDir(), "kioslaverc"))
		if err != nil {
			return Proxy{}
		}
		return parseKioslaverc(string(b))
	}
	if strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "KDE") {
		if p := kde(); p.URL != "" {
			return p, true
		}
	}
	p, ok := gnome()
	if p.URL == "" {
		if k := kde(); k.URL != "" {
			return k, true
		}
	}
	return p, ok
}

func gnome() (Proxy, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), toolWait)
	defer cancel()
	out, err := proc.CommandContext(ctx, "gsettings", "list-recursively", "org.gnome.system.proxy").Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return parseGsettings(string(out)), true
	case errors.Is(err, exec.ErrNotFound):
		return Proxy{}, true
	case errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "No such schema"):
		return Proxy{}, true
	}
	return Proxy{}, false
}

func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(d) {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

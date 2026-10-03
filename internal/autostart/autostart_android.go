package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

// Termux:Boot runs executable scripts in ~/.termux/boot after Android boots.
func record() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".termux", "boot", "magpie")
}

func enabled() bool {
	info, err := os.Stat(record())
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func enable(exe string) error {
	p := record()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// Android's shell is available even before Termux's PATH is loaded.
	q := "'" + strings.ReplaceAll(exe, "'", "'\"'\"'") + "'"
	body := "#!/system/bin/sh\nexec " + q + " serve </dev/null >/dev/null 2>&1 &\n"
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		return err
	}
	return os.Chmod(p, 0o700)
}

func disable() error {
	if err := os.Remove(record()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func refresh() error { return nil }

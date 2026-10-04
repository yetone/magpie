package omarchy

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// The bar widget: magpie's icon in Omarchy's own bar, where Omarchy's
// widgets are, rather than in the tray's drawer, which Omarchy keeps tray
// icons behind until each is pinned. It is a third-party Omarchy plugin,
// put in by hand the way Omarchy's shell README says (the files under
// ~/.config/omarchy/plugins/<id>/, then rescanPlugins and plugin enable),
// and only when the user asks for it in Settings.
const WidgetID = "usemagpie.magpie"

//go:embed widget
var widgetFiles embed.FS

func widgetDir() string {
	cfg := appdir.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		home, _ := os.UserHomeDir()
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "omarchy", "plugins", WidgetID)
}

// WidgetOn is whether the widget has been put in.
func WidgetOn() bool {
	_, err := os.Stat(filepath.Join(widgetDir(), "manifest.json"))
	return err == nil
}

// AddWidget puts the widget in Omarchy's bar, running exe (magpie) when
// clicked, or brings it up to date.
func AddWidget(exe string) error {
	dir := widgetDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files, err := widget(exe)
	if err != nil {
		return err
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	if err := run("omarchy-shell", "shell", "rescanPlugins"); err != nil {
		return err
	}
	// enabling again is what has the shell load files it already had
	_ = run("omarchy", "plugin", "disable", WidgetID)
	return run("omarchy", "plugin", "enable", WidgetID)
}

// KeepWidget brings a widget that is in the bar up to date with this magpie,
// which may have moved or been updated since it was put in.
func KeepWidget(exe string) error {
	if !WidgetOn() {
		return nil
	}
	files, err := widget(exe)
	if err != nil {
		return err
	}
	for name, b := range files {
		if old, err := os.ReadFile(filepath.Join(widgetDir(), name)); err != nil || !bytes.Equal(old, b) {
			return AddWidget(exe)
		}
	}
	return nil
}

// widget is the widget's files, running exe.
func widget(exe string) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, name := range []string{"manifest.json", "Widget.qml"} {
		b, err := widgetFiles.ReadFile("widget/" + name)
		if err != nil {
			return nil, err
		}
		if name == "Widget.qml" {
			// a QML string, quoted as JSON is
			b = []byte(strings.Replace(string(b), `"__MAGPIE__"`, strconv.Quote(exe), 1))
		}
		files[name] = b
	}
	return files, nil
}

// RemoveWidget takes it out of the bar again.
func RemoveWidget() error {
	_ = run("omarchy", "plugin", "disable", WidgetID)
	if err := os.RemoveAll(widgetDir()); err != nil {
		return err
	}
	return run("omarchy-shell", "shell", "rescanPlugins")
}

func run(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

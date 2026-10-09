//go:build linux && cgo && gtk3 && !nogui

package gui

/*
#cgo pkg-config: gtk+-3.0
#include "fontdpi_gtk3.h"
*/
import "C"

// fontDPI gives GTK 3 a font resolution before WebKitGTK makes the first
// webview, when GDK has none (#1371, see magpie_font_dpi). Wails makes its
// GtkApplication, and so initialises GTK, only in Run, and makes the
// windows from the main loop; this queues the check to run first on that
// loop, so GTK's own start is left as it was. A user's own positive DPI is
// kept.
func fontDPI() { C.magpie_font_dpi_first() }

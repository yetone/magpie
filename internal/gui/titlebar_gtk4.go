//go:build linux && cgo && !gtk3 && !nogui

package gui

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>
#include <stdlib.h>

static void plain_titlebar(void *w) {
	GtkWidget *bar = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 0);
	gtk_widget_set_visible(bar, FALSE);
	gtk_window_set_titlebar(GTK_WINDOW(w), bar);
}

static void name_window(void *w, char *title) {
	gtk_window_set_title(GTK_WINDOW(w), title);
}
*/
import "C"

import (
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// plainTitlebar takes the system's title bar off a window whose page has a
// header of its own. GNOME draws one over it, the name and a close button
// stacked on the tabs; frameless, the window would lose its shadow, rounded
// corners and the edges it is resized by. A hidden widget as its titlebar
// keeps all those and shows no bar: the page's header drags it, and has the
// close button.
func plainTitlebar(w *application.WebviewWindow) {
	application.InvokeSync(func() {
		if p := w.NativeWindow(); p != nil {
			C.plain_titlebar(p)
		}
	})
}

// nameWindow gives a frameless window a title, which Wails leaves unset on
// one; Hyprland tells the panel from the main window by it.
func nameWindow(w *application.WebviewWindow, title string) {
	application.InvokeSync(func() {
		if p := w.NativeWindow(); p != nil {
			t := C.CString(title)
			C.name_window(p, t)
			C.free(unsafe.Pointer(t))
		}
	})
}

// ownFrame: GTK 4 asks the compositor for no frame on an undecorated window
// itself (gdk_toplevel_set_decorated), so KWin draws none (#1283).
func ownFrame(*application.WebviewWindow) {}

//go:build linux && cgo && gtk3 && !nogui

package gui

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>
#include <stdlib.h>
#ifdef GDK_WINDOWING_WAYLAND
#include <gdk/gdkwayland.h>
#endif

static void plain_titlebar(void *w) {
	GtkWidget *bar = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 0);
	gtk_widget_set_no_show_all(bar, TRUE); // kept hidden when the window shows all it holds
	gtk_window_set_titlebar(GTK_WINDOW(w), bar);
}

static void name_window(void *w, char *title) {
	gtk_window_set_title(GTK_WINDOW(w), title);
}

// announce_csd tells KWin the window draws its own frame. GTK 3 tells it
// the opposite for an undecorated window on each realize, and KWin then
// draws its title bar on it.
static void announce_csd(GtkWidget *w, gpointer data) {
#if defined(GDK_WINDOWING_WAYLAND) && GTK_CHECK_VERSION(3, 24, 0)
	GdkWindow *g = gtk_widget_get_window(w);
	if (g != NULL && GDK_IS_WAYLAND_WINDOW(g))
		gdk_wayland_window_announce_csd(g);
#endif
}

static void own_frame(void *w) {
	g_signal_connect_after(w, "realize", G_CALLBACK(announce_csd), NULL);
	if (gtk_widget_get_realized(GTK_WIDGET(w)))
		announce_csd(GTK_WIDGET(w), NULL);
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

// ownFrame keeps a frameless window frameless on KDE Plasma's Wayland
// session (#1283). GTK 3 asks KWin's decoration protocol for a server-drawn
// frame on any window without client-side decorations, an undecorated one
// too, so KWin put its title bar, with a close button, on the quick panel.
// The main window has none since plainTitlebar makes it client-decorated.
// Compositors without that protocol (GNOME, Hyprland) and X11 don't see it.
func ownFrame(w *application.WebviewWindow) {
	application.InvokeSync(func() {
		if p := w.NativeWindow(); p != nil {
			C.own_frame(p)
		}
	})
}

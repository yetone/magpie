// fontdpi_probe opens a GTK 3 window with a WebKitGTK page and prints what
// the page measures. argv[1] is the font DPI GDK is left with before the
// first webview (-1 is GDK's unset value, #1371). Built with
// -DMAGPIE_FONT_DPI it runs magpie's guard first, as the app does.
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>
#include <stdio.h>
#include <stdlib.h>
#ifdef MAGPIE_FONT_DPI
#include "fontdpi_gtk3.h"
#endif

static GMainLoop *loop;
static int status = 1;

static void measured(GObject *view, GAsyncResult *res, gpointer data) {
	GError *err = NULL;
	JSCValue *v = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(view), res, &err);
	if (v == NULL) {
		fprintf(stderr, "js: %s\n", err ? err->message : "no result");
	} else {
		char *s = jsc_value_to_string(v);
		printf("layout dpi=%g allocated=%d %s\n", gdk_screen_get_resolution(gdk_screen_get_default()),
			gtk_widget_get_allocated_width(GTK_WIDGET(view)), s);
		g_free(s);
		g_object_unref(v);
		status = 0;
	}
	g_main_loop_quit(loop);
}

static void loaded(WebKitWebView *view, WebKitLoadEvent ev, gpointer data) {
	if (ev == WEBKIT_LOAD_FINISHED)
		webkit_web_view_evaluate_javascript(view,
			"'viewport=' + innerWidth + ' font=' + parseFloat(getComputedStyle(document.body).fontSize)",
			-1, NULL, NULL, NULL, measured, NULL);
}

static gboolean timeout(gpointer data) {
	fprintf(stderr, "timed out\n");
	g_main_loop_quit(loop);
	return G_SOURCE_REMOVE;
}

int main(int argc, char **argv) {
	if (argc != 2)
		return 2;
	if (!gtk_init_check(NULL, NULL))
		return 3;
	printf("initial dpi=%g\n", gdk_screen_get_resolution(gdk_screen_get_default()));
	gdk_screen_set_resolution(gdk_screen_get_default(), g_ascii_strtod(argv[1], NULL));
#ifdef MAGPIE_FONT_DPI
	printf("guard=%d\n", magpie_font_dpi());
#endif
	loop = g_main_loop_new(NULL, FALSE);
	GtkWidget *w = gtk_window_new(GTK_WINDOW_TOPLEVEL);
	gtk_window_set_default_size(GTK_WINDOW(w), 420, 520);
	GtkWidget *view = webkit_web_view_new();
	gtk_container_add(GTK_CONTAINER(w), view);
	g_signal_connect(view, "load-changed", G_CALLBACK(loaded), NULL);
	gtk_widget_show_all(w);
	webkit_web_view_load_html(WEBKIT_WEB_VIEW(view),
		"<!doctype html><meta name=viewport content='width=device-width'><style>body{margin:0;font:16px sans-serif}</style><p>magpie</p>", NULL);
	g_timeout_add_seconds(20, timeout, NULL);
	g_main_loop_run(loop);
	return status;
}

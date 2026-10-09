// fontdpi_gtk3.h: the font DPI guard for GTK 3 builds, in C so that the
// native probe in testdata compiles the same code the app runs.
#include <gtk/gtk.h>
#include <math.h>
#include <stdlib.h>

// magpie_font_dpi gives GDK a font resolution when it has none. GDK says
// -1 for an unset one, and WebKitGTK 2.54.1's GTK 3 settings reader
// (SystemSettingsManagerProxy::xftDPI) multiplies it by 1024 without a
// check: its pages then lay out with a viewport of -91008px and a body
// font of 9000000px (#1371). A positive resolution is left as it is. An
// unset one takes the GTK setting gtk-xft-dpi, or 96, times a valid
// GDK_DPI_SCALE, as GTK itself does with a positive gtk-xft-dpi.
// Returns -1 when GTK has no display, 0 when the resolution was kept,
// 1 when it was set.
static int magpie_font_dpi(void) {
	if (!gtk_init_check(NULL, NULL))
		return -1;
	GdkScreen *screen = gdk_screen_get_default();
	if (screen == NULL)
		return -1;
	double dpi = gdk_screen_get_resolution(screen);
	if (isfinite(dpi) && dpi > 0)
		return 0;
	gint xft = -1;
	g_object_get(gtk_settings_get_default(), "gtk-xft-dpi", &xft, NULL);
	dpi = xft > 0 ? xft / 1024.0 : 96.0;
	const char *env = g_getenv("GDK_DPI_SCALE");
	if (env != NULL) {
		char *end;
		double scale = g_ascii_strtod(env, &end);
		if (end != env && *end == '\0' && isfinite(scale) && scale > 0 && isfinite(dpi * scale))
			dpi *= scale;
	}
	gdk_screen_set_resolution(screen, dpi);
	return 1;
}

static gboolean magpie_font_dpi_idle(gpointer data) {
	if (magpie_font_dpi() == 1)
		g_message("magpie: GTK has no font DPI set; using %g (#1371)",
			gdk_screen_get_resolution(gdk_screen_get_default()));
	return G_SOURCE_REMOVE;
}

// magpie_font_dpi_first runs magpie_font_dpi as the GTK main loop starts.
// GtkApplication has initialised GTK by then, with the program name Wails
// set, and Wails makes each window from an idle callback of default idle
// priority; one of G_PRIORITY_HIGH runs before all of them.
static void magpie_font_dpi_first(void) {
	g_idle_add_full(G_PRIORITY_HIGH, magpie_font_dpi_idle, NULL, NULL);
}

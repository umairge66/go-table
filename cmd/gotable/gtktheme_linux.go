//go:build !browser && linux

package main

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>

// apply_theme runs on the GTK main loop. Adwaita (GTK's own theme) is used
// because desktop themes such as Breeze take their colours from the desktop
// colour scheme and wouldn't follow GoTable's light/dark switch.
static gboolean apply_theme(gpointer data) {
	GtkSettings *s = gtk_settings_get_default();
	if (s != NULL) {
		g_object_set(s, "gtk-theme-name", "Adwaita",
			"gtk-application-prefer-dark-theme", GPOINTER_TO_INT(data) ? TRUE : FALSE, NULL);
	}
	return G_SOURCE_REMOVE;
}

static void set_theme(int dark) { g_idle_add(apply_theme, GINT_TO_POINTER(dark)); }
*/
import "C"

// setNativeTheme makes GTK-drawn parts of the window (the lists of <select>
// elements, file dialogs) light or dark like the page.
func setNativeTheme(dark bool) {
	d := 0
	if dark {
		d = 1
	}
	C.set_theme(C.int(d))
}

//go:build !browser && linux

package main

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>

// main_window finds GoTable's window (not a file dialog, which is transient).
static GtkWindow *main_window(void) {
	GtkWindow *w = NULL;
	GList *all = gtk_window_list_toplevels();
	for (GList *l = all; l != NULL; l = l->next) {
		GtkWindow *c = GTK_WINDOW(l->data);
		if (gtk_widget_get_visible(GTK_WIDGET(c)) && gtk_window_get_window_type(c) == GTK_WINDOW_TOPLEVEL &&
				gtk_window_get_transient_for(c) == NULL) {
			w = c;
			break;
		}
	}
	g_list_free(all);
	return w;
}

// Actions: 0 move, 1–8 resize from an edge (GdkWindowEdge + 1),
// 20 maximize/restore, 21 minimize.
static gboolean window_action(gpointer data) {
	int a = GPOINTER_TO_INT(data);
	GtkWindow *w = main_window();
	if (w == NULL) return G_SOURCE_REMOVE;
	if (a == 20) {
		if (gtk_window_is_maximized(w)) gtk_window_unmaximize(w); else gtk_window_maximize(w);
		return G_SOURCE_REMOVE;
	}
	if (a == 21) {
		gtk_window_iconify(w);
		return G_SOURCE_REMOVE;
	}
	// Moving and resizing are handed to the compositor while the button is
	// still held; on Wayland it uses the press's serial, not x/y.
	gint x = 0, y = 0;
	GdkSeat *seat = gdk_display_get_default_seat(gdk_display_get_default());
	if (seat != NULL) gdk_device_get_position(gdk_seat_get_pointer(seat), NULL, &x, &y);
	if (a == 0) gtk_window_begin_move_drag(w, 1, x, y, GDK_CURRENT_TIME);
	else if (a >= 1 && a <= 8) gtk_window_begin_resize_drag(w, (GdkWindowEdge)(a - 1), 1, x, y, GDK_CURRENT_TIME);
	return G_SOURCE_REMOVE;
}

static void run_window_action(int a) { g_idle_add(window_action, GINT_TO_POINTER(a)); }

// An undecorated GTK3 window asks KDE (and other compositors with server-side
// decorations) to draw a title bar. A window with GTK's client-side
// decorations doesn't, so the main window gets an empty, zero-height custom
// title bar instead: no title bar from anyone, plus GTK's shadow and resize
// borders. GTK only accepts it before the window is realized, so it's done
// when Wails first adds its content to the window ("add"), long before the
// window is shown.
static gboolean on_add(GSignalInvocationHint *hint, guint n, const GValue *params, gpointer data) {
	GObject *obj = g_value_get_object(&params[0]);
	if (!GTK_IS_WINDOW(obj) || g_object_get_data(obj, "gotable-titlebar") != NULL) return TRUE;
	if (gtk_widget_get_realized(GTK_WIDGET(obj))) return TRUE; // too late; leave it alone
	GtkWindow *w = GTK_WINDOW(obj);
	// Only GoTable's window: not menus, popups or dialogs (they have their own).
	if (gtk_window_get_window_type(w) != GTK_WINDOW_TOPLEVEL || gtk_window_get_transient_for(w) != NULL ||
			gtk_window_get_titlebar(w) != NULL || GTK_IS_DIALOG(w)) return TRUE;
	g_object_set_data(obj, "gotable-titlebar", GINT_TO_POINTER(1));
	GtkWidget *bar = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 0);
	gtk_window_set_titlebar(w, bar);
	// The theme styles .titlebar with a height and background; this one is empty.
	gtk_style_context_remove_class(gtk_widget_get_style_context(bar), "titlebar");
	gtk_widget_set_size_request(bar, -1, 0);
	gtk_widget_show(bar);
	gtk_window_set_decorated(w, TRUE);
	return TRUE; // keep the hook (once per window thanks to the flag)
}

static void install_titlebar_hook(void) {
	g_type_class_unref(g_type_class_ref(GTK_TYPE_WINDOW)); // makes sure "add" exists
	guint id = g_signal_lookup("add", GTK_TYPE_CONTAINER);
	if (id != 0) g_signal_add_emission_hook(id, 0, on_add, NULL, NULL);
}
*/
import "C"

import (
	"fmt"
	"strings"
)

// ownTitleBar: on Linux GoTable draws its own title bar (see windowAction).
const ownTitleBar = true

// prepareOwnTitleBar must run before the window exists (before wails.Run).
func prepareOwnTitleBar() { C.install_titlebar_hook() }

var resizeEdges = map[string]int{"nw": 1, "n": 2, "ne": 3, "w": 4, "e": 5, "sw": 6, "s": 7, "se": 8}

// windowAction moves, resizes, maximizes or minimizes the window for the
// page's own title bar. "close" is handled by the caller (Wails quit).
func windowAction(action string) error {
	switch action {
	case "drag":
		C.run_window_action(0)
	case "toggle-maximize":
		C.run_window_action(20)
	case "minimize":
		C.run_window_action(21)
	default:
		name, isResize := strings.CutPrefix(action, "resize-")
		edge, ok := resizeEdges[name]
		if !isResize || !ok {
			return fmt.Errorf("unknown window action %q", action)
		}
		C.run_window_action(C.int(edge))
	}
	return nil
}

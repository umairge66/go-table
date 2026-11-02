//go:build !browser

package main

import (
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// appMenu is the macOS menu bar: the app menu (About, Hide, Quit), Edit
// (without it ⌘C, ⌘V and ⌘A do nothing in text fields) and Window.
func appMenu() *menu.Menu {
	return menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu(), menu.WindowMenu())
}

// macOptions: the standard title bar, light or dark with the system.
func macOptions() *mac.Options {
	return &mac.Options{
		TitleBar:   mac.TitleBarDefault(),
		Appearance: mac.DefaultAppearance,
		About:      &mac.AboutInfo{Title: "GoTable", Message: "A database client.\n" + version, Icon: appIcon},
	}
}

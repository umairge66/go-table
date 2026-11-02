//go:build !browser && !darwin

package main

import (
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// Linux and Windows have no menu bar; shortcuts are handled by the page.
func appMenu() *menu.Menu { return nil }

func macOptions() *mac.Options { return nil }

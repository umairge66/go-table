//go:build !browser && !linux

package main

// registerDesktop: Windows and macOS take the icon from the app itself.
func registerDesktop() {}

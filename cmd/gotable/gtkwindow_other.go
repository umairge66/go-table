//go:build !browser && !linux

package main

import "errors"

// Elsewhere the system title bar stays.
const ownTitleBar = false

func prepareOwnTitleBar() {}

func windowAction(string) error { return errors.New("not available") }

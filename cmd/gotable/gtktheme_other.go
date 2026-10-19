//go:build !browser && !linux

package main

// setNativeTheme: WebView2 and WKWebView follow the page's color-scheme.
func setNativeTheme(bool) {}

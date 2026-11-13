//go:build !windows && !linux

package gui

// registerScheme: the Mac reads the scheme from the bundle's Info.plist.
func registerScheme() error { return nil }

// keepWebData: the Mac's webview keeps its data by the bundle id.
func keepWebData() {}

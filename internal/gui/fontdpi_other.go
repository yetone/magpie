//go:build !linux || !cgo || !gtk3 || nogui

package gui

// fontDPI: only GTK 3's WebKitGTK reads GDK's font resolution (#1371).
func fontDPI() {}

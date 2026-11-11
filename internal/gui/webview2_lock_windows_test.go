//go:build windows && !nogui

package gui

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Chromium holds EBWebView\lockfile without sharing while its browser
// process runs; the start waits on it (@ljlwlcz on X).
func TestWebView2FreeSeesTheHeldLockfile(t *testing.T) {
	dir := t.TempDir()
	if !webView2Free(dir) {
		t.Fatal("no lockfile is free")
	}
	lock := filepath.Join(dir, "EBWebView", "lockfile")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(lock), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if webView2Free(dir) {
		t.Fatal("a lockfile held without sharing is free")
	}
	windows.CloseHandle(h)
	if !webView2Free(dir) {
		t.Fatal("a released lockfile is held")
	}
}

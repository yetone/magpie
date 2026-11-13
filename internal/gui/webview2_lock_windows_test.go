//go:build windows && !nogui

package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// A second launch, which hands over to the magpie running, doesn't wait
// for that magpie's WebView2 to let go of the folder: it never does while
// it runs (shangyuan0818 on X: the Start menu took over 10s to show the
// window).
func TestWebView2ReadyDoesNotWaitOnTheRunningMagpie(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "EBWebView", "lockfile")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := windows.CreateFile(windows.StringToUTF16Ptr(lock), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(f)
	id := fmt.Sprintf("test.%d.%d", os.Getpid(), time.Now().UnixNano())

	// no magpie runs on it: the folder held is waited on
	start := time.Now()
	webView2Ready(dir, id, 2*time.Second)
	if d := time.Since(start); d < 2*time.Second {
		t.Fatalf("a start with no magpie running waited %s for a held folder, want 2s", d)
	}

	// Wails' mutex for the id, as the running magpie holds it
	m, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(instanceMutex(id)))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(m)
	if !instanceHeld(id) {
		t.Fatal("the mutex held reads as no magpie running")
	}
	if instanceHeld(id + "x") {
		t.Fatal("another config's id reads as running")
	}
	start = time.Now()
	webView2Ready(dir, id, 20*time.Second)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("a second launch waited %s on the running magpie's folder", d)
	}
}

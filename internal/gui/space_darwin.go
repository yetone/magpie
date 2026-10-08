//go:build !nogui

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static void setMovesToActiveSpace(void *win, int on) {
	NSWindow *w = (NSWindow *)win;
	if (on) w.collectionBehavior |= NSWindowCollectionBehaviorMoveToActiveSpace;
	else w.collectionBehavior &= ~NSWindowCollectionBehaviorMoveToActiveSpace;
}

static int movesToActiveSpace(void *win) {
	return (((NSWindow *)win).collectionBehavior & NSWindowCollectionBehaviorMoveToActiveSpace) != 0;
}

// Ordered in and not in the Dock: on a Space of its own, or covered, it is
// still open, where Wails' IsVisible (its occlusion state) says it isn't.
static int windowOpen(void *win) {
	NSWindow *w = (NSWindow *)win;
	return w.isVisible && !w.isMiniaturized;
}

static void activateApp(void) {
	[NSApp activateIgnoringOtherApps:YES];
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// setMovesToActiveSpace says whether w, ordered front or its app activated,
// is moved to the Space the user is on, rather than taking the user to its
// own; on the main thread.
func setMovesToActiveSpace(w *application.WebviewWindow, on bool) {
	if p := w.NativeWindow(); p != nil {
		C.setMovesToActiveSpace(p, C.int(boolInt(on)))
	}
}

// movesToActiveSpace says whether it is; on the main thread.
func movesToActiveSpace(w *application.WebviewWindow) bool {
	p := w.NativeWindow()
	return p != nil && C.movesToActiveSpace(p) != 0
}

// windowOpen says whether w is open: shown, on any Space, and not
// minimised; on the main thread.
func windowOpen(w *application.WebviewWindow) bool {
	p := w.NativeWindow()
	return p != nil && C.windowOpen(p) != 0
}

// activateApp brings magpie to the front as the Mac does for a click on its
// Dock icon: to the Space its window is on, as the user's Mission Control
// settings say; on the main thread.
func activateApp() { C.activateApp() }

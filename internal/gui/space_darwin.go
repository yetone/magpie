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

// The windows that were open as magpie was hidden (Command-H, Hide
// Others): hiding orders every window out, and Command-Tab back orders
// these in again. Weak, so a window let go isn't kept.
static NSHashTable *openWhenHidden;

static void watchHide(void) {
	if (openWhenHidden != nil) return;
	openWhenHidden = [[NSHashTable weakObjectsHashTable] retain];
	[[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationWillHideNotification object:nil queue:nil usingBlock:^(NSNotification *n) {
		[openWhenHidden removeAllObjects];
		for (NSWindow *w in NSApp.windows) {
			if (w.isVisible) [openWhenHidden addObject:w];
		}
	}];
}

// Up: ordered in on any Space, covered by other windows or not, or
// minimised, or open as magpie was hidden. Only a window magpie closed
// itself is down. The occlusion state, Wails' IsVisible, says a window
// covered by another app's is not visible.
static int windowUp(void *win) {
	NSWindow *w = (NSWindow *)win;
	return w.isVisible || w.isMiniaturized || (NSApp.isHidden && [openWhenHidden containsObject:w]);
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

// windowUp says whether w is up, as the user sees it: open on any Space,
// covered or not, minimised, or open as magpie was hidden; on the main thread.
// Lightweight mode lets go only of a window that isn't, and the Dock and a
// restart keep the window that is (#1381).
func windowUp(w *application.WebviewWindow) bool {
	p := w.NativeWindow()
	return p != nil && C.windowUp(p) != 0
}

// watchHide keeps which windows were open as magpie was hidden, for
// windowUp; called once, before the app runs.
func watchHide() { C.watchHide() }

// activateApp brings magpie to the front as the Mac does for a click on its
// Dock icon: to the Space its window is on, as the user's Mission Control
// settings say; on the main thread.
func activateApp() { C.activateApp() }

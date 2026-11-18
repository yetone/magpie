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

// On every Space and over a full-screen app's.
static int joinsFullScreenSpaces(void *win) {
	NSWindowCollectionBehavior b = ((NSWindow *)win).collectionBehavior;
	return (b & NSWindowCollectionBehaviorCanJoinAllSpaces) != 0 && (b & NSWindowCollectionBehaviorFullScreenAuxiliary) != 0;
}

// A non-activating panel: shown and focused, it leaves the app that is
// active so, and the Space the user is on with it.
static int nonActivating(void *win) {
	NSWindow *w = (NSWindow *)win;
	return [w isKindOfClass:[NSPanel class]] && (w.styleMask & NSWindowStyleMaskNonactivatingPanel) != 0;
}

// The quick panel's level, a menu bar extra's (NSStatusWindowLevel):
// over normal and floating windows, the Dock and the menu bar, and under
// pop-up menus and a screenshot tool's overlay above it. Wails'
// PositionWindow puts the window it places at NSPopUpMenuWindowLevel,
// where WeChat's screenshot couldn't cover the panel (zyh6036 on X).
static void setPanelLevel(void *win) {
	[(NSWindow *)win setLevel:NSStatusWindowLevel];
}

static long statusLevel(void) { return (long)NSStatusWindowLevel; }

static long windowLevel(void *win) {
	return (long)((NSWindow *)win).level;
}

// Can be made key: a borderless window can't unless its class says so,
// as Wails' panel class does.
static int canBeKey(void *win) {
	return ((NSWindow *)win).canBecomeKeyWindow;
}

// Whether this session lets an app that isn't active make a
// non-activating panel key from code: a plain titled one is shown and
// made key as the quick panel is. With no click or key press behind it,
// macOS 26.2 left even that one not key on a Mac no one was at (#2450).
static int sessionKeysPanels(void) {
	NSPanel *p = [[NSPanel alloc] initWithContentRect:NSMakeRect(0, 0, 40, 40)
		styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskNonactivatingPanel
		backing:NSBackingStoreBuffered defer:NO];
	p.releasedWhenClosed = NO;
	[p orderFrontRegardless];
	[p makeKeyWindow];
	int key = p.isKeyWindow;
	[p orderOut:nil];
	[p release];
	return key;
}

// Frontmost: the app whose menu bar shows, and whose Space the Mac goes
// to. A non-activating panel made key leaves another app frontmost.
static int appFrontmost(void) {
	return [NSWorkspace sharedWorkspace].frontmostApplication.processIdentifier == NSProcessInfo.processInfo.processIdentifier;
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

// joinsFullScreenSpaces says whether w is on every Space, a full-screen
// app's too, as the quick panel is (#2141); on the main thread.
func joinsFullScreenSpaces(w *application.WebviewWindow) bool {
	p := w.NativeWindow()
	return p != nil && C.joinsFullScreenSpaces(p) != 0
}

// setPanelLevel puts the quick panel at a menu bar extra's level, after
// Wails' PositionWindow put it at a pop-up menu's; on the main thread.
func setPanelLevel(w *application.WebviewWindow) {
	if p := w.NativeWindow(); p != nil {
		C.setPanelLevel(p)
	}
}

// panelLevel is w's window level, -1 with no window; on the main thread.
func panelLevel(w *application.WebviewWindow) int {
	if p := w.NativeWindow(); p != nil {
		return int(C.windowLevel(p))
	}
	return -1
}

// panelLevelOK says whether w is at a menu bar extra's level; on the
// main thread.
func panelLevelOK(w *application.WebviewWindow) bool {
	return panelLevel(w) == int(C.statusLevel())
}

// canBeKey says whether w can be made key, take the keyboard; on the
// main thread.
func canBeKey(w *application.WebviewWindow) bool {
	p := w.NativeWindow()
	return p != nil && C.canBeKey(p) != 0
}

// sessionKeysPanels says whether this session makes a non-activating
// panel key when an app that isn't active asks with no input behind it,
// which decides whether a test can see the quick panel take the keyboard
// (#2450); on the main thread.
func sessionKeysPanels() bool { return C.sessionKeysPanels() != 0 }

// nonActivating says whether w shows and takes the keyboard without
// activating magpie, as the quick panel does (#2141); on the main thread.
func nonActivating(w *application.WebviewWindow) bool {
	p := w.NativeWindow()
	return p != nil && C.nonActivating(p) != 0
}

// appFrontmost says whether magpie is the frontmost app, the one whose
// menu bar shows; on the main thread.
func appFrontmost() bool { return C.appFrontmost() != 0 }

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

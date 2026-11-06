//go:build darwin && cgo && !nogui

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <stdlib.h>

// AppKit keeps a status item's visibility in the app's defaults, by its
// autosave name ("NSStatusItem VisibleCC Item-0" on macOS 26, "NSStatusItem
// Visible Item-0" before), whoever set it: the user dragging it out, or the
// app's own setVisible:NO. The next item made under that name starts hidden.
static int mpHiddenKey(NSString *k, id v) {
	return [k hasPrefix:@"NSStatusItem Visible"] && [v respondsToSelector:@selector(boolValue)] && ![v boolValue];
}

// mpForgetHidden removes every status item visibility kept as hidden, and
// says how many it removed.
static int mpForgetHidden(void) {
	NSUserDefaults *d = [NSUserDefaults standardUserDefaults];
	int n = 0;
	for (NSString *k in [[d persistentDomainForName:[[NSBundle mainBundle] bundleIdentifier] ?: [[NSProcessInfo processInfo] processName]] allKeys]) {
		if (mpHiddenKey(k, [d objectForKey:k])) {
			[d removeObjectForKey:k];
			n++;
		}
	}
	if (n) [d synchronize];
	return n;
}

static int mpHiddenCount(void) {
	NSUserDefaults *d = [NSUserDefaults standardUserDefaults];
	int n = 0;
	for (NSString *k in [[d persistentDomainForName:[[NSBundle mainBundle] bundleIdentifier] ?: [[NSProcessInfo processInfo] processName]] allKeys]) {
		if (mpHiddenKey(k, [d objectForKey:k])) n++;
	}
	return n;
}

// After the blocks already queued on the main thread (Wails' setVisible:NO
// is one), so the hide is undone in the defaults only once it is made.
static void mpForgetHiddenLater(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ mpForgetHidden(); });
}

static int mpFlag(const char *k) {
	return [[NSUserDefaults standardUserDefaults] boolForKey:[NSString stringWithUTF8String:k]];
}

static void mpSetFlag(const char *k) {
	NSUserDefaults *d = [NSUserDefaults standardUserDefaults];
	[d setBool:YES forKey:[NSString stringWithUTF8String:k]];
	[d synchronize];
}

// AppKit keeps where a status item sits ("NSStatusItem Preferred Position
// Item-0", where the user or a menu bar manager like Ice put it) by its
// autosave name, and removeStatusItem: forgets it. Wails removes the item
// as the app quits, so every quit, and every restart to update, put the
// icon back at the default spot, left of the others: in Ice's hidden
// section (nb5p). Under a name of its own the removal forgets nothing that
// is kept. Every item of this app's, by its Wails controller (the button's
// target); how many there are, renamed if rename.
static int mpTrayItemsNow(int rename) {
	int n = 0;
	for (NSWindow *w in NSApp.windows) {
		NSMutableArray *views = [NSMutableArray arrayWithObject:w.contentView ?: [NSNull null]];
		while (views.count) {
			id v = views.lastObject;
			[views removeLastObject];
			if (![v isKindOfClass:[NSView class]]) continue;
			[views addObjectsFromArray:[v subviews]];
			if (![v isKindOfClass:[NSStatusBarButton class]]) continue;
			id t = [(NSStatusBarButton *)v target];
			if (![t respondsToSelector:@selector(statusItem)]) continue;
			id it = [t performSelector:@selector(statusItem)];
			if (![it isKindOfClass:[NSStatusItem class]]) continue;
			if (rename) ((NSStatusItem *)it).autosaveName = [NSString stringWithFormat:@"magpie quitting %d", n];
			n++;
		}
	}
	return n;
}

static int mpTrayItems(int rename) {
	if ([NSThread isMainThread]) return mpTrayItemsNow(rename);
	__block int n = 0;
	dispatch_sync(dispatch_get_main_queue(), ^{ n = mpTrayItemsNow(rename); });
	return n;
}

static NSString *mpPlaceKey(const char *name) {
	return [@"NSStatusItem Preferred Position " stringByAppendingString:[NSString stringWithUTF8String:name]];
}

// Where AppKit keeps the item named name, -1 for nowhere.
// Both run on Go threads, which have no autorelease pool of their own.
static double mpTrayPlace(const char *name) {
	@autoreleasepool {
		NSUserDefaults *d = [NSUserDefaults standardUserDefaults];
		[d synchronize];
		id v = [[d persistentDomainForName:[[NSBundle mainBundle] bundleIdentifier] ?: [[NSProcessInfo processInfo] processName]] objectForKey:mpPlaceKey(name)];
		return [v respondsToSelector:@selector(doubleValue)] ? [v doubleValue] : -1;
	}
}

static void mpSetTrayPlace(const char *name, double x) {
	@autoreleasepool {
		NSUserDefaults *d = [NSUserDefaults standardUserDefaults];
		[d setDouble:x forKey:mpPlaceKey(name)];
		[d synchronize];
	}
}

// Waits, off the main thread, for what is queued on it to have run.
static void mpMainSettled(void) {
	dispatch_sync(dispatch_get_main_queue(), ^{});
}
*/
import "C"

import "unsafe"

// trayHidden is how many of this app's status items macOS keeps as hidden.
func trayHidden() int { return int(C.mpHiddenCount()) }

// forgetTrayHidden has the next status item start shown: it removes the
// hidden visibility AppKit keeps for it. How many it removed.
func forgetTrayHidden() int { return int(C.mpForgetHidden()) }

// forgetTrayHiddenLater is forgetTrayHidden once the main thread has run
// what is queued on it, the tray's Hide among them.
func forgetTrayHiddenLater() { C.mpForgetHiddenLater() }

// keepTrayPlace has the menu bar icon's place outlive this magpie: run as
// the app quits, before Wails removes the item, which would forget where
// the user put it (mpKeepTrayPlaceNow). How many items it kept the place of.
func keepTrayPlace() int { return int(C.mpTrayItems(1)) }

// trayItems is how many status items this app has up.
func trayItems() int { return int(C.mpTrayItems(0)) }

// trayPlace is where AppKit keeps the status item named name ("Item-0"),
// and whether it keeps one.
func trayPlace(name string) (float64, bool) {
	k := C.CString(name)
	defer C.free(unsafe.Pointer(k))
	x := float64(C.mpTrayPlace(k))
	return x, x >= 0
}

func setTrayPlace(name string, x float64) {
	k := C.CString(name)
	defer C.free(unsafe.Pointer(k))
	C.mpSetTrayPlace(k, C.double(x))
}

// mainSettled waits for the main thread to run what is queued on it. Not
// on the main thread.
func mainSettled() { C.mpMainSettled() }

// trayBroughtBackKey marks, in the Mac's defaults (this computer's, never
// in a backup), that the hidden icon a handover left was brought back.
const trayBroughtBackKey = "magpie tray brought back after a handover"

// bringTrayBack, once per computer, brings back the menu bar icon a restart
// to update hid: from v0.1.1354 the magpie before hid its own item with
// setVisible:NO, which AppKit keeps for the app, so every later magpie
// started with no icon. A hide kept after that is the user's. Whether one
// stays hidden, to be left alone.
func bringTrayBack() (stillHidden bool) {
	k := C.CString(trayBroughtBackKey)
	defer C.free(unsafe.Pointer(k))
	if C.mpFlag(k) == 0 {
		forgetTrayHidden()
		C.mpSetFlag(k)
	}
	return trayHidden() > 0
}

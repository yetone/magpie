//go:build cgo

package windowshape

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <objc/message.h>
#import <objc/runtime.h>

// AppKit's own: no public API gives a window's corner radius.
static double cornerRadius(void *p) {
	SEL s = sel_registerName("_cornerRadius");
	if (![(id)p respondsToSelector:s]) return -1;
	return ((CGFloat (*)(id, SEL))objc_msgSend)((id)p, s);
}

static int hasToolbar(void *p) { return ((NSWindow *)p).toolbar != nil; }

static void lights(void *p, double *out) {
	NSWindow *w = (NSWindow *)p;
	NSWindowButton kinds[3] = {NSWindowCloseButton, NSWindowMiniaturizeButton, NSWindowZoomButton};
	for (int i = 0; i < 3; i++) {
		NSButton *b = [w standardWindowButton:kinds[i]];
		NSRect r = [b convertRect:b.bounds toView:nil];
		out[2 * i] = NSMidX(r);
		out[2 * i + 1] = NSHeight(w.frame) - NSMidY(r);
	}
	out[6] = NSHeight([w standardWindowButton:NSWindowCloseButton].superview.superview.frame);
}

static void *beginSheet(void *p, double h) {
	NSWindow *s = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 400, h) styleMask:NSWindowStyleMaskTitled
		backing:NSBackingStoreBuffered defer:NO];
	[(NSWindow *)p beginSheet:s completionHandler:nil];
	return s;
}

static double sheetTop(void *p, void *s) {
	return NSMaxY(((NSWindow *)p).frame) - NSMaxY(((NSWindow *)s).frame);
}

static void endSheet(void *p, void *s) {
	[(NSWindow *)p endSheet:(NSWindow *)s];
	[(NSWindow *)s release];
}

static BOOL moreContrast;
static BOOL increaseContrast(id self, SEL _cmd) { return moreContrast; }

static void flipIncreaseContrast(void) {
	static BOOL answered;
	if (!answered) {
		moreContrast = NSWorkspace.sharedWorkspace.accessibilityDisplayShouldIncreaseContrast;
		method_setImplementation(class_getInstanceMethod([NSWorkspace class], @selector(accessibilityDisplayShouldIncreaseContrast)),
			(IMP)increaseContrast);
		answered = YES;
	}
	moreContrast = !moreContrast;
	[NSWorkspace.sharedWorkspace.notificationCenter postNotificationName:NSWorkspaceAccessibilityDisplayOptionsDidChangeNotification
		object:NSWorkspace.sharedWorkspace];
}

static int drags;
static void countDrag(id self, SEL _cmd, NSEvent *e) { drags++; }

static NSEvent *press(NSWindow *w, NSEventType t, NSPoint at) {
	return [NSEvent mouseEventWithType:t location:at modifierFlags:0 timestamp:NSProcessInfo.processInfo.systemUptime
		windowNumber:w.windowNumber context:nil eventNumber:0 clickCount:1 pressure:t == NSEventTypeLeftMouseDown];
}

static int firstPress(void *p, double x, double y) {
	NSWindow *w = (NSWindow *)p;
	if (w.isKeyWindow) return -1;
	NSPoint at = NSMakePoint(x, NSHeight(w.frame) - y);
	Method m = class_getInstanceMethod([w class], @selector(performWindowDragWithEvent:));
	IMP drag = method_setImplementation(m, (IMP)countDrag);
	drags = 0;
	// The release is queued first: a loop the press begins (a resize) takes
	// it and ends.
	[NSApp postEvent:press(w, NSEventTypeLeftMouseUp, at) atStart:NO];
	[NSApp sendEvent:press(w, NSEventTypeLeftMouseDown, at)];
	NSEvent *up = [NSApp nextEventMatchingMask:NSEventMaskLeftMouseUp untilDate:[NSDate distantPast] inMode:NSDefaultRunLoopMode dequeue:YES];
	if (up != nil) [NSApp sendEvent:up];
	method_setImplementation(m, drag);
	return drags;
}
*/
import "C"

import "unsafe"

// Shape is how a window's frame looks, in points.
type Shape struct {
	Radius   float64       // of its corners; -1 when AppKit doesn't say
	Lights   [3][2]float64 // each traffic light's middle: x from the window's left, y from its top
	Titlebar float64       // the title bar's height
	Toolbar  bool          // whether it has a toolbar
}

// Of measures the NSWindow w. Call it on the main thread.
func Of(w unsafe.Pointer) Shape {
	var out [7]C.double
	C.lights(w, &out[0])
	s := Shape{Radius: float64(C.cornerRadius(w)), Titlebar: float64(out[6]), Toolbar: C.hasToolbar(w) != 0}
	for i := range 3 {
		s.Lights[i] = [2]float64{float64(out[2*i]), float64(out[2*i+1])}
	}
	return s
}

// BeginSheet shows a sheet height points tall on w. Call it on the main
// thread, and EndSheet once done.
func BeginSheet(w unsafe.Pointer, height float64) unsafe.Pointer {
	return C.beginSheet(w, C.double(height))
}

// SheetTop is how far below w's top its sheet begins.
func SheetTop(w, sheet unsafe.Pointer) float64 { return float64(C.sheetTop(w, sheet)) }

// EndSheet takes the sheet off w.
func EndSheet(w, sheet unsafe.Pointer) { C.endSheet(w, sheet) }

// FirstPress presses w once, and lets go, x points from its left and y from
// its top, as a press comes while another app is in front: w isn't the key
// window. The press is sent within this process, as AppKit sends the window
// server's, and tells how many window drags it began (counted, not begun),
// or -1 if w is the key window. Call it on the main thread.
func FirstPress(w unsafe.Pointer, x, y float64) int {
	return int(C.firstPress(w, C.double(x), C.double(y)))
}

// FlipIncreaseContrast turns Increase contrast, for this process alone, to
// the opposite of what System Settings has, and back on the next call:
// AppKit is told the display options changed, and finds the setting
// changed, so every window that follows the system takes a new
// appearance. Call it on the main thread.
func FlipIncreaseContrast() { C.flipIncreaseContrast() }

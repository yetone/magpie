//go:build !nogui

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <crt_externs.h>
#import <mach-o/loader.h>
#import <objc/runtime.h>

static const NSWindowButton lightKinds[3] = {NSWindowCloseButton, NSWindowMiniaturizeButton, NSWindowZoomButton};

// MagpieLights keeps a window's traffic lights where its toolbar had put
// them, once the toolbar is gone. AppKit lays the title bar out again
// whenever it changes (a resize or zoom, leaving full screen, a new title or
// appearance), with its own height for the title bar and its own place for
// the lights. Each time, they're put back once AppKit is done: at once after
// a resize, whose animation draws its frames before the run loop turns, else
// as the run loop turns. While AppKit lays them out they can't be: it
// ignores a move of the light it is placing. When the system's appearance
// changes (light or dark, more contrast or less transparency), and at times
// on the way into full screen, AppKit draws the title bar at its own height
// before the run loop turns: the lights keep their distance from its top,
// not its bottom, which would draw them above the window.
@interface MagpieLights : NSObject {
@public
	NSWindow *win;  // not held: the window holds this; nil once it closes
	CGFloat height; // the title bar's, as the toolbar had it
	CGFloat x[3], top[3];
	BOOL busy, pending;
}
@end

@implementation MagpieLights
- (void)apply {
	if (busy || win == nil || (win.styleMask & NSWindowStyleMaskFullScreen)) return;
	NSView *bar = [win standardWindowButton:NSWindowCloseButton].superview, *box = bar.superview, *frame = box.superview;
	if (frame == nil) return;
	busy = YES;
	BOOL moved = NO;
	NSRect f = box.frame;
	NSRect want = NSMakeRect(f.origin.x, frame.isFlipped ? 0 : NSHeight(frame.bounds) - height, f.size.width, height);
	if (!NSEqualRects(f, want)) {
		box.frame = want;
		moved = YES;
	}
	for (int i = 0; i < 3; i++) {
		NSButton *b = [win standardWindowButton:lightKinds[i]];
		if (b.superview != bar) continue;
		NSAutoresizingMaskOptions keepTop = bar.isFlipped ? NSViewMaxYMargin : NSViewMinYMargin;
		if (b.autoresizingMask != keepTop) b.autoresizingMask = keepTop;
		NSPoint o = NSMakePoint(x[i], bar.isFlipped ? top[i] : NSHeight(bar.bounds) - top[i] - NSHeight(b.frame));
		if (!NSEqualPoints(b.frame.origin, o)) {
			[b setFrameOrigin:o];
			moved = YES;
		}
	}
	// AppKit follows the mouse over the lights, to show their symbols, where
	// it laid them out; it looks again where they are as when a live resize
	// ends (and during one, it will then).
	if (moved && !frame.inLiveResize) [frame viewDidEndLiveResize];
	busy = NO;
}

- (void)later {
	if (pending) return;
	pending = YES;
	CFRunLoopPerformBlock(CFRunLoopGetMain(), kCFRunLoopCommonModes, ^{
		pending = NO;
		[self apply];
	});
	CFRunLoopWakeUp(CFRunLoopGetMain());
}
- (void)changed:(NSNotification *)n {
	if (!busy) [self later];
}
- (void)resized:(NSNotification *)n {
	[self apply];
	[self later];
}
- (void)closing:(NSNotification *)n {
	[[NSNotificationCenter defaultCenter] removeObserver:self];
	win = nil;
}
- (void)dealloc {
	[[NSNotificationCenter defaultCenter] removeObserver:self];
	[super dealloc];
}
@end

static char lightsKey;

// A sheet, such as the folder chooser, begins below the title bar, as it
// began below the toolbar. With neither, AppKit lets it rise to the window's
// top, over the header: macOS 26 centres a sheet, so in a short window.
static NSRect sheetBelowTitlebar(id self, SEL _cmd, NSWindow *w, NSWindow *sheet, NSRect r) {
	MagpieLights *l = objc_getAssociatedObject(w, &lightsKey);
	if (l != nil && l->win != nil && !(w.styleMask & NSWindowStyleMaskFullScreen)) r.origin.y = NSHeight(w.frame) - l->height;
	return r;
}

// A first press on the window while it isn't the key one (another app's is
// in front) moves it from anywhere in the title bar the toolbar had, as
// AppKit moved it then. AppKit now does so only in its own, a plain title
// bar's top 32pt, and the page doesn't take a first press: below that, the
// press would only bring the window forward. As AppKit does, a press on a
// view that takes it (a traffic light, an edge that resizes) is left alone.
static NSEvent *firstPress(NSEvent *e) {
	NSWindow *w = e.window;
	MagpieLights *l = w == nil ? nil : objc_getAssociatedObject(w, &lightsKey);
	if (l == nil || l->win == nil || w.isKeyWindow || !w.isMovable || e.clickCount != 1 || (w.styleMask & NSWindowStyleMaskFullScreen)) return e;
	NSView *frame = w.contentView.superview;
	NSPoint p = e.locationInWindow;
	// The content layout rect begins below AppKit's title bar.
	if (frame == nil || !NSPointInRect(p, w.contentLayoutRect) || NSHeight(frame.bounds) - p.y >= l->height) return e;
	NSView *hit = [frame hitTest:p];
	if (hit == nil || [hit acceptsFirstMouse:e]) return e;
	[w performWindowDragWithEvent:e];
	return e;
}

// Whether AppKit gives this program the design of macOS 26, where a toolbar
// gives a window larger corners: from macOS 26, for a program linked against
// the macOS 26 SDK or a later one, unless its Info.plist asks for the earlier
// design (UIDesignRequiresCompatibility). Each is read at run time. The SDK is
// the one the program names, which depends on the toolchain that linked it:
// in LC_BUILD_VERSION, or in LC_VERSION_MIN_MACOSX in a program that runs on
// macOS 10.13, as go build links one for an Intel Mac. The macOS version
// isn't asked with @available(macOS 26.0, *): without make's flags, clang
// compiles this code for the Mac's own macOS, though the program is linked to
// run on earlier ones too, and on a Mac with macOS 26 the check would be
// compiled away.
static BOOL designOf26(void) {
	if (![NSProcessInfo.processInfo isOperatingSystemAtLeastVersion:(NSOperatingSystemVersion){26, 0, 0}]) return NO;
	id compat = [NSBundle.mainBundle objectForInfoDictionaryKey:@"UIDesignRequiresCompatibility"];
	if ([compat respondsToSelector:@selector(boolValue)] && [compat boolValue]) return NO;
	const struct mach_header_64 *h = _NSGetMachExecuteHeader();
	const struct load_command *c = (const struct load_command *)(h + 1);
	for (uint32_t i = 0; i < h->ncmds; i++, c = (const struct load_command *)((const char *)c + c->cmdsize)) {
		if (c->cmd == LC_BUILD_VERSION) return ((const struct build_version_command *)c)->sdk >= 0x1A0000; // 26.0
		if (c->cmd == LC_VERSION_MIN_MACOSX) return ((const struct version_min_command *)c)->sdk >= 0x1A0000;
	}
	return NO;
}

// The Mac's title bar with no toolbar (the design of macOS 26). The main
// window has an empty toolbar only to inset the traffic lights into the
// page's header (MacTitleBarHiddenInset), and in that design a window with a
// toolbar has the larger corners of one: the size of Finder's, where every
// other window with a plain title bar has smaller ones (TextEdit's, and an
// Electron app's that insets its lights by moving them). The toolbar is
// taken off, and the lights are kept where it had them, so the header still
// fits them. The window is left as it was in the earlier design, where a
// toolbar doesn't change the corners (see designOf26), with its lights on
// the right (a right-to-left language), or with a title bar not laid out as
// expected.
static void insetLights(void *p) {
	if (!designOf26()) return;
	NSWindow *w = (NSWindow *)p;
	if (w.toolbar == nil || objc_getAssociatedObject(w, &lightsKey) != nil) return;
	if (w.windowTitlebarLayoutDirection == NSUserInterfaceLayoutDirectionRightToLeft) return;
	NSView *bar = [w standardWindowButton:NSWindowCloseButton].superview, *box = bar.superview, *frame = box.superview;
	if (frame == nil) return;
	[frame layoutSubtreeIfNeeded];
	MagpieLights *l = [[[MagpieLights alloc] init] autorelease];
	l->win = w;
	l->height = NSHeight(box.frame);
	NSView *views[5] = {box, bar};
	for (int i = 0; i < 3; i++) {
		NSButton *b = [w standardWindowButton:lightKinds[i]];
		if (b == nil || b.superview != bar) return;
		l->x[i] = b.frame.origin.x;
		l->top[i] = bar.isFlipped ? NSMinY(b.frame) : NSHeight(bar.bounds) - NSMaxY(b.frame);
		views[2 + i] = b;
	}
	objc_setAssociatedObject(w, &lightsKey, l, OBJC_ASSOCIATION_RETAIN);
	NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
	for (int i = 0; i < 5; i++) {
		views[i].postsFrameChangedNotifications = YES;
		[nc addObserver:l selector:@selector(changed:) name:NSViewFrameDidChangeNotification object:views[i]];
	}
	[nc addObserver:l selector:@selector(resized:) name:NSWindowDidResizeNotification object:w];
	[nc addObserver:l selector:@selector(closing:) name:NSWindowWillCloseNotification object:w];
	static BOOL watchingPresses;
	if (!watchingPresses) {
		watchingPresses = YES;
		[NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskLeftMouseDown handler:^NSEvent *(NSEvent *e) { return firstPress(e); }];
	}
	// Wails' window delegate leaves sheets to AppKit; it's taught to place
	// this window's (a window without the helper keeps AppKit's place).
	SEL s = @selector(window:willPositionSheet:usingRect:);
	id d = w.delegate;
	if (d != nil && ![d respondsToSelector:s])
		class_addMethod([d class], s, (IMP)sheetBelowTitlebar, protocol_getMethodDescription(@protocol(NSWindowDelegate), s, NO, YES).types);
	w.toolbar = nil;
	[l apply];
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// plainTitlebar gives the main window, in the design of macOS 26, the
// corners of a window with a plain title bar (see insetLights); the traffic
// lights stay inset into the page's header.
func plainTitlebar(w *application.WebviewWindow) {
	application.InvokeSync(func() {
		if p := w.NativeWindow(); p != nil {
			C.insetLights(p)
		}
	})
}

func nameWindow(*application.WebviewWindow, string) {}

func ownFrame(*application.WebviewWindow) {}

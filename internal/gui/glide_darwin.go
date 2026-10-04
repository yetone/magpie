//go:build !nogui

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework QuartzCore -framework WebKit
#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>
#import <WebKit/WebKit.h>

// The text size is the webview's page zoom, Safari's Cmd-+: the page is laid
// out again in larger CSS pixels. Wails' own zoom on the Mac is the
// webview's magnification, a pinch that lays nothing out again and scrolls
// the page sideways.
static void setPageZoom(void *w, double z) {
	NSWindow *win = (NSWindow *)w;
	dispatch_async(dispatch_get_main_queue(), ^{
		if (![win respondsToSelector:@selector(webView)]) return;
		id web = [win performSelector:@selector(webView)];
		if (![web isKindOfClass:[WKWebView class]]) return;
		if (@available(macOS 11.0, *)) {
			((WKWebView *)web).pageZoom = z;
		}
	});
}

// The panel hangs from the menu bar, so its top edge stays where it is and
// the bottom moves; the system animates the frame on its own display clock.
static void glidePanel(void *w, int height, int ms, double x1, double y1, double x2, double y2) {
	NSWindow *win = (NSWindow *)w;
	dispatch_async(dispatch_get_main_queue(), ^{
		NSRect f = win.frame;
		NSRect to = NSMakeRect(f.origin.x, NSMaxY(f) - height, f.size.width, height);
		[NSAnimationContext runAnimationGroup:^(NSAnimationContext *ctx) {
			ctx.duration = ms / 1000.0;
			ctx.timingFunction = [CAMediaTimingFunction functionWithControlPoints:x1 :y1 :x2 :y2];
			ctx.allowsImplicitAnimation = YES;
			[[win animator] setFrame:to display:YES];
		} completionHandler:nil];
	});
}

// The page is drawn a frame or two after the panel grows, and until then
// WebKit fills the new edge with its own dark background. So the webview
// draws no background of its own (a key WebKit has long honoured, though not
// a public one) and the page's colour is painted in a view under it, which the
// system sizes along with the panel: the new edge shows that colour until the
// page catches up. Says 0 when the webview can't be made to draw none, and
// the page keeps painting its own.
static int tintPanel(void *w, int r, int g, int b, int a, int ms) {
	NSWindow *win = (NSWindow *)w;
	if (![win respondsToSelector:@selector(webView)]) return 0;
	__block int ok = 0;
	dispatch_sync(dispatch_get_main_queue(), ^{
		NSView *web = [win performSelector:@selector(webView)];
		NSView *host = web.superview;
		if (host == nil) return;
		@try {
			[web setValue:@NO forKey:@"drawsBackground"];
		} @catch (NSException *e) {
			return;
		}
		NSView *tint = nil;
		for (NSView *v in host.subviews) {
			if ([v.identifier isEqualToString:@"magpie-tint"]) tint = v;
		}
		CGColorRef colour = CGColorCreateSRGB(r / 255.0, g / 255.0, b / 255.0, a / 255.0);
		if (tint == nil) {
			tint = [[NSView alloc] initWithFrame:host.bounds];
			tint.identifier = @"magpie-tint";
			tint.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
			// a layer of its own, which AppKit leaves alone
			tint.layer = [CALayer layer];
			tint.wantsLayer = YES;
			tint.layer.cornerRadius = 12;
			tint.layer.masksToBounds = YES;
			tint.layer.backgroundColor = colour;
			[host addSubview:tint positioned:NSWindowBelow relativeTo:web];
			[tint release];
		} else {
			// a change of theme fades as the page's own colours do
			[CATransaction begin];
			CABasicAnimation *fade = [CABasicAnimation animationWithKeyPath:@"backgroundColor"];
			fade.fromValue = (id)tint.layer.backgroundColor;
			fade.toValue = (__bridge id)colour;
			fade.duration = ms / 1000.0;
			fade.timingFunction = [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseInEaseOut];
			tint.layer.backgroundColor = colour;
			if (ms > 0) [tint.layer addAnimation:fade forKey:@"tint"];
			[CATransaction commit];
		}
		CGColorRelease(colour);
		ok = 1;
	});
	return ok;
}

// A Dock icon is the Regular activation policy, none is Accessory. Leaving
// the Dock deactivates the app, so with front set it is brought back to the
// front after, with the window the Settings page is in; a window just closed
// leaves it where it is.
static void setDock(int on, int front) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSApplicationActivationPolicy p = on ? NSApplicationActivationPolicyRegular : NSApplicationActivationPolicyAccessory;
		if ([NSApp activationPolicy] == p) return;
		[NSApp setActivationPolicy:p];
		if (front) [NSApp activateIgnoringOtherApps:YES];
	});
}

// A full-screen window's title bar, buttons and all, comes down from the top
// of the screen only with the menu bar, and an app out of the Dock has none.
// It joins the Dock as the window starts going full screen: once the window
// is full screen, joining brings neither back. Wails' window events reach Go
// later than that, so this is the notification's own observer.
static void dockOnFullscreen(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[[NSNotificationCenter defaultCenter] addObserverForName:NSWindowWillEnterFullScreenNotification object:nil queue:nil usingBlock:^(NSNotification *n) {
			[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
		}];
	});
}
*/
import "C"

import (
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// glidePanel moves the shown panel to height, its top edge held under the
// menu bar. It says false when it can't, for the caller to size it plainly.
func (h *host) glidePanel(height int, g Glide) bool {
	w := h.panel.NativeWindow()
	if w == nil {
		return false
	}
	c := g.Curve
	C.glidePanel(w, C.int(height), C.int(g.MS), C.double(c[0]), C.double(c[1]), C.double(c[2]), C.double(c[3]))
	return true
}

// TintPanel paints the panel's tint under the page, fading to it over ms.
func (h *host) TintPanel(c [4]uint8, ms int) bool {
	// tintPanel waits for the main thread, so it can't run on it; winMu
	// keeps lightweight mode from letting the panel go meanwhile
	h.winMu.Lock()
	defer h.winMu.Unlock()
	w := application.InvokeSyncWithResult(func() unsafe.Pointer {
		if h.panel == nil {
			return nil
		}
		return h.panel.NativeWindow()
	})
	if w == nil {
		return false
	}
	return C.tintPanel(w, C.int(c[0]), C.int(c[1]), C.int(c[2]), C.int(c[3]), C.int(ms)) == 1
}

func dockPolicy(on bool) application.ActivationPolicy {
	if on {
		return application.ActivationPolicyRegular
	}
	return application.ActivationPolicyAccessory
}

// setDock shows magpie in the Dock or takes it out, at once, bringing it to
// the front after when front.
func setDock(on, front bool) { C.setDock(C.int(boolInt(on)), C.int(boolInt(front))) }

// dockOnFullscreen puts magpie in the Dock whenever a window goes full
// screen; leaving it, the window puts magpie back as the settings say.
func dockOnFullscreen() { C.dockOnFullscreen() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// setPageZoom zooms w's page to z, the text size.
func setPageZoom(w *application.WebviewWindow, z float64) {
	if p := w.NativeWindow(); p != nil {
		C.setPageZoom(p, C.double(z))
	}
}

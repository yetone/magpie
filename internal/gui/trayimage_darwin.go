//go:build darwin && cgo

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#include <stdlib.h>
#import <Cocoa/Cocoa.h>

typedef struct {
	const void *icon;
	int iconLen;
	int mono;
	int plain; // no logo: the rows alone, a thin line before
	const char *letter;
	const char *rows; // one or two, a newline between
} mpTrayCell;

// Metrics, in points: the menu bar is 22 high (24 beside a notch), and a
// cell is a 14pt logo with its two windows beside it, stacked as small
// digits, each window's figures right-aligned over the other's. A cell
// without its logo is the digits alone, a hairline mpSepGap either side
// between it and the one before.
static const CGFloat mpLogo = 14, mpLogoGap = 3, mpCellGap = 8, mpBirdGap = 2, mpRowGap = 3.5,
	mpPlainGap = 3, mpSepGap = 5, mpSep = 1, mpSepHigh = 13;

static NSFont *mpRowFont(int rows) {
	if (rows > 1) return [NSFont monospacedDigitSystemFontOfSize:9.5 weight:NSFontWeightSemibold];
	return [NSFont monospacedDigitSystemFontOfSize:11 weight:NSFontWeightMedium];
}

// mpCells reads the cells Go sends into dictionaries the drawing keeps.
static NSArray *mpCells(mpTrayCell *cells, int n) {
	NSMutableArray *out = [NSMutableArray arrayWithCapacity:n];
	for (int i = 0; i < n; i++) {
		NSMutableDictionary *c = [NSMutableDictionary dictionary];
		if (cells[i].icon != NULL && cells[i].iconLen > 0) {
			NSImage *im = [[NSImage alloc] initWithData:[NSData dataWithBytes:cells[i].icon length:cells[i].iconLen]];
			// an SVG an older system can't read comes back as nothing, or empty
			if (im != nil && im.representations.count > 0) c[@"icon"] = im;
			[im release];
		}
		c[@"mono"] = @(cells[i].mono != 0);
		c[@"plain"] = @(cells[i].plain != 0);
		c[@"letter"] = [NSString stringWithUTF8String:cells[i].letter ? cells[i].letter : ""];
		NSArray *rows = [[NSString stringWithUTF8String:cells[i].rows ? cells[i].rows : ""] componentsSeparatedByString:@"\n"];
		if (rows.count > 2) rows = [rows subarrayWithRange:NSMakeRange(0, 2)];
		c[@"rows"] = rows;
		[out addObject:c];
	}
	return out;
}

static CGFloat mpColumn(NSDictionary *c) {
	NSArray *rows = c[@"rows"];
	NSDictionary *attrs = @{NSFontAttributeName: mpRowFont((int)rows.count)};
	CGFloat w = 0;
	for (NSString *r in rows) w = MAX(w, ceil([r sizeWithAttributes:attrs].width));
	return w;
}

// mpLead is the room before a cell's digits: its logo, and the gap from the
// cell before (the bird's for the first); a cell without its logo has the
// line between it and the one before instead.
static CGFloat mpLead(NSDictionary *c, BOOL first) {
	if ([c[@"plain"] boolValue]) return first ? mpPlainGap : mpSepGap + mpSep + mpSepGap;
	return (first ? 0 : mpCellGap) + mpLogo + mpLogoGap;
}

static CGFloat mpWidth(NSArray *cells, CGFloat h) {
	CGFloat w = h + mpBirdGap;
	BOOL first = YES;
	for (NSDictionary *c in cells) {
		w += mpLead(c, first) + mpColumn(c);
		first = NO;
	}
	return ceil(w + 1);
}

// mpTinted draws a black glyph in the text colour of what it is drawn on
// (the menu bar's, light or dark), as a template image is: in a layer of
// its own, so filling it touches nothing drawn before.
static void mpTinted(NSImage *im, NSRect r) {
	CGContextRef cg = [[NSGraphicsContext currentContext] CGContext];
	CGContextBeginTransparencyLayerWithRect(cg, NSRectToCGRect(r), NULL);
	[im drawInRect:r fromRect:NSZeroRect operation:NSCompositingOperationSourceOver fraction:1 respectFlipped:YES hints:nil];
	[[NSColor labelColor] set];
	NSRectFillUsingOperation(r, NSCompositingOperationSourceIn);
	CGContextEndTransparencyLayer(cg);
}

// mpRows draws a cell's digits in its column from x: the digits (cap
// height) centred as a block, each row right-aligned; drawAtPoint takes the
// line's top, its baseline an ascender below.
static void mpRows(NSDictionary *c, CGFloat x, CGFloat h, NSColor *ink) {
	NSArray *rows = c[@"rows"];
	NSFont *f = mpRowFont((int)rows.count);
	NSDictionary *a = @{NSFontAttributeName: f, NSForegroundColorAttributeName: ink};
	CGFloat col = mpColumn(c);
	CGFloat cap = f.capHeight;
	CGFloat top = (h - (cap * rows.count + mpRowGap * (rows.count - 1))) / 2;
	for (NSUInteger i = 0; i < rows.count; i++) {
		NSString *r = rows[i];
		CGFloat base = top + cap * (i + 1) + mpRowGap * i;
		CGFloat w = [r sizeWithAttributes:a].width;
		[r drawAtPoint:NSMakePoint(x + col - w, base - f.ascender) withAttributes:a];
	}
}

// mpDraw draws the bird, then each cell, top down (a flipped context).
static void mpDraw(NSArray *cells, NSImage *bird, CGFloat h) {
	if (bird != nil) mpTinted(bird, NSMakeRect(0, 0, h, h));
	CGFloat x = h + mpBirdGap;
	NSColor *ink = [NSColor labelColor];
	BOOL first = YES;
	for (NSDictionary *c in cells) {
		if ([c[@"plain"] boolValue]) {
			if (!first) {
				[[ink colorWithAlphaComponent:0.35] set];
				NSRectFillUsingOperation(NSMakeRect(x + mpSepGap, round((h - mpSepHigh) / 2), mpSep, mpSepHigh), NSCompositingOperationSourceOver);
			}
			x += mpLead(c, first);
			first = NO;
			mpRows(c, x, h, ink);
			x += mpColumn(c);
			continue;
		}
		x += mpLead(c, first) - mpLogo - mpLogoGap;
		first = NO;
		NSRect logo = NSMakeRect(x, round((h - mpLogo) / 2), mpLogo, mpLogo);
		NSImage *icon = c[@"icon"];
		if (icon != nil && [c[@"mono"] boolValue]) {
			mpTinted(icon, logo);
		} else if (icon != nil) {
			[icon drawInRect:logo fromRect:NSZeroRect operation:NSCompositingOperationSourceOver fraction:1 respectFlipped:YES hints:nil];
		} else {
			// no logo: the name's first letter in a ring
			NSBezierPath *ring = [NSBezierPath bezierPathWithRoundedRect:NSInsetRect(logo, 0.75, 0.75) xRadius:3.5 yRadius:3.5];
			ring.lineWidth = 1.2;
			[ink set];
			[ring stroke];
			NSDictionary *a = @{NSFontAttributeName: [NSFont systemFontOfSize:8.5 weight:NSFontWeightBold], NSForegroundColorAttributeName: ink};
			NSString *l = c[@"letter"];
			NSSize s = [l sizeWithAttributes:a];
			[l drawAtPoint:NSMakePoint(NSMidX(logo) - s.width / 2, NSMidY(logo) - s.height / 2) withAttributes:a];
		}
		x += mpLogo + mpLogoGap;
		mpRows(c, x, h, ink);
		x += mpColumn(c);
	}
}

// mpImage is the bird and the cells as one image, drawn afresh each time it
// is shown so its colours follow the menu bar's, and sharp at any scale.
static NSImage *mpImage(NSArray *cells, NSImage *bird, CGFloat h) {
	NSImage *im = [NSImage imageWithSize:NSMakeSize(mpWidth(cells, h), h) flipped:YES drawingHandler:^BOOL(NSRect r) {
		mpDraw(cells, bird, h);
		return YES;
	}];
	im.cacheMode = NSImageCacheNever;
	return im;
}

static NSStatusBarButton *mpFind(NSView *v) {
	if ([v isKindOfClass:[NSStatusBarButton class]]) return (NSStatusBarButton *)v;
	for (NSView *s in v.subviews) {
		NSStatusBarButton *b = mpFind(s);
		if (b != nil) return b;
	}
	return nil;
}

// mpButton is magpie's one status item's button, in the status bar's window.
static NSStatusBarButton *mpButton(void) {
	for (NSWindow *w in NSApp.windows) {
		NSStatusBarButton *b = mpFind(w.contentView);
		if (b != nil) return b;
	}
	return nil;
}

// What the item shows while the cells are up; the bird changes as it flaps.
static NSArray *mpShown;
static NSImage *mpBird;

static void mpApply(void) {
	NSStatusBarButton *b = mpButton();
	if (b == nil || mpShown == nil) return;
	b.title = @"";
	b.image = mpImage(mpShown, mpBird, [[NSStatusBar systemStatusBar] thickness]);
	b.imagePosition = NSImageOnly;
}

static NSImage *mpBirdImage(const void *bird, int len) {
	NSImage *im = [[NSImage alloc] initWithData:[NSData dataWithBytes:bird length:len]];
	return im;
}

// mpShow puts the cells up beside the bird; 0 when there is no item to put
// them on. On the main queue after what Wails has queued there (the label
// taken off), so nothing of Wails' lands after it.
// It runs on a Go thread, which has no autorelease pool of its own to
// drain what reading the cells leaves (every few minutes, for good).
static int mpShow(mpTrayCell *cells, int n, const void *bird, int len) {
	__block int ok = 0;
	@autoreleasepool {
		NSArray *cs = [mpCells(cells, n) retain];
		NSImage *bi = mpBirdImage(bird, len);
		dispatch_sync(dispatch_get_main_queue(), ^{
			if (mpButton() == nil) return;
			[mpShown release];
			[mpBird release];
			mpShown = cs;
			mpBird = [bi retain];
			mpApply();
			ok = 1;
		});
		if (!ok) [cs release];
		[bi release];
	}
	return ok;
}

// mpFrame is a frame of the bird's flap while the cells are up; 0 when
// they aren't, and the frame is Wails' to set as the icon.
static int mpFrame(const void *bird, int len) {
	__block int ok = 0;
	@autoreleasepool {
		NSImage *bi = mpBirdImage(bird, len);
		dispatch_sync(dispatch_get_main_queue(), ^{
			if (mpShown == nil) return;
			[mpBird release];
			mpBird = [bi retain];
			mpApply();
			ok = 1;
		});
		[bi release];
	}
	return ok;
}

// mpHide forgets the cells; the icon Wails sets next puts the bird back.
static void mpHide(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[mpShown release];
		mpShown = nil;
	});
}

// mpPNG draws the image as the menu bar would at scale, light or dark, on
// the bar's colour when bg is set: for tests and previews.
static void *mpPNG(mpTrayCell *cells, int n, const void *bird, int len, CGFloat h, CGFloat scale, int dark, int bg, int *outLen, int *pxW, int *pxH) {
	__block void *out = NULL;
	@autoreleasepool {
		NSArray *cs = mpCells(cells, n);
		NSImage *bi = len > 0 ? [mpBirdImage(bird, len) autorelease] : nil;
		CGFloat w = mpWidth(cs, h);
		NSBitmapImageRep *rep = [[[NSBitmapImageRep alloc] initWithBitmapDataPlanes:NULL pixelsWide:(NSInteger)ceil(w * scale) pixelsHigh:(NSInteger)ceil(h * scale)
			bitsPerSample:8 samplesPerPixel:4 hasAlpha:YES isPlanar:NO colorSpaceName:NSDeviceRGBColorSpace bytesPerRow:0 bitsPerPixel:0] autorelease];
		rep.size = NSMakeSize(w, h);
		*pxW = (int)rep.pixelsWide;
		*pxH = (int)rep.pixelsHigh;
		NSGraphicsContext *g = [NSGraphicsContext graphicsContextWithBitmapImageRep:rep];
		NSAppearance *look = [NSAppearance appearanceNamed:dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua];
		[NSGraphicsContext saveGraphicsState];
		[NSGraphicsContext setCurrentContext:g];
		void (^paint)(void) = ^{
			if (bg) {
				[(dark ? [NSColor colorWithSRGBRed:0.16 green:0.16 blue:0.17 alpha:1] : [NSColor colorWithSRGBRed:0.93 green:0.93 blue:0.94 alpha:1]) set];
				NSRectFill(NSMakeRect(0, 0, w, h));
			}
			[mpImage(cs, bi, h) drawInRect:NSMakeRect(0, 0, w, h)];
		};
		if (@available(macOS 11.0, *)) {
			[look performAsCurrentDrawingAppearance:paint];
		} else {
			paint(); // the system's own look, light or dark as it is
		}
		[g flushGraphics];
		[NSGraphicsContext restoreGraphicsState];
		NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
		*outLen = (int)png.length;
		out = malloc(png.length);
		memcpy(out, png.bytes, png.length);
	}
	return out;
}
*/
import "C"

import (
	"strings"
	"unsafe"
)

// The Mac's menu bar draws the cards as one image beside the bird (see
// trayusage.go): drawn by AppKit, as the bar's own text is, in its colour
// light or dark and sharp at any scale. Wails sizes an icon it is given to
// a square, so the image is set on the status item's button here; the
// bird's flap goes through it while it is up (trayImageFrame).

// withCells hands the cells to C for the length of f.
func withCells(cells []trayCell, f func(*C.mpTrayCell, C.int)) {
	if len(cells) == 0 {
		f(nil, 0)
		return
	}
	cs := (*[1 << 16]C.mpTrayCell)(C.malloc(C.size_t(len(cells)) * C.size_t(unsafe.Sizeof(C.mpTrayCell{}))))[:len(cells):len(cells)]
	var owned []unsafe.Pointer
	for i, c := range cells {
		cs[i] = C.mpTrayCell{letter: C.CString(c.Letter), rows: C.CString(strings.Join(c.Rows, "\n"))}
		owned = append(owned, unsafe.Pointer(cs[i].letter), unsafe.Pointer(cs[i].rows))
		if len(c.Icon) > 0 {
			cs[i].icon, cs[i].iconLen = C.CBytes(c.Icon), C.int(len(c.Icon))
			owned = append(owned, cs[i].icon)
		}
		if c.Mono {
			cs[i].mono = 1
		}
		if c.Plain {
			cs[i].plain = 1
		}
	}
	defer func() {
		for _, p := range owned {
			C.free(p)
		}
		C.free(unsafe.Pointer(&cs[0]))
	}()
	f(&cs[0], C.int(len(cells)))
}

func cBytes(b []byte) (unsafe.Pointer, C.int) {
	if len(b) == 0 {
		return nil, 0
	}
	return C.CBytes(b), C.int(len(b))
}

// trayImageShow puts the cells up beside the bird; false when the item
// can't be found, and the text is to be shown instead.
func trayImageShow(cells []trayCell, bird []byte) bool {
	ok := false
	b, n := cBytes(bird)
	defer C.free(b)
	withCells(cells, func(cs *C.mpTrayCell, count C.int) { ok = C.mpShow(cs, count, b, n) != 0 })
	return ok
}

// trayImageFrame sets a frame of the bird's flap while the cells are up;
// false when they aren't, for Wails to set it as the icon.
func trayImageFrame(bird []byte) bool {
	b, n := cBytes(bird)
	defer C.free(b)
	return C.mpFrame(b, n) != 0
}

// trayImageHide takes the cells down; the icon set next is the bird alone.
func trayImageHide() { C.mpHide() }

// trayImagePNG is the image as the menu bar h points high draws it at
// scale (2 for a Retina screen), light or dark, on the bar's colour when
// bg; with its size in pixels.
func trayImagePNG(cells []trayCell, bird []byte, h, scale float64, dark, bg bool) (png []byte, w, ht int) {
	b, n := cBytes(bird)
	defer C.free(b)
	withCells(cells, func(cs *C.mpTrayCell, count C.int) {
		var size, pw, ph C.int
		p := C.mpPNG(cs, count, b, n, C.CGFloat(h), C.CGFloat(scale), cBool(dark), cBool(bg), &size, &pw, &ph)
		if p != nil {
			png = C.GoBytes(p, size)
			C.free(p)
		}
		w, ht = int(pw), int(ph)
	})
	return png, w, ht
}

func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

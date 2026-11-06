//go:build darwin && cgo

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include <stdlib.h>

// The Dock tile flaps as the header logo does: the icon the system draws
// for magpie (its glass tile and glossy bird, light) is cut into body, tail
// and wing by the logo's clip paths, and each frame turns the pieces over
// the tile with the bird taken out. Drawn at mpDockPx; the tile's view
// scales it to the Dock's size.
static const int mpDockPx = 512;
// where the system puts the bird on the canvas: centred, 0.64 of it across
// (a 0.8 tile, the glyph 0.8 of that), as build/icon/gen.go's app() and
// AppIcon.icon lay it
static const CGFloat mpDockBird = 0.64;

static CGImageRef mpDockClean;   // the tile, the bird taken out
static CGImageRef mpDockPiece[3]; // body, tail, wing, each with alpha
static CGPathRef mpDockBirdPath;
static NSImageView *mpDockView;

static CGPathRef mpDockPath(const char *ops, int nops, const double *pts) {
	CGMutablePathRef p = CGPathCreateMutable();
	for (int i = 0, j = 0; i < nops; i++) {
		switch (ops[i]) {
		case 'M': CGPathMoveToPoint(p, NULL, pts[j], pts[j+1]); j += 2; break;
		case 'L': CGPathAddLineToPoint(p, NULL, pts[j], pts[j+1]); j += 2; break;
		case 'C': CGPathAddCurveToPoint(p, NULL, pts[j], pts[j+1], pts[j+2], pts[j+3], pts[j+4], pts[j+5]); j += 6; break;
		case 'Z': CGPathCloseSubpath(p); break;
		}
	}
	return p;
}

static CGContextRef mpDockContext(void) {
	CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
	CGContextRef c = CGBitmapContextCreate(NULL, mpDockPx, mpDockPx, 8, 0, cs, (CGBitmapInfo)kCGImageAlphaPremultipliedLast);
	CGColorSpaceRelease(cs);
	return c;
}

// mpDockUnits puts the viewBox's 44 units, y down, where the system draws
// the bird.
static void mpDockUnits(CGContextRef c) {
	CGFloat k = mpDockPx * mpDockBird / 44, o = mpDockPx / 2.0 - 22 * k;
	CGContextTranslateCTM(c, o, mpDockPx - o);
	CGContextScaleCTM(c, k, -k);
}

// The bird's ink reaches a little past its path: its antialiased and glossy
// edge. Each cut takes this much more, in pixels, so no edge is left on the
// tile.
static const CGFloat mpDockEdge = 4;

// Next pieces overlap by this much, in pixels, so no seam shows where
// their cuts' antialiased edges meet.
static const CGFloat mpDockSeam = 1;

// mpDockFill is a grey mask, in the canvas's pixels, of path filled
// even-odd and stroked out by edge pixels, within the mask within if any.
static CGImageRef mpDockFill(CGPathRef path, CGFloat edge, CGImageRef within) {
	CGColorSpaceRef g = CGColorSpaceCreateDeviceGray();
	CGContextRef c = CGBitmapContextCreate(NULL, mpDockPx, mpDockPx, 8, 0, g, (CGBitmapInfo)kCGImageAlphaNone);
	CGColorSpaceRelease(g);
	CGContextSetGrayFillColor(c, 0, 1);
	CGContextFillRect(c, CGRectMake(0, 0, mpDockPx, mpDockPx));
	if (within) CGContextClipToMask(c, CGRectMake(0, 0, mpDockPx, mpDockPx), within);
	mpDockUnits(c);
	CGContextSetGrayFillColor(c, 1, 1);
	CGContextSetGrayStrokeColor(c, 1, 1);
	CGContextAddPath(c, path);
	CGContextEOFillPath(c);
	CGContextSetLineWidth(c, 2 * edge * 44 / (mpDockPx * mpDockBird));
	CGContextSetLineJoin(c, kCGLineJoinRound);
	CGContextAddPath(c, path);
	CGContextStrokePath(c);
	CGImageRef m = CGBitmapContextCreateImage(c);
	CGContextRelease(c);
	return m;
}

// mpDockMask is the bird and its edge, within clip (none: the whole bird)
// grown by mpDockSeam.
static CGImageRef mpDockMask(CGPathRef clip, CGFloat edge) {
	CGImageRef within = clip ? mpDockFill(clip, mpDockSeam, NULL) : NULL;
	CGImageRef m = mpDockFill(mpDockBirdPath, edge, within);
	if (within) CGImageRelease(within);
	return m;
}

// mpDockReady cuts the icon up once: the pieces, and the tile with the
// bird filled with the tile's own colour, read either side of it on each
// row and blended across. 0 when the icon can't be had.
static int mpDockReady(const char *app, const char *bops, int nb, const double *bpts, const char **cops, const int *nc, const double **cpts) {
	if (mpDockClean) return 1;
	@autoreleasepool {
	NSString *path = app ? [NSString stringWithUTF8String:app] : [[NSBundle mainBundle] bundlePath];
	NSImage *icon = [[NSWorkspace sharedWorkspace] iconForFile:path];
	if (!icon) return 0;
	CGContextRef c = mpDockContext();
	if (!c) return 0;
	NSGraphicsContext *ns = [NSGraphicsContext graphicsContextWithCGContext:c flipped:NO];
	[NSGraphicsContext saveGraphicsState];
	[NSGraphicsContext setCurrentContext:ns];
	[[NSAppearance appearanceNamed:NSAppearanceNameAqua] performAsCurrentDrawingAppearance:^{
		[icon drawInRect:NSMakeRect(0, 0, mpDockPx, mpDockPx) fromRect:NSZeroRect operation:NSCompositingOperationCopy fraction:1];
	}];
	[NSGraphicsContext restoreGraphicsState];
	CGImageRef full = CGBitmapContextCreateImage(c);
	mpDockBirdPath = mpDockPath(bops, nb, bpts);

	for (int i = 0; i < 3; i++) {
		CGPathRef clip = mpDockPath(cops[i], nc[i], cpts[i]);
		CGImageRef m = mpDockMask(clip, mpDockEdge);
		CGContextRef p = mpDockContext();
		CGContextClipToMask(p, CGRectMake(0, 0, mpDockPx, mpDockPx), m);
		CGContextDrawImage(p, CGRectMake(0, 0, mpDockPx, mpDockPx), full);
		mpDockPiece[i] = CGBitmapContextCreateImage(p);
		CGContextRelease(p);
		CGImageRelease(m);
		CGPathRelease(clip);
	}

	// the bird out: a wider cut than the pieces take, so a piece turned
	// away leaves the tile's colour, not its old edge
	CGImageRef m = mpDockMask(NULL, mpDockEdge + 1);
	CFDataRef md = CGDataProviderCopyData(CGImageGetDataProvider(m));
	size_t mrow = CGImageGetBytesPerRow(m);
	CGImageRelease(m);
	const uint8_t *mask = CFDataGetBytePtr(md);
	uint8_t *px = CGBitmapContextGetData(c);
	size_t row = CGBitmapContextGetBytesPerRow(c);
	// either side of the bird, inside the tile and its rim: the bird spans
	// 0.18 to 0.82 of the canvas, the tile 0.1 to 0.9
	int l0 = mpDockPx * 0.12, l1 = mpDockPx * 0.16, r0 = mpDockPx * 0.84, r1 = mpDockPx * 0.87;
	CGFloat lx = (l0 + l1) / 2.0, rx = (r0 + r1) / 2.0;
	for (int y = 0; y < mpDockPx; y++) {
		uint8_t *r = px + y * row;
		// both bitmaps are top row first
		const uint8_t *mr = mask + y * mrow;
		double L[4] = {0}, R[4] = {0};
		for (int x = l0; x < l1; x++) for (int k = 0; k < 4; k++) L[k] += r[4*x+k];
		for (int x = r0; x < r1; x++) for (int k = 0; k < 4; k++) R[k] += r[4*x+k];
		for (int k = 0; k < 4; k++) { L[k] /= (l1 - l0); R[k] /= (r1 - r0); }
		for (int x = 0; x < mpDockPx; x++) {
			if (!mr[x]) continue;
			double a = mr[x] / 255.0, f = fmin(1, fmax(0, (x - lx) / (rx - lx)));
			for (int k = 0; k < 4; k++) {
				double bg = L[k] * (1 - f) + R[k] * f;
				r[4*x+k] = (uint8_t)lround(r[4*x+k] * (1 - a) + bg * a);
			}
		}
	}
	CFRelease(md);
	mpDockClean = CGBitmapContextCreateImage(c);
	CGContextRelease(c);
	CGImageRelease(full);
	return mpDockClean != NULL;
	}
}

// mpDockTurn turns the context deg degrees about (x, y) in the viewBox,
// clockwise on screen as CSS's rotate() is (the units run y down).
static void mpDockTurn(CGContextRef c, double x, double y, double deg) {
	CGContextTranslateCTM(c, x, y);
	CGContextRotateCTM(c, deg * M_PI / 180);
	CGContextTranslateCTM(c, -x, -y);
}

// mpDockFrameImage draws a frame: the bob outside, each piece's own turn
// inside, as the logo's groups nest.
static CGImageRef mpDockFrameImage(double bob, double tail, double wing, const double *at) {
	CGContextRef c = mpDockContext();
	if (!c) return NULL;
	CGContextSetInterpolationQuality(c, kCGInterpolationHigh);
	CGRect all = CGRectMake(0, 0, mpDockPx, mpDockPx);
	CGContextDrawImage(c, all, mpDockClean);
	double deg[3] = {0, tail, wing};
	for (int i = 0; i < 3; i++) {
		CGContextSaveGState(c);
		mpDockUnits(c);
		mpDockTurn(c, at[4], at[5], bob);
		// its own turn: the body none, the tail about at[0..1], the wing at[2..3]
		if (i) mpDockTurn(c, at[2*i-2], at[2*i-1], deg[i]);
		// back to pixels, y up, to draw the image upright
		CGFloat k = mpDockPx * mpDockBird / 44, o = mpDockPx / 2.0 - 22 * k;
		CGContextScaleCTM(c, 1 / k, -1 / k);
		CGContextTranslateCTM(c, -o, -(mpDockPx - o));
		CGContextDrawImage(c, all, mpDockPiece[i]);
		CGContextRestoreGState(c);
	}
	CGImageRef im = CGBitmapContextCreateImage(c);
	CGContextRelease(c);
	return im;
}

// mpDockPNG writes a frame to a file, to look at.
static int mpDockPNG(const char *file, double bob, double tail, double wing, const double *at) {
	CGImageRef im = mpDockFrameImage(bob, tail, wing, at);
	if (!im) return 0;
	@autoreleasepool {
		NSBitmapImageRep *rep = [[[NSBitmapImageRep alloc] initWithCGImage:im] autorelease];
		CGImageRelease(im);
		NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
		return [png writeToFile:[NSString stringWithUTF8String:file] atomically:YES];
	}
}

// mpDockFrameNS is a frame as an NSImage, retained. It is made on a Go
// thread, which has no autorelease pool: without one of its own, what
// AppKit autoreleases in making it (its NSCGImageSnapshotRep, holding the
// frame's 1 MB of pixels) was never let go, 29 frames a flap, for as long
// as magpie ran (#4162).
static NSImage *mpDockFrameNS(CGImageRef im) {
	@autoreleasepool {
		return [[NSImage alloc] initWithCGImage:im size:NSMakeSize(mpDockPx, mpDockPx)];
	}
}

// mpDockFrameFreed makes a frame's NSImage as mpDockShow does, on the
// calling thread, lets it go, and says whether its pixels went with it:
// 1 when nothing of it is left, for tests.
static int mpDockFrameFreed(void) {
	CGContextRef c = mpDockContext();
	if (!c) return -1;
	CGImageRef im = CGBitmapContextCreateImage(c);
	CGContextRelease(c);
	if (!im) return -1;
	NSImage *ns = mpDockFrameNS(im);
	CGImageRelease(im);
	// a weak reference to its pixels' rep; each look at it in a pool of
	// its own, since objc_loadWeak autoreleases what it gives
	id rep = nil;
	int seen, freed;
	@autoreleasepool {
		objc_storeWeak(&rep, ns.representations.firstObject);
		seen = objc_loadWeak(&rep) != nil;
	}
	[ns release];
	@autoreleasepool {
		freed = objc_loadWeak(&rep) == nil;
		objc_storeWeak(&rep, nil);
	}
	if (!seen) return -1; // nothing to watch
	return freed;
}

static void mpDockShow(double bob, double tail, double wing, const double *at) {
	CGImageRef im = mpDockFrameImage(bob, tail, wing, at);
	if (!im) return;
	NSImage *ns = mpDockFrameNS(im);
	CGImageRelease(im);
	dispatch_async(dispatch_get_main_queue(), ^{
		NSDockTile *tile = NSApp.dockTile;
		if (!mpDockView) {
			mpDockView = [[NSImageView alloc] initWithFrame:NSMakeRect(0, 0, tile.size.width, tile.size.height)];
			mpDockView.imageScaling = NSImageScaleProportionallyUpOrDown;
		}
		mpDockView.image = ns;
		[ns release];
		if (tile.contentView != mpDockView) tile.contentView = mpDockView;
		[tile display];
	});
}

// mpDockStill gives the tile back to the system's icon.
static void mpDockStill(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSDockTile *tile = NSApp.dockTile;
		tile.contentView = nil;
		mpDockView.image = nil; // the last frame's pixels, until the next flap
		[tile display];
	});
}

// mpDockPlain is 1 when the Dock draws icons as they are, light: the
// cut is of that drawing, and a dark, clear or tinted Dock would flash it.
static int mpDockPlain(void) {
	CFPropertyListRef v = CFPreferencesCopyAppValue(CFSTR("AppleIconAppearanceTheme"), kCFPreferencesAnyApplication);
	if (!v) return 1;
	int plain = 0;
	if (CFGetTypeID(v) == CFStringGetTypeID()) {
		NSString *s = (__bridge NSString *)v;
		plain = [s isEqualToString:@"RegularLight"];
		if ([s isEqualToString:@"RegularAutomatic"]) {
			NSAppearanceName n = [NSApp.effectiveAppearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
			plain = [n isEqualToString:NSAppearanceNameAqua];
		}
	}
	CFRelease(v);
	return plain;
}
*/
import "C"

import (
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var (
	dockOnce     sync.Once
	dockGood     bool
	dockFlapping atomic.Bool
)

// dockReady cuts the icon of app (magpie's own bundle when "") up the first
// time the tile flaps, by the header logo's paths; false when it can't, or
// the Dock shows icons another way.
func dockReady(app string) bool {
	if C.mpDockPlain() == 0 {
		return false
	}
	dockOnce.Do(func() {
		page, err := assets.ReadFile("assets/index.html")
		if err != nil {
			return
		}
		bird, clips, ok := logoPaths(string(page))
		if !ok {
			return
		}
		var capp *C.char
		if app != "" {
			capp = C.CString(app)
			defer C.free(unsafe.Pointer(capp))
		}
		// C copies of each path, the bird first
		var ops, pts [4]unsafe.Pointer
		for i, p := range append([]flapPath{bird}, clips[:]...) {
			ops[i], pts[i] = C.CBytes(p.ops), C.CBytes(floats(p.pts))
			defer C.free(ops[i])
			defer C.free(pts[i])
		}
		cops := (*[3]*C.char)(C.malloc(C.size_t(3 * unsafe.Sizeof(uintptr(0)))))
		cpts := (*[3]*C.double)(C.malloc(C.size_t(3 * unsafe.Sizeof(uintptr(0)))))
		nc := (*[3]C.int)(C.malloc(C.size_t(3 * unsafe.Sizeof(C.int(0)))))
		defer C.free(unsafe.Pointer(cops))
		defer C.free(unsafe.Pointer(cpts))
		defer C.free(unsafe.Pointer(nc))
		for i := range 3 {
			cops[i], cpts[i], nc[i] = (*C.char)(ops[i+1]), (*C.double)(pts[i+1]), C.int(len(clips[i].ops))
		}
		dockGood = C.mpDockReady(capp, (*C.char)(ops[0]), C.int(len(bird.ops)), (*C.double)(pts[0]),
			(**C.char)(unsafe.Pointer(cops)), (*C.int)(unsafe.Pointer(nc)), (**C.double)(unsafe.Pointer(cpts))) != 0
	})
	return dockGood
}

// floats is a []float64 as C doubles' bytes.
func floats(f []float64) []byte {
	if len(f) == 0 {
		return []byte{0}
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&f[0])), len(f)*8)
}

// dockAt is the turn points, tail, wing and bob, as the C side reads them.
func dockAt() *C.double {
	at := (*[6]C.double)(C.malloc(C.size_t(6 * unsafe.Sizeof(C.double(0)))))
	for i, v := range [6]float64{tailAt[0], tailAt[1], wingAt[0], wingAt[1], bobAt[0], bobAt[1]} {
		at[i] = C.double(v)
	}
	return &at[0]
}

// dockFlap plays the logo's flap on the Dock tile, a frame every
// flapEvery, and gives the tile back to the system's icon after. A click
// while it plays doesn't start another.
func dockFlap() {
	if !dockFlapping.CompareAndSwap(false, true) {
		return
	}
	defer dockFlapping.Store(false)
	if !dockReady("") {
		return
	}
	at := dockAt()
	defer C.free(unsafe.Pointer(at))
	secs := flapSecs
	n := int(secs/flapEvery + 0.5)
	start := time.Now()
	for f := 1; f < n; f++ {
		bob, tail, wing := flapAt(float64(f) / float64(n))
		C.mpDockShow(C.double(bob), C.double(tail), C.double(wing), at)
		// on the flap's clock, not the frames': a slow frame is skipped past
		time.Sleep(time.Until(start.Add(time.Duration(float64(f+1) * flapEvery * float64(time.Second)))))
	}
	C.mpDockStill()
}

// dockFramePNG writes the tile at t, 0..1 of the flap, cut from app's icon.
func dockFramePNG(app, file string, t float64) bool {
	if !dockReady(app) {
		return false
	}
	at := dockAt()
	defer C.free(unsafe.Pointer(at))
	cf := C.CString(file)
	defer C.free(unsafe.Pointer(cf))
	bob, tail, wing := flapAt(t)
	return C.mpDockPNG(cf, C.double(bob), C.double(tail), C.double(wing), at) != 0
}

// dockFrameFreed makes a Dock frame's image on this goroutine's thread, as
// dockFlap does, lets it go, and says whether its pixels went with it; ok
// is false when there was nothing to watch.
func dockFrameFreed() (freed, ok bool) {
	r := C.mpDockFrameFreed()
	return r == 1, r >= 0
}

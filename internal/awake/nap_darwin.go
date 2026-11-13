//go:build darwin && cgo

package awake

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>
#include <stdlib.h>

// magpie_nap_off begins an activity that keeps App Nap off and lets the
// system sleep when idle; it returns the activity, retained, for
// magpie_nap_on to end.
static void *magpie_nap_off(const char *reason) {
	@autoreleasepool {
		id a = [[NSProcessInfo processInfo]
			beginActivityWithOptions:NSActivityUserInitiatedAllowingIdleSystemSleep
			reason:[NSString stringWithUTF8String:reason]];
		return (void *)[a retain];
	}
}

static void magpie_nap_on(void *a) {
	@autoreleasepool {
		[[NSProcessInfo processInfo] endActivity:(id)a];
		[(id)a release];
	}
}
*/
import "C"

import (
	"sync"
	"unsafe"
)

// napOff begins the activity; the release it returns ends it, once.
func napOff(reason string) func() {
	r := C.CString(reason)
	defer C.free(unsafe.Pointer(r))
	a := C.magpie_nap_off(r)
	var once sync.Once
	return func() { once.Do(func() { C.magpie_nap_on(a) }) }
}

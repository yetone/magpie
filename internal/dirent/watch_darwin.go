//go:build darwin && cgo

package dirent

/*
#cgo LDFLAGS: -framework CoreServices
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdlib.h>

extern void direntHeard(size_t n, char **paths, FSEventStreamEventFlags *flags);

static void direntCallback(ConstFSEventStreamRef s, void *info, size_t n, void *paths,
	const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
	direntHeard(n, (char **)paths, (FSEventStreamEventFlags *)flags);
}

static dispatch_queue_t direntQueue;
static FSEventStreamRef direntStream;

static int direntStart(char **paths, int n) {
	if (direntStream != NULL) {
		FSEventStreamStop(direntStream);
		FSEventStreamInvalidate(direntStream);
		FSEventStreamRelease(direntStream);
		direntStream = NULL;
	}
	if (direntQueue == NULL) {
		direntQueue = dispatch_queue_create("magpie.dirent", DISPATCH_QUEUE_SERIAL);
	}
	CFMutableArrayRef list = CFArrayCreateMutable(NULL, n, &kCFTypeArrayCallBacks);
	for (int i = 0; i < n; i++) {
		CFStringRef p = CFStringCreateWithCString(NULL, paths[i], kCFStringEncodingUTF8);
		if (p == NULL) {
			CFRelease(list);
			return 0;
		}
		CFArrayAppendValue(list, p);
		CFRelease(p);
	}
	FSEventStreamRef s = FSEventStreamCreate(NULL, direntCallback, NULL, list,
		kFSEventStreamEventIdSinceNow, 0.01,
		kFSEventStreamCreateFlagNoDefer | kFSEventStreamCreateFlagWatchRoot);
	CFRelease(list);
	if (s == NULL) {
		return 0;
	}
	FSEventStreamSetDispatchQueue(s, direntQueue);
	if (!FSEventStreamStart(s)) {
		FSEventStreamInvalidate(s);
		FSEventStreamRelease(s);
		return 0;
	}
	direntStream = s;
	return 1;
}
*/
import "C"

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// FSEvents' flags that say a folder's word can't be taken alone: events
// were dropped or merged, a root moved, a volume came or went.
const heardAll = C.kFSEventStreamEventFlagMustScanSubDirs |
	C.kFSEventStreamEventFlagUserDropped |
	C.kFSEventStreamEventFlagKernelDropped |
	C.kFSEventStreamEventFlagEventIdsWrapped |
	C.kFSEventStreamEventFlagRootChanged |
	C.kFSEventStreamEventFlagMount |
	C.kFSEventStreamEventFlagUnmount

// startWatch has FSEvents tell heard of every folder under paths in which
// something changed: an entry made, renamed or removed, or a file written.
// It replaces the watch before it. watched is not held: stopping a
// stream waits for its callback, which takes it.
func startWatch(paths []string) bool {
	cs := make([]*C.char, len(paths))
	for i, p := range paths {
		cs[i] = C.CString(p)
	}
	defer func() {
		for _, c := range cs {
			C.free(unsafe.Pointer(c))
		}
	}()
	return C.direntStart((**C.char)(unsafe.Pointer(&cs[0])), C.int(len(cs))) == 1
}

// devOf is the volume fi is on.
func devOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

//export direntHeard
func direntHeard(n C.size_t, paths **C.char, flags *C.FSEventStreamEventFlags) {
	ps := unsafe.Slice(paths, int(n))
	fs := unsafe.Slice(flags, int(n))
	dirs := make([]string, 0, len(ps))
	all := false
	for i, p := range ps {
		if fs[i]&heardAll != 0 {
			all = true
		}
		dirs = append(dirs, strings.TrimSuffix(C.GoString(p), "/"))
	}
	heard(dirs, all)
}

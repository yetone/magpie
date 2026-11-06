//go:build darwin && cgo

package gui

import (
	"runtime"
	"testing"
)

// TestDockFrameLeavesNothing: a frame of the Dock tile's flap is made on a
// Go thread, which has no autorelease pool, and its megabyte of pixels was
// kept for good, 29 frames a flap (#4162: 1,074 of them, 1.1 GB, after a
// day). Each one made and let go here leaves nothing behind.
func TestDockFrameLeavesNothing(t *testing.T) {
	done := make(chan struct{})
	var freed, ok bool
	go func() {
		defer close(done)
		// a thread of its own, as a goroutine of dockFlap's runs on
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		for range 3 {
			if freed, ok = dockFrameFreed(); !ok || !freed {
				return
			}
		}
	}()
	<-done
	if !ok {
		t.Skip("no frame to watch on this system")
	}
	if !freed {
		t.Fatal("a Dock frame's image outlived its release: its pixels are kept until the thread ends")
	}
}

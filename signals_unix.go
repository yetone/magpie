//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// raise sends s to magpie again with its handling put back to the default,
// which ends the process.
func raise(s os.Signal) {
	n, ok := s.(syscall.Signal)
	if !ok {
		return
	}
	signal.Reset(s)
	if syscall.Kill(os.Getpid(), n) == nil {
		time.Sleep(time.Second) // it is on its way
	}
}

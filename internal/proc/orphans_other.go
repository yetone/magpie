//go:build !linux

package proc

import "context"

// KeepReaping does nothing off Linux: only there are the orphans of a
// container's PID 1 left to it.
func KeepReaping(context.Context) {}

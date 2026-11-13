//go:build !darwin || !cgo

package awake

// napOff does nothing: App Nap is a Mac's, and a Mac build without cgo is
// the command-line magpie, which runs under launchd or a terminal, where
// App Nap doesn't reach it.
func napOff(string) func() { return func() {} }

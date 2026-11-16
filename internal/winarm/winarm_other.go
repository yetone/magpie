//go:build !windows

package winarm

// Emulated says this process runs under Windows on Arm's x64 emulation:
// never off Windows.
func Emulated() bool { return false }

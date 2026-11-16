// Package winarm says whether magpie runs under Windows on Arm's x64
// emulation: an x64 build on an Arm64 computer. Such a magpie works, but
// its own heavy code (the local masking model) runs translated, and every
// update fetched the x64 build again (@EV_John_Liu on X: "magpie是转译的").
// It is Rosetta's counterpart (internal/rosetta), kept apart because what
// magpie tells the user differs.
package winarm

import "debug/pe"

// emulated says a process of goarch runs emulated on a computer whose
// native machine is native, as IsWow64Process2 said, or failed to (err).
// A failed call (Windows before 10 1511, which has no IsWow64Process2) is
// not translated, and so is any build but x64: an x86 magpie isn't built.
func emulated(goarch string, native uint16, err error) bool {
	return err == nil && goarch == "amd64" && native == pe.IMAGE_FILE_MACHINE_ARM64
}

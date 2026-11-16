package pii

import (
	"runtime"

	"golang.org/x/sys/cpu"
)

// osArch is the system magpie was built for.
var osArch = runtime.GOOS + "-" + runtime.GOARCH

// target is this system's name in serveSHA: the one magpie was built
// for, but Apple silicon's where an Intel magpie runs under Rosetta.
// Rosetta before macOS 15 has no AVX, so the Intel pf-serve ended with
// SIGILL on its first instruction of it, on Metal and on the CPU alike
// (echo_yip on X); Apple silicon's runs there natively, on Metal.
//
// On Windows on Arm it is the other way round: pf-serve has no arm64
// build, and Windows 11 runs the x64 one emulated, as it ran the whole of
// an x64 magpie there. The arm64 magpie an x64 one updates to (update's
// arch) finds the x64 pf-serve already on disk and goes on with it; one
// that found no pf-serve for windows-arm64 left the model "not
// downloaded" while it was on in the settings.
func target() string {
	if osArch == "darwin-amd64" && translated() {
		return "darwin-arm64"
	}
	if osArch == "windows-arm64" {
		return "windows-amd64"
	}
	return osArch
}

// onArmWindows says pf-serve, the x64 one, runs emulated on Windows on
// Arm, whose emulation may lack AVX2 (before Windows 11 24H2): fullX86
// can't ask an x64 CPU there, so pf-serve is tried and then
// pf-serve-base.
func onArmWindows() bool { return osArch == "windows-arm64" }

// fullX86 says the CPU has what pf-serve is built for on x86 (build.sh's
// x86): AVX, AVX2, FMA and BMI2, with F16C, which every CPU with AVX2
// has. Without them pf-serve ends with SIGILL (a Celeron, a Pentium, a
// Mac before Haswell), and pf-serve-base, built for SSE4.2 alone, is run
// instead. Off x86 it is true: no other system has a pf-serve-base.
var fullX86 = func() bool {
	if runtime.GOARCH != "amd64" {
		return true
	}
	return cpu.X86.HasAVX && cpu.X86.HasAVX2 && cpu.X86.HasFMA && cpu.X86.HasBMI2
}

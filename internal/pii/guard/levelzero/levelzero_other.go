//go:build !windows || (!amd64 && !arm64)

package levelzero

import "errors"

// Info describes the NPU driver and graph compiler on this computer.
type Info struct {
	Device        string
	DeviceID      uint32
	DriverVersion uint32
	CompilerMajor uint16
	CompilerMinor uint16
	MaxOpset      uint32
}

// Model is a compiled NPU graph opened on Level Zero.
type Model struct {
	Device string
}

// Probe reports that no Level Zero NPU is used on this build.
func Probe() (Info, bool, error) { return Info{}, false, nil }

// Compile is unavailable where Level Zero isn't used.
func Compile([]byte) ([]byte, error) { return nil, errors.New("no Intel NPU here") }

// Open is unavailable where Level Zero isn't used.
func Open(string, int, int, int) (*Model, error) { return nil, errors.New("no Intel NPU here") }

// Run is unavailable where Level Zero isn't used.
func (*Model) Run([]float32, []float32, int, int, int) ([]float32, error) {
	return nil, errors.New("no Intel NPU here")
}

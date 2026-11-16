package pii

import (
	"github.com/yetone/magpie/internal/rosetta"
	"github.com/yetone/magpie/internal/winarm"
)

// translated says magpie runs under Rosetta: an Intel build on Apple
// silicon. Tests replace it.
var translated = rosetta.Translated

// emulated says magpie runs under Windows on Arm's x64 emulation: an x64
// build on an Arm64 computer. Tests replace it.
var emulated = winarm.Emulated

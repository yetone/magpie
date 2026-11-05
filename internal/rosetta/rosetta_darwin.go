// Package rosetta says whether magpie runs under Rosetta: an Intel build
// on Apple silicon. Such a magpie works, but runs its own heavy code (the
// local masking model) several times slower than Apple silicon's build,
// with no CoreML, and fetches Intel builds of what it downloads unless it
// asks this.
package rosetta

import (
	"sync"

	"golang.org/x/sys/unix"
)

// Translated says this process runs under Rosetta. A failed read (an
// Intel Mac, where the sysctl doesn't exist) is native.
var Translated = sync.OnceValue(func() bool {
	v, err := unix.SysctlUint32("sysctl.proc_translated")
	return err == nil && v == 1
})

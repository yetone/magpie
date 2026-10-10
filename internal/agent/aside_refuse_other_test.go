//go:build !windows

package agent

import "testing"

// refuseWritesWindows is the answer only Windows has: no other platform
// refuses a write with an attribute, and refuseWrites has already used the
// file's permission bits there.
func refuseWritesWindows(t *testing.T, path string) {
	t.Helper()
	t.Skip("only Windows marks a file read-only")
}

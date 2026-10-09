//go:build !darwin && !linux && !windows

package usage

import (
	"os"
	"time"
)

// FileInfo exposes no portable change time here. Validate the fingerprint
// instead, so a same-size rewrite with restored mtime cannot return stale rows.
func nativeLogChangeTime(os.FileInfo) time.Time { return time.Time{} }

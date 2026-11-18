//go:build !windows

package agent

import (
	"io"
	"net"
	"path/filepath"
	"time"
)

// dimDial connects to the socket of Dim's daemon, <home>/rust/daemon.sock
// (dim-cli's connect_inner).
func dimDial(dir string) (io.ReadWriteCloser, error) {
	return net.DialTimeout("unix", filepath.Join(dir, "rust", "daemon.sock"), 5*time.Second)
}

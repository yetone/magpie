//go:build !windows

package proc

import "os/exec"

// probeGroup has the probe lead a session, and its context ending kill the
// session's group: the script at its head and what that started.
func probeGroup(cmd *exec.Cmd, p *probe) {
	ownGroup(cmd)
	cmd.Cancel = func() error {
		defer p.end()
		var g group
		g.kill(cmd)
		return cmd.Process.Kill()
	}
}

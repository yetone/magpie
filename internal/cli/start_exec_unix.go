//go:build !windows

package cli

import "syscall"

// startExecOS runs the agent at path as name in magpie's place: the
// terminal's signals and exit code are the agent's own.
func startExecOS(path, name string, args, env []string) error {
	return syscall.Exec(path, append([]string{name}, args...), env)
}

//go:build !darwin && !windows

package uilang

import "os"

// System is the system's language, as the environment names it.
func System() string { return Env(os.Getenv) }

//go:build !darwin && !windows && !linux

package ccswitchlink

func forward(string) error { return ErrNotInstalled }

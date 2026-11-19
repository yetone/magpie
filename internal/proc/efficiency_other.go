//go:build !windows

package proc

func powerNow() PowerState { return PowerState{} }

func setEfficiencyMode(bool) error { return nil }

func clearProcessEfficiencyMode(int) {}

//go:build !windows

package proc

// registeredDistros can't be told off Windows: there is no WSL here, and
// a test's wsl.exe is its own.
func registeredDistros() ([]string, bool) { return nil, false }

func wslPackageVersion() string { return "" }

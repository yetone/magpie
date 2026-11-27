//go:build !windows

package agent

func installedWindowsStorePackages() []string { return nil }

func installedWindowsStorePackageRoot(string) string { return "" }

func registeredWindowsApp(string) (dir, version string) { return "", "" }

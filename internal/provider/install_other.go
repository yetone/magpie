//go:build !windows

package provider

// refreshPath has nothing to do: magpie looks for the CLIs where their
// installers put them.
func refreshPath() {}

// registryPath is Windows' alone: elsewhere proc.UserPath gives the app a
// terminal's PATH.
func registryPath() []string { return nil }

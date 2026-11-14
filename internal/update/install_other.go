//go:build !windows

package update

// Only the Windows app installs itself; elsewhere there is nothing to do.
func installPath() string          { return "" }
func isDownloadFolder(string) bool { return false }
func exeVersion(string) uint64     { return 0 }
func webViewDefault(string) string { return "" }
func readInstallDir() string       { return "" }
func writeInstallDir(string) error { return nil }
func readDeclined() string         { return "" }
func writeDeclined(string) error   { return nil }

// ForgetInstallDir does nothing: only Windows records where it installed.
func ForgetInstallDir() error { return nil }

// hide does nothing: a name starting with a dot is hidden already.
func hide(string) {}

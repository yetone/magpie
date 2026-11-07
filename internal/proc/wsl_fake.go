package proc

// FakeWSLRegistry, for tests, has the registry read as listing names for
// the WSL distros registered (ok false: it can't be read) and pkg as WSL's
// package version; the returned func puts the real reads back.
func FakeWSLRegistry(names []string, ok bool, pkg string) func() {
	reg, p := wslRegistered, wslPackage
	wslRegistered = func() ([]string, bool) { return names, ok }
	wslPackage = func() string { return pkg }
	return func() { wslRegistered, wslPackage = reg, p }
}

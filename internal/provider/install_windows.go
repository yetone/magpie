package provider

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// refreshPath takes up the PATH an installer just wrote to the registry,
// which this process, started before it, doesn't have.
func refreshPath() {
	seen := map[string]bool{}
	var path []string
	for _, d := range append(strings.Split(os.Getenv("PATH"), ";"), registryPath()...) {
		if l := strings.ToLower(d); d != "" && !seen[l] {
			seen[l] = true
			path = append(path, d)
		}
	}
	os.Setenv("PATH", strings.Join(path, ";"))
}

// registryPath is the PATH the registry has now, the machine's then the
// user's: what a terminal opened now gets, and what a magpie started
// before a CLI was installed doesn't.
func registryPath() []string {
	var dirs []string
	for _, k := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
		{registry.CURRENT_USER, `Environment`},
	} {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if v, _, err := key.GetStringValue("Path"); err == nil {
			// %SystemRoot%\system32 as Windows expands it: os.ExpandEnv of
			// $SystemRoot$\system32 made C:\Windows$\system32
			if x, err := registry.ExpandString(v); err == nil {
				v = x
			}
			for _, d := range strings.Split(v, ";") {
				if d = strings.TrimSpace(d); d != "" {
					dirs = append(dirs, d)
				}
			}
		}
		key.Close()
	}
	return dirs
}

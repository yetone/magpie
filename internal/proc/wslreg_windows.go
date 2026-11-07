package proc

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const lxss = `Software\Microsoft\Windows\CurrentVersion\Lxss`

// registeredDistros reads HKCU's Lxss key, where WSL registers each of the
// user's distros as a key with its DistributionName. No key at all is no
// distro; any other failure, or a distro's key that can't be read, is
// unknown.
func registeredDistros() ([]string, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, lxss, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	defer k.Close()
	ids, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, false
	}
	var names []string
	for _, id := range ids {
		d, err := registry.OpenKey(k, id, registry.QUERY_VALUE)
		if err != nil {
			return nil, false
		}
		n, _, err := d.GetStringValue("DistributionName")
		d.Close()
		switch {
		case errors.Is(err, registry.ErrNotExist):
			// a key that isn't a distro's (WSL keeps others here, e.g. MSI
			// on some versions)
		case err != nil:
			return nil, false
		case n != "":
			names = append(names, n)
		}
	}
	return names, true
}

// wslPackageVersion is the WSL package's version, as its installer
// registered it.
func wslPackageVersion() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss\MSI`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("Version")
	return v
}

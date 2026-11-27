//go:build windows

package agent

import "golang.org/x/sys/windows/registry"

const appxPackagesKey = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppModel\Repository\Packages`

// installedWindowsStorePackages returns package full names registered for the
// current user. Reading this key needs no access to the protected WindowsApps
// folder and does not start PowerShell.
func installedWindowsStorePackages() []string {
	key, err := registry.OpenKey(registry.CURRENT_USER, appxPackagesKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer key.Close()
	names, _ := key.ReadSubKeyNames(-1)
	return names
}

func installedWindowsStorePackageRoot(pkg string) string {
	return readRegistry("HKCU", appxPackagesKey+`\`+pkg, "PackageRootFolder")
}

// registeredWindowsApp reads what an electron-builder installer
// registered for the app with guid (registeredIn), for the current user
// or for all users, in the 64-bit view and then the 32-bit one.
func registeredWindowsApp(guid string) (dir, version string) {
	return registeredIn(guid, readRegistry)
}

func readRegistry(hive, path, name string) string {
	root := registry.CURRENT_USER
	if hive == "HKLM" {
		root = registry.LOCAL_MACHINE
	}
	for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
		k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|view)
		if err != nil {
			continue
		}
		v, _, _ := k.GetStringValue(name)
		k.Close()
		if v != "" {
			return v
		}
	}
	return ""
}

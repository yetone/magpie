package gui

import "golang.org/x/sys/windows/registry"

func readHourCycle() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Control Panel\International`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	f, _, err := k.GetStringValue("sTimeFormat")
	if err != nil {
		return ""
	}
	return windowsHourCycle(f)
}

// readLocale is the regional format (Control Panel → International,
// LocaleName), which is not the display language WebView2 reports.
func readLocale() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Control Panel\International`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	n, _, err := k.GetStringValue("LocaleName")
	if err != nil {
		return ""
	}
	return localeTag(n)
}

package gui

import "github.com/yetone/magpie/internal/proc"

func readHourCycle() string {
	read := func(key string) string {
		out, err := proc.Command("defaults", "read", "-g", key).Output()
		if err != nil {
			return ""
		}
		return string(out)
	}
	return forcedHourCycle(read("AppleICUForce24HourTime"), read("AppleICUForce12HourTime"))
}

// readLocale is AppleLocale, the language and region the user picked in
// System Settings (en_JP), which is what macOS itself formats dates with.
func readLocale() string {
	out, err := proc.Command("defaults", "read", "-g", "AppleLocale").Output()
	if err != nil {
		return ""
	}
	return localeTag(string(out))
}

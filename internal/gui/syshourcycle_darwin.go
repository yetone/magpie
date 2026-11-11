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

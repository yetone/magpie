//go:build !windows

package pii

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// privateBytes is the memory pid holds of its own, not a file's pages:
// Linux's RssAnon, macOS's physical footprint.
func privateBytes(pid int) (int64, bool) {
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return 0, false
		}
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) == 3 && f[0] == "RssAnon:" {
				kb, err := strconv.ParseInt(f[1], 10, 64)
				return kb << 10, err == nil
			}
		}
	case "darwin":
		out, err := exec.Command("vmmap", "-summary", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0, false
		}
		m := regexp.MustCompile(`(?m)^Physical footprint:\s+([\d.]+)([KMG])`).FindSubmatch(out)
		if m == nil {
			return 0, false
		}
		v, err := strconv.ParseFloat(string(m[1]), 64)
		shift := map[string]uint{"K": 10, "M": 20, "G": 30}[string(m[2])]
		return int64(v * float64(int64(1)<<shift)), err == nil
	}
	return 0, false
}

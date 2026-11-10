package provider

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

// claudeSemverRE finds a version (1.2.3) in a CLI's --version, and
// compareClaudeVersion orders two of them.
var claudeSemverRE = lazyre.New(`\d+\.\d+\.\d+`)

func compareClaudeVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var av, bv int
		if i < len(pa) {
			av, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			bv, _ = strconv.Atoi(pb[i])
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// randomUUID is a random (version 4) UUID.
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

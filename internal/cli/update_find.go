package cli

import (
	"net"
	"os"
	"strconv"

	"github.com/yetone/magpie/internal/daemon"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// updateBase is where `magpie update` asks the running magpie to restart.
// A magpie from before gateway.json never wrote where it listens (ljlwlcz
// on X: a service with MAGPIE_ADDR=0.0.0.0:10100 was found nowhere by the
// first update, which said "isn't running" and left it on the old version
// until a cold start). So when the record is missing and nothing answers at
// the address the command would use, it looks where such a magpie runs:
// the address the installed service was written with, the port in Settings,
// the default port. The first that answers as magpie wins. --addr and
// MAGPIE_ADDR are the user's word and are never second-guessed.
func updateBase(explicit string) string {
	if explicit != "" {
		return explicit
	}
	base := gateway.ClientURL()
	if _, ok := gateway.ReadListening(); ok || envAddr() {
		return base
	}
	if updateProbe(base) {
		return base
	}
	for _, u := range updateCandidates() {
		if u != base && updateProbe(u) {
			return u
		}
	}
	return base
}

func envAddr() bool { return os.Getenv("MAGPIE_ADDR") != "" }

// updateProbe says whether a magpie answers at u; a var for the tests.
var updateProbe = func(u string) bool { return gateway.ServedAt(u).Running }

// updateCandidates are the places a magpie without a record may run; a var
// for the tests.
var updateCandidates = func() []string {
	var out []string
	if a := daemon.Addr(); a != "" {
		if h, p, err := net.SplitHostPort(a); err == nil {
			if ip := net.ParseIP(h); h == "" || ip != nil && ip.IsUnspecified() {
				h = "127.0.0.1"
			}
			out = append(out, "http://"+net.JoinHostPort(h, p))
		}
	}
	out = append(out, gateway.SavedURL(), "http://127.0.0.1:"+strconv.Itoa(settings.DefaultPort))
	return out
}

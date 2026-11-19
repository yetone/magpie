package cli

import (
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// A SIGTERM to `magpie serve` is how Kubernetes, Docker and a service
// manager end it, a rolling update's old pod included (@justforlxz on X:
// "直接重启更新会中断请求"). Before, the gateway's end gave the requests
// in flight 2s and cut the rest, a stream an agent was reading too. Now a
// SIGTERM drains: GET / answers 503 at once (so `magpie healthcheck`, the
// readiness probe, fails), the gateway keeps answering for a moment where
// Kubernetes is still sending it requests, then stops accepting and gives
// what is in flight the rest of MAGPIE_DRAIN to finish. Ctrl+C (SIGINT)
// and SIGHUP still end it at once.

// defaultDrain is MAGPIE_DRAIN unset in a Kubernetes pod: within its
// default 30s terminationGracePeriodSeconds, with room for the session
// index and the plugin hosts after it. Elsewhere it is plainDrain:
// `docker stop` and Compose kill after 10s, and a drain past that would
// lose the state magpie saves as it ends.
const (
	defaultDrain = 20 * time.Second
	plainDrain   = 7 * time.Second
)

// unsetDrain is the drain with MAGPIE_DRAIN unset (defaultDrain).
func unsetDrain() time.Duration {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return defaultDrain
	}
	return plainDrain
}

// kubeLinger is how long a gateway in a Kubernetes pod keeps accepting
// after a SIGTERM: the pod leaves its Service's endpoints as the SIGTERM
// is sent, and kube-proxy and an ingress take a few seconds to stop
// sending it new connections, which a closed port would refuse. A var
// so tests can shorten it.
var kubeLinger = 5 * time.Second

// lingerSleep is time.Sleep; a var so tests can see the linger.
var lingerSleep = time.Sleep

// drainOnSignal readies g's end for sig: a SIGTERM drains (gateway.Drain),
// any other signal ends it as before. g is nil for a `magpie web` whose
// gateway another magpie serves: nothing here to drain.
func drainOnSignal(g *gateway.Server, sig os.Signal) {
	if g == nil || sig != syscall.SIGTERM {
		return
	}
	grace := drainGrace()
	if grace <= 0 {
		return
	}
	var linger time.Duration
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		linger = min(kubeLinger, grace/2)
	}
	g.Drain(grace - linger)
	log.Printf("magpie: SIGTERM, finishing the requests in flight (up to %s; MAGPIE_DRAIN sets it)", grace)
	if linger > 0 {
		lingerSleep(linger)
	}
}

// drainGrace is MAGPIE_DRAIN: seconds ("30") or a duration ("90s",
// "2m"), unsetDrain when unset or unreadable, and "0" for the old 2s
// end.
func drainGrace() time.Duration {
	v := strings.TrimSpace(os.Getenv("MAGPIE_DRAIN"))
	if v == "" {
		return unsetDrain()
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return d
	}
	log.Printf("magpie: MAGPIE_DRAIN=%q is neither seconds nor a duration (30, 90s); draining for %s", v, unsetDrain())
	return unsetDrain()
}

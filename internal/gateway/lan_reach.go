package gateway

// A Remote magpie on a network this computer isn't on now (AlanWang1027713
// on X: two Wi-Fi networks, 192.168.51.x and 192.168.31.x, a magpie on a
// computer in each, both in one routing group): on A's network the group
// still sent requests to B's magpie. Each such try waited for a connection
// that never came (lanDial, 5s), then for a look around for where B had
// moved (provider.SeekMoved), before A's magpie was asked; B rested a minute
// for it, longer each time, and was asked again when the rest ended. Back
// on B's network, the same went the other way, with B maybe still resting.
//
// So a Remote magpie at an address on a private network (a private or
// link-local IP, a .local name) is dialled first, briefly, once for each
// network this computer is on (netPrint, its interfaces' addresses), and
// a group passes over one that took no connection while another member is
// left to ask: no wait, no rest, no failure counted. The verdict is checked
// again behind the requests every reachFresh, at once when the networks
// change, and is set by each real try too (one that answered, one whose
// connection failed). The members' order is the routing's as before; one
// that can't be reached is only passed over, and the last one left is
// always asked, so a request that reaches no one gets the real error.
// A cloud provider, a public address or a name, a Remote magpie through
// its own proxy, and one on this computer are never judged here.

import (
	"context"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// reachWait is how long the dial that tells whether a Remote magpie is
// reachable is waited for: one on the same network takes it within
// milliseconds, one through a router or a VPN within a few hundred.
var reachWait = 1500 * time.Millisecond

// reachFresh is how long a verdict stands before it is checked again,
// behind the requests that go by it meanwhile.
var reachFresh = 30 * time.Second

// printKeep is how long the networks this computer is on are kept read.
const printKeep = time.Second

// unreachableSaid is what a member passed over is told in the request's log.
const unreachableSaid = "not reachable from the network this computer is on now"

// lanReachHost says host is on a private network: an address another
// network can't reach. Loopback isn't (it is this computer), nor a public
// address or a name other than .local; swapped in tests, whose magpies
// are on loopback.
var lanReachHost = func(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && !ip.IsLoopback() && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// reachDial dials host:port once; swapped in tests.
var reachDial = func(ctx context.Context, hostport string) error {
	c, err := (&net.Dialer{Timeout: reachWait}).DialContext(ctx, "tcp", hostport)
	if err == nil {
		c.Close()
	}
	return err
}

// localNets is the networks this computer has an address on now, beyond
// loopback; swapped in tests.
var localNets = func() []*net.IPNet {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
				out = append(out, n)
			}
		}
	}
	return out
}

var printSeen = struct {
	sync.Mutex
	at    time.Time
	print string
	nets  []*net.IPNet
}{}

// netPrint names the networks this computer is on now: a verdict holds
// for one print, and a new one asks again.
func netPrint() (string, []*net.IPNet) {
	printSeen.Lock()
	defer printSeen.Unlock()
	if !printSeen.at.IsZero() && time.Since(printSeen.at) < printKeep {
		return printSeen.print, printSeen.nets
	}
	nets := localNets()
	names := make([]string, 0, len(nets))
	for _, n := range nets {
		names = append(names, n.String())
	}
	slices.Sort(names)
	printSeen.at, printSeen.print, printSeen.nets = time.Now(), strings.Join(names, ","), nets
	return printSeen.print, nets
}

type reachSeen struct {
	ok   bool
	at   time.Time     // when it was known; zero while the first dial is out
	busy chan struct{} // closed when the dial out now ends
}

var reachSeenOf = struct {
	sync.Mutex
	m map[string]*reachSeen // host:port and the print it was judged on
}{m: map[string]*reachSeen{}}

// reachAddr is the host:port the Remote magpie p is reached at, when it
// is one judged here, else "".
func reachAddr(p provider.Provider) string {
	if !p.IsRemoteMagpie() || p.Proxy != "" || len(p.Resolve) > 0 {
		return ""
	}
	hosts := hostsOf(p)
	if len(hosts) == 0 {
		return ""
	}
	host, _, _ := net.SplitHostPort(hosts[0])
	if !lanReachHost(host) {
		return ""
	}
	return hosts[0]
}

// lanUnreachable says the Remote magpie p took no connection on the networks
// this computer is on now. The first ask on a network dials it, briefly;
// after that the verdict is answered at once, and checked again behind
// the request once it is reachFresh old.
func lanUnreachable(p provider.Provider) bool {
	at := reachAddr(p)
	if at == "" {
		return false
	}
	print, nets := netPrint()
	key := at + " " + print
	reachSeenOf.Lock()
	s, ok := reachSeenOf.m[key]
	if !ok {
		if len(reachSeenOf.m) >= 256 {
			clear(reachSeenOf.m)
		}
		s = &reachSeen{busy: make(chan struct{})}
		reachSeenOf.m[key] = s
		reachSeenOf.Unlock()
		probeReach(p, at, nets, s)
		reachSeenOf.Lock()
		off := !s.ok
		reachSeenOf.Unlock()
		return off
	}
	if !s.at.IsZero() {
		if s.busy == nil && time.Since(s.at) >= reachFresh {
			s.busy = make(chan struct{})
			go probeReach(p, at, nets, s)
		}
		off := !s.ok
		reachSeenOf.Unlock()
		return off
	}
	// the first dial on this network is out for another request
	wait := s.busy
	reachSeenOf.Unlock()
	<-wait
	reachSeenOf.Lock()
	off := !s.ok
	reachSeenOf.Unlock()
	return off
}

// probeReach dials at, notes on s whether it took the connection, and
// ends s's busy. One that didn't, at an address on a network this computer
// is on, may have moved there (a router handing its computer a new
// address): it is looked for, as a request to it would (provider.SeekMoved,
// once a minute at most), so that passing it over doesn't keep it lost.
func probeReach(p provider.Provider, at string, nets []*net.IPNet, s *reachSeen) {
	ctx, cancel := context.WithTimeout(context.Background(), reachWait+time.Second)
	err := reachDial(ctx, at)
	cancel()
	reachSeenOf.Lock()
	s.ok, s.at = err == nil, time.Now()
	done := s.busy
	s.busy = nil
	reachSeenOf.Unlock()
	if done != nil {
		close(done)
	}
	if err != nil && onNets(at, nets) {
		go provider.SeekMoved(context.Background(), p, err)
	}
}

// onNets says host:port's address is on one of nets.
func onNets(hostport string, nets []*net.IPNet) bool {
	host, _, _ := net.SplitHostPort(hostport)
	ip := net.ParseIP(host)
	return ip != nil && slices.ContainsFunc(nets, func(n *net.IPNet) bool { return n.Contains(ip) })
}

// noteReach sets the verdict on p from a real try: it answered, or its
// connection was never made.
func noteReach(p provider.Provider, ok bool) {
	at := reachAddr(p)
	if at == "" {
		return
	}
	print, _ := netPrint()
	key := at + " " + print
	reachSeenOf.Lock()
	defer reachSeenOf.Unlock()
	s, had := reachSeenOf.m[key]
	if !had {
		if len(reachSeenOf.m) >= 256 {
			clear(reachSeenOf.m)
		}
		s = &reachSeen{}
		reachSeenOf.m[key] = s
	}
	if s.at.IsZero() && s.busy != nil {
		return // the first dial, out now, says it
	}
	s.ok, s.at = ok, time.Now()
}

// lanUnreached says the failure c met, status and body, is a Remote
// magpie judged here taking no connection: what a network it isn't on
// leaves behind, which says nothing of it. It isn't rested for it nor
// its failure counted; the verdict passes it over while it holds.
func lanUnreached(c candidate, status int, body []byte) bool {
	if status != 502 || proxyDown.Match(body) || !connFailed.Match(body) || reachAddr(c.p) == "" {
		return false
	}
	noteReach(c.p, false)
	return true
}

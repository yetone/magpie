package gateway

import (
	"context"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
)

// A LazyCat (懒猫微服) app's container is on a bridge network of the box's
// own: its address (172.28.x.x) and the bridge's (the container's default
// route, 172.28.3.65) are reached from nothing outside the box. LazyCat
// serves the app at its domain (lazyCatURL), which only devices running
// LazyCat's client resolve, and publishes no container port on the box's
// LAN address. So "Share on local network" offered
// https://magpie.<box>.heiyu.space and http://172.28.3.65:3425, and neither
// was the http://192.168.0.100:3425 other devices on the home network use
// (mtoolstec on X, after v0.1.3432).
//
// A package whose service runs on the box's network (network_mode: host,
// as LazyCat's Emby, Lucky and LAN Sheriff do) has the box's interfaces,
// and the gateway listens on its LAN address itself: those addresses are
// offered first. On a bridge network they can't be known (LazyCat names
// the box's domain to a container, never its LAN address), and Settings
// says how the port gets there.

// LazyCatURL is a LazyCat app's own address, served to devices with
// LazyCat's client; "" outside LazyCat.
func LazyCatURL() string { return lazyCatURL() }

// LazyCatBox: magpie runs in a LazyCat app on the box's own network, so the
// addresses it finds are the box's, which other devices on its LAN reach.
func LazyCatBox() bool { return lazyCatURL() != "" && len(lazyCatBoxHosts()) > 0 }

// lazyCatBoxHosts are the box's LAN addresses while magpie runs on its
// network: the IPv4 addresses (as LANHosts takes them) of the interfaces
// the default route leaves by, which is the box's link to the router, not
// one of its container bridges or the virtual address other LazyCat apps
// reach it at (host.lzcapp). nil on a bridge network, where the default
// route is the container's own eth0.
func lazyCatBoxHosts() []string {
	if !lazyCatHostNet() {
		return nil
	}
	lzc := lzcHostIPs()
	var ips []net.IP
	for _, name := range defaultIfaces(routeTable()) {
		for _, ip := range ifaceIPs(name) {
			if !slices.ContainsFunc(lzc, ip.Equal) {
				ips = append(ips, ip)
			}
		}
	}
	return lanHostsOf(ips)
}

// lazyCatHostNet: the network magpie is on is the box's: the box runs its
// apps in containers, so its own network has their bridges and veth pairs
// (hostNetwork), and host.lzcapp, which LazyCat gives apps for the box, is
// one of this network's own addresses (on a bridge network it is the
// bridge's, another machine's). A lookup that fails is asked again a
// minute on, as LazyCat's DNS may not answer while the app starts.
func lazyCatHostNet() bool {
	if hostNetwork(ifaceNames()) {
		return true
	}
	own := hostIPs()
	return slices.ContainsFunc(lzcHostIPs(), func(ip net.IP) bool { return slices.ContainsFunc(own, ip.Equal) })
}

// lzcHostIPs are host.lzcapp's addresses; a variable for the tests.
var lzcHostIPs = func() []net.IP { return lzcLookup.get() }

// lzcLookup holds host.lzcapp's addresses: a found answer for the
// process's life (a box's network doesn't change under a running app), a
// failure for a minute.
var lzcLookup = &cachedLookup{name: "host.lzcapp", retry: time.Minute}

type cachedLookup struct {
	name  string
	retry time.Duration
	mu    sync.Mutex
	ips   []net.IP
	found bool
	at    time.Time
}

func (c *cachedLookup) get() []net.IP {
	if lazyCatURL() == "" {
		return nil
	}
	c.mu.Lock()
	if c.found || !c.at.IsZero() && time.Since(c.at) < c.retry {
		defer c.mu.Unlock()
		return c.ips
	}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, c.name)
	var ips []net.IP
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ips, c.found, c.at = ips, err == nil && len(ips) > 0, time.Now()
	return c.ips
}

// defaultIfaces are the interfaces of /proc/net/route's default routes
// (defaultGateway's), in the table's order.
func defaultIfaces(table string) []string {
	var out []string
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] != "00000000" || f[0] == "Iface" || slices.Contains(out, f[0]) {
			continue
		}
		out = append(out, f[0])
	}
	return out
}

// ifaceIPs are the addresses on the interface named, when it is up; a
// variable for the tests.
var ifaceIPs = func(name string) []net.IP {
	i, err := net.InterfaceByName(name)
	if err != nil || i.Flags&net.FlagUp == 0 {
		return nil
	}
	addrs, _ := i.Addrs()
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			out = append(out, n.IP)
		}
	}
	return out
}

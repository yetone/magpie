package gateway

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// clientOf is the other computer req came from, for the Routing page's
// agent nodes: its address, and its name when one has been learnt. Agents
// on two computers sharing magpie's gateway drew as one node, both
// "DeepSeek Harness", with nothing to say which computer asked
// (jzing.draw on Discord). A request made on this computer has neither.
// The name is never waited for: the first request from a computer shows
// its address, and the name once a lookup (clientNames) has found it.
func clientOf(req *http.Request) (addr, name string) {
	a, ok := peerAddr(req)
	if !ok || !a.IsValid() || a.IsLoopback() || a.IsUnspecified() || ownAddr(a) {
		return "", ""
	}
	return a.String(), clientNames.of(a)
}

// clientNames are the names of the computers on this network that called
// magpie, by address, each looked up once an hour at most (a miss once
// every ten minutes), in the background. Only an address no computer
// online could have (not PublicIP) is looked up: names are a LAN's.
var clientNames = &nameCache{look: lookClientName, ttl: time.Hour, missTTL: 10 * time.Minute, max: 256}

type nameCache struct {
	look         func(context.Context, netip.Addr) string
	ttl, missTTL time.Duration
	max          int

	mu sync.Mutex
	m  map[netip.Addr]*nameEntry
}

type nameEntry struct {
	name    string
	at      time.Time
	looking bool
}

// of is a's name as last found, "" when none is known yet; a lookup
// starts when a has none, or an old one.
func (c *nameCache) of(a netip.Addr) string {
	if PublicIP(net.IP(a.AsSlice())) {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[netip.Addr]*nameEntry{}
	}
	e := c.m[a]
	if e == nil {
		if len(c.m) >= c.max {
			clear(c.m)
		}
		e = &nameEntry{}
		c.m[a] = e
	}
	ttl := c.ttl
	if e.name == "" {
		ttl = c.missTTL
	}
	if !e.looking && (e.at.IsZero() || time.Since(e.at) > ttl) {
		e.looking = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			n := c.look(ctx, a)
			cancel()
			c.mu.Lock()
			e.name, e.at, e.looking = cleanHostName(n), time.Now(), false
			c.mu.Unlock()
		}()
	}
	return e.name
}

// cleanHostName is a looked-up name as a person calls the computer:
// "mote.local." → "mote", "desk-7.lan." → "desk-7.lan".
func cleanHostName(n string) string {
	n = strings.TrimSuffix(strings.TrimSpace(n), ".")
	if h, ok := strings.CutSuffix(n, ".local"); ok && h != "" {
		return h
	}
	return n
}

// lookClientName is a's name: what the network's DNS says (a router that
// names its clients, Tailscale's MagicDNS), else what the computer says
// of itself over multicast DNS (a Mac, Linux with Avahi), asked of it
// alone, on its own port 5353, rather than of the whole network.
func lookClientName(ctx context.Context, a netip.Addr) string {
	if ns, err := net.DefaultResolver.LookupAddr(ctx, a.String()); err == nil {
		for _, n := range ns {
			if n = cleanHostName(n); n != "" {
				return n
			}
		}
	}
	return mdnsName(ctx, a)
}

// mdnsName asks a, at a:5353, for the name of its own address (a PTR
// query as a one-shot unicast client sends it, RFC 6762 §5.1), and is ""
// when it doesn't answer in time.
func mdnsName(ctx context.Context, a netip.Addr) string {
	q, err := reverseName(a)
	if err != nil {
		return ""
	}
	var idb [2]byte
	rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id})
	if b.StartQuestions() != nil || b.Question(dnsmessage.Question{Name: q, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}) != nil {
		return ""
	}
	msg, err := b.Finish()
	if err != nil {
		return ""
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", netip.AddrPortFrom(a, 5353).String())
	if err != nil {
		return ""
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if _, err := conn.Write(msg); err != nil {
		return ""
	}
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return ""
		}
		if name := ptrAnswer(buf[:n], id, q); name != "" {
			return name
		}
	}
}

// ptrAnswer is the name a reply msg to query id gives q, "" when it
// gives none.
func ptrAnswer(msg []byte, id uint16, q dnsmessage.Name) string {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || !h.Response || h.ID != id {
		return ""
	}
	if p.SkipAllQuestions() != nil {
		return ""
	}
	for {
		ah, err := p.AnswerHeader()
		if err != nil {
			return ""
		}
		if ah.Type != dnsmessage.TypePTR || !strings.EqualFold(ah.Name.String(), q.String()) {
			if p.SkipAnswer() != nil {
				return ""
			}
			continue
		}
		r, err := p.PTRResource()
		if err != nil {
			return ""
		}
		return r.PTR.String()
	}
}

// reverseName is a's name under in-addr.arpa or ip6.arpa.
func reverseName(a netip.Addr) (dnsmessage.Name, error) {
	var s strings.Builder
	b := a.AsSlice()
	if a.Is4() {
		for i := len(b) - 1; i >= 0; i-- {
			s.WriteString(strconv.Itoa(int(b[i])) + ".")
		}
		s.WriteString("in-addr.arpa.")
	} else {
		const hex = "0123456789abcdef"
		for i := len(b) - 1; i >= 0; i-- {
			s.WriteByte(hex[b[i]&0xf])
			s.WriteByte('.')
			s.WriteByte(hex[b[i]>>4])
			s.WriteByte('.')
		}
		s.WriteString("ip6.arpa.")
	}
	return dnsmessage.NewName(s.String())
}

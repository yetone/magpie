package gui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/egress"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
)

// Settings › Proxy's Test (#2076, c4ex): whether the proxy in force is the
// way magpie's requests leave. One request to Cloudflare's trace page goes
// as a vendor request would (the proxy Settings, *_PROXY or the system
// names), and one goes direct; each says the address Cloudflare saw it
// from and the country, and the request through the proxy says whether
// its connection went to the proxy. Different addresses say the proxy is
// taking the requests. The same address with the connection made to the
// proxy says it took them too, but both leave by one exit: a TUN or
// system VPN can send even the direct request through the same proxy
// (#2369, Emt-lin), so the address can't tell the two paths apart. The
// same address with the connection made elsewhere says the proxy was
// passed by.

// proxyTraceURL is the page asked; a test points it at its own server.
var proxyTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

// proxyTraceTimeout is how long each request may take.
const proxyTraceTimeout = 10 * time.Second

// proxyLeg is one of the two requests: the address and country Cloudflare
// saw it from, how long it took, or why it failed. Proxied, on the request
// through the proxy, is whether the connection that carried it went to
// the proxy in force, which an exit address shared with the direct
// request can't say.
type proxyLeg struct {
	IP      string `json:"ip,omitempty"`
	Loc     string `json:"loc,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	Error   string `json:"error,omitempty"`
	Proxied bool   `json:"proxied,omitempty"`
}

// proxyCheck is the answer: the proxy in force (redacted) and where it came
// from, as Settings says it (netproxy.Describe), the request through it and
// the one without. Through is absent when no proxy is in force, since the
// request "through" it is the direct one.
type proxyCheck struct {
	Proxy   string    `json:"proxy,omitempty"`
	Source  string    `json:"source"`
	Through *proxyLeg `json:"through,omitempty"`
	Direct  proxyLeg  `json:"direct"`
}

// checkProxy asks the trace page through via (a context as a vendor
// request carries) and direct, at once. through is false when no proxy is
// in force: only the direct request is made.
func checkProxy(ctx context.Context, client *http.Client, via context.Context, through bool) proxyCheck {
	var out proxyCheck
	var wg sync.WaitGroup
	if through {
		out.Through = &proxyLeg{}
		wg.Add(1)
		go func() { defer wg.Done(); *out.Through = throughLeg(via, client) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); out.Direct = traceLeg(netproxy.With(ctx, "direct"), client) }()
	wg.Wait()
	return out
}

// throughLeg is traceLeg through the proxy ctx's requests take
// (netproxy.Func), with Proxied set when the answer came over a
// connection to that proxy. A proxy the trace page is passed by (a
// bypass list naming it) leaves Proxied false.
func throughLeg(ctx context.Context, client *http.Client) proxyLeg {
	var remote net.Addr
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		if i.Conn != nil {
			remote = i.Conn.RemoteAddr()
		}
	}}
	leg := traceLeg(httptrace.WithClientTrace(ctx, trace), client)
	if leg.Error != "" || remote == nil {
		return leg
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyTraceURL, nil)
	if err != nil {
		return leg
	}
	if p, err := netproxy.Func(req); err == nil && p != nil {
		leg.Proxied = connectedTo(ctx, remote, p)
	}
	return leg
}

// connectedTo is whether remote, a connection's far end, is the proxy p:
// its port, and its address or one its name resolves to.
func connectedTo(ctx context.Context, remote net.Addr, p *url.URL) bool {
	host, port, err := net.SplitHostPort(remote.String())
	if err != nil {
		return false
	}
	want := p.Port()
	if want == "" {
		want = map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[p.Scheme]
	}
	if port != want {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if pip := net.ParseIP(p.Hostname()); pip != nil {
		return pip.Equal(ip)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, p.Hostname())
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// traceLeg makes one request to the trace page and reads its ip= and loc=
// lines.
func traceLeg(ctx context.Context, client *http.Client) proxyLeg {
	ctx, cancel := context.WithTimeout(ctx, proxyTraceTimeout)
	defer cancel()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyTraceURL, nil)
	if err != nil {
		return proxyLeg{Error: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		// a proxy's password can be in the error's URL
		return proxyLeg{Error: redactProxyErr(err.Error())}
	}
	defer resp.Body.Close()
	var leg proxyLeg
	leg.Ms = time.Since(start).Milliseconds()
	if resp.StatusCode != http.StatusOK {
		leg.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return leg
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 16<<10))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			leg.IP = strings.TrimSpace(v)
		case "loc":
			leg.Loc = strings.TrimSpace(v)
		}
	}
	if leg.IP == "" {
		leg.Error = "no address in the answer"
	}
	return leg
}

// redactProxyErr takes a user:password@ out of an error's text.
func redactProxyErr(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if strings.Contains(w, "://") && strings.Contains(w, "@") {
			q := strings.Trim(w, `"':,`)
			if r := netproxy.Redact(q); r != "" {
				words[i] = strings.Replace(w, q, r, 1)
			} else {
				words[i] = "[proxy]"
			}
		}
	}
	return strings.Join(words, " ")
}

func handleProxyCheck(rw http.ResponseWriter, r *http.Request) {
	proxy, source := netproxy.Describe()
	through := source == "settings" || source == "environment" || source == "system"
	out := checkProxy(r.Context(), http.DefaultClient, r.Context(), through)
	out.Proxy, out.Source = proxy, source
	writeJSON(rw, out)
}

// handleEgressCheck looks the Claude accounts' way out up again now,
// through each of their proxies (the Providers page's Check again), and
// answers the worst of what it found.
func handleEgressCheck(rw http.ResponseWriter, r *http.Request) {
	cs := provider.ClaudeProxies()
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		go func() { defer wg.Done(); egress.Check(r.Context(), c) }()
	}
	wg.Wait()
	writeJSON(rw, egress.Worst(cs))
}

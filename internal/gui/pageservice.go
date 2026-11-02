package gui

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// A plugin's page can be the way into a local service its host part runs
// (package.json's magpie.page.service, plugin.ServiceSpec): the community
// chat page (@magpie-community/page-chat) sends its frame to the chat
// server on 127.0.0.1. In the desktop window that is this computer. In a
// browser on another computer, opening `magpie web` (aigcfree on Discord:
// a server behind nginx, a Windows PC's browser), 127.0.0.1 is that
// computer, and the frame showed "127.0.0.1 refused to connect".
//
// So a page served to a browser is told (/api/plugin-pages' service, the
// bridge's magpie.service) where the same service is on this server's own
// address: pageServicePath under the page's token. The service stays on
// 127.0.0.1, and reaching it takes what reaching the page's files takes:
// the token, which only a signed-in /api hands out (webGuard), and the
// allow list before it (gateway.AllowGuard).

// pageServicePath is where a page's service is, under the page's token:
// pluginPagePrefix + token + "/" + pageServicePath + the service's path.
const pageServicePath = "_magpie/service/"

// pageServicePolicy is added to everything the service sends: whatever
// it serves runs sandboxed, in an origin of its own, as the page's frame
// does, even opened by itself in a tab. On this server's own origin, the
// plugin's scripts would have the window's cookie and its /api.
const pageServicePolicy = "sandbox allow-scripts; frame-ancestors 'self'"

// serviceSelf is policy, a Content-Security-Policy the page's service
// sent, with every 'self' followed by self, the service's root under this
// server named with its scheme (ownSources). To the service, 'self' is
// all of 127.0.0.1:<port>, which is that root here. But what it sends runs
// sandboxed (pageServicePolicy), in an opaque origin, and WebKit doesn't
// match a fetch to 'self' there: the service's own page refused its every
// fetch of api/… (YangHonghang on #4198, `connect-src 'self'`), while
// Chromium let it through. connect-src gets ws:// and wss:// too, for a WebSocket,
// which 'self' covers and an http(s) source doesn't. 'self' stays, so
// nothing a browser allowed before is taken away. A policy in a <meta>
// tag of the service's page isn't seen here, and is the plugin's to write
// with whole sources.
func serviceSelf(policy string, self []string) string {
	ds := strings.Split(policy, ";")
	for i, d := range ds {
		f := strings.Fields(d)
		if len(f) < 2 {
			continue
		}
		add := append([]string(nil), self...)
		if strings.EqualFold(f[0], "connect-src") || strings.EqualFold(f[0], "default-src") {
			for _, s := range self {
				if rest, ok := strings.CutPrefix(s, "http"); ok {
					add = append(add, "ws"+rest)
				}
			}
		}
		out := []string{f[0]}
		named := false
		for _, src := range f[1:] {
			out = append(out, src)
			if strings.EqualFold(src, "'self'") && !named {
				out = append(out, add...)
				named = true
			}
		}
		if named {
			ds[i] = " " + strings.Join(out, " ")
			if i == 0 {
				ds[i] = ds[i][1:]
			}
		}
	}
	return strings.Join(ds, ";")
}

// pageServiceTransport dials the service on loopback, never through a
// proxy.
var pageServiceTransport = &http.Transport{
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	MaxIdleConns:          16,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 0, // a chat's first token can be slow
}

// servicePort is the port p's service is on, or 0 when it has none that
// can be served: the gateway's own port never is, since a request from
// this server comes from loopback, which the gateway takes without a key.
func (p *pluginPage) servicePort() int {
	if p == nil || p.service == nil {
		return 0
	}
	port := p.service.ServicePort(p.options)
	if strconv.Itoa(port) == gateway.Port() {
		return 0
	}
	return port
}

// servePageService passes r, a request under p's pageServicePath, to p's
// service on 127.0.0.1, as a request from this computer: Host is the
// service's own, which its DNS-rebinding check wants; the window's cookie
// is taken off; Origin goes as it came, "null" from the page's sandboxed
// frame. What comes back is sandboxed (pageServicePolicy), sets no cookie
// on this server, sends no Referer on (the address holds the token), and
// streams as it comes (an event stream, a WebSocket).
func servePageService(rw http.ResponseWriter, r *http.Request, p *pluginPage, tok string) {
	// a sandboxed page's requests carry Origin: null, and a navigation
	// none; another site's page has no business here
	if o, sent := r.Header["Origin"]; sent && (len(o) != 1 || o[0] != "null") {
		http.Error(rw, "this is a plugin page's service: only its page asks it", http.StatusForbidden)
		return
	}
	port := p.servicePort()
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	root := pluginPagePrefix + tok + "/" + pageServicePath
	raw := strings.TrimPrefix(r.URL.EscapedPath(), root)
	if raw == r.URL.EscapedPath() {
		raw = strings.TrimPrefix(r.URL.Path, root)
	}
	upPath, err := url.PathUnescape("/" + raw)
	if err != nil {
		http.Error(rw, "bad path", http.StatusBadRequest)
		return
	}
	// the page itself: its plugin's host, which runs the service, is up
	// first, as for the page's HTML (pageHost)
	if raw == "" && p.hosted {
		pageHost(r.Context())
	}
	// how deep the request is under the service's root, for a redirect
	// the service sends to one of its own paths
	up := strings.Repeat("../", strings.Count(raw, "/"))
	rp := &httputil.ReverseProxy{
		Transport: pageServiceTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = target
			pr.Out.URL.Path = upPath
			pr.Out.URL.RawPath = ""
			if "/"+raw != upPath {
				pr.Out.URL.RawPath = "/" + raw
			}
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = target
			pr.Out.Header.Del("Cookie")
		},
		FlushInterval: -1,
		ModifyResponse: func(res *http.Response) error {
			h := res.Header
			h.Del("Set-Cookie")
			// the service's own policy, its 'self' named for WebKit
			// (serviceSelf); then magpie's sandbox
			if v := h.Values("Content-Security-Policy"); len(v) > 0 {
				self := ownSources(r, root)
				out := make([]string, len(v))
				for i, policy := range v {
					out[i] = serviceSelf(policy, self)
				}
				h["Content-Security-Policy"] = out
			}
			h.Add("Content-Security-Policy", pageServicePolicy)
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Content-Type-Options", "nosniff")
			if strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "text/event-stream") {
				// nginx in front holds a response back until it ends
				// unless told not to
				h.Set("X-Accel-Buffering", "no")
			}
			if loc := h.Get("Location"); loc != "" {
				h.Set("Location", serviceLocation(loc, port, up))
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, r *http.Request, err error) {
			log.Printf("plugin page: %s's service on %s: %v", p.Name, target, err)
			h := rw.Header()
			h.Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'self'")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
			if raw == "" && r.Method == http.MethodGet {
				// the page's frame, sent here before the service is up
				// (a host still starting): it tries again by itself; a
				// sandboxed document refreshes only with allow-scripts
				h.Set("Content-Type", "text/html; charset=utf-8")
				rw.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(rw, `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="2"><style>body{font:13px/1.5 system-ui,sans-serif;display:grid;place-items:center;height:90vh;margin:0;color:GrayText}</style><p>The plugin's service isn't answering yet. Trying again…</p>`)
				return
			}
			http.Error(rw, "the plugin's service isn't answering", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(rw, r)
}

// serviceLocation is a redirect the service on port sent, made to go
// through this server: one to the service's own address, or to a path
// of its own, becomes that path under the service's root, reached from
// a request up levels below it; one to anywhere else is left alone.
func serviceLocation(loc string, port int, up string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	if u.Scheme != "" || u.Host != "" {
		own := u.Scheme == "http" && (u.Host == "127.0.0.1:"+strconv.Itoa(port) || u.Host == "localhost:"+strconv.Itoa(port))
		if !own {
			return loc
		}
	} else if !strings.HasPrefix(u.Path, "/") {
		return loc // already relative to the request
	}
	rel := up + strings.TrimPrefix(u.EscapedPath(), "/")
	if rel == "" {
		rel = "./"
	}
	if u.RawQuery != "" {
		rel += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		rel += "#" + u.EscapedFragment()
	}
	return rel
}

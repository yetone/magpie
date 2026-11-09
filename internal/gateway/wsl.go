package gateway

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/wslrun"
)

// A WSL distro in NAT networking (WSL's default) reaches Windows at the
// address Windows has on the WSL network (vEthernet (WSL), 172.17.32.1 for
// a distro at 172.17.38.240), not at 127.0.0.1. magpie wrote that address
// into the distro's agents, but the gateway listened on 127.0.0.1 alone, so
// Codex and the other CLIs there were refused or timed out until the user
// shared magpie on the local network (zaidu259434 on X: "启动codex这些cli连接
// 会失败，没自动启动magpie的网关"). Now, while the gateway is on loopback, it
// also listens on each address Windows has on a WSL network, and a request
// that arrives there from that network with the WSL key (WSLKey, which
// magpie writes into those agents only) is this computer's, as it is from a
// distro in mirrored networking, whose 127.0.0.1 is Windows'. Anything else
// arriving there is judged as a request from another machine: a container
// of Docker Desktop's, which shares that network, has no key.

// WSLNets are the networks Windows has an address on that WSL distros reach
// it through (wslrun.Nets): the vEthernet (WSL…) adapters', NAT's and a
// bridged switch's. None off Windows. A var for the tests.
var WSLNets = wslNets

func wslNets() []*net.IPNet { return wslrun.Nets() }

// OnWSLNet says base (an agent's gateway URL) names an address Windows has
// on a WSL network: where a distro under NAT reaches the gateway.
func OnWSLNet(base string) bool {
	h := base
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	if ip == nil {
		return false
	}
	return slices.ContainsFunc(WSLNets(), func(n *net.IPNet) bool { return n.IP.Equal(ip) })
}

// wslKeyPrefix starts the WSL key. Neither sk-magpie- (a named gateway
// key's, looked up among the keys) nor magpie- (the token with an agent's
// id after it, agentOf).
const wslKeyPrefix = "wsl-"

var wslKey struct {
	sync.Mutex
	v, at string // the key, and the file it was read from
}

func wslKeyPath() string { return filepath.Join(settings.Dir(), "wsl-key") }

// WSLKey is the key magpie gives the agents of a WSL distro that reaches
// the gateway on the WSL network (OnWSLNet): made once, kept in magpie's
// folder, readable by this user only. "" when it can't be read or made, and
// the agent is given the gateway's token as before.
func WSLKey() string {
	wslKey.Lock()
	defer wslKey.Unlock()
	path := wslKeyPath()
	if wslKey.v != "" && wslKey.at == path {
		return wslKey.v
	}
	b, err := os.ReadFile(path)
	if err == nil {
		if k := strings.TrimSpace(string(b)); strings.HasPrefix(k, wslKeyPrefix) && len(k) > len(wslKeyPrefix)+16 {
			wslKey.v, wslKey.at = k, path
			return k
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "" // unknown: never written over
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	k := wslKeyPrefix + hex.EncodeToString(buf)
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return ""
	}
	// made 0600 first, so the write keeps that mode
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return ""
	}
	if err := edit.WriteAtomic(path, []byte(k+"\n")); err != nil {
		return ""
	}
	wslKey.v, wslKey.at = k, path
	return k
}

// WSLKeyMade is the WSL key when one was made, "" when not: for telling
// whether a key is it without making one.
func WSLKeyMade() string { return wslKeyHeld() }

// wslKeyHeld is the WSL key as the gateway takes it: the one in magpie's
// folder, read again when the cache is empty (another magpie made it).
func wslKeyHeld() string {
	path := wslKeyPath()
	wslKey.Lock()
	v, at := wslKey.v, wslKey.at
	wslKey.Unlock()
	if v != "" && at == path {
		return v
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return WSLKey()
}

// wslToken is what v, a key a caller sent, stands for when it is the WSL
// key: the gateway's token, with the agent's id after it when the key has
// one (WSLKey()+"-codex" is Token+"-codex", TokenFor). "" when it isn't.
func wslToken(v, k string) string {
	v = strings.TrimSpace(v)
	if k == "" || len(v) < len(k) {
		return ""
	}
	head, rest := v[:len(k)], v[len(k):]
	if subtle.ConstantTimeCompare([]byte(head), []byte(k)) != 1 {
		return ""
	}
	if rest == "" {
		return Token
	}
	if id, ok := strings.CutPrefix(rest, "-"); ok && id != "" {
		return Token + "-" + id
	}
	return ""
}

// fromWSL: r came to an address Windows has on a WSL network, from that
// network, with the WSL key.
func fromWSL(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(peer)
	la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	tcp, ok := la.(*net.TCPAddr)
	if ip == nil || ip.IsLoopback() || !ok || tcp.IP.IsLoopback() || tcp.IP.IsUnspecified() {
		return false
	}
	if !slices.ContainsFunc(WSLNets(), func(n *net.IPNet) bool { return n.IP.Equal(tcp.IP) && n.Contains(ip) }) {
		return false
	}
	k := wslKeyHeld()
	if k == "" {
		return false
	}
	if v, _ := r.Context().Value(wslPathKeyCtx{}).(string); wslToken(v, k) != "" {
		return true
	}
	return slices.ContainsFunc(callerKeys(r), func(v string) bool { return wslToken(v, k) != "" })
}

// wslPathKeyCtx holds the WSL key a request's path began with
// (takeWSLPathKey).
type wslPathKeyCtx struct{}

// takeWSLPathKey is r without the WSL key its path begins with, which is
// kept for fromWSL: /wsl-…/v1/messages is /v1/messages. A client that
// can't send the key as its own is given it in the address: Claude Science
// takes only ANTHROPIC_BASE_URL, with no key of magpie's (it sends its
// Claude sign-in as its bearer), and http there only to a loopback
// address, so in a distro under NAT it is given the relay magpie runs
// there (agent/claudescience_wsl.go), http://127.0.0.1:<port>/wsl-…, to
// this address on the WSL network. A path whose first segment isn't the
// key is left as it is.
func takeWSLPathKey(r *http.Request) *http.Request {
	seg, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !strings.HasPrefix(seg, wslKeyPrefix) {
		return r
	}
	if wslToken(seg, wslKeyHeld()) == "" {
		return r
	}
	r2 := r.Clone(context.WithValue(r.Context(), wslPathKeyCtx{}, seg))
	r2.URL.Path = "/" + rest
	if raw := r2.URL.RawPath; raw != "" {
		_, rawRest, _ := strings.Cut(strings.TrimPrefix(raw, "/"), "/")
		r2.URL.RawPath = "/" + rawRest
	}
	r2.RequestURI = r2.URL.RequestURI()
	return r2
}

// asFromLoopback is r as a request from this computer: from loopback, its
// WSL key put back as the gateway's token, so it is judged, counted and
// routed as an agent on this machine's is.
func asFromLoopback(r *http.Request) *http.Request {
	k := wslKeyHeld()
	r2 := r.Clone(r.Context())
	_, port, _ := net.SplitHostPort(r.RemoteAddr)
	r2.RemoteAddr = net.JoinHostPort("127.0.0.1", port)
	if a := strings.TrimSpace(r2.Header.Get("Authorization")); len(a) >= 7 && strings.EqualFold(a[:7], "Bearer ") {
		if t := wslToken(a[7:], k); t != "" {
			r2.Header.Set("Authorization", "Bearer "+t)
		}
	}
	for _, h := range []string{"x-api-key", "x-goog-api-key"} {
		if t := wslToken(r2.Header.Get(h), k); t != "" {
			r2.Header.Set(h, t)
		}
	}
	if q := r2.URL.Query(); q.Has("key") {
		if t := wslToken(q.Get("key"), k); t != "" {
			q.Set("key", t)
			r2.URL.RawQuery = q.Encode()
		}
	}
	return r2
}

// wslWatch is how often the gateway looks for WSL networks come or gone:
// WSL's VM, and its adapter with it, starts with the first distro and goes
// with wsl --shutdown, often on another address.
var wslWatch = 10 * time.Second

// serveWSL serves srv on each address Windows has on a WSL network, while
// the gateway listens on loopback alone, until ctx ends.
func (s *Server) serveWSL(ctx context.Context, srv *http.Server) {
	if runtime.GOOS != "windows" && !wslServeEverywhere {
		return
	}
	lns := map[string]net.Listener{}
	defer func() {
		for _, ln := range lns {
			ln.Close()
		}
	}()
	look := func() {
		want := map[string]bool{}
		if s.onLoopback() {
			for _, n := range WSLNets() {
				want[net.JoinHostPort(n.IP.String(), Port())] = true
			}
		}
		for a, ln := range lns {
			if !want[a] {
				ln.Close()
				delete(lns, a)
			}
		}
		for a := range want {
			if lns[a] != nil {
				continue
			}
			ln, err := net.Listen("tcp", a)
			if err != nil {
				log.Printf("gateway: can't listen for WSL at %s: %v", a, err)
				continue
			}
			lns[a] = ln
			go srv.Serve(ln)
		}
	}
	look()
	t := time.NewTicker(wslWatch)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			look()
		}
	}
}

// wslServeEverywhere lets the tests serve WSL networks off Windows.
var wslServeEverywhere = false

// onLoopback: the gateway listens on loopback alone now — not shared on
// every address, nor at an address MAGPIE_ADDR pins beyond loopback.
func (s *Server) onLoopback() bool {
	s.lnMu.Lock()
	ln := s.ln
	s.lnMu.Unlock()
	if ln == nil {
		return false
	}
	a, ok := ln.Addr().(*net.TCPAddr)
	return ok && a.IP.IsLoopback()
}

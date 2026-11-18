package mcpauth_test

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/settings"
)

// dropper accepts each connection, reads the request's first bytes and
// closes it without a word, as a proxy or a network that cuts the way to
// a server does: Go reads EOF (CHENGJIAHEGOXIS on X, mcp.notion.com).
func dropper(t *testing.T) (addr string, seen *atomic.Int32) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	seen = new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			seen.Add(1)
			c.Read(make([]byte, 512))
			c.Close()
		}
	}()
	return ln.Addr().String(), seen
}

// A first request whose connection is cut is asked once more on a new
// connection: Go doesn't send a POST again by itself, and a kept-alive
// connection a server or proxy closed reads EOF on it.
func TestSignInAsksAgainAfterACutConnection(t *testing.T) {
	home(t)
	settings.Save(settings.Settings{Proxy: "direct"})
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			c, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				c.Close()
			}
			return
		}
		w.WriteHeader(http.StatusOK) // asks for no sign-in
	}))
	defer srv.Close()
	_, err := mcpauth.Start(t.Context(), "notion", srv.URL+"/mcp")
	if !errors.Is(err, mcpauth.ErrNoSignIn) {
		t.Fatalf("after one cut connection: %v", err)
	}
	if got := n.Load(); got != 2 {
		t.Fatalf("server asked %d times, want 2", got)
	}
}

// A server never reached is the network, not the server's sign-in: the
// error says the host and the way the request went, direct or through
// which proxy (its password left out), with Go's words kept.
func TestSignInUnreachableSaysTheWay(t *testing.T) {
	home(t)
	t.Run("direct", func(t *testing.T) {
		settings.Save(settings.Settings{Proxy: "direct"})
		addr, seen := dropper(t)
		_, err := mcpauth.Start(t.Context(), "notion", "http://"+addr+"/mcp")
		var u *mcpauth.UnreachableError
		if !errors.As(err, &u) {
			t.Fatalf("error %T %v", err, err)
		}
		if u.Host != addr || u.Proxy != "" || u.Err == nil {
			t.Fatalf("got %+v", u)
		}
		if !strings.Contains(err.Error(), "couldn't reach "+addr+" (directly, with no proxy)") || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("error %v", err)
		}
		if got := seen.Load(); got != 2 {
			t.Fatalf("asked %d times, want 2", got)
		}
	})
	t.Run("proxy", func(t *testing.T) {
		addr, _ := dropper(t)
		if err := settings.Save(settings.Settings{Proxy: "http://amy:hunter2@" + addr}); err != nil {
			t.Fatal(err)
		}
		// an address on no one's LAN, never dialled: the proxy cuts it
		_, err := mcpauth.Start(t.Context(), "notion", "http://203.0.113.5/mcp")
		var u *mcpauth.UnreachableError
		if !errors.As(err, &u) {
			t.Fatalf("error %T %v", err, err)
		}
		if u.Host != "203.0.113.5" || !strings.Contains(u.Proxy, addr) {
			t.Fatalf("got %+v", u)
		}
		if strings.Contains(u.Proxy, "hunter2") || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("proxy password shown: %v", err)
		}
		if !strings.Contains(err.Error(), "through the proxy") {
			t.Fatalf("error %v", err)
		}
	})
}

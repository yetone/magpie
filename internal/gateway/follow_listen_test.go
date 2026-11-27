package gateway

import (
	"encoding/json"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// rael_tao on X: Share on local network turned off where the gateway isn't
// served (the app while a service serves it, the `magpie web` page, or
// settings.json by hand) left it listening on every interface, [::], until
// a restart. The serving gateway follows the saved setting, and the port.
func TestGatewayFollowsSharingChangedElsewhere(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	_, port, _ := net.SplitHostPort(freeAddr(t))
	p, _ := strconv.Atoi(port)
	st := settings.Load()
	st.Port, st.LAN = p, true
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	stop, done := listening(t, s)
	defer func() { stop(); <-done }()
	at := func() net.Addr {
		s.lnMu.Lock()
		defer s.lnMu.Unlock()
		return s.ln.Addr()
	}
	// the state it waits for, not a duration: 20s leaves room for a
	// machine where changes can't be watched (a look every 5s)
	waitAt := func(what, want string) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !listensAt(at(), want); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: still listening on %s, want %s", what, at(), want)
			}
		}
	}
	waitAt("shared", "0.0.0.0:"+port)

	// another process turns sharing off: settings.json written whole, as
	// the user's editor or another magpie does, not through this one
	b, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "lan")
	b, _ = json.Marshal(m)
	if err := os.WriteFile(settings.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	waitAt("sharing turned off by hand", "127.0.0.1:"+port)
	if l, ok := ReadListening(); !ok || l.Addr != "127.0.0.1:"+port {
		t.Errorf("gateway.json says %+v %v, want 127.0.0.1:%s", l, ok, port)
	}

	// and on again, from a save of this process that didn't move it
	st = settings.Load()
	st.LAN = true
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	waitAt("sharing turned on", "0.0.0.0:"+port)

	// a port changed elsewhere moves it too, sharing off
	_, port2, _ := net.SplitHostPort(freeAddr(t))
	st = settings.Load()
	st.LAN = false
	st.Port, _ = strconv.Atoi(port2)
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	waitAt("port changed", "127.0.0.1:"+port2)
}

// 0.0.0.0 and [::] are one listener, and port 0 is any port: a gateway
// shared, or on a port the system picked, is where it should be and isn't
// closed and opened again on every look.
func TestListensAt(t *testing.T) {
	for _, c := range []struct {
		addr *net.TCPAddr
		to   string
		want bool
	}{
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 3425}, "0.0.0.0:3425", true},
		{&net.TCPAddr{IP: net.IPv4zero, Port: 3425}, "0.0.0.0:3425", true},
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 3425}, "127.0.0.1:3425", false},
		{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3425}, "0.0.0.0:3425", false},
		{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3425}, "127.0.0.1:3425", true},
		{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3425}, "127.0.0.1:3426", false},
		{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 51234}, "127.0.0.1:0", true},
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 51234}, "127.0.0.1:0", false},
	} {
		if got := listensAt(c.addr, c.to); got != c.want {
			t.Errorf("listensAt(%s, %s) = %v, want %v", c.addr, c.to, got, c.want)
		}
	}
}

// A Relisten with nothing to move keeps the socket: shared, the old string
// check ([::]:P against 0.0.0.0:P) closed and opened it on every call.
func TestRelistenKeepsASharedSocket(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	_, port, _ := net.SplitHostPort(freeAddr(t))
	p, _ := strconv.Atoi(port)
	st := settings.Load()
	st.Port, st.LAN = p, true
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	s := New()
	ln, err := s.listen(listenAddr())
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	defer func() { s.ln.Close() }()
	if err := s.Relisten(); err != nil {
		t.Fatal(err)
	}
	if s.ln != ln {
		t.Fatal("Relisten closed and opened again a socket already where settings say")
	}
}

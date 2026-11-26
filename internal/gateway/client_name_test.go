package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// jzing.draw on Discord: agents on two other computers, served through
// this one's gateway, drew as one "DeepSeek Harness" node. A route says
// which computer asked: its address at once, its name once looked up; a
// request made on this computer says none.
func TestRouteSaysWhichComputer(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.LAN, s.LANKey = true, "sk-magpie-test-lan-client"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := access.MigrateLegacyLANKey(); err != nil {
		t.Fatal(err)
	}
	var looked atomic.Int32
	names := &nameCache{ttl: time.Hour, missTTL: time.Hour, max: 8, look: func(_ context.Context, a netip.Addr) string {
		looked.Add(1)
		if a == netip.MustParseAddr("192.168.1.9") {
			return "desk-7.local."
		}
		return ""
	}}
	was := clientNames
	clientNames = names
	t.Cleanup(func() { clientNames = was })

	srv := New()
	h := lanGuard(srv.Handler())
	call := func(from string, key bool) Route {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.RemoteAddr = from
		if key {
			r.Header.Set("Authorization", "Bearer "+s.LANKey)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("from %s: %d %s", from, w.Code, w.Body)
		}
		return lastRoute(srv)
	}
	if rt := call("192.168.1.9:5000", true); rt.Client != "192.168.1.9" || rt.ClientName != "" {
		t.Fatalf("first from the LAN: client %q name %q", rt.Client, rt.ClientName)
	}
	// the name is looked up beside the request, never waited for
	deadline := time.Now().Add(10 * time.Second)
	for names.of(netip.MustParseAddr("192.168.1.9")) == "" {
		if time.Now().After(deadline) {
			t.Fatal("name never found")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rt := call("192.168.1.9:5001", true); rt.Client != "192.168.1.9" || rt.ClientName != "desk-7" {
		t.Fatalf("again from the LAN: client %q name %q", rt.Client, rt.ClientName)
	}
	if rt := call("127.0.0.1:5002", false); rt.Client != "" || rt.ClientName != "" {
		t.Fatalf("this computer: client %q name %q", rt.Client, rt.ClientName)
	}
	if n := looked.Load(); n != 1 {
		t.Fatalf("looked up %d times", n)
	}
}

// A name is looked up once while it is fresh, however many requests come
// at once, and never for an address anyone online could have.
func TestClientNamesLookedUpOnce(t *testing.T) {
	var looked atomic.Int32
	release := make(chan struct{})
	c := &nameCache{ttl: time.Hour, missTTL: time.Hour, max: 8, look: func(context.Context, netip.Addr) string {
		looked.Add(1)
		<-release
		return "mote.local."
	}}
	lan := netip.MustParseAddr("10.0.0.124")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); c.of(lan) }()
	}
	wg.Wait()
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for c.of(lan) != "mote" {
		if time.Now().After(deadline) {
			t.Fatal("name never found")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, a := range []string{"8.8.8.8", "2606:4700::1111"} {
		if c.of(netip.MustParseAddr(a)) != "" {
			t.Fatalf("%s named", a)
		}
	}
	if n := looked.Load(); n != 1 {
		t.Fatalf("looked up %d times", n)
	}
	// Tailscale's range is no one's online
	c.of(netip.MustParseAddr("100.101.102.103"))
	deadline = time.Now().Add(10 * time.Second)
	for looked.Load() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("a Tailscale address wasn't looked up")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCleanHostName(t *testing.T) {
	for in, want := range map[string]string{
		"mote.local.": "mote", "MOTE.local": "MOTE", "desk-7.lan.": "desk-7.lan", ".local": ".local", "": "", "x.tail1234.ts.net.": "x.tail1234.ts.net",
	} {
		if got := cleanHostName(in); got != want {
			t.Errorf("cleanHostName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The reply a Mac gave to a unicast PTR query on 11-26 is read for its
// name; one to another query, or of another name, is not.
func TestPTRAnswer(t *testing.T) {
	a := netip.MustParseAddr("10.0.0.124")
	q, err := reverseName(a)
	if err != nil || q.String() != "124.0.0.10.in-addr.arpa." {
		t.Fatalf("reverse name %q %v", q, err)
	}
	if q6, _ := reverseName(netip.MustParseAddr("fe80::1")); !strings.HasPrefix(q6.String(), "1.0.0.0.") || !strings.HasSuffix(q6.String(), ".0.8.e.f.ip6.arpa.") {
		t.Fatalf("ip6 reverse name %q", q6)
	}
	reply := func(id uint16, of dnsmessage.Name, ptr string) []byte {
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true, Authoritative: true})
		b.StartQuestions()
		b.Question(dnsmessage.Question{Name: of, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET})
		b.StartAnswers()
		b.PTRResource(dnsmessage.ResourceHeader{Name: of, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET, TTL: 10}, dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(ptr)})
		msg, err := b.Finish()
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	if n := ptrAnswer(reply(7, q, "mote.local."), 7, q); n != "mote.local." {
		t.Fatalf("name %q", n)
	}
	if n := ptrAnswer(reply(8, q, "mote.local."), 7, q); n != "" {
		t.Fatalf("another query's reply named %q", n)
	}
	other, _ := reverseName(netip.MustParseAddr("10.0.0.113"))
	if n := ptrAnswer(reply(7, other, "mica.local."), 7, q); n != "" {
		t.Fatalf("another address's name %q", n)
	}
	if n := ptrAnswer([]byte{0, 7, 0x80}, 7, q); n != "" {
		t.Fatalf("a cut reply named %q", n)
	}
}

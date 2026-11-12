package gateway

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/proc"
)

func noHolder(context.Context, int) (proc.Holder, bool) { return proc.Holder{}, false }
func noServe(context.Context, int) bool                 { return false }

// pluoki on X: Tailscale's serve held the gateway's port. Whoever holds it
// is named when it is read, Tailscale gets the way out, and a holder that
// can't be read is not named at all.
func TestWhoHolds(t *testing.T) {
	ctx := context.Background()
	tsd := func(context.Context, int) (proc.Holder, bool) {
		return proc.Holder{PID: 612, Name: "tailscaled", Path: "/usr/sbin/tailscaled"}, true
	}
	nginx := func(context.Context, int) (proc.Holder, bool) {
		return proc.Holder{PID: 77, Name: "nginx", Path: "/usr/sbin/nginx"}, true
	}

	b, ok := whoHolds(ctx, "8848", tsd, noServe)
	if !ok || !b.Tailscale || b.Name != "tailscaled" || b.PID != 612 || b.Alt != "8443" {
		t.Fatalf("tailscaled: %+v %v", b, ok)
	}
	s := b.Sentence()
	for _, want := range []string{"port 8848", "Tailscale (tailscaled)", "pid 612", "tailscale serve --https=8443 http://127.0.0.1:8848"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q lacks %q", s, want)
		}
	}

	// the daemon is root's and out of sight: Tailscale's own list names the port
	b, ok = whoHolds(ctx, "8848", noHolder, func(_ context.Context, p int) bool { return p == 8848 })
	if !ok || !b.Tailscale || b.Name != "" || b.PID != 0 {
		t.Fatalf("unseen daemon: %+v %v", b, ok)
	}
	if s := b.Sentence(); !strings.Contains(s, "held by Tailscale:") || strings.Contains(s, "pid") {
		t.Errorf("unseen daemon says %q", s)
	}

	// another program: named, with no Tailscale advice (and Tailscale's list is not asked)
	b, ok = whoHolds(ctx, "8848", nginx, func(context.Context, int) bool { t.Fatal("asked Tailscale about nginx's port"); return false })
	if !ok || b.Tailscale || b.Name != "nginx" {
		t.Fatalf("nginx: %+v %v", b, ok)
	}
	if s := b.Sentence(); !strings.Contains(s, "nginx (pid 77)") || strings.Contains(s, "Tailscale") || strings.Contains(s, "tailscale") {
		t.Errorf("nginx says %q", s)
	}

	// nothing read: unknown, so nothing is said
	if b, ok := whoHolds(ctx, "8848", noHolder, noServe); ok {
		t.Fatalf("an unread holder was named: %+v", b)
	}
	if _, ok := whoHolds(ctx, "x", tsd, noServe); ok {
		t.Fatal("a port that isn't a number was named")
	}

	// the other port isn't the gateway's own
	if b, _ := whoHolds(ctx, "8443", tsd, noServe); b.Alt != "9443" {
		t.Errorf("alt for 8443: %q", b.Alt)
	}
}

// A bind that fails because the port is taken says who has it in the error
// that the log and `magpie serve` print, and an unread holder leaves the
// error as the system worded it.
func TestListenErrorNamesTheHolder(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	t.Setenv("MAGPIE_ADDR", taken.Addr().String())
	_, port, _ := net.SplitHostPort(taken.Addr().String())

	wasHolder, wasServes := readHolder, readServes
	defer func() {
		readHolder, readServes = wasHolder, wasServes
		ClearHeld()
	}()
	readHolder = func(context.Context, int) (proc.Holder, bool) {
		return proc.Holder{PID: 612, Name: "tailscaled", Path: "/usr/sbin/tailscaled"}, true
	}
	readServes = noServe

	err = New().ListenAndServe(t.Context())
	if err == nil {
		t.Fatal("listened on a taken port")
	}
	if !addrInUse(err) {
		t.Fatalf("not taken as an address in use: %v", err)
	}
	if !strings.Contains(err.Error(), "port "+port+" is held by Tailscale (tailscaled) (pid 612)") {
		t.Fatalf("the holder isn't named: %v", err)
	}
	if h := LastHeld(); h == nil || !h.Tailscale || h.Port != port {
		t.Fatalf("LastHeld %+v", h)
	}

	// nobody read: the system's words alone, and nothing kept
	readHolder = noHolder
	err = New().ListenAndServe(t.Context())
	if err == nil || !addrInUse(err) || strings.Contains(err.Error(), "held by") {
		t.Fatalf("an unread holder changed the error: %v", err)
	}
	if h := LastHeld(); h != nil {
		t.Fatalf("LastHeld kept %+v", h)
	}
}

// A listen that fails for another reason isn't taken for a busy port.
func TestAddrInUseIsOnlyThat(t *testing.T) {
	_, err := net.Listen("tcp", "256.256.256.256:1")
	if err == nil || addrInUse(err) {
		t.Fatalf("a bad address: %v", err)
	}
}

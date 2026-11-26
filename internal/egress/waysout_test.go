package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// mamba on Discord: show the address a Claude request goes out from, let
// the user say which ways out are allowed, and hold the request before it
// goes when the way out isn't one of them.

func waysOut(t *testing.T, hold bool, ways ...string) {
	t.Helper()
	if err := settings.Save(settings.Settings{ClaudeRegionHold: hold, ClaudeWaysOut: ways}); err != nil {
		t.Fatal(err)
	}
}

// stale makes choice's last lookup older than Fresh.
func stale(choice string) {
	mu.Lock()
	r := results[choice]
	r.At = time.Now().Add(-2 * Fresh)
	results[choice] = r
	mu.Unlock()
}

func TestCheckReadsTheAddress(t *testing.T) {
	sandbox(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("US")
	fakeTrace(t, &loc, &status)
	if r := Check(context.Background(), ""); r.IP != "203.0.113.9" || r.Loc != "US" || r.State != OK || r.Mine {
		t.Fatalf("got %+v", r)
	}
}

func TestJudgeByTheUsersWaysOut(t *testing.T) {
	sandbox(t)
	now := time.Now()
	for _, c := range []struct {
		ways    []string
		loc, ip string
		want    string
	}{
		// a region of the user's
		{[]string{"US"}, "US", "203.0.113.9", OK},
		{[]string{"us", "JP"}, "JP", "", OK},
		// Anthropic lists GB; the user didn't
		{[]string{"US"}, "GB", "203.0.113.9", Unsupported},
		// Anthropic doesn't list HK; the user's own list is what counts
		{[]string{"HK"}, "HK", "203.0.113.9", OK},
		// an address of the user's, an IP or a range, in any region
		{[]string{"203.0.113.9"}, "CN", "203.0.113.9", OK},
		{[]string{"203.0.113.0/24"}, "XX", "203.0.113.200", OK},
		{[]string{"2001:db8::/32"}, "US", "2001:db8::1", OK},
		{[]string{"203.0.113.9"}, "US", "198.51.100.1", Unsupported},
		{[]string{"US", "203.0.113.9"}, "US", "198.51.100.1", OK},
		{[]string{"US", "203.0.113.9"}, "JP", "203.0.113.9", OK},
		{[]string{"US", "203.0.113.9"}, "JP", "198.51.100.1", Unsupported},
		// what can't be told is Unknown, never fine and never held
		{[]string{"US"}, "XX", "203.0.113.9", Unknown},
		{[]string{"203.0.113.9"}, "US", "", Unknown},
		// regions alone need no address, addresses alone no region
		{[]string{"US"}, "JP", "", Unsupported},
		{[]string{"203.0.113.0/24"}, "", "198.51.100.1", Unsupported},
	} {
		waysOut(t, false, c.ways...)
		r := judge(c.loc, c.ip, now)
		if r.State != c.want || !r.Mine {
			t.Errorf("ways %v, loc %q ip %q: %s (mine %v), want %s", c.ways, c.loc, c.ip, r.State, r.Mine, c.want)
		}
	}
}

// A list changed counts at once, without another lookup.
func TestWaysOutChangedCountAtOnce(t *testing.T) {
	sandbox(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("US")
	asked := fakeTrace(t, &loc, &status)
	Check(context.Background(), "")
	waysOut(t, false, "JP")
	if r := Of(""); r.State != Unsupported || !r.Mine || r.IP != "203.0.113.9" {
		t.Fatalf("after JP alone: %+v", r)
	}
	if r := Worst([]string{""}); r.State != Unsupported {
		t.Fatalf("Worst: %+v", r)
	}
	waysOut(t, false)
	if r := Of(""); r.State != OK || r.Mine {
		t.Fatalf("after the list was cleared: %+v", r)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("looked up %d times, want 1", n)
	}
}

// A node switched to another country inside a proxy app changes nothing
// magpie can watch: with the hold on, a request after Fresh looks again,
// and is held when the way out moved outside what is allowed.
func TestHoldLooksAgainAfterANodeSwitch(t *testing.T) {
	sandbox(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("US")
	asked := fakeTrace(t, &loc, &status)
	waysOut(t, true, "US")
	Check(context.Background(), "")
	if err := Hold(context.Background(), ""); err != nil {
		t.Fatalf("held in the US: %v", err)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("a fresh lookup was made again: %d", n)
	}
	loc.Store("JP") // the node switched; the proxy's address didn't
	stale("")
	err := Hold(context.Background(), "")
	var he *HeldError
	if !errors.As(err, &he) || !strings.Contains(err.Error(), "203.0.113.9 in region JP") || !strings.Contains(err.Error(), "allowed for Claude") {
		t.Fatalf("not held after the switch: %v", err)
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("looked up %d times, want 2", n)
	}
	// with the hold off, nothing is looked up for a request
	waysOut(t, false, "US")
	stale("")
	if err := Hold(context.Background(), ""); err != nil || asked.Load() != 2 {
		t.Fatalf("hold off: %v, %d lookups", err, asked.Load())
	}
}

// Requests that come while a lookup for them is under way wait on that
// one: one lookup, not one each.
func TestHoldLooksAgainOnceForMany(t *testing.T) {
	sandbox(t)
	var asked atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asked.Add(1) > 1 {
			<-release
		}
		w.Write([]byte("ip=203.0.113.9\nloc=CN\n"))
	}))
	t.Cleanup(srv.Close)
	was, wasClient := TraceURL, Client
	TraceURL, Client = srv.URL, srv.Client()
	t.Cleanup(func() { TraceURL, Client = was, wasClient; Forget() })
	Forget()
	waysOut(t, true)
	Check(context.Background(), "")
	stale("")
	var wg sync.WaitGroup
	held := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); held <- Hold(context.Background(), "") }()
	}
	// every request is waiting before the lookup answers
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		_, busy := checking[""]
		mu.Unlock()
		if busy && asked.Load() == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(held)
	for err := range held {
		if err == nil {
			t.Error("a request went out from CN")
		}
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("looked up %d times, want 2 (the first, then one for all eight)", n)
	}
}

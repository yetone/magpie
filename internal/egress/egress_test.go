package egress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// fakeTrace points the lookup at a server of the test's, answering loc
// (or failing with status when it is not 200), and counts what it was
// asked.
func fakeTrace(t *testing.T, loc *atomic.Value, status *atomic.Int32) *atomic.Int32 {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if s := status.Load(); s != 0 && s != 200 {
			w.WriteHeader(int(s))
			return
		}
		fmt.Fprintf(w, "fl=1\nh=www.cloudflare.com\nip=203.0.113.9\nts=1\nvisit_scheme=https\nloc=%s\ntls=TLSv1.3\n", loc.Load())
	}))
	t.Cleanup(srv.Close)
	was, wasClient := TraceURL, Client
	TraceURL, Client = srv.URL, srv.Client()
	t.Cleanup(func() { TraceURL, Client = was, wasClient })
	mu.Lock()
	results = map[string]Result{}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		results = map[string]Result{}
		mu.Unlock()
	})
	return &asked
}

// sandbox starts the test with no settings, and leaves none.
func sandbox(t *testing.T) {
	t.Helper()
	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(settings.Settings{}) })
}

func holdOn(t *testing.T) {
	t.Helper()
	if err := settings.Save(settings.Settings{ClaudeRegionHold: true}); err != nil {
		t.Fatal(err)
	}
}

func TestSupportedIsThePolicysList(t *testing.T) {
	b, err := os.ReadFile("testdata/supported-regions.txt")
	if err != nil {
		t.Fatal(err)
	}
	var page []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		page = append(page, strings.TrimSpace(l))
	}
	var ours []string
	for code, name := range Supported {
		if len(code) != 2 || strings.ToUpper(code) != code {
			t.Errorf("code %q is not an ISO 3166-1 alpha-2 code", code)
		}
		ours = append(ours, name)
	}
	slices.Sort(page)
	slices.Sort(ours)
	if !slices.Equal(page, ours) {
		for _, n := range page {
			if !slices.Contains(ours, n) {
				t.Errorf("the policy lists %q; Supported doesn't", n)
			}
		}
		for _, n := range ours {
			if !slices.Contains(page, n) {
				t.Errorf("Supported has %q; the policy doesn't list it", n)
			}
		}
	}
	for terr, of := range territories {
		if _, ok := Supported[of]; !ok {
			t.Errorf("territory %s belongs to %s, which isn't listed", terr, of)
		}
		if _, ok := Supported[terr]; ok {
			t.Errorf("territory %s is listed itself", terr)
		}
	}
}

func TestJudge(t *testing.T) {
	now := time.Now()
	for loc, want := range map[string]string{
		"US": OK, "JP": OK, "PR": OK, "GB": OK, "TW": OK, "SG": OK,
		"CN": Unsupported, "HK": Unsupported, "MO": Unsupported, "RU": Unsupported, "IR": Unsupported, "KP": Unsupported,
		"": Unknown, "XX": Unknown, "T1": Unknown, "usa": Unknown, "1A": Unknown,
	} {
		if got := judge(loc, now).State; got != want {
			t.Errorf("loc %q: %s, want %s", loc, got, want)
		}
	}
}

func TestCheckReadsTheTrace(t *testing.T) {
	sandbox(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("cn")
	fakeTrace(t, &loc, &status)
	r := Check(context.Background(), "direct")
	if r.State != Unsupported || r.Loc != "CN" || r.Via != "direct" {
		t.Fatalf("got %+v", r)
	}
	if got := Of("direct"); got.State != Unsupported {
		t.Fatalf("Of kept %+v", got)
	}
	loc.Store("JP")
	if r := Check(context.Background(), "direct"); r.State != OK || r.Loc != "JP" {
		t.Fatalf("got %+v", r)
	}
}

// A lookup that fails says nothing of the region: Unknown, never OK and
// never Unsupported, and it holds nothing.
func TestFailedLookupIsUnknown(t *testing.T) {
	sandbox(t)
	holdOn(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("CN")
	fakeTrace(t, &loc, &status)
	if r := Check(context.Background(), ""); r.State != Unsupported {
		t.Fatalf("got %+v", r)
	}
	status.Store(503)
	if r := Check(context.Background(), ""); r.State != Unknown || r.Error == "" {
		t.Fatalf("a 503 read as %+v", r)
	}
	if err := Hold(context.Background(), ""); err != nil {
		t.Fatalf("an unknown way out held: %v", err)
	}
	// and one that can't be reached at all
	TraceURL = "http://127.0.0.1:1/cdn-cgi/trace"
	if r := Check(context.Background(), ""); r.State != Unknown {
		t.Fatalf("an unreachable trace read as %+v", r)
	}
}

func TestHoldOnlyWhenTurnedOnAndUnsupported(t *testing.T) {
	sandbox(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("CN")
	asked := fakeTrace(t, &loc, &status)
	Check(context.Background(), "")
	if err := Hold(context.Background(), ""); err != nil {
		t.Fatalf("held with the hold off: %v", err)
	}
	holdOn(t)
	err := Hold(context.Background(), "")
	var he *HeldError
	if !errors.As(err, &he) || !strings.HasPrefix(err.Error(), HeldWords) || !strings.Contains(err.Error(), "CN") {
		t.Fatalf("not held: %v", err)
	}
	for _, w := range []string{"ban", "Ban", "safe"} {
		if strings.Contains(err.Error(), w) {
			t.Errorf("the hold's text says %q: %s", w, err)
		}
	}
	// a stale Unsupported is looked up again first: the proxy back on,
	// the request goes
	before := asked.Load()
	mu.Lock()
	r := results[""]
	r.At = time.Now().Add(-time.Minute)
	results[""] = r
	mu.Unlock()
	loc.Store("US")
	if err := Hold(context.Background(), ""); err != nil {
		t.Fatalf("held after the proxy came back: %v", err)
	}
	if asked.Load() != before+1 {
		t.Fatalf("looked up %d times, want 1", asked.Load()-before)
	}
	// a choice never looked up goes, and is looked up for the next one
	if err := Hold(context.Background(), "socks5://127.0.0.1:9"); err != nil {
		t.Fatalf("a choice not looked up yet held: %v", err)
	}
	select {
	case <-kick:
	default:
		t.Fatal("a choice not looked up yet wasn't asked for")
	}
}

// Keep looks up at the start, again when the network or a proxy changes,
// and not on every poll.
func TestKeepLooksUpOnChange(t *testing.T) {
	sandbox(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("US")
	asked := fakeTrace(t, &loc, &status)
	wasPoll, wasFP := Poll, fingerprint
	t.Cleanup(func() { Poll, fingerprint = wasPoll, wasFP })
	Poll = 5 * time.Millisecond
	var net atomic.Value
	net.Store("wifi-a")
	fingerprint = func(cs []string) string { return net.Load().(string) + strings.Join(cs, ",") }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); Keep(ctx, func() []string { return []string{"", "direct"} }) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, func() bool { return asked.Load() == 2 })
	// polls with nothing changed ask nothing
	time.Sleep(50 * time.Millisecond)
	if n := asked.Load(); n != 2 {
		t.Fatalf("asked %d times with nothing changed, want 2", n)
	}
	loc.Store("CN")
	net.Store("wifi-b")
	waitFor(t, func() bool { return Of("direct").State == Unsupported && Of("").State == Unsupported })
	if n := asked.Load(); n != 4 {
		t.Fatalf("asked %d times after one change, want 4", n)
	}
	Again()
	waitFor(t, func() bool { return asked.Load() == 6 })
}

// No Claude account, no lookup.
func TestKeepAsksNothingWithoutChoices(t *testing.T) {
	sandbox(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("US")
	asked := fakeTrace(t, &loc, &status)
	wasPoll := Poll
	t.Cleanup(func() { Poll = wasPoll })
	Poll = time.Millisecond
	var polled atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Keep(ctx, func() []string { polled.Add(1); return nil })
	}()
	waitFor(t, func() bool { return polled.Load() >= 5 })
	cancel()
	<-done
	if n := asked.Load(); n != 0 {
		t.Fatalf("looked up %d times with no Claude account", n)
	}
}

func TestWorst(t *testing.T) {
	sandbox(t)
	var loc atomic.Value
	var status atomic.Int32
	loc.Store("US")
	fakeTrace(t, &loc, &status)
	Check(context.Background(), "")
	if r := Worst([]string{"", "direct"}); r.State != Unknown {
		t.Fatalf("one not looked up: %+v", r)
	}
	Check(context.Background(), "direct")
	if r := Worst([]string{"", "direct"}); r.State != OK {
		t.Fatalf("both fine: %+v", r)
	}
	loc.Store("RU")
	Check(context.Background(), "direct")
	if r := Worst([]string{"", "direct"}); r.State != Unsupported || r.Via != "direct" {
		t.Fatalf("one unsupported: %+v", r)
	}
}

func TestRedactErr(t *testing.T) {
	got := redactErr(`proxyconnect tcp: dial http://bob:hunter2@10.0.0.1:8080: refused`, "")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("the password is in %q", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// minuteVendor answers chat completions, Messages and count_tokens, noting
// when each request came; fail is how many chat completions it answers
// 503 first.
type minuteVendor struct {
	mu   sync.Mutex
	at   []time.Time
	path []string
	fail int
}

func (v *minuteVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	v.mu.Lock()
	v.at = append(v.at, time.Now())
	v.path = append(v.path, r.URL.Path)
	failing := v.fail > 0 && strings.HasSuffix(r.URL.Path, "/chat/completions")
	if failing {
		v.fail--
	}
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case failing:
		w.WriteHeader(503)
		io.WriteString(w, `{"error":{"message":"busy a moment"}}`)
	case strings.HasSuffix(r.URL.Path, "/count_tokens"):
		io.WriteString(w, `{"input_tokens":7}`)
	case strings.HasSuffix(r.URL.Path, "/messages"):
		io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	default:
		io.WriteString(w, `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}
}

func (v *minuteVendor) times() []time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]time.Time(nil), v.at...)
}

// shortMinute makes MaxRPM's window and longest wait w and longest for the
// test.
func shortMinute(t *testing.T, w, longest time.Duration) {
	t.Helper()
	ow, ol := rpmWindow, rpmLongest
	rpmWindow, rpmLongest = w, longest
	t.Cleanup(func() { rpmWindow, rpmLongest = ow, ol })
}

// rpmGateway is a gateway as the real server serves it (lanGuard in
// front), with a provider "rl" of one key whose MaxRPM is limit.
func rpmGateway(t *testing.T, v *minuteVendor, limit int) (*Server, string) {
	t.Helper()
	fresh(t)
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "rl", Name: "Limited", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1", Anthropic: up.URL, MaxRPM: limit}); err != nil {
		t.Fatal(err)
	}
	s := New()
	gw := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(gw.Close)
	return s, gw.URL
}

func chatOnce(ctx context.Context, gw string) (*http.Response, string, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", gw+"/v1/chat/completions", strings.NewReader(`{"model":"rl/m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b), nil
}

// coeo91 on Discord: OpenRouter's free models take 20 requests a minute,
// which a limit at once of 1 didn't keep to. With a MaxRPM of 2, through
// the real server, two requests go at once and the third waits until the
// first is a minute old, then is answered.
func TestMaxRPMWaitsForRoomInTheMinute(t *testing.T) {
	window := 1500 * time.Millisecond
	shortMinute(t, window, time.Minute)
	v := &minuteVendor{}
	_, gw := rpmGateway(t, v, 2)
	start := time.Now()
	for i := range 3 {
		res, body, err := chatOnce(context.Background(), gw)
		if err != nil || res.StatusCode != 200 || !strings.Contains(body, `"ok"`) {
			t.Fatalf("request %d: %v %v %s", i+1, err, res, body)
		}
	}
	at := v.times()
	if len(at) != 3 {
		t.Fatalf("the vendor saw %d requests, want 3", len(at))
	}
	if d := at[1].Sub(start); d > window/2 {
		t.Fatalf("the second waited %s with room in the minute", d)
	}
	if d := at[2].Sub(at[0]); d < window-50*time.Millisecond {
		t.Fatalf("the third went %s after the first, inside the %s window", d, window)
	}
}

// One that would wait longer than a request waits is turned away at once
// with a 429 whose Retry-After says when the minute has room, and never
// reaches the vendor; nobody rests for it, and once the minute has room
// the next is answered.
func TestMaxRPMTooLongIs429WithRetryAfter(t *testing.T) {
	window := 1500 * time.Millisecond
	shortMinute(t, window, 200*time.Millisecond)
	v := &minuteVendor{}
	_, gw := rpmGateway(t, v, 1)
	if res, body, err := chatOnce(context.Background(), gw); err != nil || res.StatusCode != 200 {
		t.Fatalf("first: %v %v %s", err, res, body)
	}
	began := time.Now()
	res, body, err := chatOnce(context.Background(), gw)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 429 || res.Header.Get("Retry-After") != "2" || !strings.Contains(body, "1 requests a minute is its limit") {
		t.Fatalf("second = %d Retry-After %q %s, want a 429 saying the limit, Retry-After 2", res.StatusCode, res.Header.Get("Retry-After"), body)
	}
	if d := time.Since(began); d > window/2 {
		t.Fatalf("turned away after %s, not at once", d)
	}
	if n := len(v.times()); n != 1 {
		t.Fatalf("the vendor saw %d, want 1", n)
	}
	if r := restingNow(t); len(r) != 0 {
		t.Fatalf("%v rest for a limit of the user's", r)
	}
	time.Sleep(window)
	if res, body, err := chatOnce(context.Background(), gw); err != nil || res.StatusCode != 200 {
		t.Fatalf("after the minute: %v %v %s", err, res, body)
	}
}

// An agent that goes away while its request waits for room ends the wait:
// nothing is sent, and its place in the minute is given back.
func TestMaxRPMAgentGoneWhileWaiting(t *testing.T) {
	shortMinute(t, 3*time.Second, time.Minute)
	v := &minuteVendor{}
	s, gw := rpmGateway(t, v, 1)
	if res, _, err := chatOnce(context.Background(), gw); err != nil || res.StatusCode != 200 {
		t.Fatalf("first: %v %v", err, res)
	}
	who := meterWho(provider.Provider{ID: "rl", Key: "k"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := chatOnce(ctx, gw)
		done <- err
	}()
	within(t, "the second waiting", func() bool { return s.rpms.used(who) == 2 })
	cancel()
	if err := <-done; err == nil {
		t.Fatal("the canceled request answered")
	}
	within(t, "its place given back", func() bool { return s.rpms.used(who) == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := len(v.times()); n != 1 {
		t.Fatalf("the vendor saw %d, want 1", n)
	}
}

// What magpie sends on its own counts too: a retry after the vendor's 503
// is a request of the minute, and so is a count_tokens that reaches the
// vendor. With 3 a minute, one request tried twice and one count leave no
// room, so the next chat waits for the minute.
func TestMaxRPMCountsRetriesAndCounts(t *testing.T) {
	window := 1500 * time.Millisecond
	shortMinute(t, window, time.Minute)
	v := &minuteVendor{fail: 1}
	_, gw := rpmGateway(t, v, 3)
	if res, body, err := chatOnce(context.Background(), gw); err != nil || res.StatusCode != 200 {
		t.Fatalf("first: %v %v %s", err, res, body)
	}
	if n := len(v.times()); n != 2 {
		t.Fatalf("the vendor saw %d, want the 503 and its retry", n)
	}
	req, _ := http.NewRequest("POST", gw+"/v1/messages/count_tokens", strings.NewReader(`{"model":"rl/m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var count struct {
		InputTokens int `json:"input_tokens"`
	}
	json.NewDecoder(res.Body).Decode(&count)
	res.Body.Close()
	if count.InputTokens != 7 {
		t.Fatalf("count = %d, want the vendor's 7", count.InputTokens)
	}
	// with no room in the minute, a count doesn't wait: the estimate
	// answers, and the vendor isn't asked
	req, _ = http.NewRequest("POST", gw+"/v1/messages/count_tokens", strings.NewReader(`{"model":"rl/m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	began := time.Now()
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || time.Since(began) > window/2 {
		t.Fatalf("a count with no room: %d after %s", res.StatusCode, time.Since(began))
	}
	if n := len(v.times()); n != 3 {
		t.Fatalf("the vendor saw %d, want the count estimated", n)
	}
	if res, body, err := chatOnce(context.Background(), gw); err != nil || res.StatusCode != 200 {
		t.Fatalf("last: %v %v %s", err, res, body)
	}
	at := v.times()
	if len(at) != 4 {
		t.Fatalf("the vendor saw %d (%v), want 4", len(at), v.path)
	}
	if d := at[3].Sub(at[0]); d < window-50*time.Millisecond {
		t.Fatalf("the last went %s after the first, inside the %s window: the retry or the count wasn't counted", d, window)
	}
}

// A group whose member has sent its MaxRPM in the last minute asks another
// member with room first, as it does one whose slots are all out, rather
// than wait.
func TestMaxRPMGroupPrefersMemberWithRoom(t *testing.T) {
	shortMinute(t, 5*time.Second, time.Minute)
	fresh(t)
	a, b := &minuteVendor{}, &minuteVendor{}
	ua, ub := httptest.NewServer(a), httptest.NewServer(b)
	t.Cleanup(ua.Close)
	t.Cleanup(ub.Close)
	if err := provider.Save(provider.Provider{ID: "pa", Name: "A", Key: "ka", Models: []string{"m"}, Chat: ua.URL + "/v1", MaxRPM: 1}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "pb", Name: "B", Key: "kb", Models: []string{"m"}, Chat: ub.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Duo", Routing: provider.Ordered, Members: []string{"pa/m", "pb/m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	gw := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(gw.Close)
	ask := func() {
		req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"group/duo","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%d %s", res.StatusCode, b)
		}
	}
	ask()
	if len(a.times()) != 1 || len(b.times()) != 0 {
		t.Fatalf("first: A saw %d, B %d; want A, first in order", len(a.times()), len(b.times()))
	}
	began := time.Now()
	ask()
	if len(a.times()) != 1 || len(b.times()) != 1 {
		t.Fatalf("second: A saw %d, B %d; want B, A having no room", len(a.times()), len(b.times()))
	}
	if d := time.Since(began); d > time.Second {
		t.Fatalf("the second waited %s", d)
	}
}

// The wait for room never holds the lock a request giving its place back
// needs: many waiting and many giving up at once all end.
func TestRPMWaitsAndGiveBacksTogether(t *testing.T) {
	shortMinute(t, 200*time.Millisecond, time.Minute)
	var l rpms
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%4)*100*time.Millisecond+50*time.Millisecond)
			defer cancel()
			l.wait(ctx, "x", 3, 0)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waits didn't end")
	}
}

// The minute's arithmetic: room while fewer than the limit fall in the
// last minute, then a minute after the limit-th from the end, in order;
// one given back leaves room, and one past the longest wait is refused
// with when it could go.
func TestRPMReserve(t *testing.T) {
	shortMinute(t, time.Minute, 2*time.Minute)
	var l rpms
	t0 := time.Now()
	at := func(d time.Duration, longest time.Duration) (time.Duration, error) {
		got, err := l.reserve("x", 2, longest, t0.Add(d))
		return got.Sub(t0), err
	}
	for _, c := range []struct{ now, want time.Duration }{
		{0, 0}, {10 * time.Second, 10 * time.Second}, // room
		{20 * time.Second, 60 * time.Second},  // a minute after the first
		{20 * time.Second, 70 * time.Second},  // after the second
		{30 * time.Second, 120 * time.Second}, // after the third, to come
	} {
		if got, err := at(c.now, 2*time.Minute); err != nil || got != c.want {
			t.Fatalf("at %s: %s %v, want %s", c.now, got, err, c.want)
		}
	}
	_, err := at(30*time.Second, 2*time.Minute) // would go at 130s: 100s away
	if err != nil {
		t.Fatalf("100s away refused: %v", err)
	}
	_, err = at(30*time.Second, 90*time.Second) // at 180s: 150s away
	e, ok := err.(*errRPM)
	if !ok || e.retryAfter() != "150" {
		t.Fatalf("past the longest: %v, want an errRPM in 150s", err)
	}
	l.giveBack("x", t0.Add(130*time.Second))
	l.giveBack("x", t0.Add(120*time.Second))
	if got, err := at(31*time.Second, 2*time.Minute); err != nil || got != 120*time.Second {
		t.Fatalf("after two given back: %s %v, want 120s", got, err)
	}
	if l.free("x", 0) != true {
		t.Fatal("no limit isn't free")
	}
}

// A try's own requests are counted once, against the key or account the
// try is of, however the provider names its account; a request to another
// provider made on the way is counted against that one's.
func TestRPMTicketOfATry(t *testing.T) {
	s := &Server{}
	p := provider.Provider{ID: "codex", MaxRPM: 3, Account: &provider.Account{}}
	ctx := s.paidFor(context.Background(), p, "codex@someone")
	if got := s.metered(ctx, p, ""); got != ctx {
		t.Fatal("a try's own request got a ticket of its own: it would be counted twice")
	}
	other := provider.Provider{ID: "kimi", Key: "k", MaxRPM: 3}
	got := s.metered(ctx, other, "")
	tk, _ := got.Value(rpmKey{}).(*rpmTicket)
	if tk == nil || tk.who != meterWho(other) || tk.paid.Load() {
		t.Fatalf("another provider's request on the way: %+v", tk)
	}
}

package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// curveClock is a meter's clock the test moves by hand.
type curveClock struct{ at atomic.Int64 }

func (c *curveClock) now() time.Time { return time.Unix(c.at.Load(), 0) }
func (c *curveClock) tick(d int64)   { c.at.Add(d) }

func meterAt(start int64) (*curveMeter, *curveClock) {
	c := &curveClock{}
	c.at.Store(start)
	return &curveMeter{now: c.now}, c
}

// learnRate teaches liveRates tokens-per-byte for a pair, past liveRateMin.
func learnRate(provider, model string, bytes int64, output int) {
	for range liveRateMin {
		liveRates.learn(provider, model, bytes, output)
	}
}

func onlySpeed(t *testing.T, m *curveMeter) Speed {
	t.Helper()
	sp := m.speeds()
	if len(sp) != 1 {
		t.Fatalf("speeds %+v, want one session", sp)
	}
	return sp[0]
}

// The curve is tokens a second over whole seconds, a gap where nothing of
// the session's was in flight (a tool ran), and the round's average over
// the seconds it wrote in, its peak and its output.
func TestSessionCurveCountsSecondsAndGaps(t *testing.T) {
	freshRates(t)
	learnRate("p", "m", 400, 100) // a byte is a quarter token
	m, c := meterAt(1_000_000)

	shut := m.open("claude", "s1")
	for range 5 {
		m.add("claude", "s1", "p", "m", 400)
		c.tick(1)
	}
	shut()
	c.tick(10) // the agent runs a tool
	shut = m.open("claude", "s1")
	for range 3 {
		m.add("claude", "s1", "p", "m", 800)
		c.tick(1)
	}

	sp := onlySpeed(t, m)
	if !sp.Live || !sp.Rated {
		t.Fatalf("live %v rated %v", sp.Live, sp.Rated)
	}
	if len(sp.Curve) != curveSpan {
		t.Fatalf("curve has %d seconds", len(sp.Curve))
	}
	tail := sp.Curve[curveSpan-19:]
	var got []string
	for _, v := range tail {
		if v == nil {
			got = append(got, "-")
		} else {
			got = append(got, strconv.FormatFloat(*v, 'f', -1, 64))
		}
	}
	// 5 seconds at 100, the second the first request ended in (live, 0),
	// 9 seconds of nothing in flight, then 3 at 200
	want := "100 100 100 100 100 0 - - - - - - - - - 200 200 200"
	if g := strings.Join(got[1:], " "); g != want {
		t.Fatalf("curve tail\n got %s\nwant %s", g, want)
	}
	if sp.Now != 200 {
		t.Errorf("now %v, want 200", sp.Now)
	}
	if want := float64(5*400+3*800) / 8 / 4; sp.Avg != want {
		t.Errorf("avg %v, want %v", sp.Avg, want)
	}
	if sp.Peak != 200 {
		t.Errorf("peak %v, want 200", sp.Peak)
	}
	if sp.Output != 1100 {
		t.Errorf("output %v, want 1100", sp.Output)
	}

	shut()
	c.tick(4)
	sp = onlySpeed(t, m)
	if sp.Live || sp.Now != 0 || sp.Idle != 4 {
		t.Fatalf("after the turn: live %v now %v idle %v", sp.Live, sp.Now, sp.Idle)
	}
}

// roundBytes is the content bytes a session's round has so far.
func (m *curveMeter) roundBytes(session string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, t := range m.m {
		if k.session == session {
			return t.roundBytes
		}
	}
	return 0
}

// A model with no rate yet has the curve's shape in bytes and no numbers:
// a guess would be a wrong number on the page.
func TestSessionCurveUnratedHasNoNumbers(t *testing.T) {
	freshRates(t)
	m, c := meterAt(2_000_000)
	shut := m.open("codex", "s2")
	defer shut()
	m.add("codex", "s2", "openai", "gpt", 300)
	c.tick(1)
	sp := onlySpeed(t, m)
	if sp.Rated || sp.Now != 0 || sp.Avg != 0 || sp.Peak != 0 || sp.Output != 0 {
		t.Fatalf("unrated speed has numbers: %+v", sp)
	}
	if v := sp.Curve[curveSpan-1]; v == nil || *v != 300 {
		t.Fatalf("unrated curve's last second %v, want its 300 bytes", v)
	}
}

// A pause past curveRound with nothing in flight starts a new round; one
// past curveForget drops the session. A request with no session has no
// curve.
func TestSessionCurveRoundsAndForgets(t *testing.T) {
	freshRates(t)
	learnRate("p", "m", 100, 100)
	m, c := meterAt(3_000_000)
	shut := m.open("a", "s")
	m.add("a", "s", "p", "m", 1000)
	c.tick(1)
	shut()
	c.tick(int64(curveRound/time.Second) + 1)
	shut = m.open("a", "s")
	m.add("a", "s", "p", "m", 10)
	c.tick(1)
	shut()
	sp := onlySpeed(t, m)
	if sp.Output != 10 || sp.Peak != 10 || sp.Avg != 10 {
		t.Fatalf("new round kept the old one's numbers: %+v", sp)
	}
	m.open("a", "")()
	m.add("a", "", "p", "m", 10)
	if len(m.speeds()) != 1 {
		t.Fatal("a request with no session got a curve")
	}
	c.tick(int64(curveForget/time.Second) + 1)
	if sp := m.speeds(); len(sp) != 0 {
		t.Fatalf("a session quiet past curveForget is kept: %+v", sp)
	}
}

// A long tool run moves the ring on without going through every second
// in between, and leaves it all gap.
func TestSessionCurveLongGap(t *testing.T) {
	freshRates(t)
	learnRate("p", "m", 100, 100)
	m, c := meterAt(4_000_000)
	shut := m.open("a", "s")
	m.add("a", "s", "p", "m", 50)
	shut()
	c.tick(int64(curveRound/time.Second) - 1)
	m.open("a", "s")
	c.tick(1)
	sp := onlySpeed(t, m)
	for i, v := range sp.Curve[:curveSpan-1] {
		if v != nil {
			t.Fatalf("second %d of a long gap is %v, want a gap", i, *v)
		}
	}
	if sp.Peak != 50 || sp.Output != 50 {
		t.Fatalf("the round before the gap is lost: %+v", sp)
	}
}

// Through the whole gateway: a session's streamed reply is on its curve,
// in flight while it streams and not after.
func TestSessionCurveFromARealStream(t *testing.T) {
	freshRates(t)
	learnRate("live", "m", 6, 6) // a content byte is a token
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"abcd\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ef\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":6,\"total_tokens\":9}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	var once atomic.Bool
	free := func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer free()
	if err := provider.Save(provider.Provider{ID: "live", Name: "Live", Chat: up.URL + "/v1", Key: "k", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	clock := &curveClock{}
	clock.at.Store(5_000_000)
	s.curves.now = clock.now
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"live/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("content-type", "application/json")
	req.Header.Set(SessionHeader, "sess-e2e")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, req)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if sp := s.Speeds(); len(sp) == 1 && sp[0].Live {
			if s.curves.roundBytes("sess-e2e") == 4 {
				break
			}
		}
		if time.Now().After(deadline) {
			free()
			t.Fatalf("the first content never reached the session's curve: %+v", s.Speeds())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sp := s.Speeds()[0]; sp.Session != "sess-e2e" || !sp.Live {
		t.Fatalf("mid-stream %+v", sp)
	}
	free()
	<-done
	clock.tick(1)
	sp := s.Speeds()
	if len(sp) != 1 || sp[0].Live || !sp[0].Rated || sp[0].Output != 6 {
		t.Fatalf("after the reply: %+v", sp)
	}
	if v := sp[0].Curve[curveSpan-1]; v == nil || *v != 6 {
		t.Fatalf("the reply's second on the curve is %v, want 6 tokens", v)
	}
}

// Codex's own sign-in is served apart from serve: its reply is on the
// session's curve too.
func TestSessionCurveFromCodexOwnSignIn(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	freshRates(t)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":9,"output_tokens":5}}}`))
	})
	s := New()
	clock := &curveClock{}
	clock.at.Store(6_000_000)
	s.curves.now = clock.now
	for range liveRateMin + 1 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("session_id", "codex-sess")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	clock.tick(1)
	sp := s.Speeds()
	if len(sp) != 1 || sp[0].Session != "codex-sess" || !sp[0].Rated {
		t.Fatalf("speeds %+v", sp)
	}
	// four replies of five content bytes, each five tokens
	if sp[0].Output != 20 {
		t.Fatalf("output %d, want 20", sp[0].Output)
	}
}

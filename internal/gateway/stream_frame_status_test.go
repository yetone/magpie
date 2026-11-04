package gateway

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// relayAsks is a Chat request relayed to a provider that speaks Chat itself
// (a plugin's OpenAI-compatible fetch included, #4855).
func relayAsks(t *testing.T, s *Server, reply string) *httptest.ResponseRecorder {
	t.Helper()
	fresh(t)
	setup(t, provider.Chat, &fake{t: t, reply: reply})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	return w
}

// A relayed stream's error frame names its own status (#4855): a plugin
// that already sent 200 can only say what went wrong in the frame, and its
// code and status are the routing signal. The frame is relayed as it came
// and the turn fails as it would at the status it named, not as a 200
// whose words name nothing.
func TestRelayStreamErrorFrameSaysItsStatus(t *testing.T) {
	const frame = `{"error":{"message":"the upstream timed out","type":"upstream_error","code":"upstream_timeout","status":504}}`
	for _, c := range []struct {
		name               string
		reply              string
		wantStatus         int    // the try's failure, and the rest's
		wantFail, wantText string // what the rest says of it
	}{
		{"mid-reply", sse(
			`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"partial text"}}]}`,
			`data: `+frame), 504, failOther, "Gateway Timeout"},
		{"terse 429", sse(
			`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"partial text"}}]}`,
			`data: {"error":{"message":"busy","type":"upstream_error","code":"rate_limit","status":429}}`), 429, failRate, "Too Many Requests"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := New()
			w := relayAsks(t, s, c.reply)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"partial text"`) || !strings.Contains(w.Body.String(), `"status":`+strconv.Itoa(c.wantStatus)) {
				t.Fatalf("the frame was not relayed: %d %s", w.Code, w.Body)
			}
			state := s.Trace(context.Background(), 0, 0)
			if len(state.Routes) != 1 {
				t.Fatalf("routes %d", len(state.Routes))
			}
			var try *Try
			for i := range state.Routes[0].Tries {
				if state.Routes[0].Tries[i].Rest != nil {
					try = &state.Routes[0].Tries[i]
				}
			}
			if try == nil {
				t.Fatalf("the turn rested nobody: %+v", state.Routes[0].Tries)
			}
			if try.Fail != c.wantFail || try.Rest.Status != c.wantStatus {
				t.Fatalf("failed as %s at %d, want %s at %d", try.Fail, try.Rest.Status, c.wantFail, c.wantStatus)
			}
			if !strings.Contains(try.Rest.Error, c.wantText) {
				t.Fatalf("the rest says %q, want it to name %q", try.Rest.Error, c.wantText)
			}
			restingUntil.Lock()
			var rest *Rest
			for _, r := range restingUntil.note {
				rest = &r
			}
			restingUntil.Unlock()
			if rest == nil || rest.Status != c.wantStatus || rest.Why != c.wantFail {
				t.Fatalf("rested %+v, want %s at %d", rest, c.wantFail, c.wantStatus)
			}
		})
	}

	// the frame as the stream's first event, nothing said before it: the
	// frame is relayed as it came, and the try fails at the status it
	// named, not the 502 its words guess
	t.Run("first event", func(t *testing.T) {
		s := New()
		w := relayAsks(t, s, sse(`data: `+frame))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":504`) {
			t.Fatalf("stream: %d %s", w.Code, w.Body)
		}
		state := s.Trace(context.Background(), 0, 0)
		if len(state.Routes) != 1 || state.Routes[0].Status != 504 {
			t.Fatalf("route: %+v", state.Routes)
		}
		for _, u := range usage.Load(time.Time{}) {
			if u.Status != 504 || !u.Failed() {
				t.Fatalf("usage: %+v", u)
			}
		}
	})
}

// An error frame that names no status reads as before: the words guess,
// and a reply whose words name nothing is a 502.
func TestRelayStreamErrorFrameWithoutStatusAsBefore(t *testing.T) {
	s := New()
	w := relayAsks(t, s, sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"partial text"}}]}`,
		`data: {"error":{"type":"server_error","message":"synthetic stream failure"}}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic stream failure") {
		t.Fatalf("stream: %d %s", w.Code, w.Body)
	}
	state := s.Trace(context.Background(), 0, 0)
	var try *Try
	for i := range state.Routes[0].Tries {
		if state.Routes[0].Tries[i].Rest != nil {
			try = &state.Routes[0].Tries[i]
		}
	}
	if try == nil || try.Fail != failOther || try.Rest.Status != 200 {
		t.Fatalf("a frame that named no status failed as %+v, want other at 200 as before", try)
	}
}

// mycf_711's wps-lingxi rows (#4855): the plugin, its 200 sent, puts its
// upstream's status only in the frame's words, "（HTTP 503）", under a
// string code. Main booked the call at 200, its rest "OK: …"; the status
// the words name is the one it fails, rests and is booked at. A 4xx the
// words quote is not read so, and neither is magpie's own loop guard said
// by a magpie this one relays to: no upstream failed for it.
func TestRelayStreamErrorFrameNamesItsStatusInWords(t *testing.T) {
	const said503 = `data: {"error":{"message":"灵犀上游返回 AIError：AI服务异常（上游暂时不可用，可重试）（HTTP 503）","type":"upstream_error","code":"upstream_error"}}`
	const began = `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"partial text"}}]}`
	routeOf := func(t *testing.T, s *Server) (Route, *Try) {
		t.Helper()
		state := s.Trace(context.Background(), 0, 0)
		if len(state.Routes) != 1 {
			t.Fatalf("routes %d", len(state.Routes))
		}
		var try *Try
		for i := range state.Routes[0].Tries {
			if state.Routes[0].Tries[i].Rest != nil {
				try = &state.Routes[0].Tries[i]
			}
		}
		return state.Routes[0], try
	}
	t.Run("mid-reply", func(t *testing.T) {
		s := New()
		w := relayAsks(t, s, sse(began, said503))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "partial text") || !strings.Contains(w.Body.String(), "（HTTP 503）") {
			t.Fatalf("the frame was not relayed: %d %s", w.Code, w.Body)
		}
		route, try := routeOf(t, s)
		if try == nil || try.Rest.Status != 503 || strings.HasPrefix(try.Rest.Error, "OK") {
			t.Fatalf("rested %+v, want at 503", try)
		}
		if route.Status != 503 {
			t.Fatalf("route booked at %d, want 503", route.Status)
		}
		rows := usage.Load(time.Time{})
		if len(rows) != 1 || rows[0].Status != 503 || !rows[0].Failed() {
			t.Fatalf("usage %+v, want one failed row at 503", rows)
		}
	})
	t.Run("first event", func(t *testing.T) {
		s := New()
		relayAsks(t, s, sse(said503))
		route, _ := routeOf(t, s)
		if route.Status != 503 {
			t.Fatalf("route booked at %d, want 503 (the words' guess is 502)", route.Status)
		}
	})
	t.Run("a 4xx quoted", func(t *testing.T) {
		s := New()
		relayAsks(t, s, sse(began, `data: {"error":{"message":"the image link answered (HTTP 404)","type":"upstream_error"}}`))
		route, try := routeOf(t, s)
		if route.Status != 200 || try == nil || try.Rest.Status != 200 {
			t.Fatalf("a quoted 404 was read as the reply's: route %d, try %+v", route.Status, try)
		}
	})
	t.Run("a peer's loop guard", func(t *testing.T) {
		s := New()
		relayAsks(t, s, sse(began, `data: {"error":{"message":"magpie ended this reply: the model's text was stuck in a loop, its last 400 characters the same over and over without a break (\"ab\"), and would have run on to its output limit (HTTP 502)","type":"api_error","code":502}}`))
		route, try := routeOf(t, s)
		if try != nil || route.Status != 200 {
			t.Fatalf("magpie's own loop guard rested %+v, route at %d", try, route.Status)
		}
	})
}

// What names a status, and what doesn't (#4855).
func TestNamedStatusOf(t *testing.T) {
	for raw, want := range map[string]int{
		`{"message":"x","status":504}`:                                   504,
		`{"message":"x","code":429}`:                                     429,
		`{"message":"x","code":"429"}`:                                   0,
		`{"message":"x","status":200}`:                                   0,
		`{"message":"灵犀上游返回 AuditFailed：…（HTTP 502）"}`:                   502,
		`{"message":"upstream said (HTTP 401)"}`:                         0,
		`{"message":"prompt is too long (HTTP 503)"}`:                    0,
		`{"message":"input exceeds the context window","status":500}`:    0,
		`"灵犀上游返回 AIError：AI服务异常（HTTP 503）"`:                              503,
		`{"message":"` + emptyReply + ` (HTTP 502)","status":502}`:       0,
		`{"message":"` + loopGuardSaid + `: … (HTTP 502)","status":502}`: 0,
	} {
		if got := namedStatusOf([]byte(raw)); got != want {
			t.Errorf("namedStatusOf(%s) = %d, want %d", raw, got, want)
		}
	}
}

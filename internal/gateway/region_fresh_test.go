package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// edgeVendor is #5182's Google edge: the connection the first request
// came on is refused for where it comes from, every request on it after
// too, whatever account it carries, while a request on any other
// connection is answered. conns lists each request's connection, in the
// order they came.
type edgeVendor struct {
	srv   *httptest.Server
	mu    sync.Mutex
	bad   string
	conns []string
	who   []string
	// refuse is the refusal the bad connection answers
	refuse string
}

func newEdgeVendor(t *testing.T, h2 bool, refuse string) *edgeVendor {
	v := &edgeVendor{refuse: refuse}
	v.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		switch {
		case strings.Contains(r.URL.Path, ":loadCodeAssist"):
			io.WriteString(w, `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"p1"}`)
			return
		case strings.Contains(r.URL.Path, ":fetchAvailableModels"):
			io.WriteString(w, `{"models":{"m":{}}}`)
			return
		}
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		v.mu.Lock()
		if v.bad == "" {
			v.bad = r.RemoteAddr
		}
		v.conns = append(v.conns, r.RemoteAddr)
		v.who = append(v.who, who)
		bad := r.RemoteAddr == v.bad
		v.mu.Unlock()
		if bad {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, v.refuse)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"from `+who+`"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}}`))
	}))
	v.srv.EnableHTTP2 = h2
	v.srv.StartTLS()
	t.Cleanup(v.srv.Close)
	return v
}

func (v *edgeVendor) asked() (conns, who []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.conns...), append([]string(nil), v.who...)
}

// trustVendor has the gateway's transports, made after it, trust v's
// certificate.
func trustVendor(t *testing.T, v *httptest.Server) {
	pool := x509.NewCertPool()
	pool.AddCert(v.Certificate())
	was := upstreamTLS
	upstreamTLS = &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}
	t.Cleanup(func() { upstreamTLS = was })
}

// #5182: the gateway's own transport (upstreamTransport) doesn't send a
// request on the connection a vendor refused one on for where it comes
// from; a refusal of another kind keeps its connection, as any reply's.
// Over HTTP/2, as Google's front speaks, and HTTP/1.1.
func TestRegionRefusalDropsItsConnection(t *testing.T) {
	for _, h2 := range []bool{true, false} {
		for _, tc := range []struct {
			name, refuse string
			fresh        bool
		}{
			{"region", googleRegion400, true},
			{"other 400", `{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`, false},
		} {
			t.Run(fmt.Sprintf("h2=%v/%s", h2, tc.name), func(t *testing.T) {
				fresh(t)
				v := newEdgeVendor(t, h2, tc.refuse)
				trustVendor(t, v.srv)
				s := New()
				u := "https://" + v.srv.Listener.Addr().String() + "/v1internal:streamGenerateContent"
				ask := func() (int, string) {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(`{}`))
					req.Header.Set("Authorization", "Bearer u@example.com")
					res, err := s.client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer res.Body.Close()
					b, _ := io.ReadAll(res.Body)
					if h2 && res.ProtoMajor != 2 || !h2 && res.ProtoMajor != 1 {
						t.Fatalf("spoken %s", res.Proto)
					}
					return res.StatusCode, string(b)
				}
				if code, body := ask(); code != 400 {
					t.Fatalf("first request: %d %s", code, body)
				}
				code, body := ask()
				conns, _ := v.asked()
				if len(conns) != 2 {
					t.Fatalf("asked on %v", conns)
				}
				if tc.fresh {
					if conns[1] == conns[0] {
						t.Fatalf("the request after the region refusal went on the refused connection %s: %d %s", conns[0], code, body)
					}
					if code != 200 {
						t.Fatalf("the new connection: %d %s", code, body)
					}
				} else if conns[1] != conns[0] {
					t.Fatalf("a plain 400 dropped its connection: %v", conns)
				}
			})
		}
	}
}

// antigravityOnEdge is antigravityGroupFor's Antigravity accounts, their
// upstream v reached through the gateway's region watch (regionFresh) over
// a transport that dials v for Google's hosts.
func antigravityOnEdge(t *testing.T, users []string, v *edgeVendor) *Server {
	s, _ := antigravityGroupFor(t, users, "m", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("asked through the fake transport: %s", r.URL)
	})
	pool := x509.NewCertPool()
	pool.AddCert(v.srv.Certificate())
	addr := v.srv.Listener.Addr().String()
	tr := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	s.client = &http.Client{Transport: rpmTransport{freshAfterRegion(tr)}}
	return s
}

// #5182 (ggdayup): Antigravity refused a request for where it came from,
// and every account after it was sent the same request on the same
// kept-alive connection, refused the same, and rested, the agent told a
// 400, while a new connection was answered. The next account now goes out
// on a new connection and answers, and the account refused rests not.
func TestAntigravityRegionRefusalAsksTheNextOnANewConnection(t *testing.T) {
	v := newEdgeVendor(t, true, googleRegion400)
	s := antigravityOnEdge(t, []string{"u1@example.com", "u2@example.com"}, v)
	code, out := turnOn(t, s, directTurn("You are a helpful assistant.", false))
	conns, who := v.asked()
	if code != 200 || !strings.Contains(out, "from u") {
		t.Fatalf("the turn: %d %s (asked %v on %v)", code, out, who, conns)
	}
	if len(conns) != 2 || conns[0] == conns[1] {
		t.Fatalf("asked %v on %v; want the second on a new connection", who, conns)
	}
	if who[0] == who[1] {
		t.Fatalf("the refused account was asked first again: %v", who)
	}
	if !strings.Contains(out, "from "+who[1]) {
		t.Fatalf("answered by someone else than %s: %s", who[1], out)
	}
	for _, u := range []string{"u1@example.com", "u2@example.com"} {
		if r, ok := restOf("antigravity@" + u); ok {
			t.Fatalf("%s rests for a refusal of where the request came from: %+v", u, r)
		}
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].Status != 400 || r.Tries[0].Rest == nil || r.Tries[0].Rest.By != "off" {
		t.Fatalf("tries %+v", r.Tries)
	}
}

// #5182: the only account, refused for where the request came from, is
// asked again a moment later, on a new connection, and answers.
func TestAntigravityRegionRefusalAsksTheLastAgain(t *testing.T) {
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	v := newEdgeVendor(t, true, googleRegion400)
	s := antigravityOnEdge(t, []string{"u@example.com"}, v)
	code, out := turnOn(t, s, directTurn("You are a helpful assistant.", false))
	conns, who := v.asked()
	if code != 200 || !strings.Contains(out, "from u@example.com") {
		t.Fatalf("the turn: %d %s (asked %v on %v)", code, out, who, conns)
	}
	if len(conns) != 2 || conns[0] == conns[1] {
		t.Fatalf("asked on %v; want the second on a new connection", conns)
	}
	if r, ok := restOf("antigravity@u@example.com"); ok {
		t.Fatalf("the account rests: %+v", r)
	}
}

// #5182: a region refusal rests no account, every route's rest alike
// (restAfter), and counts no failure: the account is asked first again.
// A 500 on the same account still rests it as before.
func TestRegionRefusalRestsNoAccount(t *testing.T) {
	for _, body := range []string{googleRegion400, antigravityRegion400} {
		s, askedUsage := antigravityTurnedAwayGroup(t, []string{"u1@example.com", "u2@example.com"}, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, body)
		})
		for turn := range 2 {
			code, out := turnOn(t, s, claudeTurn("You are a helpful assistant."))
			if code != 200 || !strings.Contains(out, "from the other") {
				t.Fatalf("turn %d: %d %s", turn, code, out)
			}
			r := lastRoute(s)
			if len(r.Tries) != 3 || !strings.HasPrefix(r.Tries[0].ID, "antigravity") || !strings.HasPrefix(r.Tries[1].ID, "antigravity") || r.Tries[0].ID == r.Tries[1].ID || r.Tries[2].ID != "other" {
				t.Fatalf("turn %d asked %+v; want both accounts, then the other", turn, r.Tries)
			}
			for _, try := range r.Tries[:2] {
				if try.Rest == nil || try.Rest.By != "off" || try.Rest.Failures != 0 {
					t.Fatalf("turn %d: a try rested %+v", turn, try.Rest)
				}
			}
		}
		for _, u := range []string{"u1@example.com", "u2@example.com"} {
			if r, ok := restOf("antigravity@" + u); ok {
				t.Fatalf("%s rests for %s: %+v (%v)", u, body, r, rests())
			}
		}
		if *askedUsage != 0 {
			t.Fatalf("usage asked again %d times", *askedUsage)
		}
	}
	// the boundary: Google's own failure still rests the account
	s, _ := antigravityTurnedAwayGroup(t, []string{"u@example.com"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`)
	})
	turnOn(t, s, claudeTurn("You are a helpful assistant."))
	if _, ok := restOf("antigravity@u@example.com"); !ok {
		t.Fatalf("a 500 no longer rests the account: %+v", lastRoute(s).Tries)
	}
}

// #5182 (taoxiaoxin): Google refuses gemini-3.8-flash from where the
// user is, on every account and every connection, while the same
// accounts' Claude models answer. One refusal may be a connection's
// edge, so one more ask goes out on a new connection; refused again, it
// is the place: the provider's other accounts aren't asked the model,
// nobody rests, and the agent is told Google's regional restriction on
// the model, at the 400 it doesn't retry.
func TestRegionRefusalOfTheModelEndsAfterOneMoreAsk(t *testing.T) {
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	for _, users := range [][]string{{"u@example.com"}, {"u1@example.com", "u2@example.com", "u3@example.com"}} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d accounts/stream=%v", len(users), stream), func(t *testing.T) {
				var mu sync.Mutex
				var who []string
				s, askedUsage := antigravityTurnedAwayGroup(t, users, func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					who = append(who, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, googleRegion400)
				})
				code, out := turnOn(t, s, directTurn("You are a helpful assistant.", stream))
				mu.Lock()
				asked := slices.Clone(who)
				mu.Unlock()
				if len(asked) != 2 {
					t.Fatalf("asked %v; want one more ask after the first refusal, then none", asked)
				}
				if code != http.StatusBadRequest {
					t.Fatalf("the agent got %d: %s", code, out)
				}
				for _, want := range []string{"User location is not supported", "Google doesn't serve m to requests from the region", "not on the account"} {
					if !strings.Contains(out, want) {
						t.Fatalf("the agent isn't told %q: %s", want, out)
					}
				}
				for _, u := range users {
					if r, ok := restOf("antigravity@" + u); ok {
						t.Fatalf("%s rests: %+v", u, r)
					}
				}
				if *askedUsage != 0 {
					t.Fatalf("usage asked again %d times", *askedUsage)
				}
			})
		}
	}
}

// regionNote names the model and the vendor for a region refusal, and
// nothing for another 400 or a success that quotes the words.
func TestRegionNote(t *testing.T) {
	if n := regionNote("gemini-3.8-flash", 400, []byte(googleRegion400)); !strings.Contains(n, "Google doesn't serve gemini-3.8-flash") {
		t.Fatalf("Google's: %q", n)
	}
	if n := regionNote("gpt-5", 403, []byte(`{"error":{"code":"unsupported_country_region_territory","message":"Country, region, or territory not supported"}}`)); !strings.HasPrefix(n, "the vendor doesn't serve gpt-5") {
		t.Fatalf("OpenAI's: %q", n)
	}
	for _, c := range []struct {
		status int
		body   string
	}{{400, `{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`}, {200, googleRegion400}} {
		if n := regionNote("m", c.status, []byte(c.body)); n != "" {
			t.Fatalf("%d %s: %q", c.status, c.body, n)
		}
	}
}

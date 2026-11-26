package gateway

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// heldHeaders holds each request for hold before its headers, as a vendor
// thinking before it answers, then streams a reply.
type heldHeaders struct{ hold time.Duration }

func (h heldHeaders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	time.Sleep(h.hold)
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, chatStreamOK)
}

// A try says where its time went up to the reply's first byte (TryNet):
// a new connection's DNS, connect and TLS, writing the request, and the
// vendor's wait before its headers, which is where a vendor's slow start
// shows and not as magpie's (Discord, Magic_zero). The next request on
// the open connection spends none of the three. The route has the
// usage record's split of the time before the request went out.
func TestTryTellsWhereItsTimeWent(t *testing.T) {
	fresh(t)
	const hold = 300 * time.Millisecond
	up := httptest.NewTLSServer(heldHeaders{hold: hold})
	t.Cleanup(up.Close)
	// by name, so the resolver is asked
	base := strings.Replace(up.URL, "127.0.0.1", "localhost", 1) + "/v1"
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m"}, Chat: base}); err != nil {
		t.Fatal(err)
	}
	s := New()
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(tr.CloseIdleConnections)
	s.client = &http.Client{Transport: tr}
	ask := func() (Route, TryNet) {
		t.Helper()
		code, body := postAs(t, s, "", `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "from b") {
			t.Fatalf("%d %s", code, body)
		}
		r := lastRoute(s)
		if len(r.Tries) != 1 || r.Tries[0].Net == nil {
			t.Fatalf("no timing: %+v", r.Tries)
		}
		return r, *r.Tries[0].Net
	}
	r, n := ask()
	t.Logf("a new connection: %+v", n)
	// the vendor's wait is the hold, give or take the machine. The hold
	// starts once the vendor has read the body, which can be before the
	// write is seen done here (a busy machine runs the trace's hook late),
	// so some of it may be counted in Send: the two together hold it, and
	// the wait is still the most of it.
	holdMs := float64(hold.Milliseconds())
	if n.Send+n.Wait < holdMs-1 || n.Wait < holdMs/2 || n.Wait > holdMs+5000 {
		t.Fatalf("wait %v for a %v hold: %+v", n.Wait, hold, n)
	}
	if n.Reused || n.TLS <= 0 || n.Asks != 1 || n.Before != 0 {
		t.Fatalf("a new connection: %+v", n)
	}
	// each step is in the time to the first byte, in order
	if sum := n.Own + n.Pool + n.DNS + n.Connect + n.TLS + n.Send + n.Wait; sum > n.Head+1 {
		t.Fatalf("steps %v over the %v to the first byte: %+v", sum, n.Head, n)
	}
	if try := r.Tries[0]; float64(try.TTFT) < n.Head-1 || float64(try.Millis) < n.Head-1 {
		t.Fatalf("first byte %v after the first token %d or the end %d", n.Head, try.TTFT, try.Millis)
	}
	if r.Sent <= 0 || r.Own <= 0 || r.Sent > r.TTFT || r.Own > r.Sent {
		t.Fatalf("route: sent %d own %d ttft %d", r.Sent, r.Own, r.TTFT)
	}
	// the open connection carries the next one
	_, n = ask()
	if !n.Reused || n.DNS != 0 || n.Connect != 0 || n.TLS != 0 || n.Send+n.Wait < holdMs-1 || n.Wait < holdMs/2 {
		t.Fatalf("the open connection: %+v", n)
	}
}

// A try that sent nothing through Go's client (a plugin's own fetch) has
// no timing, rather than one made of nothing.
func TestTryNetWithoutARequest(t *testing.T) {
	var n netTrace
	if got := n.timing(time.Now(), 0); got != nil {
		t.Fatalf("%+v", got)
	}
}

// lute on Discord: some requests on the Routing page said where their
// time went and some didn't. A plugin sends its request itself, through
// its host, so magpie has no timing for it: its try says who sent it,
// which the page shows in the timing's place.
func TestPluginTrySaysWhoSentIt(t *testing.T) {
	pid := besideFake(t, "cursor", &cursorUp{})
	r := postProto(t, New(), "/v1/chat/completions", `{"model":"`+pid+`/fake-1","messages":[{"role":"user","content":"hi"}]}`)
	if len(r.Tries) != 1 || r.Tries[0].Net != nil || r.Tries[0].SentBy != "plugin" {
		t.Fatalf("tries %+v", r.Tries)
	}
}

// Who sent a try: a plugin, Claude Code for a Claude subscription, and
// magpie itself, whose try has its timing, for the rest.
func TestSentBy(t *testing.T) {
	for _, c := range []struct {
		p    provider.Provider
		want string
	}{
		{provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "u"}}, "claude"},
		{provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "u"}}, ""},
		{provider.Provider{ID: "a"}, ""},
	} {
		if got := sentBy(c.p); got != c.want {
			t.Errorf("sentBy(%s) = %q, want %q", c.p.ID, got, c.want)
		}
	}
}

// buhaodeshiyehao on X (Codex through Lody): the Routing page's "Where
// the time went" said only a turn's masking. Codex's turns to the
// ChatGPT backend, and to a Responses provider with Socket, go over a
// WebSocket, and a conversation's turns after its first go on the kept
// socket, with no HTTP request the try's trace saw, so the try had no
// timing. Each turn on a socket, a new one or a kept one, says where its
// time went: the backend's wait before the first frame is the vendor's.
func TestSocketTurnTellsWhereItsTimeWent(t *testing.T) {
	const hold = 200 * time.Millisecond
	holdMs := float64(hold.Milliseconds())
	check := func(t *testing.T, what string, try Try) {
		t.Helper()
		n := try.Net
		if n == nil {
			t.Fatalf("%s: no timing", what)
		}
		t.Logf("%s: %+v", what, *n)
		// the hold starts once the backend has read the message, which can
		// be before the write is seen done here (the trace's WroteRequest
		// runs after Write returns, and on a kept socket after its probe),
		// so some of it may be counted in Send, as on HTTP (408a9d8ba,
		// #7917): the two together hold it, and the wait is most of it
		if n.Send+n.Wait < holdMs-1 || n.Wait < holdMs/2 || n.Wait > holdMs+5000 || n.Head < n.Wait {
			t.Fatalf("%s: timing %+v for a %v hold", what, *n, hold)
		}
		if sum := n.Own + n.Before + n.Pool + n.DNS + n.Connect + n.TLS + n.Send + n.Wait; sum > n.Head+1 {
			t.Fatalf("%s: steps %v over the %v to the first byte: %+v", what, sum, n.Head, *n)
		}
		if float64(try.Millis) < n.Head-1 {
			t.Fatalf("%s: first byte %v after the end %d", what, n.Head, try.Millis)
		}
	}
	t.Run("codex sign-in", func(t *testing.T) {
		socketOn(t)
		f := &fakeSocketBackend{keep: true, hold: hold}
		f.frames = keptReply(f)
		chatgpt(t, f.ServeHTTP)
		s := New()
		post := func(body string) Try {
			t.Helper()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer chatgpt-token")
			req.Header.Set("chatgpt-account-id", "acct-1")
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			r := lastRoute(s)
			if len(r.Tries) != 1 {
				t.Fatalf("tries %+v", r.Tries)
			}
			return r.Tries[0]
		}
		check(t, "a new socket", post(keptTurn("conv-1", "gpt-5.5", userPing)))
		kept := post(keptTurn("conv-1", "gpt-5.5", userPing, asstPong, userAgain))
		if posts, sockets := f.counts(); posts != 0 || sockets != 2 || f.opened() != 1 {
			t.Fatalf("%d posts, %d turns on %d sockets; want both on one", posts, sockets, f.opened())
		}
		check(t, "the kept socket", kept)
		if n := kept.Net; !n.Reused || n.DNS != 0 || n.Connect != 0 || n.TLS != 0 {
			t.Fatalf("the kept socket dialed: %+v", *n)
		}
	})
	t.Run("responses provider", func(t *testing.T) {
		socketOn(t)
		f := &fakeSocketBackend{keep: true, hold: hold}
		f.frames = keptReply(f)
		relaySocket(t, f, provider.SocketOn)
		s := New()
		post := func(body string) Try {
			t.Helper()
			code, out := postProtoBody(t, s, "/v1/responses", body)
			if code != 200 || !strings.Contains(out, "pong") {
				t.Fatalf("%d %s", code, out)
			}
			r := lastRoute(s)
			if len(r.Tries) != 1 {
				t.Fatalf("tries %+v", r.Tries)
			}
			return r.Tries[0]
		}
		check(t, "a new socket", post(keptTurn("conv-2", "relay/m1", userPing)))
		kept := post(keptTurn("conv-2", "relay/m1", userPing, asstPong, userAgain))
		if posts, sockets := f.counts(); posts != 0 || sockets != 2 || f.opened() != 1 {
			t.Fatalf("%d posts, %d turns on %d sockets; want both on one", posts, sockets, f.opened())
		}
		check(t, "the kept socket", kept)
	})
}

func postProtoBody(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

package gateway

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// heldStream is a fresh HOME whose provider "plan" streams one chunk, then
// holds the rest until release is sent to (or the request ends).
func heldStream(t *testing.T) (release chan struct{}) {
	t.Helper()
	fresh(t)
	release = make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"first \"}}]}\n\n")
		f.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"last\"},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return release
}

// startStream sends a streamed chat to addr and reads up to its first
// chunk.
func startStream(t *testing.T, addr string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, _ := http.NewRequest("POST", "http://"+addr+"/v1/chat/completions", strings.NewReader(`{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	stream := bufio.NewReader(resp.Body)
	for {
		line, err := stream.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended before its first chunk: %v", err)
		}
		if strings.Contains(line, "first") {
			return resp, stream
		}
	}
}

func rootStatus(t *testing.T, addr string) (int, string) {
	t.Helper()
	r, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

// @justforlxz runs magpie in Kubernetes, and a rolling update's SIGTERM
// cut the requests in flight: the gateway's end gave them 2s. Drained
// (serve's SIGTERM), it answers GET / 503, so the readiness probe takes it
// out of the Service, stops accepting as it ends, and a stream an agent is
// reading then runs to its end, well past those 2s.
func TestDrainedGatewayFinishesItsStream(t *testing.T) {
	release := heldStream(t)
	oldShort := shortDrain
	shortDrain = 10 * time.Millisecond
	t.Cleanup(func() { shortDrain = oldShort })

	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	s := New()
	stop, done := listening(t, s)
	defer stop()
	s.lnMu.Lock()
	addr := s.ln.Addr().String()
	s.lnMu.Unlock()

	if code, b := rootStatus(t, addr); code != 200 || strings.Contains(b, "draining") {
		t.Fatalf("GET / before the drain: %d %s", code, b)
	}
	_, stream := startStream(t, addr)

	s.Drain(time.Minute)
	if code, b := rootStatus(t, addr); code != http.StatusServiceUnavailable || !strings.Contains(b, `"draining":true`) || !strings.Contains(b, `"magpie"`) {
		t.Fatalf("GET / while draining: %d %s, want 503 and draining", code, b)
	}
	stop()

	// no new connection is taken once it ends
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("the ending gateway still accepts connections")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// and its end waits for the stream, well past shortDrain
	select {
	case err := <-done:
		t.Fatalf("the gateway ended with a stream in flight: %v", err)
	case <-time.After(50 * shortDrain):
	}
	if b := s.Busy(); b.Requests != 1 {
		t.Fatalf("%d requests in flight, want the stream", b.Requests)
	}
	release <- struct{}{}
	rest, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("the stream was cut: %v", err)
	}
	if !strings.Contains(string(rest), "last") || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("the stream's end didn't arrive: %q", rest)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the gateway never ended once its stream had")
	}
}

// The drain is bounded: a stream past its grace is cut, and the gateway
// ends, as Kubernetes would kill it at terminationGracePeriodSeconds.
func TestDrainEndsAtItsGrace(t *testing.T) {
	release := heldStream(t)
	defer close(release)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	s := New()
	stop, done := listening(t, s)
	defer stop()
	s.lnMu.Lock()
	addr := s.ln.Addr().String()
	s.lnMu.Unlock()
	startStream(t, addr)
	s.Drain(100 * time.Millisecond)
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the drain outlived its grace")
	}
	// the cut request's handler outlives the gateway's end, holding the
	// provider catalog (holdCatalog) until it returns: the next test's
	// catalog would be read through the hold (TestGrokGetsAgentMessageAsUser
	// found migrations.json unread in a full run)
	for deadline := time.Now().Add(30 * time.Second); s.Busy().Requests != 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the cut stream is still in flight: %d", s.Busy().Requests)
		}
	}
}

// A rolling update's old pod keeps the connections agents kept alive to
// it after the Service sends new ones elsewhere, and its end closed each
// one after its reply with no word of it, so an agent's next request on it
// went out on a closed connection: a broken pipe, once for each, in a real
// rollout of three pods. Once draining, every reply says Connection:
// close, so that next request dials the Service again.
func TestDrainingRepliesCloseTheirConnection(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	s := New()
	stop, done := listening(t, s)
	defer func() { stop(); <-done }()
	s.lnMu.Lock()
	addr := s.ln.Addr().String()
	s.lnMu.Unlock()
	get := func() *http.Response {
		t.Helper()
		r, err := http.Get("http://" + addr + "/v1/models")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		return r
	}
	if r := get(); r.Close {
		t.Fatal("a reply before the drain closed its connection")
	}
	s.Drain(time.Minute)
	if r := get(); !r.Close {
		t.Fatalf("a draining gateway's reply (%d) kept its connection alive", r.StatusCode)
	}
}

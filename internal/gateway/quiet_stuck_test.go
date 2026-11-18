package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

const (
	stuckStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"
	stuckRest  = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"answered\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

// quietClaude serves a Claude relay on Messages that, for each request,
// sends its headers and head (none at all with noHead), says nothing for
// quiet, and then the rest of its reply. asked counts its requests.
func quietClaude(t *testing.T, head string, noHead bool, quiet time.Duration) *atomic.Int32 {
	t.Helper()
	asked := new(atomic.Int32)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		asked.Add(1)
		if !noHead {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, head)
			w.(http.Flusher).Flush()
		}
		select {
		case <-time.After(quiet):
		case <-r.Context().Done():
			return
		}
		if noHead {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, stuckStart)
		}
		io.WriteString(w, stuckRest)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"claude-fable-5-2"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	return asked
}

// askQuietClaude streams a Messages request for relay/claude-fable-5-2, as pi
// and dsh ask a Claude, and tells the body and the longest the client
// went without a byte, from the request on.
func askQuietClaude(t *testing.T) (string, time.Duration, time.Duration) {
	t.Helper()
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	began := time.Now()
	resp, err := http.Post(gw.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"relay/claude-fable-5-2","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	last := time.Now()
	gap := last.Sub(began)
	var b strings.Builder
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			gap = max(gap, time.Since(last))
			last = time.Now()
			b.WriteString(line)
		}
		if err != nil {
			break
		}
	}
	return b.String(), gap, time.Since(began)
}

// Wing8Aqua on X: pi asked Claude Fable 5.2 through magpie and gave up
// with "stream idle timeout after 300000ms". A relayed Claude stream, held
// before its headers or quiet after message_start, was kept alive with
// comments for keepaliveLongest (5 minutes) after the vendor was last
// heard, then left with nothing, and pi's 300s idle timeout ended it at
// ~10 minutes with no word of why. It is kept alive as long as a
// translated stream is (translatedQuietLongest): here the old 5 minutes
// is 100ms, the vendor is quiet for 1.5s, and the client, whose idle
// timeout stands for pi's, must never go 500ms without a byte.
func TestQuietClaudeStreamKeptAlivePastFiveMinutes(t *testing.T) {
	for _, c := range []struct {
		name   string
		head   string
		noHead bool
	}{
		{"quiet after message_start", stuckStart, false},
		{"quiet before its headers", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			quietFast(t)
			keepaliveLongest = 100 * time.Millisecond
			was := translatedQuietLongest
			translatedQuietLongest = 10 * time.Second
			t.Cleanup(func() { translatedQuietLongest = was })
			asked := quietClaude(t, c.head, c.noHead, 1500*time.Millisecond)
			body, gap, _ := askQuietClaude(t)
			if !strings.Contains(body, "answered") || !strings.Contains(body, "message_stop") {
				t.Fatalf("the reply didn't come whole:\n%s", body)
			}
			if gap >= 500*time.Millisecond {
				t.Fatalf("the client heard nothing for %v while the vendor was quiet (an idle timeout of 500ms ends it):\n%s", gap, body)
			}
			if n := asked.Load(); n != 1 {
				t.Fatalf("the vendor was asked %d times, want 1", n)
			}
		})
	}
}

// A vendor that says nothing for translatedQuietLongest while the client
// is kept alive is let go, and the client is told so in its own protocol,
// naming the provider: before, the comments stopped and the client met
// only its own idle timeout. Nobody is asked again after so long a wait.
func TestQuietClaudeStreamStuckEndsWithAnError(t *testing.T) {
	for _, c := range []struct {
		name   string
		head   string
		noHead bool
	}{
		{"quiet after message_start", stuckStart, false},
		{"quiet before its headers", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			quietFast(t)
			keepaliveLongest = 100 * time.Millisecond
			was := translatedQuietLongest
			translatedQuietLongest = 400 * time.Millisecond
			t.Cleanup(func() { translatedQuietLongest = was })
			asked := quietClaude(t, c.head, c.noHead, 5*time.Second)
			body, gap, took := askQuietClaude(t)
			if !strings.Contains(body, "event: error") || !strings.Contains(body, "Relay sent nothing for") {
				t.Fatalf("no error told the client of the stuck vendor:\n%s", body)
			}
			if strings.Contains(body, "answered") {
				t.Fatalf("the reply went on after its error:\n%s", body)
			}
			if took >= 4*time.Second {
				t.Fatalf("the stream ended after %v, with the vendor's own end at 5s: it wasn't let go", took)
			}
			if gap >= 300*time.Millisecond {
				t.Fatalf("the client heard nothing for %v before the error:\n%s", gap, body)
			}
			if n := asked.Load(); n != 1 {
				t.Fatalf("the vendor was asked %d times, want 1", n)
			}
		})
	}
}

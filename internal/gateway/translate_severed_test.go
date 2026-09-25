package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// severed speaks HTTP until it doesn't: it writes headers that promise more
// body than it sends, delivers the events given, then closes the connection.
// The client's stream read fails the way an upstream dying mid-reply fails.
func severed(t *testing.T, events ...string) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hj, ok := w.(http.Hijacker)
		if !ok {
			panic("no hijacker")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		body := strings.Join(events, "")
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", len(body)+100)
		io.WriteString(buf, body)
		buf.Flush()
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"m"}, Anthropic: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

const (
	anthropicStart = "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"m\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"
	anthropicText = "event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n"
	anthropicStop = "event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
)

// A client waiting on one JSON answer was handed whatever arrived before the
// upstream died, as if the reply were whole.
func TestTranslateBufferedSeveredUpstream(t *testing.T) {
	fresh(t)
	severed(t, anthropicStart, anthropicText)
	code, body := postAs(t, New(), "", `{"model":"up/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 502 {
		t.Fatalf("status = %d, want 502; body: %s", code, body)
	}
	if !strings.Contains(body, "UP:") {
		t.Fatalf("error does not name the provider: %s", body)
	}
}

// A streaming client got a synthetic happy ending - finish reason, usage,
// [DONE] - over a reply the upstream never finished.
func TestTranslateStreamSeveredUpstream(t *testing.T) {
	fresh(t)
	severed(t, anthropicStart, anthropicText)
	code, body := postAs(t, New(), "", `{"model":"up/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	if !strings.Contains(body, `"error"`) {
		t.Fatalf("no error event in the stream: %s", body)
	}
	if strings.Contains(body, "[DONE]") || strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("a severed stream was finished as if complete: %s", body)
	}
}

// A stream the upstream finished properly still translates end to end,
// error-free and complete.
func TestTranslateStreamCompleteUpstream(t *testing.T) {
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicStart+anthropicText+anthropicStop)
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"m"}, Anthropic: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}

	code, body := postAs(t, New(), "", `{"model":"up/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "[DONE]") || strings.Contains(body, `"error"`) {
		t.Fatalf("streamed: status %d; body: %s", code, body)
	}
	code, body = postAs(t, New(), "", `{"model":"up/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"hel"`) || strings.Contains(body, `"error"`) {
		t.Fatalf("buffered: status %d; body: %s", code, body)
	}
}

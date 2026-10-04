package gateway

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const pluginReplyChunk = 8 << 10

var pluginReplyText = strings.Repeat("0123456789abcdef", pluginReplyChunk/16)

func pluginReplyFrame() string {
	return `data: {"id":"large","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"` +
		pluginReplyText + `"},"finish_reason":null}]}` + "\n\n"
}

// The downstream is a real HTTP client, so its pace backpressures the
// gateway, not just a fake plugin reader. All upstream traffic stays local.
func pluginReplyGateway(t *testing.T, vendor http.Handler) (string, string, *http.Client) {
	t.Helper()
	pid := besideFake(t, "stream-regression", vendor)
	gw := httptest.NewUnstartedServer(New().Handler())
	gw.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		if c, ok := c.(*net.TCPConn); ok {
			_ = c.SetWriteBuffer(32 << 10)
		}
		return ctx
	}
	gw.Start()
	t.Cleanup(func() {
		gw.CloseClientConnections()
		gw.Close()
	})
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DisableCompression = true
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err == nil {
			if tcp, ok := c.(*net.TCPConn); ok {
				_ = tcp.SetReadBuffer(32 << 10)
			}
		}
		return c, err
	}
	t.Cleanup(tr.CloseIdleConnections)
	return gw.URL, pid + "/fake-1", &http.Client{Transport: tr, Timeout: 75 * time.Second}
}

func pluginReplyRequest(model string, stream bool) string {
	return fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"local fixture"}]}`, model, stream)
}

type pluginPacedReader struct {
	r       io.Reader
	rate    int64
	read    int64
	started time.Time
}

func (p *pluginPacedReader) Read(b []byte) (int, error) {
	if p.started.IsZero() {
		p.started = time.Now()
	}
	n, err := p.r.Read(b)
	p.read += int64(n)
	if n > 0 && p.rate > 0 {
		due := p.started.Add(time.Duration(p.read * int64(time.Second) / p.rate))
		if wait := time.Until(due); wait > 0 {
			time.Sleep(wait)
		}
	}
	return n, err
}

func pluginReplyComplete(t *testing.T, size int, stream bool, rate int64) time.Duration {
	t.Helper()
	produced := make(chan time.Time, 1)
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Plugin accounts require an upstream stream even for a downstream
		// JSON reply; the gateway assembles that reply from these events.
		w.Header().Set("Content-Type", "text/event-stream")
		frame := pluginReplyFrame()
		for i := 0; i < size/pluginReplyChunk; i++ {
			if _, err := io.WriteString(w, frame); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		select {
		case produced <- time.Now():
		default:
		}
	})
	url, model, client := pluginReplyGateway(t, up)
	start := time.Now()
	res, err := client.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(pluginReplyRequest(model, stream)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	reader := &pluginPacedReader{r: res.Body, rate: rate}
	got := sha256.New()
	total := 0
	if !stream {
		var reply struct {
			Choices []struct {
				Message struct{ Content string }
			}
		}
		if err := json.NewDecoder(reader).Decode(&reply); err != nil {
			t.Fatalf("JSON reply: %v", err)
		}
		if len(reply.Choices) != 1 {
			t.Fatalf("JSON choices = %d, want 1", len(reply.Choices))
		}
		text := reply.Choices[0].Message.Content
		total = len(text)
		_, _ = io.WriteString(got, text)
	} else {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 16<<10), 256<<10)
		done := false
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				done = true
				continue
			}
			var event struct {
				Choices []struct {
					Delta struct{ Content string }
				}
				Error json.RawMessage
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatalf("SSE event after %d content bytes: %v; tail %q", total, err, data[max(0, len(data)-512):])
			}
			if len(event.Error) > 0 {
				t.Fatalf("SSE error after %d content bytes: %s", total, event.Error)
			}
			for _, choice := range event.Choices {
				total += len(choice.Delta.Content)
				_, _ = io.WriteString(got, choice.Delta.Content)
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("SSE read after %d content bytes: %v", total, err)
		}
		if !done {
			t.Fatalf("SSE ended without [DONE] after %d content bytes", total)
		}
	}
	want := sha256.New()
	for i := 0; i < size/pluginReplyChunk; i++ {
		_, _ = io.WriteString(want, pluginReplyText)
	}
	if total != size || string(got.Sum(nil)) != string(want.Sum(nil)) {
		t.Fatalf("reply bytes/hash = %d/%x, want %d/%x", total, got.Sum(nil), size, want.Sum(nil))
	}
	elapsed := time.Since(start)
	select {
	case at := <-produced:
		t.Logf("content=%d wire_read=%d rate=%dB/s elapsed=%v vendor_finished_after=%v", total, reader.read, rate, elapsed, at.Sub(start))
	case <-time.After(time.Second):
		t.Fatal("the vendor did not finish its reply")
	}
	return elapsed
}

func TestPluginStreamLargeReplies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		rate   int64
	}{
		{"JSON fast", false, 0},
		{"JSON throttled", false, 3 << 20},
		{"SSE fast", true, 0},
		{"SSE throttled", true, 3 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pluginReplyComplete(t, 24<<20, tc.stream, tc.rate)
		})
	}
}

// The downstream keeps reading for longer than the ten-second stall window.
// Its duration is observable here; the private plugin queue's watermark is
// not. The host's focused tests cover progress while above that mark.
func TestPluginStreamLongActiveReader(t *testing.T) {
	elapsed := pluginReplyComplete(t, 40<<20, true, 5<<19)
	if elapsed < 12*time.Second {
		t.Fatalf("long throttled reader ran for %v, want more than the stall window", elapsed)
	}
}

func TestPluginStreamStalledClientKeepsOtherRequestsMoving(t *testing.T) {
	sent := make(chan struct{}, 1)
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Messages []struct{ Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(q.Messages) == 1 && q.Messages[0].Content == "second fixture" {
			_, _ = io.WriteString(w, `data: {"id":"small","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		frame := pluginReplyFrame()
		for i := 0; i < (8<<20)/pluginReplyChunk; i++ {
			if _, err := io.WriteString(w, frame); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if i == (1<<20)/pluginReplyChunk-1 {
				select {
				case sent <- struct{}{}:
				default:
				}
			}
		}
		<-r.Context().Done()
	})
	url, model, client := pluginReplyGateway(t, up)
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(url, "http://"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(16 << 10)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	body := pluginReplyRequest(model, true)
	if _, err := fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: fixture\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(line, " 200 ") {
		t.Fatalf("stalled client's status = %q, %v", line, err)
	}
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the vendor did not stream a backlog")
	}
	// Only the status line was consumed. Let the finite socket buffers fill.
	time.Sleep(250 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	second := strings.Replace(pluginReplyRequest(model, false), "local fixture", "second fixture", 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/chat/completions", strings.NewReader(second))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("second plugin request with a stalled reader: %v", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK || !strings.Contains(string(b), `"content":"ok"`) {
		t.Fatalf("second plugin reply = %d %q, %v", res.StatusCode, b, err)
	}
	t.Logf("second plugin request completed in %v while the first client stopped reading", time.Since(start))
}

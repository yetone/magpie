package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/provider"
)

func TestRequestBodyTooLarge(t *testing.T) {
	for _, api := range []string{"chat", "responses", "messages", "count", "gemini", "codex", "images", "edits", "videos"} {
		t.Run(api, func(t *testing.T) {
			s := New()
			r := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
			r.ContentLength = 129 << 20
			w := httptest.NewRecorder()
			switch api {
			case "chat":
				s.handle(provider.Chat)(w, r)
			case "responses":
				s.handle(provider.Responses)(w, r)
			case "messages":
				s.handle(provider.Anthropic)(w, r)
			case "count":
				s.countTokens(w, r)
			case "codex":
				s.codexBackend(w, r)
			case "images":
				s.images(false)(w, r)
			case "edits":
				s.images(true)(w, r)
			case "videos":
				s.videosCreate(w, r)
			case "gemini":
				r.SetPathValue("call", "m:generateContent")
				s.gemini(w, r)
			}
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", w.Code)
			}
		})
	}
}

func TestRequestBodySocketTimeout(t *testing.T) {
	s := New()
	s.requestLimits.readTimeout = 40 * time.Millisecond
	gw := httptest.NewServer(s.handle(provider.Chat))
	defer gw.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 20\r\n\r\n{")
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 408 {
		t.Fatalf("slow body: status %d", res.StatusCode)
	}
}

func TestRequestBodyDeadlineCleared(t *testing.T) {
	s := New()
	s.requestLimits.readTimeout = 30 * time.Millisecond
	var connections atomic.Int64
	gw := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadlines := []time.Time{}
		_, ok := s.requestBody(deadlineRecorder{w, &deadlines}, r, provider.Chat)
		if len(deadlines) < 3 || !deadlines[len(deadlines)-1].IsZero() {
			t.Errorf("body deadline was not cleared: %v", deadlines)
		}
		if !ok {
			return
		}
		io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
		// The read deadline must be cleared before the first response byte;
		// otherwise the server closes keepalive input during a long stream.
		io.WriteString(w, "last\n")
	}))
	gw.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	gw.Start()
	defer gw.Close()
	for i := 0; i < 2; i++ {
		res, err := gw.Client().Post(gw.URL, "application/json", strings.NewReader("abc"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || string(b) != "first\nlast\n" {
			t.Fatalf("long response = %q, %v", b, err)
		}
	}
	if n := connections.Load(); n != 1 {
		t.Fatalf("streaming deadline broke keepalive: %d connections", n)
	}
}

func TestCodexExpandedBodyBound(t *testing.T) {
	for _, enc := range []string{"gzip", "zstd"} {
		t.Run(enc, func(t *testing.T) {
			var compressed bytes.Buffer
			if enc == "gzip" {
				zw := gzip.NewWriter(&compressed)
				zw.Write(bytes.Repeat([]byte("x"), 2048))
				zw.Close()
			} else {
				zw, err := zstd.NewWriter(&compressed)
				if err != nil {
					t.Fatal(err)
				}
				zw.Write(bytes.Repeat([]byte("x"), 2048))
				zw.Close()
			}
			s := New()
			s.requestLimits.body = 1024
			r := httptest.NewRequest("POST", CodexPath+"/responses", &compressed)
			r.Header.Set("Content-Encoding", enc)
			w := httptest.NewRecorder()
			s.codexBackend(w, r)
			if w.Code != 413 {
				t.Fatalf("inflated %s: status %d", enc, w.Code)
			}
		})
	}
}

func TestRequestBodyUnknownLength(t *testing.T) {
	s := New()
	s.requestLimits.body = 16
	for _, size := range []int{16, 17} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", size)))
		r.ContentLength = -1
		w := httptest.NewRecorder()
		body, ok := s.requestBody(w, r, provider.Chat)
		if size == 16 && (!ok || len(body) != size) {
			t.Fatalf("exact limit: %d, %v", len(body), ok)
		}
		if size == 17 && (ok || w.Code != 413) {
			t.Fatalf("unknown length overflow: %d, %v", w.Code, ok)
		}
	}
}

func TestRequestBodyReadError(t *testing.T) {
	s := New()
	r := httptest.NewRequest("POST", "/", nil)
	r.Body = io.NopCloser(io.MultiReader(strings.NewReader("abc"), failedBodyReader{}))
	w := httptest.NewRecorder()
	if _, ok := s.requestBody(w, r, provider.Chat); ok || w.Code != 400 {
		t.Fatalf("read failure: status %d", w.Code)
	}
}

type failedBodyReader struct{}

func (failedBodyReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// The read deadline ends with the body, before any generation/stream starts.
type deadlineRecorder struct {
	http.ResponseWriter
	deadlines *[]time.Time
}

func (w deadlineRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w deadlineRecorder) SetReadDeadline(at time.Time) error {
	*w.deadlines = append(*w.deadlines, at)
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(at)
}

func TestRejectedBodySocketDoesNotDrain(t *testing.T) {
	s := New()
	s.requestLimits.body = 4
	gw := httptest.NewServer(s.handle(provider.Chat))
	defer gw.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 20\r\n\r\n{")
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("rejected slow input: status %d", res.StatusCode)
	}
}

func TestRequestBodyLargeConversations(t *testing.T) {
	s := New()
	payload := bytes.Repeat([]byte("x"), 30<<20)
	// The old retained 256 MiB budget rejected the ninth 30 MiB conversation.
	for i := 0; i < 9; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", bytes.NewReader(payload))
		body, ok := s.requestBody(w, r, provider.Chat)
		if !ok || len(body) != len(payload) {
			t.Fatalf("conversation %d refused: status %d", i+1, w.Code)
		}
	}
}

func TestRequestBodiesDoNotLimitStreams(t *testing.T) {
	s := New()
	stop := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requestBody(w, r, provider.Chat); !ok {
			return
		}
		io.WriteString(w, "started\n")
		w.(http.Flusher).Flush()
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	defer gw.Close()
	defer close(stop)
	client := &http.Client{Timeout: 10 * time.Second}
	var responses []*http.Response
	defer func() {
		for _, r := range responses {
			r.Body.Close()
		}
	}()
	// The old budget refused stream 129 before routing. Each handler stays live.
	for i := 0; i < 160; i++ {
		res, err := client.Post(gw.URL, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, res)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("stream %d refused: %d", i+1, res.StatusCode)
		}
	}
}

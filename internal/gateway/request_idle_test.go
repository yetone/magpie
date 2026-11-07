package gateway

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func TestRequestBodyProgressRenewsIdleDeadline(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			payload := bytes.Repeat([]byte("body"), 128)
			wire := encodedBody(t, payload, encoding)
			s := New()
			// The idle window is ten times the gap between writes: the
			// client and the gateway share one process with the whole
			// package's tests, and a loaded runner stalled a 50ms gap past
			// a 200ms window, a real idle the gateway rightly refused
			// (#2567).
			idle := time.Second
			gap := idle / 10
			s.requestLimits.readTimeout = idle
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, ok := s.readRequestBody(w, r, provider.Responses, 0)
				if !ok {
					return
				}
				if !bytes.Equal(body, payload) {
					t.Error("body changed")
				}
				io.WriteString(w, "ok")
			}))
			defer gw.Close()
			conn, err := net.Dial("tcp", strings.TrimPrefix(gw.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: %d\r\nContent-Encoding: %s\r\n\r\n", len(wire), encoding)
			// Sixteen progressing writes take longer than one idle window,
			// so only renewing on progress lets the upload finish.
			const writes = 16
			step := (len(wire) + writes - 1) / writes
			start := time.Now()
			last, longest := start, time.Duration(0)
			for offset := 0; offset < len(wire); offset += step {
				time.Sleep(gap)
				longest = max(longest, time.Since(last))
				last = time.Now()
				if _, err := conn.Write(wire[offset:min(offset+step, len(wire))]); err != nil {
					break
				}
			}
			if took := time.Since(start); took <= idle {
				t.Fatalf("upload took %v, inside one idle window of %v: nothing was renewed", took, idle)
			}
			res, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("progressing upload: %d (longest gap between writes %v, idle window %v)", res.StatusCode, longest, idle)
			}
			body, err := io.ReadAll(res.Body)
			if err != nil || string(body) != "ok" {
				t.Fatalf("response: %q, %v", body, err)
			}
		})
	}
}

func TestAdditionalBodyReadersHaveDeadlines(t *testing.T) {
	for _, api := range []string{"systemone", "mcp"} {
		t.Run(api, func(t *testing.T) {
			deadlines := []time.Time{}
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
			if api == "systemone" {
				New().serveSystemOne(deadlineRecorder{w, &deadlines}, r)
			} else {
				r.SetPathValue("token", "run")
				b := &subscriptionBridge{runs: map[string]*subscriptionRun{"run": {}}}
				b.mcpCall(deadlineRecorder{w, &deadlines}, r)
			}
			if len(deadlines) < 2 || deadlines[0].IsZero() || !deadlines[len(deadlines)-1].IsZero() {
				t.Fatalf("body deadlines: %v", deadlines)
			}
		})
	}
}

func TestMCPBodyTooLarge(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	r.SetPathValue("token", "run")
	r.ContentLength = 17 << 20
	w := httptest.NewRecorder()
	b := &subscriptionBridge{runs: map[string]*subscriptionRun{"run": {}}}
	b.mcpCall(w, r)
	if w.Code != 413 {
		t.Fatalf("oversize MCP: %d", w.Code)
	}
}

func TestMediaJSONPastOldBodyLimit(t *testing.T) {
	payload := append(bytes.Repeat([]byte(" "), oldBodyLimit), []byte(`{"model":"fake/m1","prompt":"tail"}`)...)
	for _, api := range []string{"images", "videos"} {
		t.Run(api, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", bytes.NewReader(payload))
			var prompt string
			var err error
			if api == "images" {
				d, e := readDrawing(r)
				prompt, err = d.Prompt, e
			} else {
				f, e := readFilming(r)
				prompt, err = f.Prompt, e
			}
			if err != nil || prompt != "tail" {
				t.Fatalf("media body: %q, %v", prompt, err)
			}
		})
	}
}

func TestBodyRejectionErrorShapes(t *testing.T) {
	for _, tc := range []struct {
		proto  provider.Protocol
		status int
		want   string
	}{
		{provider.Anthropic, 413, `"type":"request_too_large"`},
		{provider.Chat, 413, `"type":"request_too_large"`},
		{provider.Responses, 413, `"type":"request_too_large"`},
		{provider.Gemini, 413, `"status":"INVALID_ARGUMENT"`},
		{provider.Gemini, 408, `"status":"DEADLINE_EXCEEDED"`},
	} {
		w := httptest.NewRecorder()
		writeError(w, tc.proto, tc.status, "request body rejected")
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s/%d: %s", tc.proto, tc.status, w.Body.String())
		}
	}
}

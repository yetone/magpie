package gateway

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/provider"
)

const oldBodyLimit = 64 << 20

func encodedBody(t *testing.T, body []byte, encoding string) []byte {
	t.Helper()
	switch encoding {
	case "gzip":
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	case "zstd":
		w, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		return w.EncodeAll(body, nil)
	default:
		return body
	}
}

func TestCodexBodyReadsPastOldLimit(t *testing.T) {
	for _, encoding := range []string{"identity", "gzip", "zstd"} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/%+d", encoding, delta), func(t *testing.T) {
				body := bytes.Repeat([]byte("x"), oldBodyLimit+delta)
				copy(body[len(body)-4:], "END!")
				// Hide the reader's length, as with a chunked request.
				r := httptest.NewRequest("POST", CodexPath+"/responses", struct{ io.Reader }{bytes.NewReader(encodedBody(t, body, encoding))})
				r.Header.Set("Content-Encoding", encoding)
				got, ok := New().readRequestBody(httptest.NewRecorder(), r, provider.Responses, 0)
				if !ok {
					t.Fatal("body rejected")
				}
				if !bytes.Equal(got, body) {
					t.Fatalf("read %d bytes, want all %d bytes including the tail", len(got), len(body))
				}
			})
		}
	}
}

func TestLargeRequestEndpoints(t *testing.T) {
	const payload = `{"model":"fake/m1","input":"END!","messages":[{"role":"user","content":"END!"}],"contents":[{"role":"user","parts":[{"text":"END!"}]}]}`
	// Padding keeps the test about reading the whole body, not token limits.
	body := append(bytes.Repeat([]byte(" "), oldBodyLimit), payload...)
	for _, tc := range []struct {
		path  string
		proto provider.Protocol
		count bool
	}{
		{CodexPath + "/responses", provider.Responses, false},
		{"/v1/responses", provider.Responses, false},
		{"/v1/chat/completions", provider.Chat, false},
		{"/v1/messages", provider.Anthropic, false},
		{"/v1/messages/count_tokens", provider.Anthropic, true},
		{"/v1beta/models/fake/m1:generateContent", provider.Chat, false},
		{"/v1beta/models/fake/m1:countTokens", provider.Chat, true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			f := &fake{t: t, ctype: "application/json", reply: `{"id":"ok","input_tokens":7,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"output":[],"content":[]}`}
			if strings.HasSuffix(tc.path, ":generateContent") {
				f.ctype = "text/event-stream"
				f.reply = sse(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)
			}
			setup(t, tc.proto, f)
			s := New()
			// The same prompt without transport padding provides the count control.
			control := httptest.NewRecorder()
			s.Handler().ServeHTTP(control, httptest.NewRequest("POST", tc.path, strings.NewReader(payload)))
			if control.Code != 200 {
				t.Fatalf("control: %d %s", control.Code, control.Body.String())
			}
			f.calls = 0
			f.got = nil
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", tc.path, bytes.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("large: %d %s", rec.Code, rec.Body.String())
			}
			if tc.count && rec.Body.String() != control.Body.String() {
				t.Fatalf("count changed: %s / %s", rec.Body.String(), control.Body.String())
			}
			if strings.HasSuffix(tc.path, ":countTokens") {
				if f.calls != 0 {
					t.Fatal("Gemini counting should remain local")
				}
			} else if f.calls != 1 || !bytes.Contains(f.got, []byte("END!")) {
				t.Fatalf("upstream calls=%d; tail present=%v", f.calls, bytes.Contains(f.got, []byte("END!")))
			}
		})
	}
}

type bodyReadError struct{}

func (bodyReadError) Read([]byte) (int, error) { return 0, errors.New("body read failed") }

func TestRequestTailAfterOldLimit(t *testing.T) {
	const payload = `{"model":"fake/m1","input":"hi","messages":[],"contents":[]}`
	body := append([]byte(payload), bytes.Repeat([]byte(" "), oldBodyLimit-len(payload))...)
	for _, path := range []string{CodexPath + "/responses", "/v1/responses", "/v1/messages/count_tokens", "/v1beta/models/fake/m1:generateContent"} {
		for _, tail := range []string{"whitespace", "second-object", "read-error"} {
			t.Run(path+"/"+tail, func(t *testing.T) {
				f := &fake{t: t, ctype: "application/json", reply: `{"id":"ok","input_tokens":7,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"output":[],"content":[]}`}
				proto := provider.Responses
				if strings.Contains(path, "count_tokens") {
					proto = provider.Anthropic
				}
				if strings.Contains(path, "v1beta") {
					proto = provider.Chat
				}
				if proto == provider.Chat {
					f.ctype = "text/event-stream"
					f.reply = sse(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)
				}
				setup(t, proto, f)
				s := New()
				var suffix io.Reader = strings.NewReader(" \n")
				if tail == "second-object" {
					suffix = strings.NewReader(`{"extra":true}`)
				}
				if tail == "read-error" {
					suffix = bodyReadError{}
				}
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, io.MultiReader(bytes.NewReader(body), suffix)))
				if tail == "whitespace" {
					if rec.Code != 200 || f.calls != 1 {
						t.Fatalf("valid whitespace: %d, calls=%d", rec.Code, f.calls)
					}
				} else if rec.Code != 400 || f.calls != 0 {
					t.Fatalf("%s: status=%d, calls=%d", tail, rec.Code, f.calls)
				}
			})
		}
	}
}

func TestCodexCompressedTailError(t *testing.T) {
	const payload = `{"model":"fake/m1","input":"hi"}`
	body := append([]byte(payload), bytes.Repeat([]byte(" "), oldBodyLimit-len(payload))...)
	for _, encoding := range []string{"gzip", "zstd"} {
		for _, damage := range []string{"checksum", "truncated"} {
			t.Run(encoding+"/"+damage, func(t *testing.T) {
				wire := encodedBody(t, body, encoding)
				if damage == "truncated" {
					wire = wire[:len(wire)-1]
				} else {
					pos := len(wire) - 1
					if encoding == "gzip" {
						pos = len(wire) - 8
					}
					wire[pos] ^= 0xff
				}
				f := &fake{t: t}
				setup(t, provider.Responses, f)
				r := httptest.NewRequest("POST", CodexPath+"/responses", bytes.NewReader(wire))
				r.Header.Set("Content-Encoding", encoding)
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, r)
				if rec.Code != 400 || f.calls != 0 {
					t.Fatalf("status=%d, upstream calls=%d", rec.Code, f.calls)
				}
			})
		}
	}
}

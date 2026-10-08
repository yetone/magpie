package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
)

// postEncoded sends body to the running gateway at path with the given
// Content-Encoding, as Codex sends a custom provider's /v1/responses zstd
// (#1223); Go's client leaves a body it is handed as it is.
func postEncoded(t *testing.T, gw *httptest.Server, path string, body []byte, enc string, header http.Header) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", gw.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	if enc != "" {
		req.Header.Set("Content-Encoding", enc)
	}
	res, err := gw.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// Every route that reads an agent's body reads it gzip or zstd as well as
// plain, and sends the provider the plain JSON with no Content-Encoding
// (#1223: Codex's custom provider on /v1/responses got "expected a JSON
// object").
func TestEncodedBodiesOnEveryRoute(t *testing.T) {
	const payload = `{"model":"fake/m1","input":"END!","messages":[{"role":"user","content":"END!"}],"contents":[{"role":"user","parts":[{"text":"END!"}]}]}`
	for _, tc := range []struct {
		path  string
		proto provider.Protocol
		count bool
	}{
		{"/v1/responses", provider.Responses, false},
		{"/responses", provider.Responses, false},
		{"/v1/chat/completions", provider.Chat, false},
		{"/v1/messages", provider.Anthropic, false},
		{"/v1/messages/count_tokens", provider.Anthropic, true},
		{"/v1beta/models/fake/m1:generateContent", provider.Chat, false},
		{"/v1beta/models/fake/m1:countTokens", provider.Chat, true},
		{CodexPath + "/responses", provider.Responses, false},
	} {
		for _, enc := range []string{"", "identity", "gzip", "zstd"} {
			t.Run(tc.path+"/"+enc, func(t *testing.T) {
				f := &fake{t: t, ctype: "application/json", reply: `{"id":"ok","input_tokens":7,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"output":[],"content":[]}`}
				if strings.HasSuffix(tc.path, ":generateContent") {
					f.ctype = "text/event-stream"
					f.reply = sse(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)
				}
				setup(t, tc.proto, f)
				gw := httptest.NewServer(New().Handler())
				defer gw.Close()
				control, want := postEncoded(t, gw, tc.path, []byte(payload), "", nil)
				if control != 200 {
					t.Fatalf("plain: %d %s", control, want)
				}
				f.calls, f.got, f.head = 0, nil, nil
				code, got := postEncoded(t, gw, tc.path, encodedBody(t, []byte(payload), enc), enc, nil)
				if code != 200 {
					t.Fatalf("%s body: %d %s", enc, code, got)
				}
				if tc.count {
					if got != want {
						t.Fatalf("%s count = %s, plain = %s", enc, got, want)
					}
					return
				}
				if f.calls != 1 || !bytes.Contains(f.got, []byte("END!")) || !bytes.HasPrefix(bytes.TrimSpace(f.got), []byte("{")) {
					t.Fatalf("provider got %d calls, body %q", f.calls, f.got)
				}
				if ce := f.head.Get("Content-Encoding"); ce != "" {
					t.Fatalf("provider was told Content-Encoding %q of a decoded body", ce)
				}
			})
		}
	}
}

// Embeddings and rerank read the agent's body the same way.
func TestEncodedEmbeddingsBody(t *testing.T) {
	for _, enc := range []string{"gzip", "zstd"} {
		t.Run(enc, func(t *testing.T) {
			s, l := shelved(t)
			gw := httptest.NewServer(s.Handler())
			defer gw.Close()
			body := []byte(`{"model":"lib/embed-1","input":["END!","two"]}`)
			if code, got := postEncoded(t, gw, "/v1/embeddings", encodedBody(t, body, enc), enc, nil); code != 200 {
				t.Fatalf("%s embeddings: %d %s", enc, code, got)
			}
			if sent := l.got("/v1/embeddings"); len(sent) != 1 || !strings.Contains(sent[0], `"END!"`) {
				t.Fatalf("provider got %q", sent)
			}
		})
	}
}

// The gateway's limit holds for the decoded body, so a small compressed
// body that inflates past it is refused, on every route.
func TestEncodedBodyInflatedPastLimit(t *testing.T) {
	big := []byte(`{"model":"fake/m1","input":"` + strings.Repeat("x", 4096) + `"}`)
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens", "/v1beta/models/fake/m1:generateContent", "/v1/embeddings", "/v1/images/generations", "/v1/videos", CodexPath + "/responses"} {
		for _, enc := range []string{"gzip", "zstd"} {
			t.Run(path+"/"+enc, func(t *testing.T) {
				f := &fake{t: t}
				setup(t, provider.Chat, f)
				s := New()
				s.requestLimits.body = 1024
				gw := httptest.NewServer(s.Handler())
				defer gw.Close()
				sent := encodedBody(t, big, enc)
				if len(sent) >= 1024 {
					t.Fatalf("compressed body is %d bytes, not under the limit", len(sent))
				}
				if code, got := postEncoded(t, gw, path, sent, enc, nil); code != http.StatusRequestEntityTooLarge {
					t.Fatalf("inflated %s body: %d %s", enc, code, got)
				}
				if f.calls != 0 {
					t.Fatal("an oversize body reached the provider")
				}
			})
		}
	}
}

// A body in an encoding magpie can't read is refused 415, saying which,
// rather than read as broken JSON.
func TestUnknownBodyEncodingRefused(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages", "/v1beta/models/fake/m1:generateContent", CodexPath + "/responses"} {
		t.Run(path, func(t *testing.T) {
			f := &fake{t: t}
			setup(t, provider.Chat, f)
			gw := httptest.NewServer(New().Handler())
			defer gw.Close()
			code, got := postEncoded(t, gw, path, []byte(`{"model":"fake/m1"}`), "br", nil)
			if code != http.StatusUnsupportedMediaType || !strings.Contains(got, `\"br\"`) || !strings.Contains(got, "zstd") {
				t.Fatalf("br body: %d %s", code, got)
			}
			if f.calls != 0 {
				t.Fatal("an unreadable body reached the provider")
			}
		})
	}
}

// A gateway key's limit holds a compressed request at what it decodes to,
// not at its compressed size.
func TestKeyLimitReservesDecodedBody(t *testing.T) {
	fresh(t)
	budget.Forget()
	now := pinLimitClock(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":300,"completion_tokens":50}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Zipped")
	limit := &access.Limit{Period: "month", Tokens: 1 << 30}
	setLimit(t, keys[0].ID, limit)
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	body := []byte(`{"model":"plan/m1","messages":[{"role":"user","content":"` + strings.Repeat("x", 40000) + `"}]}`)
	sent := encodedBody(t, body, "zstd")
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer free()
	done := make(chan string, 1)
	go func() {
		req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", bytes.NewReader(sent))
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		req.Header.Set("Content-Encoding", "zstd")
		res, err := gw.Client().Do(req)
		if err != nil {
			done <- err.Error()
			return
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		done <- res.Status + " " + string(b)
	}()
	select {
	case <-entered:
	case got := <-done:
		t.Fatalf("the request never reached the provider: %s", got)
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the provider")
	}
	st := budget.Of(access.Key{ID: keys[0].ID, Limit: limit}, now)
	free()
	if got := <-done; !strings.HasPrefix(got, "200 ") {
		t.Fatalf("zstd request: %s", got)
	}
	if st.InFlight != 1 || st.Reserved < int64(len(body))/4 {
		t.Fatalf("held at %d tokens for a %d-byte body sent as %d bytes", st.Reserved, len(body), len(sent))
	}
}

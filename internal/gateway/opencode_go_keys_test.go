package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// goUsageLimitBody is what OpenCode's gateway answers a Go subscriber at
// a limit, as OpenCode's own client records it (its composer.spec.ts
// fixture): a 429 GoUsageLimitError naming the window that ran out.
const goUsageLimitBody = `{"type":"error","error":{"type":"GoUsageLimitError","message":"Go usage limit exceeded"},"metadata":{"workspace":"wrk_go_limit","limitName":"5 hour"}}`

// An OpenCode Go provider holds several keys, as a subscription holds
// several accounts (MelWoutersgghi on X: "支持opencode多个key配置和切换"):
// when the first key's Go usage runs out, the next key that's on serves
// the request, and the spent key rests until its window renews, so the
// next request goes straight to the key with room.
func TestOpenCodeGoKeysTakeOverAtUsageLimit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
	restingUntil.Unlock()
	if err := provider.Save(provider.Provider{ID: "opencode-go", Preset: "opencode-go", Name: "OpenCode Go",
		Key: "k-spent", KeyName: "Personal",
		Keys: []provider.KeyAccount{{Name: "Idle", Key: "k-idle", Off: true}, {Name: "Second", Key: "k-room"}},
		Chat: "https://opencode.ai/zen/go/v1", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	var mu sync.Mutex
	var seen []string
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		h := http.Header{"Content-Type": {"application/json"}}
		if key == "k-spent" {
			h.Set("Retry-After", "3600")
			return &http.Response{StatusCode: 429, Status: "429 Too Many Requests", Header: h,
				Body: io.NopCloser(strings.NewReader(goUsageLimitBody)), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: h,
			Body: io.NopCloser(strings.NewReader(strings.Replace(openCodeServed, `"ok"`, `"from-`+key+`"`, 1))), Request: r}, nil
	})}
	send := func() (int, string) {
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"opencode-go/deepseek-v4.1-flash","messages":[{"role":"user","content":"hello"}]}`))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	tried := func() string {
		mu.Lock()
		defer mu.Unlock()
		out := strings.Join(seen, ",")
		seen = nil
		return out
	}
	if code, out := send(); code != 200 || !strings.Contains(out, "from-k-room") {
		t.Fatalf("%d %s, tried %s", code, out, tried())
	}
	if got := tried(); got != "k-spent,k-room" {
		t.Fatalf("tried %s, want the spent key then the second; the one that's off never", got)
	}
	// the spent key rests: the next request goes to the key with room
	if code, out := send(); code != 200 || !strings.Contains(out, "from-k-room") {
		t.Fatalf("%d %s", code, out)
	}
	if got := tried(); got != "k-room" {
		t.Fatalf("tried %s, want only the second key while the first rests", got)
	}
}

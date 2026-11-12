package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Codex kept signed in to acct-1 (KeepLoginAs) while the pool puts the
// spare first: a drawing goes where the text turns go, to the spare, and
// the signed-in account, whose image allowance is spent, is not asked
// first (Richyisaflower on X).
func TestDrawingFollowsThePoolNotTheKeptLogin(t *testing.T) {
	for _, path := range []string{"/v1/images/generations", CodexPath + "/images/generations"} {
		t.Run(path, func(t *testing.T) { drawingFollowsThePool(t, path) })
	}
}

func drawingFollowsThePool(t *testing.T, path string) {
	codexSignedIn(t, "spare@example.com")
	var mu sync.Mutex
	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		tried = append(tried, r.Header.Get("chatgpt-account-id")+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("chatgpt-account-id") == "acct-1" {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"image generation limit reached","resets_in_seconds":7200}}`)
			return
		}
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}],"usage":{"input_tokens":5,"output_tokens":196}}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeepLoginAs("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountOrder("codex", []string{"spare@example.com", "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.ForgetReorders)
	for _, l := range provider.Logins("codex") {
		if l.Active != (l.User == "me@example.com") {
			t.Fatalf("kept on me, signed in as %+v", l)
		}
	}
	s := New()
	for i := 0; i < 2; i++ {
		mu.Lock()
		tried = nil
		mu.Unlock()
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"gpt-image-2","prompt":"a magpie"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", "codex_cli_rs/0.130.0")
		if strings.HasPrefix(path, CodexPath) {
			// as Codex sends it, signed in to acct-1
			tok, _, _ := codexSignInToken()
			r.Header.Set("Authorization", "Bearer "+tok)
			r.Header.Set("Chatgpt-Account-Id", "acct-1")
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		code, raw := rec.Code, rec.Body.String()
		var a imagesAnswer
		json.Unmarshal(rec.Body.Bytes(), &a)
		mu.Lock()
		got := strings.Join(tried, " | ")
		mu.Unlock()
		if code != 200 || len(a.Data) != 1 || !strings.HasPrefix(got, "acct-2 ") {
			t.Fatalf("drawing %d: %d %s; asked %s", i, code, raw, got)
		}
	}
}

// Codex's image call with the sign-in the only account is relayed as it came.
func TestCodexImagesRelayedWithTheSignInAlone(t *testing.T) {
	codexSignedIn(t)
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		got = r.URL.Path + " " + r.Header.Get("chatgpt-account-id")
		io.WriteString(w, `{"created":1,"data":[]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	r := httptest.NewRequest("POST", CodexPath+"/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"a magpie"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "codex_cli_rs/0.130.0")
	tok, _, _ := codexSignInToken()
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Chatgpt-Account-Id", "acct-1")
	New().Handler().ServeHTTP(httptest.NewRecorder(), r)
	if got != "/backend-api/codex/images/generations acct-1" {
		t.Fatalf("asked %q", got)
	}
}

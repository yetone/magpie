package gateway

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// forgetImageHeard forgets every image window heard, now and when t ends.
func forgetImageHeard(t *testing.T) {
	t.Helper()
	clear := func() {
		codexImageHeard.Lock()
		codexImageHeard.m = map[string]provider.CodexLimit{}
		codexImageHeard.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// imageRowOf is the image row of user's ChatGPT card as it is handed out.
func imageRowOf(t *testing.T, user string) provider.QuotaWindow {
	t.Helper()
	in := []provider.SubscriptionQuota{{Provider: "codex", User: user, Windows: []provider.QuotaWindow{{Name: "5 hours", Used: 40}, {Name: "7 days", Used: 10}}}}
	out := provider.WithImageLane(in, time.Now())
	if len(out[0].Windows) != 3 || out[0].Windows[2].Lane != provider.ImageLane {
		t.Fatalf("windows = %+v", out[0].Windows)
	}
	return out[0].Windows[2]
}

// #6412 (hqqqqqqqh): a drawing's 200 carries the account's image window,
// x-codex-active-limit: imagegen_premium with x-codex-primary-* of 1440
// minutes, as the reporter captured it on a Pro account. The image row
// then says when it renews, not "unknown"; and those headers are never
// taken for the account's 5-hour or weekly window.
func TestCodexImageRowFromA200sHeaders(t *testing.T) {
	codexSignedIn(t)
	forgetImageHeard(t)
	at := time.Now().Add(12 * time.Hour).Truncate(time.Second)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		// the reporter's capture, its reset moved to the test's time
		w.Header().Set("x-codex-active-limit", "imagegen_premium")
		w.Header().Set("x-codex-plan-type", "pro")
		w.Header().Set("x-codex-primary-used-percent", "0")
		w.Header().Set("x-codex-primary-window-minutes", "1440")
		w.Header().Set("x-codex-primary-reset-after-seconds", strconv.FormatInt(int64(time.Until(at)/time.Second), 10))
		w.Header().Set("x-codex-primary-reset-at", strconv.FormatInt(at.Unix(), 10))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}],"usage":{"input_tokens":5,"output_tokens":196}}`)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	var mu sync.Mutex
	var chat []provider.CodexLimit
	old := noteCodexLimits
	noteCodexLimits = func(_, _ string, ls []provider.CodexLimit) { mu.Lock(); chat = append(chat, ls...); mu.Unlock() }
	t.Cleanup(func() { noteCodexLimits = old })

	if w := imageRowOf(t, "me@example.com"); w.Display != "Unknown" || w.ResetsAt != nil {
		t.Fatalf("before any drawing the row = %+v", w)
	}
	code, _, raw := postImages(t, New(), "/v1/images/generations", "application/json", `{"model":"codex/gpt-image-2","prompt":"a magpie","size":"1024x1024"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	w := imageRowOf(t, "me@example.com")
	if w.ResetsAt == nil || !w.ResetsAt.Equal(at) || w.Display != "Available" || !w.Text || !w.Aside || w.Used != 0 {
		t.Fatalf("after a drawing the row = %+v, want available until %v, no share", w, at)
	}
	if w.ResetSecs < int64(11*time.Hour/time.Second) {
		t.Fatalf("resets in %ds, want about 12h", w.ResetSecs)
	}
	if other := imageRowOf(t, "other@example.com"); other.ResetsAt != nil {
		t.Fatalf("another account's row took it: %+v", other)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(chat) != 0 {
		t.Fatalf("the image window was taken for the account's own: %+v", chat)
	}
}

// A refusal whose body names no reset, as Codex's own test has
// imagegen_premium's (app-server imagegen_extension.rs), rests the image
// model until the window its headers tell renews, in the limit's own
// family (x-imagegen-premium-primary-*), not quotaRest's 15 minutes: the
// card said "2 minutes" of a 24-hour window (#6412).
func TestCodexImageRefusalRestsUntilItsWindow(t *testing.T) {
	codexSignedIn(t)
	forgetImageHeard(t)
	at := time.Now().Add(20 * time.Hour).Truncate(time.Second)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("x-codex-active-limit", "imagegen_premium")
		w.Header().Set("x-imagegen-premium-primary-used-percent", "100")
		w.Header().Set("x-imagegen-premium-primary-window-minutes", "1440")
		w.Header().Set("x-imagegen-premium-primary-reset-at", strconv.FormatInt(at.Unix(), 10))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"image limit reached","resets_at":null,"plan_type":"plus"}}`)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	t.Cleanup(func() {
		restingUntil.Lock()
		var keys []string
		for k, r := range restingUntil.note {
			if strings.EqualFold(r.user, "me@example.com") {
				keys = append(keys, k)
			}
		}
		restingUntil.Unlock()
		for _, k := range keys {
			clearRest(k)
		}
	})

	code, _, raw := postImages(t, New(), "/v1/images/generations", "application/json", `{"model":"codex/gpt-image-2","prompt":"a magpie"}`)
	if code != http.StatusTooManyRequests {
		t.Fatalf("%d %s", code, raw)
	}
	// the model rests until then, so the next drawing asks another account first
	if until, ok := imageRestOf("codex", "me@example.com", time.Now()); !ok || until.Sub(at).Abs() > 2*time.Second {
		t.Fatalf("rests until %v (%v), want %v", until, ok, at)
	}
	w := imageRowOf(t, "me@example.com")
	if w.ResetsAt == nil || w.ResetsAt.Sub(at).Abs() > 2*time.Second || w.Display != "Used up" {
		t.Fatalf("after the refusal the row = %+v, want used up until %v", w, at)
	}
}

// Codex's own drawing, relayed to ChatGPT with its sign-in the only
// account, tells the image row too.
func TestCodexRelayedImageWindowHeard(t *testing.T) {
	codexSignedIn(t)
	forgetImageHeard(t)
	at := time.Now().Add(6 * time.Hour).Truncate(time.Second)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("x-codex-active-limit", "imagegen_premium")
		w.Header().Set("x-codex-primary-used-percent", "100")
		w.Header().Set("x-codex-primary-window-minutes", "1440")
		w.Header().Set("x-codex-primary-reset-at", strconv.FormatInt(at.Unix(), 10))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString(pngBytes)+`"}]}`)
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"a magpie"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("chatgpt-account-id", "acct-1")
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if w := imageRowOf(t, "me@example.com"); w.ResetsAt == nil || !w.ResetsAt.Equal(at) || w.Display != "Used up" {
		t.Fatalf("row = %+v, want used up until %v", w, at)
	}
}

// Which headers tell the image window: the limit's own family, or the
// x-codex-* one when x-codex-active-limit names an image limit; and then
// never the account's own windows. A chat reply's are as they were.
func TestCodexImageHeaders(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "0")
	h.Set("x-codex-primary-window-minutes", "1440")
	h.Set("x-codex-primary-reset-after-seconds", "600")
	if _, ok := codexImageHeaderLimit(h, now); ok {
		t.Fatal("x-codex-* with no image limit named taken for the image window")
	}
	if ls := codexHeaderLimits(h, now); len(ls) != 1 {
		t.Fatalf("a chat reply's window lost: %+v", ls)
	}
	for _, name := range []string{"imagegen_premium", "image_gen", "imagegen_basic"} {
		h.Set("x-codex-active-limit", name)
		if l, ok := codexImageHeaderLimit(h, now); !ok || l.Span != 24*time.Hour || !l.Resets.Equal(now.Add(10*time.Minute)) {
			t.Fatalf("%s: %+v %v", name, l, ok)
		}
		if ls := codexHeaderLimits(h, now); ls != nil {
			t.Fatalf("%s: the image window taken for the account's: %+v", name, ls)
		}
	}
	f := http.Header{}
	f.Set("x-codex-active-limit", "image_gen")
	f.Set("x-image-gen-primary-used-percent", "100")
	f.Set("x-image-gen-primary-window-minutes", "1440")
	f.Set("x-image-gen-primary-reset-at", "1800003600")
	if l, ok := codexImageHeaderLimit(f, now); !ok || l.Used != 100 || l.Resets.Unix() != 1800003600 {
		t.Fatalf("image_gen's own family: %+v %v", l, ok)
	}
}

package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// goWeekly is OpenCode Go's 429 for a key whose weekly window is used up,
// as tkhs101's trace in #7496 quotes its message (ggbdpq's fixture).
const goWeekly = `{"error":{"message":"OpenCode Go usage limit reached (weekly, resets in 14h 54m)"}}`

// goWindowServer is a gateway with tkhs101's provider from #7496: one
// OpenCode Go key, as the preset adds it, with glm-5.3 (paid) and
// hy4-flash-free (free). models.dev prices hy4-flash-free at nothing on
// every count; with goWord, Go's own list says it is free too. Go answers
// goWeekly to glm-5.3 and a stream to hy4-flash-free, as it does with its
// windows used up; the models each request reached Go for are returned.
func goWindowServer(t *testing.T, goWord bool, caps map[string]int) (*Server, provider.Provider, func() []string) {
	t.Helper()
	fresh(t)
	old := allowances
	allowances = func(string) map[string]provider.Allowance { return nil }
	t.Cleanup(func() { allowances = old })
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"opencode-go":{"id":"opencode-go","models":{
		"glm-5.3":{"id":"glm-5.3","cost":{"input":1,"output":3.2,"cache_read":0.2}},
		"hy4-flash-free":{"id":"hy4-flash-free","cost":{"input":0,"output":0,"cache_read":0}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := catalog.SaveLive("go", provider.OpenCodeGoChat, []catalog.Model{{ID: "glm-5.3"}, {ID: "hy4-flash-free", Free: goWord}}); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{ID: "go", Name: "OpenCode Go", Preset: "opencode-go", Key: "sk-go-users-own",
		Chat: provider.OpenCodeGoChat, Responses: provider.OpenCodeGoChat, Anthropic: provider.OpenCodeGoAnthropic,
		AccountCaps: caps}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var reached []string
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if !strings.HasPrefix(r.URL.Host+r.URL.Path, "opencode.ai/zen/go/") && !strings.HasPrefix(r.URL.Host+r.URL.Path, "opencode.ai/inference/go/") {
			t.Errorf("asked %s%s", r.URL.Host, r.URL.Path)
		}
		model := "?"
		for _, m := range []string{"glm-5.3", "hy4-flash-free"} {
			if strings.Contains(string(b), `"model":"`+m+`"`) {
				model = m
			}
		}
		mu.Lock()
		reached = append(reached, model)
		mu.Unlock()
		if model == "hy4-flash-free" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(streamFor(r.URL.Path, "from go")))}, nil
		}
		return &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(goWeekly))}, nil
	})}
	return s, p, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), reached...) }
}

func askGo(s *Server, model string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"go/`+model+`","stream":true,"messages":[{"role":"user","content":"hello `+model+`"}]}`)))
	return rec
}

// Winnie7572 on X, tkhs101's #7496: with an OpenCode Go key's weekly
// window used up, Go refuses its paid models and still serves its free
// ones. The refusal of glm-5.3 rested the whole key, and hy4-flash-free
// was answered the kept 429 in ~10ms without reaching Go. The rest is
// glm-5.3's alone now, and hy4-flash-free reaches Go and is served —
// free by models.dev's price alone, as in #7496, and by Go's own word.
func TestOpenCodeGoPaidRefusalLeavesFreeModels(t *testing.T) {
	for _, goWord := range []bool{false, true} {
		t.Run(fmt.Sprint("Go's word ", goWord), func(t *testing.T) {
			s, _, reached := goWindowServer(t, goWord, nil)
			keyWindows(t, nil) // the key's windows not read: the refusal alone
			if rec := askGo(s, "glm-5.3"); rec.Code != 429 {
				t.Fatalf("glm-5.3 answered %d %s", rec.Code, rec.Body)
			}
			restingUntil.Lock()
			var rests []string
			for id, r := range restingUntil.note {
				rests = append(rests, fmt.Sprintf("%s (%s)", id, r.Why))
				if id != "go/glm-5.3" {
					defer t.Errorf("rests more than the paid model: %s by %s (%s)", id, r.By, r.Why)
				}
			}
			restingUntil.Unlock()
			if len(rests) == 0 {
				t.Fatal("glm-5.3's refusal rested nothing")
			}
			for i := 0; i < 2; i++ {
				if rec := askGo(s, "hy4-flash-free"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from go") {
					t.Fatalf("hy4-flash-free answered %d %s; rests %v", rec.Code, rec.Body, rests)
				}
			}
			// glm-5.3 waits out its own rest, unasked
			if rec := askGo(s, "glm-5.3"); rec.Code != 429 {
				t.Fatalf("glm-5.3 answered %d %s", rec.Code, rec.Body)
			}
			if got := reached(); strings.Join(got, " ") != "glm-5.3 hy4-flash-free hy4-flash-free" {
				t.Fatalf("Go reached for %v", got)
			}
		})
	}
}

// A free model Go refuses all the same (its docs say it doesn't) rests on
// its own: the refusal says nothing of the paid ones (ggbdpq).
func TestOpenCodeGoFreeRefusalRestsItself(t *testing.T) {
	_, p, _ := goWindowServer(t, true, nil)
	if !p.FreeBesidePaid("hy4-flash-free") {
		t.Fatal("hy4-flash-free not free beside paid")
	}
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(goWeekly))}, nil
	})}
	keyWindows(t, nil)
	if rec := askGo(s, "hy4-flash-free"); rec.Code != 429 {
		t.Fatalf("hy4-flash-free answered %d %s", rec.Code, rec.Body)
	}
	restingUntil.Lock()
	defer restingUntil.Unlock()
	if len(restingUntil.note) == 0 {
		t.Fatal("rested nothing")
	}
	for id := range restingUntil.note {
		if id != "go/hy4-flash-free" {
			t.Errorf("rests %s, want go/hy4-flash-free alone", id)
		}
	}
}

// The key's 5 hours read 100% from Go's /usage, and the user capped the
// key at 80% of its windows: the cap holds glm-5.3, which draws on them,
// and not hy4-flash-free, which Go serves with them used up.
func TestOpenCodeGoKeyCapLeavesFreeModels(t *testing.T) {
	five := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	week := time.Now().Add(50 * time.Hour).UTC().Format(time.RFC3339)
	month := time.Now().Add(400 * time.Hour).UTC().Format(time.RFC3339)
	oldDefault := http.DefaultClient.Transport
	http.DefaultClient.Transport = countTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host+r.URL.Path != "opencode.ai/zen/go/v1/usage" {
			t.Errorf("read %s%s", r.URL.Host, r.URL.Path)
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"usage":{"rolling":{"status":"rate-limited","percent":100,"resetsAt":"` + five + `"},
				"weekly":{"status":"ok","percent":40,"resetsAt":"` + week + `"},
				"monthly":{"status":"ok","percent":20,"resetsAt":"` + month + `"}}}`))}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = oldDefault })
	s, p, reached := goWindowServer(t, false, nil)
	old := keyAllowance
	keyAllowance = provider.KeyAllowance // the key's windows as read, here
	t.Cleanup(func() { keyAllowance = old })
	if err := provider.SetAccountCap("go", provider.KeyID(p.Key), 80); err != nil {
		t.Fatal(err)
	}
	if q, err := provider.Find("go"); err != nil || q.CapsOf(provider.KeyID(p.Key)).All != 80 {
		t.Fatalf("cap not kept: %v %+v", err, q)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if a, ok := provider.KeyAllowance(p); ok && len(a) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the key's windows were never read")
		}
		time.Sleep(10 * time.Millisecond)
	}
	q, _ := provider.Find("go")
	k, ok := q.KeyAccountOf(provider.KeyID(p.Key))
	if !ok {
		t.Fatal("no key")
	}
	if h := keyCapHeld(*q, k, "glm-5.3", time.Now()); h == nil {
		t.Fatal("glm-5.3 not held at the key's cap")
	}
	if h := keyCapHeld(*q, k, "hy4-flash-free", time.Now()); h != nil {
		t.Fatalf("hy4-flash-free held at the key's cap: %+v", h)
	}
	if rec := askGo(s, "glm-5.3"); rec.Code != 429 {
		t.Fatalf("glm-5.3 answered %d %s", rec.Code, rec.Body)
	}
	if rec := askGo(s, "hy4-flash-free"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from go") {
		t.Fatalf("hy4-flash-free answered %d %s", rec.Code, rec.Body)
	}
	if got := reached(); strings.Join(got, " ") != "hy4-flash-free" {
		t.Fatalf("Go reached for %v, want hy4-flash-free alone (glm-5.3 held at the cap)", got)
	}
}

// xzhih on X: a 429 on a free model beside paid ones is about that free
// model alone. A rate limit on hy4-flash-free rested the whole Go key, so glm-5.3, which the key's
// windows still had room for, was held for the rate limit's time, and its
// row said the key was out. The rest and its notice are hy4-flash-free's
// alone now, and glm-5.3 reaches Go and is served.
func TestOpenCodeGoFreeRateLimitLeavesPaid(t *testing.T) {
	for _, c := range []struct {
		name   string
		hdr    http.Header
		body   string
		rested bool // magpie rests on it (a time named), rather than trying again
	}{
		{"named, with Retry-After", http.Header{"Content-Type": {"application/json"}, "Retry-After": {"600"}},
			`{"error":{"message":"Rate limit exceeded. Please try again later.","type":"rate_limit_error"}}`, true},
		{"named, no time", http.Header{"Content-Type": {"application/json"}},
			`{"error":{"message":"Rate limit exceeded. Please try again later.","type":"rate_limit_error"}}`, false},
		{"bare", http.Header{}, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, p, _ := goWindowServer(t, true, nil)
			keyWindows(t, nil)
			if !p.FreeBesidePaid("hy4-flash-free") {
				t.Fatal("hy4-flash-free not free beside paid")
			}
			var mu sync.Mutex
			var reached []string
			s := New()
			s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
				b, _ := io.ReadAll(r.Body)
				r.Body.Close()
				mu.Lock()
				defer mu.Unlock()
				if strings.Contains(string(b), `"model":"hy4-flash-free"`) {
					reached = append(reached, "hy4-flash-free")
					return &http.Response{StatusCode: 429, Header: c.hdr.Clone(), Body: io.NopCloser(strings.NewReader(c.body))}, nil
				}
				reached = append(reached, "glm-5.3")
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: io.NopCloser(strings.NewReader(streamFor(r.URL.Path, "from go")))}, nil
			})}
			if rec := askGo(s, "hy4-flash-free"); rec.Code != 429 {
				t.Fatalf("hy4-flash-free answered %d %s", rec.Code, rec.Body)
			}
			restingUntil.Lock()
			var rests []string
			for id, r := range restingUntil.note {
				rests = append(rests, id)
				if id != "go/hy4-flash-free" {
					t.Errorf("the free model's 429 rests %s by %s (%s), want go/hy4-flash-free alone", id, r.By, r.Why)
				}
			}
			restingUntil.Unlock()
			if c.rested && len(rests) == 0 {
				t.Fatal("hy4-flash-free's 429 rested nothing")
			}
			if n, ok := noticeOf(t, "go"); ok {
				t.Errorf("the Go key has a whole-key notice: %+v", n)
			}
			sunk.Lock()
			_, sank := sunk.m["go"]
			sunk.Unlock()
			if sank {
				t.Error("the Go key sank behind the others for the free model's 429")
			}
			for i := 0; i < 2; i++ {
				if rec := askGo(s, "glm-5.3"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "from go") {
					t.Fatalf("glm-5.3 answered %d %s; rests %v", rec.Code, rec.Body, rests)
				}
			}
			mu.Lock()
			got := strings.Join(reached, " ")
			mu.Unlock()
			if !strings.HasPrefix(got, "hy4-flash-free") || !strings.HasSuffix(got, " glm-5.3 glm-5.3") {
				t.Fatalf("Go reached for %s", got)
			}
		})
	}
}

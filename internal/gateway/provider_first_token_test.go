package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// slowKey answers a request sent with the key "slow" as slowStart does, its
// text only after wait, and any other key at once, counting each.
type slowKey struct {
	wait       time.Duration
	slow, fast atomic.Int32
}

func (b *slowKey) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "text/event-stream")
	if r.Header.Get("Authorization") != "Bearer slow" {
		b.fast.Add(1)
		io.WriteString(w, chatStreamOK)
		return
	}
	b.slow.Add(1)
	w.WriteHeader(200)
	io.WriteString(w, `data: {"id":"s","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n")
	w.(http.Flusher).Flush()
	select {
	case <-r.Context().Done():
		return
	case <-time.After(b.wait):
	}
	io.WriteString(w, `data: {"id":"s","choices":[{"index":0,"delta":{"content":"slow answer"},"finish_reason":"stop"}]}`+"\n\n"+"data: [DONE]\n\n")
}

func keyedOn(t *testing.T, id string, firstToken int, h http.Handler, keys ...string) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: keys[0], Models: []string{"m"}, Chat: up.URL + "/v1", Routing: provider.Ordered, FirstToken: firstToken}
	for _, k := range keys[1:] {
		p.Keys = append(p.Keys, provider.KeyAccount{Key: k})
	}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

const streamHi = `"stream":true,"messages":[{"role":"user","content":"hi"}]}`

// A provider's own first-token time (GoForceX on X: "支持调整对应 provider
// 的超时等待时间"): a key slow to begin is let go before any of it reaches
// the agent and the provider's next key answers, through no group; the
// last key left is waited for; with none set, a slow key is waited for as
// before.
func TestProviderFirstTokenFailsOver(t *testing.T) {
	t.Run("next key", func(t *testing.T) {
		fresh(t)
		h := &slowKey{wait: 10 * time.Second}
		keyedOn(t, "a", 1, h, "slow", "fast")
		s := New()
		began := time.Now()
		code, body := postAs(t, s, "", `{"model":"a/m",`+streamHi)
		if took := time.Since(began); took > 5*time.Second {
			t.Fatalf("took %s", took)
		}
		if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "slow answer") || h.slow.Load() != 1 || h.fast.Load() != 1 {
			t.Fatalf("%d %s (slow %d, fast %d)", code, body, h.slow.Load(), h.fast.Load())
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		if len(r.Tries) != 2 || r.Tries[0].Fail != failSlow || !strings.HasSuffix(r.Tries[0].Error, "no first token in 1s") || r.Tries[1].Status != 200 {
			t.Fatalf("tries: %+v", r.Tries)
		}
	})
	t.Run("the last key is waited for", func(t *testing.T) {
		fresh(t)
		h := &slowKey{wait: 1500 * time.Millisecond}
		keyedOn(t, "a", 1, h, "slow")
		if code, body := postAs(t, New(), "", `{"model":"a/m",`+streamHi); code != 200 || !strings.Contains(body, "slow answer") {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("none set waits", func(t *testing.T) {
		fresh(t)
		h := &slowKey{wait: 1500 * time.Millisecond}
		keyedOn(t, "a", 0, h, "slow", "fast")
		if code, body := postAs(t, New(), "", `{"model":"a/m",`+streamHi); code != 200 || !strings.Contains(body, "slow answer") || h.fast.Load() != 0 {
			t.Fatalf("%d %s (fast %d)", code, body, h.fast.Load())
		}
	})
}

// In a group, a member's provider that sets its own first-token time is
// timed by it, not by the group's: shorter, the next member is asked
// where the group would wait; longer, it is waited for where the group
// would move on.
func TestProviderFirstTokenOverTheGroups(t *testing.T) {
	for _, c := range []struct {
		name          string
		group, own    int
		wait          time.Duration
		slowAnswers   bool
		nextAskedOnce bool
	}{
		{"shorter than the group's wait", provider.FirstTokenWait, 1, 10 * time.Second, false, true},
		{"shorter than the group's", 60, 1, 10 * time.Second, false, true},
		{"longer than the group's", 1, 5, 1500 * time.Millisecond, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			h := &slowKey{wait: c.wait}
			keyedOn(t, "a", c.own, h, "slow")
			b := &scripted{replies: []reply{{200, "text/event-stream", chatStreamOK}}}
			scriptedOn(t, "b", provider.Chat, b)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered, FirstToken: c.group}); err != nil {
				t.Fatal(err)
			}
			began := time.Now()
			code, body := postAs(t, New(), "", `{"model":"group/g",`+streamHi)
			if took := time.Since(began); took > 5*time.Second {
				t.Fatalf("took %s", took)
			}
			if code != 200 || strings.Contains(body, "slow answer") != c.slowAnswers || (b.n == 1) != c.nextAskedOnce {
				t.Fatalf("%d %s (b %d)", code, body, b.n)
			}
		})
	}
}

// The provider's first-token time is a setting saved with it: kept to
// what the gateway can give (0 to FirstTokenLongest) and carried to its
// subscription accounts as its other settings are.
func TestProviderFirstTokenSaved(t *testing.T) {
	fresh(t)
	for _, c := range []struct{ in, want int }{{0, 0}, {30, 30}, {-5, 0}, {9999, provider.FirstTokenLongest}} {
		if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m"}, Chat: "https://example.invalid/v1", FirstToken: c.in}); err != nil {
			t.Fatal(err)
		}
		got, err := provider.Find("a")
		if err != nil || got.FirstToken != c.want {
			t.Fatalf("%d saved as %v, %v; want %d", c.in, got, err, c.want)
		}
	}
}

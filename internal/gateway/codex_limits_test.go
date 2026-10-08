package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #1295: a Codex account capped at 99%, read at 96% by /wham/usage, was
// sent turn after turn until ChatGPT refused it at 100%, the reading kept
// for a minute and more. Each reply says what the account has used, in its
// codex.rate_limits event ahead of the reply: a reply at 99% holds the
// account at its cap from the next turn on, which goes to the other
// account, with /wham/usage not asked again.
func TestCodexReplyHoldsTheCap(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })
	now := time.Now()
	five, week := now.Add(2*time.Hour).Unix(), now.Add(4*24*time.Hour).Unix()
	var mu sync.Mutex
	var tried []string
	reads := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.Header.Get("chatgpt-account-id")
		used := map[string]float64{"acct-1": 96, "acct-2": 20}[acct]
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/backend-api/codex/responses":
			io.ReadAll(r.Body)
			tried = append(tried, acct)
			// this turn takes the account to 99% of its five hours
			io.WriteString(w, sse(
				`event: codex.rate_limits`+"\n"+`data: {"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":99,"window_minutes":300,"reset_at":`+jsonInt(five)+`},"secondary":{"used_percent":41,"window_minutes":10080,"reset_at":`+jsonInt(week)+`}},"credits":null,"metered_limit_name":null,"limit_name":null}`,
				`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
				`data: {"type":"response.output_text.delta","delta":"pong"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		case "/backend-api/wham/usage":
			reads++
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus",
				"rate_limit": map[string]any{"allowed": true,
					"primary_window":   map[string]any{"used_percent": used, "limit_window_seconds": 18000, "reset_at": five},
					"secondary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 604800, "reset_at": week}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountCap("codex", "me@example.com", 99); err != nil {
		t.Fatal(err)
	}
	// read now, from the fake backend, not as another test left them
	for _, u := range []string{"me@example.com", "spare@example.com"} {
		provider.StaleAllowance("codex", u)
	}
	t.Cleanup(func() {
		for _, u := range []string{"me@example.com", "spare@example.com"} {
			provider.StaleAllowance("codex", u)
		}
	})
	share := func(u string) float64 {
		n, _ := provider.Allowances("codex")[u].For("gpt-5.5", time.Now())
		return n
	}
	for deadline := time.Now().Add(5 * time.Second); share("me@example.com") != 96 || share("spare@example.com") != 20; {
		if time.Now().After(deadline) {
			t.Fatalf("never read: %v%%, %v%%", share("me@example.com"), share("spare@example.com"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	before := reads
	mu.Unlock()

	srv := New()
	for i := range 2 {
		if code, body := resetPost(t, srv); code != 200 || !strings.Contains(body, "pong") {
			t.Fatalf("turn %d: %d %s", i+1, code, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(tried, ","); got != "acct-1,acct-2" {
		t.Fatalf("sent to %s; want the capped account once, then the other", got)
	}
	if reads != before {
		t.Fatalf("/wham/usage read %d more times: the reply told it", reads-before)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The event is found however the reply comes in pieces, and the reply
// passes on as it came; the headers tell the same. A model's own limit
// (metered_limit_name) and a window that doesn't say how long it runs
// tell nothing of the account's windows.
func TestCodexLimitsHeard(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: codex.rate_limits\ndata: {\"type\":\"codex.rate_limits\",\"rate_limits\":{\"primary\":{\"used_percent\":97.5,\"window_minutes\":300,\"reset_at\":1800003600},\"secondary\":{\"used_percent\":12}}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n"
	var heard [][]provider.CodexLimit
	tap := &codexLimitsTap{ReadCloser: io.NopCloser(&chunks{b: []byte(body), n: 7}), note: func(ls []provider.CodexLimit) { heard = append(heard, ls) }}
	got, _ := io.ReadAll(tap)
	if string(got) != body {
		t.Fatalf("the reply changed:\n%s", got)
	}
	if len(heard) != 1 || len(heard[0]) != 1 || heard[0][0].Used != 97.5 || heard[0][0].Span != 5*time.Hour || heard[0][0].Resets.Unix() != 1800003600 {
		t.Fatalf("heard %+v", heard)
	}
	if ls := codexEventLimits([]byte(`{"type":"codex.rate_limits","metered_limit_name":"codex_other","rate_limits":{"primary":{"used_percent":100,"window_minutes":300}}}`), now); ls != nil {
		t.Fatalf("a model's own limit taken for the account's: %+v", ls)
	}
	if ls := codexEventLimits([]byte(`{"type":"codex.rate_limits","metered_limit_name":"codex","rate_limits":{"primary":{"used_percent":3,"window_minutes":300,"reset_after_seconds":100}}}`), now); len(ls) != 1 || !ls[0].Resets.Equal(now.Add(100*time.Second)) {
		t.Fatalf("the account's own limit, named: %+v", ls)
	}
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "99")
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-primary-reset-at", "1800003600")
	h.Set("x-codex-secondary-used-percent", "40")
	h.Set("x-codex-secondary-window-minutes", "10080")
	h.Set("x-codex-secondary-reset-after-seconds", "60")
	ls := codexHeaderLimits(h, now)
	if len(ls) != 2 || ls[0].Used != 99 || ls[0].Span != 5*time.Hour || ls[0].Resets.Unix() != 1800003600 ||
		ls[1].Used != 40 || ls[1].Span != 7*24*time.Hour || !ls[1].Resets.Equal(now.Add(time.Minute)) {
		t.Fatalf("headers heard %+v", ls)
	}
}

// chunks reads b n bytes at a time.
type chunks struct {
	b []byte
	n int
}

func (c *chunks) Read(p []byte) (int, error) {
	if len(c.b) == 0 {
		return 0, io.EOF
	}
	k := copy(p[:min(len(p), c.n)], c.b)
	c.b = c.b[k:]
	return k, nil
}

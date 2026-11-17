package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/plugin"
)

// zenBunCase is a transport of Dispatch's that dials nothing, and the
// requests the plugin host's fetch was handed.
func zenBunCase(t *testing.T, fetchErr error, bun bool) (*http.Client, *[]plugin.FetchRequest, *int) {
	t.Helper()
	var got []plugin.FetchRequest
	dials := 0
	was, wasBun := bunFetch, hasBun
	t.Cleanup(func() { bunFetch, hasBun = was, wasBun })
	bunFetch = func(_ context.Context, r plugin.FetchRequest) (*http.Response, error) {
		got = append(got, r)
		if fetchErr != nil {
			return nil, fetchErr
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	}
	hasBun = func() bool { return bun }
	base := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("dialed")
	}}
	return &http.Client{Transport: netproxy.Dispatch(base)}, &got, &dials
}

// The reporter's case: a free model with the user's own key, asked at
// Zen's free tier, goes out through Bun's fetch, with the request's
// headers and body as they are, and is never dialed by Go.
func TestZenFreeTierGoesThroughBun(t *testing.T) {
	c, got, dials := zenBunCase(t, nil, true)
	body := `{"model":"deepseek-v4.1-flash-free","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", "https://opencode.ai/inference/openai/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-user-own")
	req.Header.Set("User-Agent", "opencode/latest/2.2.5/cli")
	req.Header.Set("x-opencode-session", "ses_1")
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("free tier: %v (dials %d)", err, *dials)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(b) != `{"ok":true}` {
		t.Fatalf("reply %d %s", res.StatusCode, b)
	}
	if *dials != 0 || len(*got) != 1 {
		t.Fatalf("dials %d, Bun fetches %d: want 0 and 1", *dials, len(*got))
	}
	r := (*got)[0]
	if r.URL != "https://opencode.ai/inference/openai/v1/chat/completions" || r.Method != "POST" || string(r.Body) != body {
		t.Fatalf("sent %s %s %s", r.Method, r.URL, r.Body)
	}
	if r.Headers["authorization"] != "Bearer sk-user-own" || r.Headers["user-agent"] != "opencode/latest/2.2.5/cli" || r.Headers["x-opencode-session"] != "ses_1" {
		t.Fatalf("headers %v", r.Headers)
	}
	if r.Provider != zenBunProvider || r.Account != "" {
		t.Fatalf("told the host provider %q account %q", r.Provider, r.Account)
	}
}

// Everything else, Zen's paid paths and OpenCode Go included, goes
// through Go's transport as before.
func TestZenPaidAndOthersStayInGo(t *testing.T) {
	c, got, dials := zenBunCase(t, nil, true)
	for _, u := range []string{
		"https://opencode.ai/zen/v1/chat/completions",
		"https://opencode.ai/zen/go/v1/chat/completions",
		"https://opencode.ai/zen/v1/models",
		"https://api.example.test/inference/openai/v1/chat/completions",
	} {
		req, _ := http.NewRequest("POST", u, strings.NewReader("{}"))
		if _, err := c.Do(req); err == nil || !strings.Contains(err.Error(), "dialed") {
			t.Fatalf("%s: %v, want Go's own dial", u, err)
		}
	}
	if len(*got) != 0 || *dials != 4 {
		t.Fatalf("Bun fetches %d, dials %d: want 0 and 4", len(*got), *dials)
	}
}

// Where Bun can't be had, the request still goes, through Go, body and
// all; where it can, the host's error is the request's.
func TestZenFreeTierWithoutBun(t *testing.T) {
	c, got, dials := zenBunCase(t, errors.New("downloading Bun 1.4.2 to run plugins: offline"), false)
	req, _ := http.NewRequest("POST", "https://opencode.ai/inference/openai/v1/chat/completions", strings.NewReader(`{"a":1}`))
	if _, err := c.Do(req); err == nil || !strings.Contains(err.Error(), "dialed") || *dials != 1 || len(*got) != 1 {
		t.Fatalf("no Bun: %v, dials %d, fetches %d", err, *dials, len(*got))
	}
	c, _, dials = zenBunCase(t, errors.New("ECONNRESET"), true)
	req, _ = http.NewRequest("POST", "https://opencode.ai/inference/openai/v1/chat/completions", strings.NewReader(`{"a":1}`))
	if _, err := c.Do(req); err == nil || !strings.Contains(err.Error(), "ECONNRESET") || *dials != 0 {
		t.Fatalf("Bun's error: %v, dials %d", err, *dials)
	}
}

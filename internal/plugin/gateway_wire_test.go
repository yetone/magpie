package plugin

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// wireBigBody builds a request body over maxWireLine (32 MiB), the most one
// line the host reads from the child, so any whole echo of it on the way back
// crosses the cap. The reporter's machine never showed the body itself, only
// "the plugin host sent a line longer than 33554432 bytes" 268 restarts in
// 2.5 hours (#5699); these tests build the body the report could not.
func wireBigBody() []byte {
	const head = `{"note":"original","pad":"`
	pad := maxWireLine + 4096 - len(head) - len(`"}`)
	return []byte(head + strings.Repeat("a", pad) + `"}`)
}

// wireHostAlive probes the shared host with one light request and reports
// whether the same host answered: a body echoed whole as one line over
// maxWireLine fails the host (errLineTooLong), every plugin on it loses its
// provider, and the next request starts a new host — the restart loop the
// report saw 268 times in 2.5 hours.
func wireHostAlive(t *testing.T, before *host) {
	t.Helper()
	again := GatewayBegin(t.Context(), []byte(`{}`), GatewayContext{})
	if again == nil {
		t.Fatal("a follow-up request could not reach the plugin host")
	}
	again.Close()
	if currentHost() != before {
		t.Fatal("the shared host was restarted between requests: a line over maxWireLine killed it (#5699)")
	}
}

// TestGatewayBeginLargeBodyUnchangedKeepsHostAlive: hooks that leave the
// request as it came (returning undefined) must not echo the body back —
// the host returns the run's identity alone and Go keeps the bytes it has.
// Before the fix the whole body came back as one line, over maxWireLine,
// and the shared host died serving a request no hook had touched.
func TestGatewayBeginLargeBodyUnchangedKeepsHostAlive(t *testing.T) {
	sandbox(t)
	gatewayFixture(t, `export function onRequest(body){}`)
	if _, err := Plugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := wireBigBody()
	before := currentHost()
	run := GatewayBegin(t.Context(), body, GatewayContext{})
	if run == nil {
		t.Fatal("the shared host died serving a body over maxWireLine though no hook changed it (#5699)")
	}
	if !bytes.Equal(run.Body, body) {
		t.Fatal("an unchanged request did not come back as it came")
	}
	run.Close()
	wireHostAlive(t, before)
	// the short path holds for small requests too: identity only, body kept
	small := []byte(`{"note":"original"}`)
	run = GatewayBegin(t.Context(), small, GatewayContext{})
	if run == nil || !bytes.Equal(run.Body, small) {
		t.Fatal("a small unchanged request did not keep its body")
	}
	run.Close()
}

// TestGatewayBeginLargeBodyTimeoutKeepsRequestAndHost: a hook that times out
// keeps the request — the bytes on the way back are the caller's own, which
// the host no longer echoes whole. Before the fix the timeout path itself
// echoed the body over maxWireLine and killed the shared host.
func TestGatewayBeginLargeBodyTimeoutKeepsRequestAndHost(t *testing.T) {
	sandbox(t)
	started, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		select {
		case stopped <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()
	gatewayFixture(t, `export async function onRequest(body, ctx) { body.note="corrupt"; await fetch(`+string(mustJSON(t, srv.URL))+`,{signal:ctx.signal}); return body }`)
	if _, err := Plugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := wireBigBody()
	before := currentHost()
	out := gatewayRequest(t.Context(), body, GatewayContext{})
	if !bytes.Equal(out, body) {
		t.Fatalf("a timed-out hook changed the request: %.120s", out)
	}
	select {
	case <-started:
	default:
		t.Fatal("hook never reached HTTP")
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("timed-out hook did not cancel HTTP")
	}
	wireHostAlive(t, before)
}

// TestGatewayBeginLargeBodyRewriteTooLargeGivesUpThatRequest: a hook that
// really rewrites a body too large to return gives up that one request —
// keeping the request, as a timed-out hook does — without retiring itself or
// the shared host. Before the fix the rewritten body came back as one line
// over maxWireLine and the shared host died; every plugin on it went down
// with it and the next request started the loop again.
func TestGatewayBeginLargeBodyRewriteTooLargeGivesUpThatRequest(t *testing.T) {
	sandbox(t)
	gatewayFixture(t, `export function onRequest(body){ return {...body, note:"recalled"} }`)
	if _, err := Plugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := wireBigBody()
	before := currentHost()
	if out := gatewayRequest(t.Context(), body, GatewayContext{}); !bytes.Equal(out, body) {
		t.Fatalf("a rewrite too large to return was served: %.120s", out)
	}
	// giving up one oversized rewrite does not retire the hook: it still
	// serves the next, ordinary request on the same host
	again := GatewayBegin(t.Context(), []byte(`{"note":"original"}`), GatewayContext{})
	if again == nil || !strings.Contains(string(again.Body), `"recalled"`) {
		t.Fatal("giving up one oversized rewrite retired the hook itself")
	}
	again.Close()
	wireHostAlive(t, before)
}

// TestGatewayBeginSmallBodyOrdinaryRewrite: the negative case — a small body
// through an ordinary rewrite is untouched by the wire-cap paths: recalled
// bytes served as before.
func TestGatewayBeginSmallBodyOrdinaryRewrite(t *testing.T) {
	sandbox(t)
	gatewayFixture(t, `export function onRequest(body){return {...body,note:"recalled"}}`)
	out := gatewayRequest(t.Context(), []byte(`{"note":"original","keep":true}`), GatewayContext{})
	if !strings.Contains(string(out), `"recalled"`) || !strings.Contains(string(out), `"keep":true`) {
		t.Fatalf("ordinary small-body rewrite no longer serves the recall: %s", out)
	}
}

// TestPluginAnswerTooLongFailsThatCallOnly: the begin's body was the one
// answer sure to be that big, but any answer the host writes can be: here
// a plugin's fetch fails with an error the size of a vendor's reply. Too
// long for one line, it fails that request alone, and the same host serves
// the next. Before, the line killed the host every plugin shares (#5699,
// #7306).
func TestPluginAnswerTooLongFailsThatCallOnly(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	src := `export const Huge = async () => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider.hugeco = { name: "HugeCo", npm: "@ai-sdk/openai-compatible", api: "https://huge.invalid/v1", models: { "h-1": { name: "H One" } } }
  },
  auth: {
    provider: "hugeco",
    methods: [{ type: "api", label: "API key" }],
    loader: async () => ({
      apiKey: "k",
      async fetch(url) {
        if (String(url).endsWith("/huge")) throw new Error("x".repeat(` + strconv.Itoa(maxWireLine+4096) + `))
        return new Response("fine")
      },
    }),
  },
})
`
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, filepath.Join(dir, "index.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(AuthPath(), []byte(`{"hugeco":{"type":"api","key":"k"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fetch := func(path string) (string, error) {
		res, err := Fetch(ctx, FetchRequest{Provider: "hugeco", Model: "h-1", NPM: "@ai-sdk/openai-compatible",
			URL: "https://huge.invalid/v1" + path, Method: "POST", Body: []byte(`{}`)})
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return string(b), err
	}
	if got, err := fetch("/fine"); err != nil || got != "fine" {
		t.Fatalf("first request: %q, %v", got, err)
	}
	before := currentHost()
	if _, err := fetch("/huge"); err == nil || !strings.Contains(err.Error(), "the plugin's answer is") {
		t.Fatalf("an error too long for one line: %v; want that request failed on its own", err)
	}
	if got, err := fetch("/fine"); err != nil || got != "fine" {
		t.Fatalf("the request after it: %q, %v", got, err)
	}
	if currentHost() != before {
		t.Fatal("the shared host was restarted: an answer over maxWireLine killed it (#5699)")
	}
}

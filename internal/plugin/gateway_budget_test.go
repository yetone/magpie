package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGatewayHookSlowRecallUsesConfiguredBudget (#5890, aboat0625-cpu): a
// recall that takes 12s, given 15s, gets its result, past the 10s chain
// and 11s RPC bounds every hook had; and its onResponse gets the same 15s.
func TestGatewayHookSlowRecallUsesConfiguredBudget(t *testing.T) {
	sandbox(t)
	output := filepath.Join(t.TempDir(), "captured")
	spec := gatewayFixture(t, `import fs from "node:fs";
export async function onRequest(body, ctx) {
  await new Promise(resolve => setTimeout(resolve, 12000));
  ctx.report("ready", 12);
  return {...body, note: "recalled"};
}
export async function onResponse(reply, ctx) {
  await new Promise(resolve => setTimeout(resolve, 12000));
  if (!ctx.signal.aborted) fs.writeFileSync(ctx.options.output, JSON.stringify(reply));
}`)
	if err := SetOptions(spec, map[string]any{"gatewayTimeoutMs": 15000, "output": output}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	run := GatewayBegin(ctx, []byte(`{"note":"original"}`), GatewayContext{})
	if run == nil || !strings.Contains(string(run.Body), `"recalled"`) {
		if run != nil {
			run.Close()
		}
		t.Fatal("slow recall was dropped despite its configured budget")
	}
	run.Finish(ctx, json.RawMessage(`{"answer":"done"}`), true, run.Body)
	for {
		if b, err := os.ReadFile(output); err == nil {
			if string(b) != `{"answer":"done"}` {
				t.Fatalf("captured %s", b)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("a slow onResponse was cut before its configured budget")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestGatewayHookDefaultBudgetStillCuts: without a longer timeout a slow
// hook is cut where it always was, by the host's own timer ("timeout", not
// the RPC's deadline, which would read "cancelled"): at the default second,
// and at 10s for the 10000 every host took. A first, quick request starts
// the host, so the start doesn't eat the second's margin.
func TestGatewayHookDefaultBudgetStillCuts(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts map[string]any
		most float64 // ms the hook may have taken: far over its bound, far under the 11s RPC's
	}{{"default", nil, 8000}, {"10000", map[string]any{"gatewayTimeoutMs": 10000}, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			// The first request only starts the host: the RPC's 11s counts
			// the host's start too, and on a busy CI machine a start past a
			// second let it cut the 10000 hook first ("cancelled", run
			// 42819148533 on macOS). The second request is the one measured.
			spec := gatewayFixture(t, `let seen = 0;
export async function onRequest(body) {
  if (seen++ === 0) return body;
  await new Promise(resolve => setTimeout(resolve, 12000));
  return {...body, note: "recalled"};
}`)
			if tc.opts != nil {
				if err := SetOptions(spec, tc.opts); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			if out := gatewayRequest(ctx, []byte(`{"note":"original"}`), GatewayContext{}); string(out) != `{"note":"original"}` {
				t.Fatalf("the warming request came back changed: %s", out)
			}
			if out := gatewayRequest(ctx, []byte(`{"note":"original"}`), GatewayContext{}); string(out) != `{"note":"original"}` {
				t.Fatalf("a hook past its default budget was kept: %s", out)
			}
			state := GatewayStates(t.Context())[spec]
			if state.LastOutcome != "timeout" || tc.most > 0 && state.AvgMillis > tc.most {
				t.Fatalf("the hook wasn't cut by its own budget: %+v", state)
			}
		})
	}
}

// TestGatewayHookChainStillTenSeconds: hooks of 10000 or less share the
// chain's 10s as before. Two of 8000: the first takes 4s and is kept (4s
// to spare for the host's start on a busy CI machine, where 6s of 8 ran
// out), the second takes 7s, inside its own 8s but past the ~6s the chain
// has left, so only the chain's 10s can cut it.
func TestGatewayHookChainStillTenSeconds(t *testing.T) {
	sandbox(t)
	var specs []string
	for _, h := range []struct {
		name string
		ms   int
	}{{"first", 4000}, {"second", 7000}} {
		name := h.name
		spec := gatewayFixture(t, `export async function onRequest(body) {
  await new Promise(resolve => setTimeout(resolve, `+strconv.Itoa(h.ms)+`));
  return {...body, `+name+`: true};
}`)
		if err := SetOptions(spec, map[string]any{"gatewayTimeoutMs": 8000}); err != nil {
			t.Fatal(err)
		}
		specs = append(specs, spec)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	out := gatewayRequest(ctx, []byte(`{"note":"original"}`), GatewayContext{})
	if !strings.Contains(string(out), `"first":true`) || strings.Contains(string(out), `"second"`) {
		t.Fatalf("the chain's 10s changed: %s", out)
	}
	if state := GatewayStates(t.Context())[specs[1]]; state.LastOutcome != "timeout" {
		t.Fatalf("the second hook wasn't cut by the chain: %+v", state)
	}
}

// TestGatewayHookTimeoutClamps: a timeout over 30000 is taken as 30000,
// not as the default second (the reporter's 130000 fell back to 1000).
func TestGatewayHookTimeoutClamps(t *testing.T) {
	sandbox(t)
	spec := gatewayFixture(t, `export async function onRequest(body) {
  await new Promise(resolve => setTimeout(resolve, 1500));
  return {...body, note: "recalled"};
}`)
	if err := SetOptions(spec, map[string]any{"gatewayTimeoutMs": 130000}); err != nil {
		t.Fatal(err)
	}
	if out := gatewayRequest(t.Context(), []byte(`{"note":"original"}`), GatewayContext{}); !strings.Contains(string(out), `"recalled"`) {
		t.Fatalf("an over-long timeout fell back to the default: %s", out)
	}
}

// TestGatewayHookLongBudgetCallerCancellation: a hook given 15s still
// stops at once when the caller goes.
func TestGatewayHookLongBudgetCallerCancellation(t *testing.T) {
	sandbox(t)
	started, stopped := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer srv.Close()
	spec := gatewayFixture(t, `export async function onRequest(body,ctx) { await fetch(`+string(mustJSON(t, srv.URL))+`,{signal:ctx.signal}); return {...body, note: "recalled"} }`)
	if err := SetOptions(spec, map[string]any{"gatewayTimeoutMs": 15000}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan []byte, 1)
	go func() { done <- gatewayRequest(ctx, []byte(`{"note":"original"}`), GatewayContext{}) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("hook never started")
	}
	cancel()
	// well under the 15s the hook was given
	select {
	case out := <-done:
		if string(out) != `{"note":"original"}` {
			t.Fatalf("cancel changed body: %s", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("caller cancellation waited for the hook's budget")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("caller cancellation did not stop the hook's HTTP")
	}
}

// TestGatewayBudgetMirrorsHost: gateway.go reckons the hooks' timeouts and
// the chain's budget as host.js does, and the RPC bound stays 11s for
// every budget a host had before.
func TestGatewayBudgetMirrorsHost(t *testing.T) {
	for v, want := range map[any]time.Duration{
		nil: time.Second, "15000": time.Second, 15000.5: time.Second, true: time.Second,
		float64(15000): 15 * time.Second, 15000: 15 * time.Second, float64(99): 100 * time.Millisecond,
		float64(-5): 100 * time.Millisecond, float64(130000): 30 * time.Second, json.Number("2000"): 2 * time.Second,
	} {
		if got := gatewayTimeout(map[string]any{"gatewayTimeoutMs": v}); got != want {
			t.Errorf("gatewayTimeout(%v) = %v, want %v", v, got, want)
		}
	}
	s := time.Second
	for _, tc := range []struct {
		each []time.Duration
		want time.Duration
	}{
		{nil, 2 * s}, {[]time.Duration{s}, 2 * s}, {[]time.Duration{5 * s, 5 * s, 5 * s}, 10 * s},
		{[]time.Duration{10 * s}, 10 * s}, {[]time.Duration{15 * s, s}, 15 * s}, {[]time.Duration{30 * s, 30 * s}, 30 * s},
	} {
		if got := gatewayBudget(tc.each); got != tc.want {
			t.Errorf("gatewayBudget(%v) = %v, want %v", tc.each, got, tc.want)
		}
	}
	sandbox(t)
	spec := gatewayFixture(t, `export function onRequest(body){return body}`)
	if got := gatewayWait(); got != 11*s {
		t.Fatalf("default RPC bound %v, want 11s", got)
	}
	if err := SetOptions(spec, map[string]any{"gatewayTimeoutMs": 10000}); err != nil {
		t.Fatal(err)
	}
	if got := gatewayWait(); got != 11*s {
		t.Fatalf("RPC bound for 10000: %v, want 11s", got)
	}
	if err := SetOptions(spec, map[string]any{"gatewayTimeoutMs": 15000}); err != nil {
		t.Fatal(err)
	}
	if got := gatewayWait(); got != 16*s {
		t.Fatalf("RPC bound for 15000: %v, want 16s", got)
	}
}

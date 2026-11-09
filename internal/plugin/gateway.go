package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

// Gateway is the asynchronous request-hook entry a package runs in Bun:
// package.json magpie.hooks, or a *.hooks.{js,mjs,ts,mts} file. Unlike
// middleware (magpie.middleware/magpie.gateway, *.gateway.js), it can await
// I/O and observe the completed exchange; the names never overlap, so one
// file is never loaded as both.
func Gateway(target string) string {
	st, err := os.Stat(target)
	if err != nil {
		return ""
	}
	if !st.IsDir() {
		base := strings.ToLower(filepath.Base(target))
		// Bun runs TypeScript directly, so a hook may be a .ts file.
		for _, ext := range []string{".hooks.js", ".hooks.mjs", ".hooks.ts", ".hooks.mts"} {
			if strings.HasSuffix(base, ext) {
				return target
			}
		}
		return ""
	}
	var pkg struct {
		Magpie struct {
			Hooks string `json:"hooks"`
		} `json:"magpie"`
	}
	b, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil || json.Unmarshal(b, &pkg) != nil || strings.TrimSpace(pkg.Magpie.Hooks) == "" {
		return ""
	}
	return filepath.Join(target, filepath.FromSlash(strings.TrimSpace(pkg.Magpie.Hooks)))
}

// GatewayOnly distinguishes request hooks from packages that also serve a
// provider or add an agent. Keep the provider entry rules aligned with Bun.
func GatewayOnly(target string) bool {
	if Gateway(target) == "" {
		return false
	}
	st, err := os.Stat(target)
	if err != nil {
		return false
	}
	if !st.IsDir() {
		return true
	}
	var pkg struct {
		Main    string
		Exports json.RawMessage
	}
	b, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil || json.Unmarshal(b, &pkg) != nil || pkg.Main != "" || len(pkg.Exports) != 0 {
		return false
	}
	if file, _ := Agent(target); file != "" {
		return false
	}
	for _, name := range []string{"index.ts", "index.tsx", "index.js", "index.mjs", "index.cjs"} {
		if _, err := os.Stat(filepath.Join(target, name)); err == nil {
			return false
		}
	}
	return true
}

// GatewayContext contains only the request identity and text, never its
// authorization headers. Empty fields mean the agent supplied no identity.
type GatewayContext struct {
	Origin   string `json:"origin"` // local, remote, magpie-key or gateway-key; empty means unknown
	Protocol string `json:"protocol"`
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Path     string `json:"path"`
	Agent    string `json:"agent"`
	Session  string `json:"session"`
	Cwd      string `json:"cwd"`
	Query    string `json:"query"`
}

// GatewayState is aggregate diagnostics for this host's lifetime. Outcomes
// are fixed codes; no request text, credentials or private context is kept.
type GatewayState struct {
	Calls       uint64  `json:"calls"`
	Failures    uint64  `json:"failures"`
	AvgMillis   float64 `json:"avgMillis"`
	LastOutcome string  `json:"lastOutcome"`
	LastBytes   uint64  `json:"lastBytes"`
}

// GatewayStates asks only an existing host, so opening Settings does not
// start a gateway hook merely to display its diagnostics.
func GatewayStates(ctx context.Context) map[string]GatewayState {
	if !HasGateway() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if !hostMu.TryLock() {
		return nil
	}
	h := current
	hostMu.Unlock()
	if h == nil {
		return nil
	}
	var states map[string]GatewayState
	if h.call(ctx, "gatewayStates", nil, &states) != nil {
		return nil
	}
	return states
}

// HasGateway avoids starting Bun for users who have no asynchronous hooks.
// Every request asks it, so the plugins are looked at once a second, and at
// once after magpie wrote a file of its own (filememo.Gen): reading
// plugins.json and each plugin's package.json took ~1ms a request with 12
// plugins on Windows (#3725).
func HasGateway() bool {
	now, g, list := time.Now().UnixNano(), filememo.Gen(), listPath()
	if c := hasGateway.Load(); c != nil && c.gen == g && c.list == list && now < c.until {
		return c.on
	}
	on := false
	for _, e := range Load().Plugins {
		if !e.Off && Gateway(Target(e.Spec)) != "" {
			on = true
			break
		}
	}
	hasGateway.Store(&gatewaySeen{on: on, gen: g, list: list, until: now + int64(time.Second)})
	return on
}

// gatewaySeen is HasGateway's last look: for the plugins.json at list,
// while filememo's generation is gen, until until (UnixNano).
type gatewaySeen struct {
	on    bool
	gen   uint64
	list  string
	until int64
}

var hasGateway atomic.Pointer[gatewaySeen]

// GatewayRun belongs to the host that began it, even if the plugin list changes
// before the response. Close releases its private state and cancels active I/O.
type GatewayRun struct {
	Body json.RawMessage

	h        *host
	id       string
	token    string
	mu       sync.Mutex
	closed   bool
	finished bool
}

// GatewayBegin bounds startup and the entire request-hook chain. Failure or an
// absent hook returns nil so the gateway can keep the original request.
func GatewayBegin(ctx context.Context, body []byte, info GatewayContext) *GatewayRun {
	if !HasGateway() {
		return nil
	}
	start, cancel := context.WithTimeout(ctx, 11*time.Second)
	defer cancel()
	checkList()
	// Provider discovery may already be starting the shared host. Wait within
	// the same hook budget rather than skipping recall on the first request.
	wait := time.NewTicker(10 * time.Millisecond)
	defer wait.Stop()
	for !hostMu.TryLock() {
		select {
		case <-start.Done():
			return nil
		case <-wait.C:
		}
	}
	if start.Err() != nil {
		hostMu.Unlock()
		return nil
	}
	h, err := getLocked(start)
	hostMu.Unlock()
	if err != nil || start.Err() != nil {
		return nil
	}
	var out struct {
		ID    string          `json:"id"`
		Token string          `json:"token"`
		Body  json.RawMessage `json:"body"`
	}
	err = h.call(start, "gatewayRequest", map[string]any{"operation": "begin", "body": json.RawMessage(body), "context": info}, &out)
	if err != nil || out.ID == "" || out.Token == "" || len(out.Body) == 0 || out.Body[0] != '{' || !json.Valid(out.Body) {
		return nil
	}
	// the run ends when its owner finishes or closes it, never when the
	// client goes: Codex hangs up on response.completed, and the reply it
	// got is still the exchange to observe
	return &GatewayRun{Body: out.Body, h: h, id: out.ID, token: out.Token}
}

// Finish schedules a bounded completion notification without waiting for it.
// Client cancellation after delivery does not cancel the notification. The
// host retains bounded request state; shutdown may discard pending callbacks.
func (r *GatewayRun) Finish(ctx context.Context, reply json.RawMessage, success bool, request ...json.RawMessage) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed || r.finished {
		r.mu.Unlock()
		return
	}
	r.finished, r.closed = true, true
	r.mu.Unlock()
	if !json.Valid(reply) {
		reply = json.RawMessage("null")
		success = false
	}
	params := map[string]any{"operation": "finish", "id": r.id, "token": r.token, "reply": append(json.RawMessage(nil), reply...), "success": success}
	if len(request) != 0 && json.Valid(request[0]) {
		params["request"] = append(json.RawMessage(nil), request[0]...)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 11*time.Second)
		defer cancel()
		_ = r.h.call(ctx, "gatewayRequest", params, nil)
	}()
}

// Close releases an abandoned request and cancels active plugin I/O.
func (r *GatewayRun) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = r.h.call(ctx, "gatewayRequest", map[string]any{"operation": "close", "id": r.id, "token": r.token}, nil)
}

// SignInEvent is a subscription account that has to be signed in again
// (State "signed-out"), or whose sign-in ends at EndsAt (State "ending",
// Claude's), as magpie's own sign-in alert tells it. Account is the
// account's label as the Providers page shows it; no token, mark or
// vendor text is sent.
type SignInEvent struct {
	Provider     string     `json:"provider"`
	ProviderName string     `json:"providerName"`
	Account      string     `json:"account"`
	State        string     `json:"state"`
	EndsAt       *time.Time `json:"endsAt,omitempty"`
}

// GatewaySignIn hands ev to every request hook's onSignIn, without waiting
// for them: the host is started if it isn't, within the request hooks'
// eleven seconds. Nothing is done for a user with no hooks.
func GatewaySignIn(ctx context.Context, ev SignInEvent) {
	if !HasGateway() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 11*time.Second)
		defer cancel()
		checkList()
		wait := time.NewTicker(10 * time.Millisecond)
		defer wait.Stop()
		for !hostMu.TryLock() {
			select {
			case <-ctx.Done():
				return
			case <-wait.C:
			}
		}
		h, err := getLocked(ctx)
		hostMu.Unlock()
		if err != nil {
			return
		}
		_ = h.call(ctx, "gatewayRequest", map[string]any{"operation": "signIn", "event": ev}, nil)
	}()
}

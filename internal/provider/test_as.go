package provider

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/filememo"
)

// A relay can serve only the agents its plan is sold for, and tell them by
// the headers they send: "This account only allows Codex official
// clients", "only accessible via the official Claude CLI". The gateway
// lets those headers go on to a provider reached with a key, as the agent
// sent them (gateway.go: fromCodex, fromClaudeCode), so the agent's own
// requests are served, while a test request, magpie's own, was turned away
// and said the provider didn't work (耍赖天都爱 on Discord). A test can be
// asked as one of them asks instead (TestAs): on the API that agent's
// requests go to, streamed as they are, with the headers the gateway last
// let through for it.
//
// Claude Code's are never made up: they are the ones a Claude Code request
// that came through this magpie had, and they go to a relay alone, never to
// Anthropic's own API or a cloud's, nor to a signed-in account. Until
// Claude Code has come through, a test as it says so. Codex's, until Codex
// has come through, are the ones a ChatGPT account's requests are made with
// (codexSign).

// The clients a test can be asked as.
const (
	ClientCodex      = "codex"
	ClientClaudeCode = "claude-code"
)

type testClientKey struct{}

// TestAs is ctx with Test and TestModels asking as client asks ("" as
// magpie does).
func TestAs(ctx context.Context, client string) context.Context {
	return context.WithValue(ctx, testClientKey{}, client)
}

func testClient(ctx context.Context) string {
	c, _ := ctx.Value(testClientKey{}).(string)
	return c
}

// clientProto is the API client's requests go to, of those p speaks: Codex
// asks on Responses, else Chat Completions; Claude Code on Anthropic's
// Messages alone.
func clientProto(client string, speaks []Protocol) Protocol {
	want := []Protocol{Responses, Chat}
	if client == ClientClaudeCode {
		want = []Protocol{Anthropic}
	}
	for _, pr := range want {
		if slices.Contains(speaks, pr) {
			return pr
		}
	}
	return ""
}

// TestsAs says why p can't be tested as client asks ("" when it can).
func (p Provider) TestsAs(client string) string {
	switch client {
	case "":
		return ""
	case ClientCodex, ClientClaudeCode:
	default:
		return "no such client \"" + client + "\": codex or claude-code"
	}
	if p.Account != nil || p.DecideOnly() {
		return "a signed-in account is asked as its own client already"
	}
	if p.IsOpenCode() || p.IsCline() || p.IsKilo() {
		return "its requests are asked as its own client already"
	}
	if p.IsVertex() && client == ClientCodex {
		// Google's own endpoints, as Claude Code's are below: its chat
		// completions serve the partner models alone
		return "a test is asked as Codex only of a relay"
	}
	if clientProto(client, p.Speaks()) == "" {
		if client == ClientClaudeCode {
			return "it has no Anthropic endpoint, where Claude Code's requests go"
		}
		return "it has no Responses or Chat Completions endpoint, where Codex's requests go"
	}
	if client == ClientClaudeCode {
		h := HostOf(p.Base(Anthropic))
		if strings.HasSuffix(h, "anthropic.com") || strings.HasSuffix(h, "googleapis.com") || p.IsBedrock() || p.IsAzure() {
			return "a test is asked as Claude Code only of a relay"
		}
		if clientShape(ClientClaudeCode) == nil {
			return errNotSeen
		}
	}
	return ""
}

// clientShapes are the headers the gateway last let through for each
// client, kept in client-shapes.json beside the providers so a test from
// the CLI has them too. written is what was last written there, so a
// request whose headers are the same writes nothing.
var clientShapes struct {
	sync.Mutex
	written map[string]http.Header // by path and client
}

func clientShapesPath() string {
	return filepath.Join(filepath.Dir(Path()), "client-shapes.json")
}

// shapeHeader is one of client's headers a test asks with: not what is
// this conversation's or this turn's alone (its session, thread or turn,
// a request id, a subagent's kind, the Codex window it came from, a
// retry's count), nor the betas this request asked for, nor anything that
// signs it. One that differs between two of the client's windows would
// write the file again on each of their requests (#3394).
func shapeHeader(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"session", "thread", "conversation", "turn", "request-id", "window", "retry-count", "authorization", "api-key", "attestation", "subagent", "luna-reserve"} {
		if strings.Contains(k, s) {
			return false
		}
	}
	return k != "anthropic-beta"
}

// SawClient notes the headers the gateway let go on as client sent them,
// for a test asked as client to ask with.
func SawClient(client string, h http.Header) {
	shape := http.Header{}
	for k, vs := range h {
		if shapeHeader(k) && len(vs) > 0 {
			shape[http.CanonicalHeaderKey(k)] = slices.Clone(vs)
		}
	}
	if shape.Get("User-Agent") == "" {
		return
	}
	path := clientShapesPath()
	clientShapes.Lock()
	defer clientShapes.Unlock()
	if clientShapes.written == nil {
		clientShapes.written = map[string]http.Header{}
	}
	if equalHeaders(clientShapes.written[path+"\x00"+client], shape) {
		return
	}
	m := readClientShapes(path)
	m[client] = shape
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	if writeFileAtomic(path, b) == nil {
		clientShapes.written[path+"\x00"+client] = shape
	}
}

func readClientShapes(path string) map[string]http.Header {
	m := map[string]http.Header{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &m)
	}
	if m == nil {
		m = map[string]http.Header{}
	}
	return m
}

func equalHeaders(a, b http.Header) bool {
	return a != nil && maps.EqualFunc(map[string][]string(a), map[string][]string(b), slices.Equal[[]string])
}

// shapeParses counts client-shapes.json's parses (a test reads it).
var shapeParses atomic.Int64

// clientShape is the headers last seen from client, nil when it hasn't
// come through. The file is parsed once until it changes: /api/providers
// asks it of every relay a list shows, and read anew each time it was a
// sixth of the list's CPU. A copy goes out, as a request's headers take
// it.
func clientShape(client string) http.Header {
	m, err := filememo.Read("client-shapes", clientShapesPath(), func(b []byte) (map[string]http.Header, error) {
		shapeParses.Add(1)
		m := map[string]http.Header{}
		json.Unmarshal(b, &m)
		return m, nil
	})
	if err != nil {
		return nil
	}
	return m[client].Clone()
}

// clientHeaders puts client's headers on a test request: those last seen
// from it, else, for Codex, the ones a ChatGPT account's requests go with.
func clientHeaders(client string, h http.Header) {
	shape := clientShape(client)
	if shape == nil && client == ClientCodex {
		v := codexVersion()
		shape = http.Header{"User-Agent": {codexUserAgent(v)}, "Originator": {"codex_cli_rs"}, "Version": {v}}
	}
	for k, vs := range shape {
		h[k] = vs
	}
	h.Set("Accept", "application/json, text/event-stream")
}

// asClient is a test body asked as client asks: streamed, as both agents
// ask every request, and on Responses not kept, as Codex asks.
func asClient(client string, proto Protocol, body string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return body
	}
	m["stream"] = true
	if client == ClientCodex && proto == Responses {
		m["store"] = false
	}
	b, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return string(b)
}

// errNotSeen is TestsAs's answer for Claude Code before it has come through.
const errNotSeen = "no Claude Code request has come through magpie yet: run Claude Code through magpie once, then test again"

// TestClients is the clients p's models can be tested as, each with why
// it can't be yet ("" when it can): those whose API p has, of a provider
// reached with a key; Claude Code only of a relay.
func (p Provider) TestClients() map[string]string {
	if p.ModelTest() != "" {
		return nil
	}
	out := map[string]string{}
	for _, c := range []string{ClientCodex, ClientClaudeCode} {
		if why := p.TestsAs(c); why == "" || why == errNotSeen {
			out[c] = why
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

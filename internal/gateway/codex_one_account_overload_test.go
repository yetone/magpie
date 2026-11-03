package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// overloadedStatus is the ChatGPT backend's overload said as a status.
const overloadedStatus = `{"error":{"type":"server_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`

// codexAnswer is a turn the ChatGPT backend answers, its frames first.
var codexAnswer = sse(append(slices.Clone(overloadedLead),
	`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"hello"}`,
	`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":9,"output_tokens":2}}}`)...)

// overloadedBackend stands in for the ChatGPT backend, answering the n-th
// request (from 0) with replies[n], the last one after that; it keeps
// every request's body and headers. A 200 goes with no Content-Type, as
// the backend sends it.
type overloadedBackend struct {
	replies []reply
	mu      sync.Mutex
	bodies  [][]byte
	heads   []http.Header
}

func (o *overloadedBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	o.mu.Lock()
	i := min(len(o.bodies), len(o.replies)-1)
	o.bodies = append(o.bodies, b)
	o.heads = append(o.heads, r.Header.Clone())
	o.mu.Unlock()
	x := o.replies[i]
	if x.ctype != "" {
		w.Header().Set("Content-Type", x.ctype)
	} else {
		w.Header()["Content-Type"] = nil
	}
	w.WriteHeader(x.code)
	io.WriteString(w, x.body)
}

func (o *overloadedBackend) asked() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.bodies)
}

// oneCodexAccount is Codex signed in to ChatGPT with its one account and
// nothing else on in magpie, the ChatGPT backend o: a turn on one of
// Codex's own models is relayed as it came (codexAccounts), nobody else
// to ask.
func oneCodexAccount(t *testing.T, o *overloadedBackend) {
	t.Helper()
	codexSignedIn(t)
	up := httptest.NewServer(o)
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	pause := retryPause
	retryPause = time.Millisecond
	t.Cleanup(func() { retryPause = pause })
	if id, routed := codexAccounts(httptest.NewRequest("POST", CodexPath+"/responses", nil), "gpt-5.5"); routed {
		t.Fatalf("one account goes through routing as %s", id)
	}
}

const oneAccountTurn = `{"model":"gpt-5.5","stream":true,"input":"ping"}`

// noneResting says nothing rests after an overload: no account is at
// fault for it.
func noneResting(t *testing.T) {
	t.Helper()
	restingUntil.Lock()
	defer restingUntil.Unlock()
	if len(restingUntil.m) > 0 {
		t.Fatalf("rested for an overload: %v", restingUntil.m)
	}
}

// With one ChatGPT account, Codex's own turn the backend says it is too
// busy for (529) is asked again of the same account, after the pause the
// accounts loop takes, as Codex sent it (夜松云 on Discord): the answer
// reaches Codex, nothing of the failure.
func TestCodexOneAccountOverloadAskedAgain(t *testing.T) {
	o := &overloadedBackend{replies: []reply{{529, "application/json", overloadedStatus}, {200, "", codexAnswer}}}
	oneCodexAccount(t, o)
	code, body := codexPost(t, oneAccountTurn)
	if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "overloaded") || o.asked() != 2 {
		t.Fatalf("%d %q, asked %d times", code, body, o.asked())
	}
	// the turn goes again as it went: the same bytes, the same sign-in
	if !bytes.Equal(o.bodies[0], o.bodies[1]) {
		t.Fatalf("asked again with another body:\n%s\n%s", o.bodies[0], o.bodies[1])
	}
	for _, k := range []string{"Authorization", "Chatgpt-Account-Id"} {
		if o.heads[0].Get(k) == "" || o.heads[0].Get(k) != o.heads[1].Get(k) {
			t.Fatalf("%s: %q then %q", k, o.heads[0].Get(k), o.heads[1].Get(k))
		}
	}
	noneResting(t)
}

// The ChatGPT backend's overload as it says it on a turn (#1418): a 200,
// the frames before a reply, then server_is_overloaded. Codex doesn't ask
// again for it; held while it has only those frames, the turn is asked
// again, and Codex has the answer and nothing of the failure.
func TestCodexOneAccountStreamOverloadAskedAgain(t *testing.T) {
	failed := reply{200, "", sse(append(slices.Clone(overloadedLead), overloadedFailed)...)}
	o := &overloadedBackend{replies: []reply{failed, {200, "", codexAnswer}}}
	oneCodexAccount(t, o)
	code, body := codexPost(t, oneAccountTurn)
	if code != 200 || !strings.Contains(body, "hello") || strings.Contains(body, "server_is_overloaded") || o.asked() != 2 {
		t.Fatalf("%d %q, asked %d times", code, body, o.asked())
	}
	// the answer's frames go with it, once
	if strings.Count(body, "event: response.created") != 1 {
		t.Fatalf("frames: %q", body)
	}
	noneResting(t)
}

// An overload that lasts is asked again as often as the accounts loop
// asks its last account (lastRetries), and then Codex has the backend's
// own failure, as it had before.
func TestCodexOneAccountOverloadBounded(t *testing.T) {
	for name, failed := range map[string]reply{
		"status": {529, "application/json", overloadedStatus},
		"stream": {200, "", sse(append(slices.Clone(overloadedLead), overloadedFailed)...)},
	} {
		t.Run(name, func(t *testing.T) {
			o := &overloadedBackend{replies: []reply{failed}}
			oneCodexAccount(t, o)
			code, body := codexPost(t, oneAccountTurn)
			if o.asked() != 1+lastRetries || !strings.Contains(body, "server_is_overloaded") {
				t.Fatalf("%d %q, asked %d times", code, body, o.asked())
			}
			if code != failed.code {
				t.Fatalf("status %d, the backend's %d", code, failed.code)
			}
			noneResting(t)
		})
	}
}

// With fallbacks set on the Codex subscription, an overload that lasts
// goes on to them once the account was asked again, as a turn through
// magpie's accounts does; without the account asked any more.
func TestCodexOneAccountOverloadFallsBack(t *testing.T) {
	for name, failed := range map[string]reply{
		"status": {529, "application/json", overloadedStatus},
		"stream": {200, "", sse(append(slices.Clone(overloadedLead), overloadedFailed)...)},
	} {
		t.Run(name, func(t *testing.T) {
			o := &overloadedBackend{replies: []reply{failed}}
			oneCodexAccount(t, o)
			fb := &scripted{replies: []reply{{200, "text/event-stream", sse(
				`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from the fallback"}`,
				`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"f1","status":"completed","output":[]}}`)}}}
			responsesOn(t, "fb", fb)
			if err := provider.Save(provider.Provider{ID: "codex", Fallback: []string{"fb/m"}}); err != nil {
				t.Fatal(err)
			}
			code, body := codexPost(t, oneAccountTurn)
			if code != 200 || !strings.Contains(body, "from the fallback") || strings.Contains(body, "server_is_overloaded") {
				t.Fatalf("%d %q", code, body)
			}
			if o.asked() != 1+lastRetries || fb.n != 1 {
				t.Fatalf("ChatGPT asked %d times, the fallback %d", o.asked(), fb.n)
			}
			noneResting(t)
		})
	}
}

// A usage limit isn't an overload: it isn't asked again, and with no
// fallback it goes to Codex as the backend said it, asked once
// (TestCodexOwnModelOneAccountRelayed; with a fallback,
// TestCodexOneAccountSpentFallsBack). So does a refusal of the request.
func TestCodexOneAccountRefusalNotAskedAgain(t *testing.T) {
	o := &overloadedBackend{replies: []reply{{400, "application/json", `{"error":{"type":"invalid_request_error","message":"Unsupported parameter: foo"}}`}}}
	oneCodexAccount(t, o)
	code, body := codexPost(t, oneAccountTurn)
	if code != 400 || !strings.Contains(body, "Unsupported parameter") || o.asked() != 1 {
		t.Fatalf("%d %q, asked %d times", code, body, o.asked())
	}
}

// A backend that is slow to say it is overloaded (#1418: 36s after its
// frames) has Codex kept alive meanwhile with the stream's headers and
// comments, none of the failed tries; what follows goes on in that
// stream: the last try's failure as it was said, or the fallback's reply.
func TestCodexOneAccountOverloadKeptAlive(t *testing.T) {
	k, w := keepHeldAfter, watchEvery
	keepHeldAfter, watchEvery = 20*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery = k, w })
	for _, withFallback := range []bool{false, true} {
		codexSignedIn(t)
		slow := &slowFrames{wait: 150 * time.Millisecond, then: func(int) string { return sse(overloadedFailed) }}
		up := httptest.NewServer(slow)
		t.Cleanup(up.Close)
		was := provider.CodexBase
		provider.CodexBase = up.URL + "/backend-api/codex"
		t.Cleanup(func() { provider.CodexBase = was })
		pause := retryPause
		retryPause = time.Millisecond
		t.Cleanup(func() { retryPause = pause })
		fb := &scripted{replies: []reply{{200, "text/event-stream", sse(
			`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","delta":"from the fallback"}`,
			`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"f1","status":"completed","output":[]}}`)}}}
		if withFallback {
			responsesOn(t, "fb", fb)
			if err := provider.Save(provider.Provider{ID: "codex", Fallback: []string{"fb/m"}}); err != nil {
				t.Fatal(err)
			}
		}
		code, body := codexPost(t, oneAccountTurn)
		slow.mu.Lock()
		asked := slow.n
		slow.mu.Unlock()
		if code != 200 || !strings.Contains(body, ": keepalive") || asked != 1+lastRetries {
			t.Fatalf("fallback %v: %d %q, asked %d times", withFallback, code, body, asked)
		}
		if withFallback {
			if !strings.Contains(body, "from the fallback") || strings.Contains(body, "server_is_overloaded") || fb.n != 1 {
				t.Fatalf("fallback: %q, asked %d times", body, fb.n)
			}
		} else if strings.Count(body, "server_is_overloaded") != 1 || strings.Count(body, "event: response.created") != 1 {
			// the last try's own failure, once, and none of the others'
			t.Fatalf("no fallback: %q", body)
		}
		noneResting(t)
	}
}

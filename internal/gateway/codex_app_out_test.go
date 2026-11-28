package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A turn refused with usage_limit_reached on the account Codex is signed
// in to has that account read again at once (provider.CodexMayBeUsedUp),
// for magpie to become Codex's provider before the next five-minute look:
// the Codex app greys its Send on the account meanwhile (MasterYi on
// Discord).
func TestCodexUsedUpReplyAsksAtOnce(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var tried, models []string
	usedUp(t, &tried, &models)
	var mu sync.Mutex
	var asked []string
	old := codexMayBeUsedUp
	codexMayBeUsedUp = func(user string) { mu.Lock(); asked = append(asked, user); mu.Unlock() }
	t.Cleanup(func() { codexMayBeUsedUp = old })

	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, ",") != "me@example.com" {
		t.Fatalf("asked %v, want me@example.com", asked)
	}
}

// The same refusal with Codex's sign-in the only account: the turn is
// relayed to ChatGPT as Codex sent it, and still has the account read
// again at once. Only the next look, minutes on, made magpie Codex's
// provider before, and the Codex app kept Send greyed until it was
// restarted (unsplashz on Discord). A request signed by a ChatGPT account
// magpie doesn't have (a Codex in WSL) asks nothing.
func TestCodexUsedUpRelayedAsksAtOnce(t *testing.T) {
	codexSignedIn(t)
	var tried, models []string
	usedUp(t, &tried, &models)
	var mu sync.Mutex
	var asked []string
	old := codexMayBeUsedUp
	codexMayBeUsedUp = func(user string) { mu.Lock(); asked = append(asked, user); mu.Unlock() }
	t.Cleanup(func() { codexMayBeUsedUp = old })

	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 429 || !strings.Contains(body, "usage_limit_reached") || strings.Join(tried, ",") != "acct-1" {
		t.Fatalf("%d %s tried %v", code, body, tried)
	}
	mu.Lock()
	got := strings.Join(asked, ",")
	asked = nil
	mu.Unlock()
	if got != "me@example.com" {
		t.Fatalf("asked %q, want me@example.com", got)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1-elsewhere")
	New().Handler().ServeHTTP(rec, req)
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 0 {
		t.Fatalf("another sign-in's refusal asked for %v", asked)
	}
}

// A relayed turn that answers with the account's window heard full keeps
// it as the sign-in's usage, as a turn through magpie's accounts does
// (#1295), which reads the account again at once (NoteCodexLimits).
func TestCodexRelayedWindowsHeard(t *testing.T) {
	codexSignedIn(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("x-codex-primary-used-percent", "100")
		w.Header().Set("x-codex-primary-window-minutes", "300")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	var mu sync.Mutex
	var heard []string
	old := noteCodexLimits
	noteCodexLimits = func(agent, user string, ls []provider.CodexLimit) {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range ls {
			if l.Used >= 100 {
				heard = append(heard, agent+"/"+user)
			}
		}
	}
	t.Cleanup(func() { noteCodexLimits = old })

	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(heard, ",") != "codex/me@example.com" {
		t.Fatalf("heard %v, want codex/me@example.com", heard)
	}
}

// Codex's sign-in, the one account, known to be out: a Codex started
// before magpie became its provider (codex.out) still sends its own
// model's turns to its built-in provider. They go through magpie's
// accounts as codex/<model>, as a restarted Codex's do, rather than
// relayed to the same refusal until a restart (unsplashz on Discord).
// Not known out, or signed by an account magpie doesn't have, they go
// as they came.
func TestCodexHeldSignInServedWithoutRestart(t *testing.T) {
	codexSignedIn(t)
	held := false
	old := codexHeldKnown
	codexHeldKnown = func() bool { return held }
	t.Cleanup(func() { codexHeldKnown = old })
	ask := func(acct string) (string, bool) {
		r := httptest.NewRequest("POST", CodexPath+"/responses", nil)
		r.Header.Set("Authorization", "Bearer chatgpt-token")
		r.Header.Set("chatgpt-account-id", acct)
		return codexAccounts(r, "gpt-5.5")
	}
	if id, ok := ask("acct-1"); ok {
		t.Fatalf("with room, routed as %s", id)
	}
	held = true
	if id, ok := ask("acct-1"); !ok || id != "codex/gpt-5.5" {
		t.Fatalf("held: %q %v, want codex/gpt-5.5", id, ok)
	}
	if id, ok := ask("acct-elsewhere"); ok {
		t.Fatalf("another sign-in routed as %s", id)
	}

	// end to end: the turn goes through routing, which answers in its own
	// words (the vendor named) where the relay hands ChatGPT's on as they
	// came
	var tried, models []string
	usedUp(t, &tried, &models)
	code, body := codexPost(t, `{"model":"gpt-5.5","stream":true,"input":"ping"}`)
	if code != 429 || strings.Join(tried, ",") != "acct-1" || !strings.Contains(body, "ChatGPT: The usage limit") {
		t.Fatalf("not served through routing: %d %s tried %v", code, body, tried)
	}
}

package gateway

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// What celestial_eagle_99230's request log had from the ChatGPT backend on
// /backend-api/codex/responses (Discord, v0.1.2670, eight Plus accounts on
// gpt-6-sol): a 529 whose error is the server's overload.
const chatgptOverloaded = `{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`

// An upstream overload is the vendor's moment, not the account's fault:
// every Codex account that met ChatGPT's 529 server_is_overloaded rests
// its model alone, briefly (a Retry-After honoured up to a minute), and
// counts no failure, however often it comes. Before, each overload counted
// as the account failing: "failed 5 times in a row", every one of the
// eight accounts resting up to the 10-minute cap, and nothing left to
// answer the user (celestial_eagle_99230 on Discord).
func TestOverloadRestsNoAccount(t *testing.T) {
	codexSignedIn(t, "b@example.com", "c@example.com")
	if err := provider.SetRouting("codex", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	var tried []string
	chatgptRefusing(t, &tried, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(529)
		io.WriteString(w, chatgptOverloaded)
	}, "acct-2")
	s := New()
	// every request of the spell: the first two accounts overloaded, the
	// third answers
	rested := 0
	for range 6 {
		restingUntil.Lock()
		for k := range restingUntil.m {
			restingUntil.m[k] = time.Now().Add(-time.Second)
		}
		restingUntil.Unlock()
		if code, out := codexAskOn(s, `{"model":"gpt-6-sol","stream":true,"input":"ping"}`); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		for _, who := range []string{"codex@me@example.com", "codex@b@example.com"} {
			if r, ok := restOf(who); ok {
				t.Fatalf("%s rests whole for an overload: %+v", who, r)
			}
			r, ok := restOf(who + "/gpt-6-sol")
			if !ok {
				continue
			}
			rested++
			if r.Why != failOther || r.Failures != 0 || r.By != "retry-after" || r.Model != "gpt-6-sol" || time.Until(r.Until) > 7*time.Second {
				t.Fatalf("%s rests %v by %s, %d failures in a row, for an overload", who, time.Until(r.Until).Round(time.Second), r.By, r.Failures)
			}
		}
	}
	if rested == 0 {
		t.Fatalf("no account's model rests (tried %v): the test proves nothing", tried)
	}
}

// The rest an overload takes is the same each time and counts nothing,
// for every vendor's way of saying it; a Retry-After is honoured up to a
// minute. The account's own failures, a 401 and a usage limit, rest it
// as before, and climb the ladder from where the overloads left it.
func TestOverloadRestIsShortAndFlat(t *testing.T) {
	fresh(t)
	s := New()
	c := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}, model: "gpt-6-sol", rest: "codex@me@example.com"}
	over := func() {
		restingUntil.Lock()
		for k := range restingUntil.m {
			restingUntil.m[k] = time.Now().Add(-time.Second)
		}
		restingUntil.Unlock()
	}
	for _, x := range []struct {
		name   string
		status int
		body   string
	}{
		{"chatgpt 529", 529, chatgptOverloaded},
		{"chatgpt stream", 529, `{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`},
		{"anthropic 529", 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		{"gemini 503", 503, `{"error":{"code":503,"message":"The model is overloaded. Please try again later.","status":"UNAVAILABLE"}}`},
	} {
		t.Run(x.name, func(t *testing.T) {
			for i := range 6 {
				over()
				r := s.restAfter(c, x.status, http.Header{}, []byte(x.body))
				if r.Why != failOther || r.Failures != 0 || r.Key != c.restID() || r.By != "cooldown" || time.Until(r.Until) > overloadRest {
					t.Fatalf("overload %d: rests %s %v by %s, %d failures", i+1, r.Key, time.Until(r.Until).Round(time.Second), r.By, r.Failures)
				}
			}
		})
	}

	h := http.Header{}
	h.Set("Retry-After", "600")
	over()
	if r := s.restAfter(c, 529, h, []byte(chatgptOverloaded)); r.By != "retry-after" || time.Until(r.Until) > overloadRestMost {
		t.Fatalf("Retry-After 600: rests %v by %s", time.Until(r.Until).Round(time.Second), r.By)
	}

	// a plain 5xx is the account's failure, as before: the first step,
	// not one past the overloads
	over()
	if r := s.restAfter(c, 500, http.Header{}, []byte(`{"error":{"message":"An error occurred while processing your request.","type":"server_error"}}`)); r.Failures != 1 || r.By != "backoff" {
		t.Fatalf("a 500 after the overloads: %d failures by %s", r.Failures, r.By)
	}
	over()
	if r := s.restAfter(c, 401, http.Header{}, []byte(`{"error":{"message":"Your authentication token has expired.","code":"token_expired"}}`)); r.Key != c.restKey() || r.Failures != 2 {
		t.Fatalf("a 401 rests %s, %d failures", r.Key, r.Failures)
	}
	over()
	if r := s.restAfter(c, 429, http.Header{}, []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)); r.Why != failQuota {
		t.Fatalf("a usage limit rests as %s", r.Why)
	}
}

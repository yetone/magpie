package gateway

import (
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// overflowTold says an agent's reply tells it the prompt is too long.
func overflowTold(body string) bool {
	return strings.Contains(body, "prompt is too long") || strings.Contains(body, "context_length_exceeded")
}

// magpieError is the body magpie writes for a try that got no answer, as
// the gateway hands it to tooLongUnsaid.
func magpieError(msg string) []byte {
	rec := httptest.NewRecorder()
	writeError(rec, provider.Responses, 502, msg)
	return rec.Body.Bytes()
}

// A try that got no vendor's answer at all — the connection never made,
// a read that timed out, a reply cut off — says nothing of the request's
// length, however long it is: ne9ko9 on Discord had a TCP connect
// timeout and a read timeout on a custom Responses relay each told to
// Codex as "prompt is too long … more than gpt-6-astra's context window
// of 272000", and Codex ended the turn instead of asking again. Not for
// Devin either, whose own 5xx is read as an overflow.
func TestTransportFailureIsNotTooLong(t *testing.T) {
	devin := provider.PluginForTest("devin", "devin")
	devin.Name, devin.Contexts = "Devin", map[string]int{"m": 2000}
	plain := provider.Provider{ID: "relay", Name: "Codex relay", Contexts: map[string]int{"m": 2000}}
	for _, msg := range []string{
		// the reporter's connect timeout, as Go on Windows says it
		`Codex relay: Post "https://relay.example/v1/responses": dial tcp 203.0.113.7:443: connectex: A connection attempt failed because the connected party did not properly respond after a period of time, or established connection failed because connected host has failed to respond.`,
		// a read timeout
		`Codex relay: Post "https://relay.example/v1/responses": read tcp 192.168.1.5:53012->203.0.113.7:443: i/o timeout`,
		`Codex relay: read tcp 192.168.1.5:53012->203.0.113.7:443: wsarecv: An existing connection was forcibly closed by the remote host.`,
		`Codex relay: Post "https://relay.example/v1/responses": net/http: timeout awaiting response headers`,
		`Codex relay: Post "https://relay.example/v1/responses": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
		`Codex relay: read: connection reset by peer`,
		`Codex relay: unexpected EOF`,
		// a plugin's fetch, as Bun says it
		`Devin: Unable to connect. Is the computer able to access the url? (ConnectionRefused)`,
		// a stream that broke off before any of it was sent
		"Codex relay: " + errEndedShort.Error(),
	} {
		for _, p := range []provider.Provider{plain, devin} {
			if said, ok := tooLongUnsaid(p, "m", 3000, 0, 502, magpieError(msg)); ok {
				t.Errorf("%s, %s: told as too long: %s", p.ID, msg, said)
			}
		}
	}
}

// Devin answered a conversation well past its model's window with a 502
// "capacity issues" (b3ab3e42e), which is still told as too long, at
// once, its plugin served under its own id or another; one it holds is
// a busy moment.
func TestDevinBusyOverflowStillTooLong(t *testing.T) {
	body := []byte(`{"error":{"message":"We are currently experiencing capacity issues. Please try again later."}}`)
	for _, id := range []string{"devin", "devin-plugin"} {
		p := provider.PluginForTest(id, "devin")
		p.Name, p.Contexts = "Devin", map[string]int{"m": 2000}
		if said, ok := tooLongUnsaid(p, "m", 3000, 0, 502, body); !ok || !strings.Contains(said, "prompt is too long") {
			t.Errorf("%s: past the window: %q %v", id, said, ok)
		}
		if said, ok := tooLongUnsaid(p, "m", 1000, 0, 502, body); ok {
			t.Errorf("%s: within the window: %s", id, said)
		}
	}
}

// A plain 502 from a vendor not seen to say an overflow so is the
// vendor's busy moment, asked again, however long the request: the
// reporter's relay answered "Upstream service temporarily unavailable"
// at 385K, between replies at 454K and 474K (ne9ko9 on Discord).
func TestPlain502IsNotTooLong(t *testing.T) {
	reply := `{"code":"upstream_error","message":"Upstream service temporarily unavailable"}`
	f := &fake{code: 502, ctype: "application/json", reply: reply}
	up := setup(t, provider.Responses, f)
	p := provider.Provider{ID: "fake", Name: "Codex relay", Key: "k", Models: []string{"m1"}, Responses: up.URL + "/v1", Contexts: map[string]int{"m1": 2000}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("word ", 2400) // about 3000 tokens as estimated
	code, body := post(t, "/v1/responses", `{"model":"fake/m1","input":"`+big+`"}`)
	if code != 502 || overflowTold(body) || f.calls != 1+lastRetries+replanRounds {
		t.Fatalf("status %d after %d calls (want 502 after %d): %s", code, f.calls, 1+lastRetries+replanRounds, body)
	}
	// a 400 that says it overflowed still says so
	f.code, f.reply, f.calls = 400, `{"error":`+overflowError+`}`, 0
	code, body = post(t, "/v1/responses", `{"model":"fake/m1","input":"`+big+`"}`)
	if code != 400 || !strings.Contains(body, "context_length_exceeded") || f.calls != 1 {
		t.Fatalf("an overflow the vendor said: %d after %d calls: %s", code, f.calls, body)
	}
}

// A provider that can't be reached at all hands the agent the failure to
// connect, not an overflow, whatever the request's size.
func TestUnreachableIsNotTooLong(t *testing.T) {
	setup(t, provider.Responses, &fake{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	p := provider.Provider{ID: "fake", Name: "Codex relay", Key: "k", Models: []string{"m1"}, Responses: "http://" + addr + "/v1", Contexts: map[string]int{"m1": 2000}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("word ", 2400)
	code, body := post(t, "/v1/responses", `{"model":"fake/m1","input":"`+big+`"}`)
	if code < 500 || overflowTold(body) {
		t.Fatalf("unreachable: %d %s", code, body)
	}
}

// A model that has answered a prompt holds one of that size, whatever its
// window is said to be: gpt-6-astra, said at 272K, answered 454K and 474K
// in the reporter's session. A failure that doesn't say why, of a request
// no larger, is the vendor's error, not an overflow; one well past what
// was answered still is.
func TestWindowNotBelowWhatWasAnswered(t *testing.T) {
	fresh(t)
	invalid := `{"error":{"message":"Invalid request parameters"}}`
	answered := `{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5000,"completion_tokens":1,"total_tokens":5001}}`
	s := &scripted{replies: []reply{{400, "", invalid}, {200, "", answered}, {400, "", invalid}, {400, "", invalid}}}
	roomyMember(t, "relay", 2000, s)
	srv := New()
	ask := func(words int) (int, string) {
		return postAs(t, srv, "", `{"model":"relay/m","messages":[{"role":"user","content":"`+strings.Repeat("word ", words)+`"}]}`)
	}
	// nothing answered yet: past the window, it overflowed
	if code, body := ask(2400); code != 400 || !overflowTold(body) {
		t.Fatalf("before an answer: %d %s", code, body)
	}
	if code, body := ask(10); code != 200 {
		t.Fatalf("the answer of 5000: %d %s", code, body)
	}
	if code, body := ask(2400); code != 400 || overflowTold(body) || !strings.Contains(body, "Invalid request parameters") {
		t.Fatalf("after an answer of 5000, at about 3000: %d %s", code, body)
	}
	// about 7500, past 5000 by more than the margin
	if code, body := ask(6000); code != 400 || !overflowTold(body) {
		t.Fatalf("well past what was answered: %d %s", code, body)
	}
}

// The window an overflow is inferred against is no less than the most the
// provider's list says the model may be asked for: a Codex entry's
// max_context_window of 872K above its 272K context_window, which Codex
// is handed and works to (ne9ko9 on Discord: Codex's window 828,400).
func TestWindowNotBelowListedMax(t *testing.T) {
	invalid := `{"error":{"message":"Invalid request parameters"}}`
	f := &fake{code: 400, ctype: "application/json", reply: invalid}
	up := setup(t, provider.Chat, f)
	if err := catalog.SaveLive("fake", up.URL+"/v1", []catalog.Model{{ID: "m1", Context: 2000, MaxContext: 8000}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	// its working window, as the reporter's 272K
	p.Contexts = map[string]int{"m1": 2000}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if w := windowOf(*p, "m1"); w != 2000 {
		t.Fatalf("the window is %d, want 2000", w)
	}
	ask := func(words int) (int, string) {
		return post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"`+strings.Repeat("word ", words)+`"}]}`)
	}
	if code, body := ask(2400); code != 400 || overflowTold(body) {
		t.Fatalf("about 3000, within the max: %d %s", code, body)
	}
	// about 10000, past the max by more than the margin
	if code, body := ask(8000); code != 400 || !overflowTold(body) || !strings.Contains(body, "8000") {
		t.Fatalf("past the max: %d %s", code, body)
	}
}

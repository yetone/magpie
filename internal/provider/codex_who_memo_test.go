package provider

import (
	"encoding/json"
	"testing"
)

// TestCodexWhoKeptByBytes says codexWho reads an auth.json once for its
// bytes: codexName compares every saved account with each other one, and
// liveLogin is asked from four loops at rest, so each read was a JSON parse
// and a JWT decode of the same few accounts. Another account's bytes read
// their own email and workspace, and a malformed one reads nothing.
func TestCodexWhoKeptByBytes(t *testing.T) {
	auth := func(email, ws string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": ws}}),
			"access_token": "at"}})
		return b
	}
	a, b := auth("a@example.com", "ws-a"), auth("b@example.com", "ws-b")
	for range 2 {
		if e, ws := codexWho(a); e != "a@example.com" || ws != "ws-a" {
			t.Fatalf("a reads %q %q", e, ws)
		}
		if e, ws := codexWho(b); e != "b@example.com" || ws != "ws-b" {
			t.Fatalf("b reads %q %q", e, ws)
		}
		if e, ws := codexWho(json.RawMessage(`{`)); e != "" || ws != "" {
			t.Fatalf("a malformed auth reads %q %q", e, ws)
		}
	}
	if n := testing.AllocsPerRun(50, func() { codexWho(a) }); n != 0 {
		t.Errorf("a second read of the same auth allocates %v times, want 0", n)
	}
	// a fresh copy of the same bytes is the same account
	c := append(json.RawMessage(nil), a...)
	if n := testing.AllocsPerRun(50, func() { codexWho(c) }); n != 0 {
		t.Errorf("the same bytes in another slice allocate %v times, want 0", n)
	}
}

// TestAuthMemoBounded says the memo never holds more than authMemoMax
// credentials, so tokens renewed every hour don't pile up in a magpie
// running for weeks.
func TestAuthMemoBounded(t *testing.T) {
	var m authMemo[int]
	for i := range 3 * authMemoMax {
		m.of([]byte{byte(i), byte(i >> 8)}, func() int { return i })
		if len(m.m) > authMemoMax {
			t.Fatalf("%d entries kept, want at most %d", len(m.m), authMemoMax)
		}
	}
	if got := m.of([]byte{1, 2}, func() int { return -1 }); got != -1 {
		t.Fatalf("a new key read %d, want what read gives", got)
	}
}

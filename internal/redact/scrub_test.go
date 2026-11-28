package redact

import (
	"strings"
	"testing"
)

// What the request archive keeps has its secrets gone for good: a header
// or field named for one wholly, one that looks like a key wherever it is,
// in a JSON body, a stream's events or plain text — and no placeholder is
// made for them, so nothing is held to put them back.
func TestScrub(t *testing.T) {
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJlc2lnbmF0dXJl"
	for name, v := range map[string]string{
		"Authorization": "Bearer abc", "x-api-key": "plainvalue", "Cookie": "a=b", "Set-Cookie": "sid=1; HttpOnly",
		"Proxy-Authorization": "Basic Zm9vOmJhcg==", "X-Goog-Api-Key": "whatever", "X-Session-Token": "t",
		"X-Custom": "Bearer " + jwt,
	} {
		if got := ScrubHeader(name, v); got != Scrubbed {
			t.Errorf("%s: %q kept as %q", name, v, got)
		}
	}
	if got := ScrubHeader("X-Forwarded", "id "+key); strings.Contains(got, key) || !strings.Contains(got, "[REDACTED:API_KEY]") {
		t.Errorf("a key in another header: %q", got)
	}
	for name, v := range map[string]string{"Content-Type": "application/json", "Anthropic-Version": "2023-06-01", "User-Agent": "claude-cli/2.1"} {
		if got := ScrubHeader(name, v); got != v {
			t.Errorf("%s: %q became %q", name, v, got)
		}
	}

	body := `{"model":"m","max_tokens":100,"api_key":"plain-but-named","nested":{"access_token":"x","client_secret":"y"},` +
		`"messages":[{"role":"user","content":"my key is ` + key + ` and token ` + jwt + `"}],` +
		`"image":"data:image/png;base64,QUtJQUlPU0ZPRE5ON0VYQU1QTEU="}`
	got := string(ScrubJSON([]byte(body)))
	for _, secret := range []string{key, jwt, "plain-but-named", `"x"`, `"y"`} {
		if strings.Contains(got, secret) {
			t.Errorf("%s kept: %s", secret, got)
		}
	}
	for _, kept := range []string{`"max_tokens":100`, `"model":"m"`, `"api_key":"[REDACTED]"`, "[REDACTED:API_KEY]", "[REDACTED:TOKEN]", "data:image/png;base64,QUtJQUlPU0ZPRE5ON0VYQU1QTEU="} {
		if !strings.Contains(got, kept) {
			t.Errorf("want %s in %s", kept, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("a placeholder made: %s", got)
	}

	sse := "event: message_start\ndata: {\"type\":\"x\",\"text\":\"" + key + "\",\"refresh_token\":\"r\"}\n\ndata: [DONE]\n"
	got = string(ScrubJSON([]byte(sse)))
	if strings.Contains(got, key) || strings.Contains(got, `"r"`) || !strings.Contains(got, "event: message_start\n") || !strings.Contains(got, "data: [DONE]\n") {
		t.Errorf("stream: %q", got)
	}
	// a body cut at 256 KB is no JSON: what looks like a secret goes still
	cut := `{"messages":[{"content":"` + key + `", "password": "hunter2hunter2`
	if got := string(ScrubJSON([]byte(cut))); strings.Contains(got, key) || strings.Contains(got, "hunter2hunter2") {
		t.Errorf("cut body: %q", got)
	}
}

// A header's name says whether it holds a credential, by its words, for
// the names vendors and clients really send (usrname1833's header view
// shows them all): a count of tokens, a request id or an idempotency key
// is what debugging reads, and stays.
func TestSecretHeaderRealNames(t *testing.T) {
	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "x-api-key", "api-key", "X-Goog-Api-Key", "Cookie", "Set-Cookie",
		"Copilot-Session-Token", "X-Amz-Security-Token", "cf-access-client-secret", "X-Client-Key", "X-Auth-Token",
		"X-Csrf-Token", "X-Amz-Signature", "Ocp-Apim-Subscription-Key", "X-Github-Token", "x-goog-user-project-key",
	} {
		if !SecretHeader(name) {
			t.Errorf("%s shown", name)
		}
	}
	for _, name := range []string{
		"anthropic-ratelimit-tokens-remaining", "anthropic-ratelimit-input-tokens-limit", "x-ratelimit-remaining-tokens",
		"x-ratelimit-limit-tokens", "Idempotency-Key", "Content-Type", "User-Agent", "Anthropic-Version", "request-id",
		"X-Request-Id", "Anthropic-Beta", "Openai-Processing-Ms", "Retry-After", "Content-Length", "Cf-Ray",
	} {
		if SecretHeader(name) {
			t.Errorf("%s taken out", name)
		}
	}
	if got := ScrubHeader("X-Forwarded-Client", "Bearer abcdef"); got != Scrubbed {
		t.Errorf("a Bearer in another header: %q", got)
	}
	if got := ScrubHeader("anthropic-ratelimit-tokens-remaining", "9000"); got != "9000" {
		t.Errorf("a count of tokens: %q", got)
	}
}

// Scrubbing a kept body can carry the user's own masking options without
// changing what the old secrets-only entry points do.
func TestScrubWithOptions(t *testing.T) {
	Forget()
	const relay = "rz_ScrubOptions1234567"
	o := Options{Secrets: true, Rules: []Rule{{Kind: "RELAY", Prefix: "rz_"}}}
	body := `{"prompt":"send it to ` + relay + `","note":"keep this"}`

	if got := string(ScrubJSON([]byte(body))); !strings.Contains(got, relay) {
		t.Errorf("secrets-only ScrubJSON: %s", got)
	}
	if got := Scrub("send it to " + relay); !strings.Contains(got, relay) {
		t.Errorf("secrets-only Scrub: %q", got)
	}
	if got := ScrubHeader("X-Note", "key "+relay); !strings.Contains(got, relay) {
		t.Errorf("secrets-only ScrubHeader: %q", got)
	}

	kept := string(ScrubJSONWith([]byte(body), o))
	if strings.Contains(kept, relay) || !strings.Contains(kept, "[REDACTED:RELAY]") || !strings.Contains(kept, `"note":"keep this"`) {
		t.Errorf("ScrubJSONWith: %s", kept)
	}
	if strings.Contains(kept, "{{") {
		t.Errorf("a placeholder made: %s", kept)
	}
	if got := ScrubWith("send it to "+relay, o); got != "send it to [REDACTED:RELAY]" {
		t.Errorf("ScrubWith: %q", got)
	}
	if got := ScrubHeaderWith("X-Note", "key "+relay, o); got != "key [REDACTED:RELAY]" {
		t.Errorf("ScrubHeaderWith: %q", got)
	}
	if got := ScrubWith("Project Nightjar ships", Options{Secrets: true, Words: []string{"Nightjar"}}); got != "Project [REDACTED:TERM] ships" {
		t.Errorf("words: %q", got)
	}
	if got := ScrubWith("write to agent@relay.acme-corp.dev", Options{Secrets: true, Personal: true}); got != "write to [REDACTED:EMAIL]" {
		t.Errorf("personal: %q", got)
	}
}

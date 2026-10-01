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

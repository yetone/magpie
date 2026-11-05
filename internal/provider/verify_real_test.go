package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// realVerifyURL is a validation_url in the shape Google's VALIDATION_REQUIRED
// gives it: accounts.google.com/signin/continue with sarp, scc, a continue
// URL of its own, a plt token and the flow's name (the forum reports cut it
// after continue=; gemini-cli's tests after scc=1).
const realVerifyURL = "https://accounts.google.com/signin/continue?sarp=1&scc=1&continue=https://developers.google.com/gemini-code-assist/auth/auth_success_gemini%3Fsrc%3Dag%26x%3D1&plt=AKgnsbvK-3x_Yq9Zt0lPq2vN7m8w-AbCdEf_0123456789AbCdEfGhIjKlMnOpQrStUvWxYz&flowName=GlifWebSignIn&authuser=0"

func realVerifyBody(escape bool) []byte {
	u := realVerifyURL
	body := map[string]any{"error": map[string]any{
		"code": 403, "message": "Verify your account to continue.", "status": "PERMISSION_DENIED",
		"details": []any{
			map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "VALIDATION_REQUIRED", "domain": "cloudcode-pa.googleapis.com",
				"metadata": map[string]any{"validation_error_message": "Verify your account to continue.", "validation_url_link_text": "Verify your account", "validation_url": u,
					"validation_learn_more_link_text": "Learn more", "validation_learn_more_url": "https://support.google.com/accounts?p=al_alert"}},
			map[string]any{"@type": "type.googleapis.com/google.rpc.Help", "links": []any{
				map[string]any{"description": "Further action is required to use this service. Navigate to the following URL to complete verification:\n\n" + u + "\n\nLearn more:\n\nhttps://support.google.com/accounts?p=al_alert\n", "url": u},
				map[string]any{"description": "Learn more", "url": "https://support.google.com/accounts?p=al_alert"},
			}},
		},
	}}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(escape) // Google's ESF writes &, Go's default writes &
	_ = enc.Encode(body)
	return []byte(b.String())
}

func TestVerificationLinkWhole(t *testing.T) {
	for _, esc := range []bool{false, true} {
		b := realVerifyBody(esc)
		if link, ok := Verification(b); !ok || link != realVerifyURL {
			t.Fatalf("escape %v: %q %v", esc, link, ok)
		}
		msg := "Antigravity: " + APIError(b, "403 Forbidden")
		if !strings.Contains(msg, " open "+realVerifyURL+" in a browser") {
			t.Fatalf("escape %v: %s", esc, msg)
		}
		if link, _ := Verification([]byte(msg)); link != realVerifyURL {
			t.Fatalf("escape %v: from the message %q", esc, link)
		}
		// the message passed on again as an error body (a Remote magpie's,
		// the gateway's answer to an agent): the link read back whole
		again, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg}})
		if link, _ := Verification(again); link != realVerifyURL {
			t.Fatalf("escape %v: from %s: %q", esc, again, link)
		}
		if got := APIError(again, ""); got != msg {
			t.Fatalf("escape %v: %s", esc, got)
		}
	}
}

// The link is the one Gemini CLI opens: the Help detail's (past a "Learn
// more"), then validation_link, then validation_url.
func TestVerificationLinkAsGeminiCLI(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","domain":"cloudcode-pa.googleapis.com","metadata":{"validation_url":"https://accounts.google.com/meta"}},{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more","url":"https://support.google.com/accounts?p=al_alert"},{"description":"Complete validation","url":"https://accounts.google.com/help?a=1&b=2"}]}]}}`, "https://accounts.google.com/help?a=1&b=2"},
		{`{"error":{"details":[{"reason":"VALIDATION_REQUIRED","metadata":{"validation_link":"https://accounts.google.com/l","validation_url":"https://accounts.google.com/u"}}]}}`, "https://accounts.google.com/l"},
		{`{"error":{"details":[{"reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://accounts.google.com/u","retry":3}}]}}`, "https://accounts.google.com/u"},
		{`{"error":{"details":[{"reason":"VALIDATION_REQUIRED","metadata":{}},{"links":[{"description":"More","url":"https://support.google.com/x"}]}]}}`, ""},
	} {
		if link, ok := Verification([]byte(c.body)); !ok || link != c.want {
			t.Fatalf("%s: %q %v", c.body, link, ok)
		}
	}
}

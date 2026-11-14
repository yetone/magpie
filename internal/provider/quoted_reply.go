package provider

import "github.com/yetone/magpie/internal/redact"

// quotedReplyMax is the most of a reply QuotedReply scrubs whole, as JSON;
// past it the head alone is scrubbed, as text.
const quotedReplyMax = 64 << 10

// QuotedReply is a vendor's reply as an error may quote it: its secrets
// taken out first (redact.ScrubJSON: a field named for one, AccessToken,
// access_token, cliAccessToken, client_secret…, wholly, and anything that
// looks like a key). An error is shown in the GUI, kept in app.log and
// pasted into reports: the Qwen AI platform's sign-in quoted a reply it
// couldn't read, live access token and all, and the user posted it on X
// (@imthe99galaxy, v0.1.4159). Every error that quotes a reply goes
// through it, a sign-in's or a token's above all.
func QuotedReply(raw []byte) string {
	if len(raw) > quotedReplyMax {
		return redact.Scrub(string(raw[:quotedReplyMax]))
	}
	return string(redact.ScrubJSON(raw))
}

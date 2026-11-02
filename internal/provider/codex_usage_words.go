package provider

// Why a ChatGPT account's usage read failed, in words the account's row
// can say (hmy on Discord: five ChatGPT accounts in use, each saying only
// "Allowance unavailable"). The read's error used to be Go's: the bare
// status text ("Forbidden", "Unauthorized"), a JSON decode error, or a
// transport error naming the URL. The GUI read none of them, so the card
// said nothing it could act on, and why was in a tooltip at best.
//
// The words keep what the old ones were matched for: the status text
// (passing in quota_last.go, keeping the last reading over a 429 or a
// 5xx), a transport error's own words ("timeout", "connection refused"),
// and the vendor's answer itself (codexExpired, codexRevoked), which
// errors.As still finds through Unwrap.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// codexUsageFailed is a failed /wham/usage read: err as it was, and the
// words it is said in.
type codexUsageFailed struct {
	err   error
	words string
}

func (e *codexUsageFailed) Error() string { return e.words }
func (e *codexUsageFailed) Unwrap() error { return e.err }

// codexUsageWords is err, a failed /wham/usage read, with words that say
// why. A read held back (allowances read only when asked) is left as it
// is: it isn't a failure, and readDeferred knows its words.
func codexUsageWords(err error) error {
	if err == nil || errors.Is(err, errNotAsked) || strings.Contains(err.Error(), errNotAsked.Error()) {
		return err
	}
	var se *accountStatusError
	var ue *url.Error
	var syn *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		return &codexUsageFailed{err, codexStatusWords(se)}
	case errors.As(err, &ue):
		return &codexUsageFailed{err, "couldn't reach chatgpt.com for the usage read: " + ue.Err.Error()}
	case errors.As(err, &syn), errors.As(err, &te):
		return &codexUsageFailed{err, "ChatGPT's usage reply isn't one magpie reads: " + err.Error()}
	}
	return err
}

// codexStatusWords says what a non-2xx /wham/usage answer was: always
// with its status as "HTTP <code> <text>", and the backend's own words
// when it gave some. A 401 that says the token expired or the sign-in
// is gone is renewed or marked by codexWindowsRenewing; any other is
// unknown (TestCodexUsageOtherFailuresMarkNobody), so it is told as it
// was, never as a sign-in to make again.
func codexStatusWords(se *accountStatusError) string {
	status := strings.TrimSpace(fmt.Sprintf("HTTP %d %s", se.status, http.StatusText(se.status)))
	if cloudflareChallenge(se.body) {
		// Cloudflare's "Just a moment..." page, sent in place of the
		// backend's answer to a client it wants a browser check of
		return "ChatGPT's Cloudflare check turned the usage read away (" + status + "): chatgpt.com wants a browser check from this network; a proxy or VPN address is the usual cause"
	}
	said := codexSaid(se.body)
	if said != "" {
		said = ": " + said
	}
	return "ChatGPT answered the usage read with " + status + said
}

// cloudflareChallenge says body is Cloudflare's challenge page rather
// than anything the backend wrote.
func cloudflareChallenge(body []byte) bool {
	b := bytes.ToLower(body)
	return bytes.Contains(b, []byte("<html")) && (bytes.Contains(b, []byte("just a moment")) ||
		bytes.Contains(b, []byte("challenge-platform")) || bytes.Contains(b, []byte("cf_chl")) ||
		bytes.Contains(b, []byte("cf-chl")) || bytes.Contains(b, []byte("attention required")))
}

// codexSaid is the backend's own words in an error body: its detail, as
// a string ({"detail":"…"}) or an object ({"detail":{"message","code"}}),
// or an OpenAI-style {"error":{"message","code"}}; "" when it said none
// magpie can find. Cut to a line's length.
func codexSaid(body []byte) string {
	var v struct {
		Detail json.RawMessage `json:"detail"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	said := ""
	for _, raw := range []json.RawMessage{v.Detail, v.Error} {
		if len(raw) == 0 || said != "" {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			said = s
			continue
		}
		var o struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(raw, &o) == nil {
			said = o.Message
			if said == "" {
				said = o.Code
			}
		}
	}
	said = strings.Join(strings.Fields(said), " ")
	if r := []rune(said); len(r) > 200 {
		said = string(r[:199]) + "…"
	}
	return said
}

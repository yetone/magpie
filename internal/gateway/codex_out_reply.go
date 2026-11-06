package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// A turn on one of magpie's ChatGPT accounts that the ChatGPT backend
// refuses as the account's usage limit (429 usage_limit_reached) or as a
// plan without Codex (usage_not_included) reaches the agent in the
// backend's own shape: the error's type, plan_type, resets_at and
// resets_in_seconds as the backend said them, and its x-codex-* headers
// (the windows' used percents and resets). Codex reads the limit from
// exactly those (codex-rs codex-api api_bridge.rs: a 429 whose error type
// is usage_limit_reached is UsageLimitReached, with its plan, its reset
// and the rate limits parsed from the x-codex-* headers). Re-typed as
// magpie's rate_limit_error, with the headers left behind, Codex read the
// same 429 as a retryable one: it asked again, then stopped on "exceeded
// retry limit, last status: 429" with no reset time and no limit for the
// app to show (bitssly on X, the ChatGPT app on Windows with every Codex
// account out). Only the message is magpie's (the provider named), as
// every other vendor error's is.
//
// A turn magpie holds before asking any account (each at a usage cap the
// user set, or used up and set not to spend its credits) keeps magpie's
// words and type: those say which setting to change, and Codex shows a
// usage_limit_reached in its own words alone.

// codexOutTypes are the ChatGPT backend's error types for an account
// that has nothing left for Codex.
var codexOutTypes = []string{"usage_limit_reached", "usage_not_included"}

// codexOutRefusal is whether b, a refusal of a turn on p, is the ChatGPT
// backend saying p's account has nothing left for Codex, answered on the
// Responses API it came on.
func codexOutRefusal(p provider.Provider, proto provider.Protocol, status int, b []byte) bool {
	if proto != provider.Responses || p.Account == nil || p.Account.Agent != "codex" || status != http.StatusTooManyRequests && status != http.StatusForbidden {
		return false
	}
	t := gjson.GetBytes(b, "error.type").String()
	for _, o := range codexOutTypes {
		if t == o {
			return true
		}
	}
	return false
}

// writeCodexOut answers a codexOutRefusal as the backend did: its error
// object, its message magpie's msg, and the x-codex-* headers of h.
func writeCodexOut(w http.ResponseWriter, h http.Header, status int, msg string, b []byte) int {
	var e struct {
		Error map[string]any `json:"error"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber() // resets_at as written, never through a float
	if d.Decode(&e) != nil || e.Error == nil {
		return writeVendorError(w, provider.Responses, status, msg, b)
	}
	e.Error["message"] = msg
	out, err := json.Marshal(e)
	if err != nil {
		return writeVendorError(w, provider.Responses, status, msg, b)
	}
	for k, vs := range h {
		if strings.HasPrefix(strings.ToLower(k), "x-codex-") {
			w.Header()[k] = vs
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(out)
	return status
}

package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// redacted masks a request's secrets, as the settings say, before it goes
// to a vendor, and wraps w so what the vendor answers has them back; done
// writes what the wrapper still holds. Nothing masked, nothing wrapped.
func redacted(w http.ResponseWriter, body []byte) (http.ResponseWriter, []byte, func()) {
	o := redactionOptions()
	if !o.Secrets && !o.Personal && len(o.Words) == 0 {
		return w, body, func() {}
	}
	masked, n := redact.MaskJSON(body, o)
	if n == 0 {
		return w, body, func() {}
	}
	rw := redact.NewWriter(w)
	return rw, masked, rw.Finish
}

// redactedPrompt masks a parsed image or video prompt before any vendor
// body is built, including multipart forms, and restores the reply's text.
func redactedPrompt(w http.ResponseWriter, prompt string) (http.ResponseWriter, string, func()) {
	masked, n := redact.Mask(prompt, redactionOptions())
	if n == 0 {
		return w, prompt, func() {}
	}
	rw := redact.NewWriter(w)
	return rw, masked, rw.Finish
}

func redactionOptions() redact.Options {
	st := settings.Load()
	return redact.Options{Secrets: st.Redact, Personal: st.RedactPersonal, Words: st.RedactWords, Rules: st.RedactRules}
}

// unredactedRoute says a request resolved to p, or to the group whose
// models are ms, goes only where the user set requests to go unmasked
// (provider.SkipsRedaction): p and each of its fallbacks, or every model of
// the group (a member's own fallbacks are not the group's, see planGroup).
// One that may go on to anyone else stays masked for all of them.
func unredactedRoute(p provider.Provider, isGroup bool, ms []provider.Member) bool {
	if isGroup {
		if len(ms) == 0 {
			return false
		}
		for _, m := range ms {
			if !m.Provider.SkipsRedaction() {
				return false
			}
		}
		return true
	}
	if !p.SkipsRedaction() {
		return false
	}
	for _, id := range p.Fallback {
		// one that resolves to nothing isn't tried (plan)
		if fp, _, ok := provider.Resolve(id); ok && !fp.SkipsRedaction() {
			return false
		}
	}
	return true
}

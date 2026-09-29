package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// redacted masks a request's secrets, as the settings say, before it goes
// to a vendor, and wraps w so what the vendor answers has them back; done
// writes what the wrapper still holds. Nothing masked, nothing wrapped.
func redacted(w http.ResponseWriter, body []byte) (http.ResponseWriter, []byte, func()) {
	st := settings.Load()
	if !st.Redact && !st.RedactPersonal && len(st.RedactWords) == 0 {
		return w, body, func() {}
	}
	masked, n := redact.MaskJSON(body, redact.Options{Secrets: st.Redact, Personal: st.RedactPersonal, Words: st.RedactWords, Rules: st.RedactRules})
	if n == 0 {
		return w, body, func() {}
	}
	rw := redact.NewWriter(w)
	return rw, masked, rw.Finish
}

package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// passError answers a vendor's error for a provider whose errors the user
// leaves to the client (errors=pass, ne9ko9 on Discord): nothing in it is
// inferred or said in magpie's words. A vendor on the client's own API has
// its status, headers' Content-Type and body go as they came; one on
// another API has its status, type, code (a number too), param and words
// written in the client's shape. Retry-After and the rate-limit headers go
// with it, as the vendor sent them. The message returned, for the log, is
// the provider's name and the vendor's words.
func passError(w http.ResponseWriter, p provider.Provider, proto, upstream provider.Protocol, res *http.Response, u *Usage) (int, string) {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	status := res.StatusCode
	words := provider.APIError(b, res.Status)
	u.ErrType = provider.ErrorType(b)
	for k, vs := range res.Header {
		if l := strings.ToLower(k); l == "retry-after" || strings.Contains(l, "ratelimit") {
			w.Header()[k] = vs
		}
	}
	if shape := errorShape(proto); shape != 0 && errorShape(upstream) == shape {
		if ct := res.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		w.Write(b)
		return status, p.Name + ": " + words
	}
	typ, msg := errorTypeFor(status), words
	var code, param any
	if e := gjson.GetBytes(b, "error"); e.IsObject() {
		if t := e.Get("type"); t.Type == gjson.String && t.String() != "" {
			typ = t.String()
		} else if t := e.Get("status"); t.Type == gjson.String && t.String() != "" {
			typ = t.String() // Gemini's name for it
		}
		if m := e.Get("message"); m.Type == gjson.String {
			msg = m.String()
		}
		if c := e.Get("code"); c.Exists() && c.Type != gjson.Null {
			code = json.RawMessage(c.Raw)
		}
		if pr := e.Get("param"); pr.Exists() && pr.Type != gjson.Null {
			param = json.RawMessage(pr.Raw)
		}
	}
	return writeErrorAs(w, proto, status, typ, msg, code, param), p.Name + ": " + words
}

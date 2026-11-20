package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// magpieSays begins the line of an error told to the agent that is
// magpie's own, after the vendor's: what magpie read of the error and
// what to do, kept apart from the vendor's words so a user can copy
// those as they were said, to the vendor (@gptchan404 on X).
const magpieSays = "\nmagpie: "

// upstreamTold is a vendor's error as the agent is told it: on its first
// line the provider's name, the vendor's own words as it said them and
// what it named the error by — its HTTP status, its code and its request
// id, each left out when its words already say it — and after it, on a
// line of its own, whatever magpie said beyond those words (msg, the
// error as explained, less the provider's name and the vendor's words it
// begins with). An agent that shows only an error's first line shows the
// vendor's. A plugin's error (plugin) is its vendor's words with the
// plugin's advice after a " — ", as provider.Explain joins them: only the
// words before it are the vendor's, and the advice is told on magpie's
// line (pluginWords).
func upstreamTold(name string, status int, header http.Header, b []byte, msg string, plugin bool) string {
	words := provider.VendorWords(b)
	if plugin {
		words = pluginWords(words)
	}
	head := name + ": "
	rest := strings.TrimPrefix(msg, head)
	// APIError's status line ("403 Forbidden: …"), which the first
	// line says as HTTP 403
	rest = strings.TrimPrefix(rest, fmt.Sprintf("%d %s: ", status, http.StatusText(status)))
	if words != "" && strings.HasPrefix(rest, words) {
		rest = rest[len(words):]
	}
	rest = strings.TrimLeft(rest, " —-:")
	named := upstreamNamed(status, vendorCode(b), vendorRequestID(header, b), words)
	line := head + words
	switch {
	case words == "" && named != "":
		line = head + named
	case named != "":
		line += " (" + named + ")"
	}
	if rest == "" || rest == words {
		return line
	}
	return line + magpieSays + rest
}

// upstreamNamed is what a vendor named its error by, as the first line
// of upstreamTold says it: "HTTP 403, content_policy_rejected, request id
// …", each part the vendor's words don't already hold, and no code that
// is only the status again (Google's "code": 403).
func upstreamNamed(status int, code, reqID, words string) string {
	var parts []string
	if status >= 400 {
		parts = append(parts, fmt.Sprintf("HTTP %d", status))
	}
	if code != "" && code != fmt.Sprint(status) && !strings.Contains(words, code) {
		parts = append(parts, code)
	}
	if reqID != "" && !strings.Contains(words, reqID) {
		parts = append(parts, "request id "+reqID)
	}
	return strings.Join(parts, ", ")
}

// vendorCode is the code a vendor's error body names its error by: the
// error's code (a string or a number), else its type, else its status
// (Gemini's RESOURCE_EXHAUSTED…), at the top level for a body with no
// error object. "" for none, and for the bare type "error".
func vendorCode(b []byte) string {
	e := gjson.GetBytes(b, "error")
	if !e.IsObject() {
		e = gjson.ParseBytes(b)
		if !e.IsObject() {
			return ""
		}
	}
	for _, k := range []string{"code", "type", "status"} {
		v := e.Get(k)
		if v.Type != gjson.String && v.Type != gjson.Number {
			continue
		}
		if s := strings.TrimSpace(v.String()); s != "" && s != "error" && s != "0" {
			return s
		}
	}
	return ""
}

// vendorRequestID is the id a vendor gave the failed request, for the
// user to quote to it: the reply's Request-Id or X-Request-Id, else the
// body's request_id (Anthropic's).
func vendorRequestID(h http.Header, b []byte) string {
	if id := requestID(h); id != "" {
		return id
	}
	for _, k := range []string{"request_id", "error.request_id", "requestId"} {
		if v := gjson.GetBytes(b, k); v.Type == gjson.String && strings.TrimSpace(v.String()) != "" {
			return strings.TrimSpace(v.String())
		}
	}
	return ""
}

// refusalTold is what the agent is told of a request the vendor's safety
// filter refused and nobody else answered: the vendor's own words first,
// as upstreamTold puts them (status the vendor's, when it answered with
// an error status), and magpie's line after them, which names the model
// and the filter's reason (refusedError). why is the refusal's note,
// "safety filter: <reason> — <the vendor's words>" (refusedNote) or the
// reason with what Anthropic's stop_details said (refusedDetail).
func refusalTold(p provider.Provider, model string, status int, reqID, why string, plugin bool) string {
	reason, words, said := strings.Cut(why, " — ")
	reason = strings.TrimPrefix(reason, "safety filter: ")
	if !said {
		// a stop reason, with nothing said beside it but what
		// stop_details may give, in the reason's parentheses
		words = reason
	}
	advice := ""
	if plugin && said {
		// the plugin's advice goes on magpie's line (pluginWords)
		if w := pluginWords(words); w != words {
			words, advice = w, " — "+strings.TrimSpace(words[len(w)+len(" — "):])
		}
	}
	code := reason
	if i := strings.IndexByte(code, ' '); i > 0 {
		code = code[:i]
	}
	line := p.Name + ": " + words
	if named := upstreamNamed(status, code, reqID, words); named != "" {
		line += " (" + named + ")"
	}
	return line + magpieSays + refusedError(p, model, "safety filter: "+code) + advice
}

// pluginWords is the vendor's part of a plugin's error message: what comes
// before the first " — ", after which a plugin puts its own advice
// (Factory's "… (upstream request id …, code …) — Factory's usage policy
// check reads the whole conversation, …").
func pluginWords(said string) string {
	w, _, _ := strings.Cut(said, " — ")
	return w
}

// retelling is one error's text as magpie read it (from) and as the agent
// is told it (to).
type retelling struct{ from, to string }

// holdOf is the try's holdWriter under w and the writers wrapped around
// it, nil for none.
func holdOf(w http.ResponseWriter) *holdWriter {
	for w != nil {
		if h, ok := w.(*holdWriter); ok {
			return h
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

// retellAs has the try's reply tell the agent to where it says from.
func (h *holdWriter) retellAs(from, to string) {
	if from == "" || from == to {
		return
	}
	h.mu.Lock()
	h.retold = append(h.retold, retelling{from, to})
	h.mu.Unlock()
}

// retell is b with each error magpie wrote in it told as rs say, in each
// way a JSON string of it can be written (with and without HTML's
// characters escaped); b itself, never written to, when none is in it.
func retell(rs []retelling, b []byte) []byte {
	for _, r := range rs {
		from, to := jsonStringBody(r.from, true), jsonStringBody(r.to, true)
		if bytes.Contains(b, from) {
			b = bytes.ReplaceAll(b, from, to)
			continue
		}
		// once only: to holds from, so a second pass of the same bytes
		// would tell it twice
		if from, to = jsonStringBody(r.from, false), jsonStringBody(r.to, false); bytes.Contains(b, from) {
			b = bytes.ReplaceAll(b, from, to)
		}
	}
	return b
}

// retellText is s told as rs say.
func retellText(rs []retelling, s string) string {
	for _, r := range rs {
		s = strings.ReplaceAll(s, r.from, r.to)
	}
	return s
}

// jsonStringBody is s as a JSON string's contents, its HTML characters escaped
// as encoding/json does by default or not.
func jsonStringBody(s string, escapeHTML bool) []byte {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(escapeHTML)
	e.Encode(s)
	return bytes.TrimSuffix(bytes.TrimSuffix(bytes.TrimPrefix(buf.Bytes(), []byte(`"`)), []byte("\n")), []byte(`"`))
}

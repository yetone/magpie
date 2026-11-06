package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// errorShape is the family of error bodies an API answers with: Chat's and
// Responses' are OpenAI's {"error":{"message","type","code","param"}},
// Anthropic's {"type":"error","error":{"type","message"}}, Gemini's
// {"error":{"code","message","status"}}. 0 for one with none of its own.
func errorShape(proto provider.Protocol) int {
	switch proto {
	case provider.Chat, provider.Responses:
		return 1
	case provider.Anthropic:
		return 2
	case provider.Gemini:
		return 3
	}
	return 0
}

// writeVendorErrorFrom answers a vendor's error as writeVendorError does,
// but for a vendor that speaks the client's own API (upstream's error
// shape is the client's): the vendor's body goes to the client as it came,
// its status, type, a numeric code and every field magpie doesn't know
// (ne9ko9 on Discord). Only the message is magpie's: the provider's name
// and the vendor's words, with a note of why where magpie has one (an
// account's Explain, a region, the wrong endpoint), a busy relay said so
// (saysBusy) and a 413's hint. A prompt too long for the model is said as
// the client's API says it (tooLong: the status, type and code that make
// the agent compact). Every other byte is the vendor's. A body not in the client's
// shape is rebuilt in it, as before.
func writeVendorErrorFrom(w http.ResponseWriter, proto, upstream provider.Protocol, p provider.Provider, res *http.Response, msg string, body []byte) int {
	if status, ok := writeKeptError(w, proto, upstream, p, res, msg, body); ok {
		return status
	}
	return writeVendorError(w, proto, res.StatusCode, msg, body)
}

// writeKeptError writes the vendor's error body b as writeVendorErrorFrom
// says, false with nothing written when b isn't an error in proto's shape.
func writeKeptError(w http.ResponseWriter, proto, upstream provider.Protocol, p provider.Provider, res *http.Response, msg string, b []byte) (int, bool) {
	shape := errorShape(proto)
	if shape == 0 || errorShape(upstream) != shape || !json.Valid(b) {
		return 0, false
	}
	said := gjson.GetBytes(b, "error.message")
	if said.Type != gjson.String || !gjson.GetBytes(b, "error").IsObject() {
		return 0, false
	}
	status := res.StatusCode
	// the message is magpie's: the provider's name, the vendor's words as
	// APIError reads them, then any notes (explained, wrongAPINote…), so
	// the agent, a describer asking for another model and a sign-in's
	// rewording all know which provider said it.
	text := msg
	if text == "" {
		text = provider.APIError(b, res.Status)
	}
	text = saysBusy(status, text)
	out := b
	if tooLong(status, text) {
		status = http.StatusBadRequest
		switch shape {
		case 1:
			out = setErrorField(out, "type", "invalid_request_error")
			out = setErrorField(out, "code", "context_length_exceeded")
		case 2:
			out = setErrorField(out, "type", "invalid_request_error")
			if !strings.Contains(strings.ToLower(text), "prompt is too long") {
				text = "prompt is too long: " + text
			}
		case 3:
			out = setErrorField(out, "code", http.StatusBadRequest)
			out = setErrorField(out, "status", "INVALID_ARGUMENT")
		}
	}
	if h, held := w.(*holdWriter); status == http.StatusRequestEntityTooLarge && (!held || !h.hold) {
		if !strings.Contains(text, requestBytesHint) {
			text += " — " + requestBytesHint
		}
	}
	if text != said.String() {
		out = setErrorField(out, "message", text)
	}
	ct := res.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(ct), "json") {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(status)
	w.Write(out)
	return status, true
}

// setErrorField sets name in b's error object to v, splicing it where the
// field is (or first in the object when it isn't) so every other byte of b
// stays as it was.
func setErrorField(b []byte, name string, v any) []byte {
	var enc bytes.Buffer
	e := json.NewEncoder(&enc)
	e.SetEscapeHTML(false) // a vendor's <, > and & as it wrote them (#260)
	if e.Encode(v) != nil {
		return b
	}
	val := bytes.TrimRight(enc.Bytes(), "\n")
	if f := gjson.GetBytes(b, "error."+gjson.Escape(name)); f.Exists() && f.Index > 0 {
		if f.Raw == string(val) {
			return b
		}
		out := make([]byte, 0, len(b)+len(val))
		out = append(out, b[:f.Index]...)
		out = append(out, val...)
		return append(out, b[f.Index+len(f.Raw):]...)
	}
	obj := gjson.GetBytes(b, "error")
	if !obj.IsObject() || obj.Index <= 0 {
		return b
	}
	at := obj.Index + strings.IndexByte(obj.Raw, '{') + 1
	key, _ := json.Marshal(name)
	field := append(append(append([]byte{}, key...), ':'), val...)
	if strings.TrimSpace(obj.Raw[strings.IndexByte(obj.Raw, '{')+1:]) != "}" {
		field = append(field, ',')
	}
	out := make([]byte, 0, len(b)+len(field))
	out = append(out, b[:at]...)
	out = append(out, field...)
	return append(out, b[at:]...)
}

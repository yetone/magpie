package provider

import (
	"encoding/json"

	"github.com/yetone/magpie/internal/lazyre"
)

// A backend that only streams (claude-relay-service's Responses endpoint,
// like Codex's own ChatGPT backend) turns a request without stream:true
// away with a 400: "Stream must be set to true" (Cyrus_4433 on X: every
// model of a CRS relay red in the editor's test).
//
// The probe asks once more streamed when it hears this, rather than
// streaming every probe: a vendor that takes one body keeps being asked
// for one, and only a backend that says so is asked otherwise.
var streamRequired = lazyre.New(`(?i)\bstream(ing)?\b[^.\n]{0,40}\b(must|need|needs|should|has to|have to)\b[^.\n]{0,30}\btrue\b|\bonly\s+(supports?|accepts?|allows?|serves?)\s+(requests\s+(with|in)\s+)?stream|\brequires?\s+stream|\bstream(ing)?\s+(is\s+)?(required|mandatory)\b`)

// StreamRequired is whether a 400's words say the request must ask for a
// stream.
func StreamRequired(msg []byte) bool { return streamRequired.Match(msg) }

// WithStream is body, a JSON object that doesn't ask for a stream, asking
// for one, and whether it changed. Its other fields keep their bytes.
func WithStream(body []byte) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil || streams(body) {
		return body, false
	}
	m["stream"] = json.RawMessage("true")
	out, err := json.Marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}

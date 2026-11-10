package gateway

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// tooFewTokens reads a provider's floor on the reply's length from the 400
// it sends when a request asks for less ("max_tokens must be greater than
// 2"). Apps checking a model is up ask for a token or two, which most
// providers answer and some turn away. A > may come JSON-escaped, as
// \u003e: the error is read from the body magpie re-encoded for the agent,
// or a vendor encodes its own that way (#260: "Expected a value >= 16").
var tooFewTokens = lazyre.New(`(?i)max_(?:completion_|output_)?tokens.{0,60}?(greater than|more than|larger than|at least|(?:>|\\u003e)=?)\s*(\d+)`)

// tokenFloor is the least a provider said it takes, or 0 when the error
// isn't about that.
func tokenFloor(msg []byte) int {
	m := tooFewTokens.FindSubmatch(msg)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(string(m[2]))
	if err != nil || n < 0 || n > 1024 {
		return 0
	}
	if sign := string(m[1]); strings.EqualFold(sign, "at least") || strings.HasSuffix(sign, "=") {
		return n
	}
	return n + 1
}

// withTokenFloor raises the reply's length the request asks for to floor,
// wherever the request's protocol keeps it; false when it asked for that
// much already, so the error was about something else.
func withTokenFloor(body []byte, floor int) ([]byte, bool) {
	var q map[string]json.RawMessage
	if floor <= 0 || json.Unmarshal(body, &q) != nil {
		return nil, false
	}
	raise := func(m map[string]json.RawMessage, key string) bool {
		var n int
		if json.Unmarshal(m[key], &n) != nil || n >= floor {
			return false
		}
		m[key], _ = json.Marshal(floor)
		return true
	}
	raised := false
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		raised = raise(q, k) || raised
	}
	var gc map[string]json.RawMessage // Gemini's
	if json.Unmarshal(q["generationConfig"], &gc) == nil && raise(gc, "maxOutputTokens") {
		q["generationConfig"], _ = json.Marshal(gc)
		raised = true
	}
	if !raised {
		return nil, false
	}
	b, err := marshalJSON(q)
	return b, err == nil
}

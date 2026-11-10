package provider

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/lazyre"
)

// Chat Completions has two names for a reply's length. OpenAI's own API
// reads max_completion_tokens and turns max_tokens away on its newer
// models ("Unsupported parameter: 'max_tokens' is not supported with this
// model. Use 'max_completion_tokens' instead.", GoxxKintBlade on X), while
// DeepSeek, Zhipu and most OpenAI-compatible relays read max_tokens alone.
const (
	MaxTokensField     = "max_tokens"
	CompletionTokField = "max_completion_tokens"
)

// OpenAIHost is whether host is OpenAI's own API (api.openai.com), whose
// Chat Completions takes max_completion_tokens on every model; not Azure
// OpenAI (*.openai.azure.com) and not a relay.
func OpenAIHost(host string) bool {
	host = strings.ToLower(host)
	return host == "openai.com" || strings.HasSuffix(host, ".openai.com")
}

// tokenFieldAsked matches a vendor's 400 that names the other field as
// the one to send: OpenAI's "Use 'max_completion_tokens' instead", and a
// vendor that says the reverse ("use max_tokens instead").
var tokenFieldAsked = lazyre.New("(?i)\\buse\\s+['\"`]?(max_completion_tokens|max_tokens)['\"`]?")

// TokenFieldWanted is the field a Chat Completions refusal asks for in
// place of the one sent, or "": only when its words name it as the one to
// use, so a limit too high ("max_tokens must be at most 32768") is not
// read as the wrong name.
func TokenFieldWanted(msg []byte) string {
	m := tokenFieldAsked.FindSubmatch(msg)
	if m == nil {
		return ""
	}
	return strings.ToLower(string(m[1]))
}

// WithTokenField is body with its reply length named want (MaxTokensField
// or CompletionTokField), and whether it changed. The key is renamed in
// place, so the rest of the body keeps its bytes; a body that names both
// keeps want's value and drops the other.
func WithTokenField(body []byte, want string) ([]byte, bool) {
	from := MaxTokensField
	if want == MaxTokensField {
		from = CompletionTokField
	} else if want != CompletionTokField {
		return body, false
	}
	if !bytes.Contains(body, []byte(`"`+from+`"`)) {
		return body, false
	}
	v := gjson.GetBytes(body, from)
	if !v.Exists() || v.Index <= 0 {
		return body, false
	}
	if gjson.GetBytes(body, want).Exists() {
		// both named: the one the vendor reads stays
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		var m map[string]any
		if dec.Decode(&m) != nil {
			return body, false
		}
		delete(m, from)
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		if enc.Encode(m) != nil {
			return body, false
		}
		return bytes.TrimSuffix(out.Bytes(), []byte("\n")), true
	}
	// the key is the last string before its value: only a colon and
	// spaces lie between them
	quoted := `"` + from + `"`
	key := bytes.LastIndex(body[:v.Index], []byte(quoted))
	if key < 0 || strings.Trim(string(body[key+len(quoted):v.Index]), " \t\r\n:") != "" {
		return body, false
	}
	out := make([]byte, 0, len(body)+len(want)-len(from))
	out = append(out, body[:key]...)
	out = append(out, `"`+want+`"`...)
	return append(out, body[key+len(quoted):]...), true
}

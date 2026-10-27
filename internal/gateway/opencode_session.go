package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// openCodeSeed names the conversation a request to OpenCode's gateway is a
// turn of, for its x-opencode-session (provider.OpenCodeClient): the
// client's own x-opencode-session when it sent one, else the session the
// client names (X-Magpie-Session, Codex's session_id, Claude Code's
// session, Pi's affinity: sessionOf), else the body's prompt_cache_key
// (Codex's thread), else how the conversation opens
// (conversationID). OpenCode itself sends one session id for every turn of
// a session (packages/core/src/session/affinity.ts), and Zen and Go route
// a session to the backend that has its prompt cached: every turn of one
// conversation names the same, and two conversations never one. Without
// the cache key, two of Codex's conversations in one folder, which open on
// the same environment message, were one session.
func openCodeSeed(in http.Header, body []byte) string {
	if v := strings.TrimSpace(in.Get("x-opencode-session")); v != "" {
		return v
	}
	if v := sessionOf(in); v != "" {
		return v
	}
	if k := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); k != "" {
		return "prompt_cache_key\x00" + k
	}
	return conversationID(in, body)
}

// logOpenCodeRefusal writes to magpie's log what went to OpenCode's
// gateway with a request it refused: where, the names of the headers sent
// (never a value), and whether x-opencode-session was among them, by its
// first 8 characters. OpenCode answers a request without one 400
// "Request is missing x-opencode-session and cannot be routed
// efficiently" (luci on Discord), and this tells that apart from a
// session the user's own header pins every conversation to.
func logOpenCodeRefusal(p provider.Provider, req *http.Request, status int) {
	var names []string
	for k := range req.Header {
		names = append(names, strings.ToLower(k))
	}
	slices.Sort(names)
	session := "missing"
	if v := req.Header.Get("x-opencode-session"); v != "" {
		session = v[:min(len(v), 8)] + "…"
		for k, v := range p.Headers {
			// ${sessionId} in it is each conversation's own
			if strings.EqualFold(k, "x-opencode-session") && !strings.Contains(v, provider.SessionPlaceholder) && !strings.Contains(v, provider.ThreadPlaceholder) {
				session += " (the provider's own header: one session for every conversation)"
			}
		}
	}
	log.Printf("opencode: %s answered %d to %s %s%s; x-opencode-session %s; headers sent: %s",
		p.ID, status, req.Method, req.URL.Host, req.URL.Path, session, strings.Join(names, ", "))
}

// errorKeys are the fields any error an API answers carries, in its own
// body or in an error object of it: a body none of them name says nothing
// of why.
var errorKeys = []string{"error", "message", "msg", "code", "type", "param", "detail", "reason", "description"}

// openCodeBare is said in the 502 openCodeBareRefusal makes of a bare 400,
// for restAfterMarked to rest nobody for it.
const openCodeBare = "which is no error, not an API reply"

// openCodeBareRefusal is res as the 502 it stands for, when OpenCode's
// gateway answers a request with a 400 whose body is no error at all:
// {"model":"deepseek-v4.1-flash"}, the model's name and nothing else
// (#3740, s-leeq), with nothing in or out and no reply id. The same
// request is answered some seconds later (s-leeq's timelines: four to six
// 400s over 13s to 25s, then a 200 of the same prompt), so it is read as
// the 502 such a failure is: a group's next member is asked, and a lone
// provider is asked again within its patience, which outlasts that
// window, where the 400 ended the turn at once. It is tied to the request
// (in s-leeq's day, 30 of 214 requests carrying ~246K tokens of tools
// failed so and none of 420 carrying under 1K), so nobody rests for it
// (restAfterMarked): the key goes on serving the requests it serves. A
// 400 that names anything, as the missing x-opencode-session refusal
// does, goes on as it came, as does a body that isn't a JSON object.
func openCodeBareRefusal(req *http.Request, res *http.Response) *http.Response {
	if res == nil || res.StatusCode != http.StatusBadRequest || res.Body == nil ||
		req == nil || !provider.OpenCodeURL(req.URL.String()) {
		return res
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	// what wasn't read is read after what was: a body is never cut
	res.Body = readCloser{io.MultiReader(bytes.NewReader(b), res.Body), res.Body}
	var v map[string]any
	if len(b) == 64<<10 || json.Unmarshal(bytes.TrimSpace(b), &v) != nil {
		return res
	}
	for _, k := range errorKeys {
		if _, ok := v[k]; ok {
			return res
		}
	}
	return asBadGateway(res, "", "answered "+res.Status+" with "+clipRunes(strings.TrimSpace(string(b)), 200)+", "+openCodeBare)
}

package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// gathers is whether a request to p on proto that asked for one body is
// relayed streamed and given back whole (gathered) rather than translated:
// a plugin's account, which only streams, on Responses (plugins#112:
// translating it dropped the tools the vendor runs itself, Grok's x_search,
// so Grok answered without searching, and the response's tools and usage
// details). A subscription magpie signs in to itself (Codex's ChatGPT
// backend, Qoder) is translated as before: its own code fits its requests,
// and no report asks for more. Messages isn't gathered, as its stream has
// no event that holds the whole reply.
func gathers(p provider.Provider, proto provider.Protocol, body []byte) bool {
	return proto == provider.Responses && p.IsPlugin() && p.Account.Stream && !streamOf(body)
}

// gathered holds a relayed Responses stream that the client asked for
// whole, and gives the client the response the stream ends with.
type gathered struct {
	h      http.Header
	status int
	buf    bytes.Buffer
	// capped: the max_output_tokens the client asked, for a reply that
	// stopped at it with no end said (endsAtProbeCap)
	capped int
}

func (g *gathered) Header() http.Header         { return g.h }
func (g *gathered) WriteHeader(status int)      { g.status = status }
func (g *gathered) Write(b []byte) (int, error) { return g.buf.Write(b) }

// finish writes the reply to w: the response of the stream's
// response.completed (or response.incomplete), its output filled from the
// stream's items, or else its text, when the event left it empty.
// A reply that came whole goes as it came. A stream that failed, or ended
// before saying its reply had, is a 502, as a translated one is
// (TestNonStreamEndedShort), never an empty 200 — unless a short probe's
// reply stopped at its own small limit (endsAtProbeCap): its response as
// the stream began it, incomplete at max_output_tokens.
func (g *gathered) finish(w http.ResponseWriter, p provider.Provider, sse bool, cut string, failed string) (int, string) {
	if !sse {
		for k, vs := range g.h {
			w.Header()[k] = vs
		}
		w.WriteHeader(g.status)
		w.Write(g.buf.Bytes())
		return g.status, failed
	}
	var resp, begun json.RawMessage // begun: the response as the stream began it
	spent := false                  // a delta of the reply's came (endsAtProbeCap)
	items := map[int]json.RawMessage{}
	var text strings.Builder // the reply's text, for a stream with no items
	streamErr := ""
	readSSE(bytes.NewReader(g.buf.Bytes()), func(event, data string) error {
		var e struct {
			Type        string          `json:"type"`
			Response    json.RawMessage `json:"response"`
			Item        json.RawMessage `json:"item"`
			OutputIndex int             `json:"output_index"`
			Delta       string          `json:"delta"`
		}
		if json.Unmarshal([]byte(data), &e) != nil {
			return nil
		}
		if e.Type == "" {
			e.Type = event
		}
		if strings.HasSuffix(e.Type, ".delta") {
			spent = true
		}
		switch e.Type {
		case "response.created", "response.in_progress":
			begun = e.Response
		case "response.output_text.delta":
			text.WriteString(e.Delta)
		case "response.output_item.done":
			if len(e.Item) > 0 {
				items[e.OutputIndex] = e.Item
			}
		case "response.completed", "response.incomplete":
			resp = e.Response
		case "response.failed", "error":
			streamErr = provider.APIError([]byte(data), "upstream stream failed")
		}
		return nil
	})
	if (len(resp) == 0 || resp[0] != '{') && streamErr == "" && cut == "" && failed == "" &&
		len(begun) > 0 && begun[0] == '{' && endsAtProbeCap(provider.Responses, g.capped, spent) {
		resp = incompleteAtLength(begun)
	}
	if len(resp) == 0 || resp[0] != '{' {
		msg := failed
		switch {
		case streamErr != "":
			msg = p.Name + ": " + streamErr
		case cut != "":
			msg = cut
		case msg == "":
			msg = p.Name + ": the reply ended before it was complete"
		}
		return writeError(w, provider.Responses, 502, msg), msg
	}
	if len(items) == 0 && text.Len() > 0 {
		msg, _ := json.Marshal(map[string]any{"type": "message", "id": "msg_" + newID(), "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}}})
		items[0] = msg
	}
	resp = withStreamedOutput(resp, items)
	for _, k := range []string{"Request-Id", "X-Request-Id"} {
		if v := g.h.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(resp)
	return http.StatusOK, ""
}

// incompleteAtLength is a response as its stream began it, made the
// response.incomplete a reply stopped at its max_output_tokens ends with.
func incompleteAtLength(begun json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(begun, &m) != nil {
		return nil
	}
	m["status"] = json.RawMessage(`"incomplete"`)
	m["incomplete_details"] = json.RawMessage(`{"reason":"max_output_tokens"}`)
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

// withStreamedOutput is resp with the stream's items as its output when
// its own output is missing or empty.
func withStreamedOutput(resp json.RawMessage, items map[int]json.RawMessage) json.RawMessage {
	if len(items) == 0 {
		return resp
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(resp, &m) != nil {
		return resp
	}
	var out []json.RawMessage
	if json.Unmarshal(m["output"], &out) == nil && len(out) > 0 {
		return resp
	}
	idx := make([]int, 0, len(items))
	for i := range items {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out = out[:0]
	for _, i := range idx {
		out = append(out, items[i])
	}
	b, err := json.Marshal(out)
	if err != nil {
		return resp
	}
	m["output"] = b
	if nb, err := json.Marshal(m); err == nil {
		return nb
	}
	return resp
}

package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

const conversationBodyMax = 8 << 20

type gatewaySessionKey struct{}

// withGatewaySession assigns unidentified requests their own ledger identity.
// Keep it out of the input headers so routing still uses the client's context.
func withGatewaySession(w http.ResponseWriter, r *http.Request) *http.Request {
	id := sessionOf(r.Header)
	if id == "" {
		id = "request-" + rand.Text()
		ctx := context.WithValue(r.Context(), gatewaySessionKey{}, id)
		who := callerOf(r)
		who.session = id // A remote Magpie must keep the same ledger identity.
		r = r.WithContext(context.WithValue(ctx, callerCtx{}, who))
	}
	w.Header().Set(SessionHeader, id)
	return r
}

func gatewaySessionOf(r *http.Request) string {
	if id := sessionOf(r.Header); id != "" {
		return id
	}
	id, _ := r.Context().Value(gatewaySessionKey{}).(string)
	return id
}

func init() {
	WhileServing = append(WhileServing, func(ctx context.Context) {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			if err := sessions.PruneGatewayConversations(time.Now()); err != nil {
				log.Printf("gateway conversation cleanup failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	})
}

type conversationWriter struct {
	*captureResponseWriter
	status int
}

func (w *conversationWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

// recordConversation wraps the client-facing path, outside middleware and
// translation, so retries never become duplicate assistant replies.
func recordConversation(w http.ResponseWriter, r *http.Request, proto provider.Protocol, body []byte) (http.ResponseWriter, func()) {
	id := gatewaySessionOf(r)
	if !settings.Load().GatewayConversations || id == "" {
		return w, func() {}
	}
	at := time.Now()
	input := append([]byte(nil), body[:min(len(body), conversationBodyMax)]...)
	sink := &conversationWriter{captureResponseWriter: &captureResponseWriter{
		ResponseWriter: w, full: &spool{limit: conversationBodyMax, pattern: "magpie-conversation-*"},
	}}
	return sink, func() {
		defer sink.full.discard()
		cut := len(body) > conversationBodyMax || sink.full.cut()
		output, err := sink.full.read()
		if err != nil {
			log.Printf("gateway conversation capture failed: %v", err)
			return
		}
		turn := conversationTurn(proto, input, output)
		turn.Agent, turn.Session, turn.Time = usage.AgentOf(callerOf(r).agent), id, at
		turn.Status = sink.status
		if turn.Status == 0 {
			turn.Status = http.StatusOK
		}
		turn.Cut = turn.Cut || cut
		if err := sessions.SaveGatewayTurn(turn); err != nil {
			log.Printf("gateway conversation save failed: %v", err)
		}
	}
}

func conversationParts(role string, parts []Part) []sessions.Part {
	var out []sessions.Part
	for _, p := range parts {
		q := sessions.Part{Role: role, Text: p.Text, Kind: string(p.Kind), Name: p.Name}
		switch p.Kind {
		case ToolCall:
			q.Kind, q.Text = "tool_use", string(redact.ScrubJSON(p.Args))
		case ToolResult:
			q.Role = "tool"
			if len(p.Images) > 0 {
				q.Text += "\n[image]"
			}
		case Image:
			q.Text = "[image]"
		case File:
			q.Kind, q.Text = "context", attachmentText(p)
		case Search:
			q.Kind, q.Name = "tool_use", "web_search"
			for _, h := range p.Hits {
				q.Text += "\n" + h.Title + " " + h.URL
			}
		}
		if q.Text != "" {
			out = append(out, q)
		}
	}
	return out
}

func conversationTurn(proto provider.Protocol, input, output []byte) sessions.GatewayTurn {
	t := sessions.GatewayTurn{Input: []sessions.Part{}, Output: []sessions.Part{}}
	input, output = redact.ScrubJSON(input), redact.ScrubJSON(output)
	q, err := parse(proto, input)
	if err != nil {
		t.Cut = true
	} else {
		t.Model = q.Model
		if q.System != "" {
			t.Input = append(t.Input, sessions.Part{Role: "system", Kind: "context", Name: "System prompt", Text: q.System})
		}
		// Keep tool definitions as context, including native tools the IR may
		// not translate; never render embedded images or execute their content.
		var raw map[string]json.RawMessage
		if json.Unmarshal(input, &raw) == nil && len(raw["tools"]) > 0 {
			t.Input = append(t.Input, sessions.Part{Role: "system", Kind: "context", Name: "Tools", Text: string(raw["tools"])})
		}
		for _, m := range q.Messages {
			t.Input = append(t.Input, conversationParts(m.Role, m.Parts)...)
		}
	}
	trim := bytes.TrimSpace(output)
	if json.Valid(trim) {
		t.Output, err = conversationJSON(proto, trim)
		t.Cut = t.Cut || err != nil
	} else if bytes.Contains(trim, []byte("data:")) {
		var c collector
		var completedItems []json.RawMessage
		decode := decoder(proto)
		ended := false
		err = readSSE(bytes.NewReader(output), func(_ string, data string) error {
			if data == "[DONE]" {
				ended = true
				return nil
			}
			if !json.Valid([]byte(data)) {
				t.Cut = true
				return nil
			}
			if proto == provider.Responses {
				var e struct {
					Type     string          `json:"type"`
					Item     json.RawMessage `json:"item"`
					Response struct {
						Output []json.RawMessage `json:"output"`
					} `json:"response"`
				}
				_ = json.Unmarshal([]byte(data), &e)
				if e.Type == "response.output_item.done" {
					completedItems = append(completedItems, e.Item)
				}
				if e.Type == "response.completed" && len(e.Response.Output) > 0 {
					completedItems = e.Response.Output
				}
			}
			return decode(data, func(e Event) {
				if e.Kind == KStop {
					ended = true
				}
				c.add(e)
			})
		})
		t.Output = conversationParts("assistant", c.finish().Parts)
		if proto == provider.Responses && ended && len(completedItems) > 0 {
			b, _ := json.Marshal(map[string]any{"output": completedItems})
			if parts, e := conversationJSON(proto, b); e == nil {
				t.Output = parts
			} else {
				t.Cut = true
			}
		}
		t.Cut = t.Cut || err != nil || !ended
		if c.err != "" {
			t.Output = append(t.Output, sessions.Part{Role: "assistant", Kind: "context", Text: c.err})
		}
	} else if len(trim) > 0 {
		t.Cut = true
	}
	return t
}

// Reuse the request readers for whole replies by placing the actual output in
// their message/input shape. The wire content, not a generated summary, is read.
func conversationJSON(proto provider.Protocol, body []byte) ([]sessions.Part, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if e := raw["error"]; len(e) > 0 && string(e) != "null" {
		return []sessions.Part{{Role: "assistant", Kind: "context", Text: string(e)}}, nil
	}
	var request []byte
	switch proto {
	case provider.Chat:
		var choices []struct {
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(raw["choices"], &choices); err != nil {
			return nil, err
		}
		var messages []json.RawMessage
		for _, c := range choices {
			messages = append(messages, c.Message)
		}
		request, _ = json.Marshal(map[string]any{"messages": messages})
	case provider.Responses:
		request, _ = json.Marshal(map[string]any{"input": raw["output"]})
	case provider.Gemini:
		var c collector
		if err := decoder(proto)(string(body), c.add); err != nil {
			return nil, err
		}
		return conversationParts("assistant", c.finish().Parts), nil
	default:
		request, _ = json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": raw["content"]}}})
	}
	q, err := parse(proto, request)
	if err != nil {
		return nil, err
	}
	var parts []sessions.Part
	for _, m := range q.Messages {
		parts = append(parts, conversationParts("assistant", m.Parts)...)
	}
	if strings.TrimSpace(q.System) != "" {
		parts = append(parts, sessions.Part{Role: "assistant", Kind: "context", Text: q.System})
	}
	return parts, nil
}

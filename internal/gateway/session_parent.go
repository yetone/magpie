package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

func isTitleKind(kind string) bool {
	return kind == "thread_title" || kind == "thread_title_reconsideration" || kind == "title_generation"
}

// Codex projects turn metadata into headers, but its canonical transport is
// client_metadata in the Responses body. Read only identity fields; never keep
// prompts or the rest of the metadata in the trace.
type sessionMetadata struct {
	Source       string `json:"thread_source"`
	Parent       string `json:"parent_thread_id"`
	Forked       string `json:"forked_from_thread_id"`
	Installation string `json:"installation_id"`
}

func requestSessionMetadata(h http.Header, body []byte) sessionMetadata {
	var envelope struct {
		Metadata map[string]json.RawMessage `json:"client_metadata"`
	}
	var m sessionMetadata
	if bytes.Contains(body, []byte("client_metadata")) && json.Unmarshal(body, &envelope) == nil {
		if raw := envelope.Metadata["x-codex-turn-metadata"]; len(raw) > 0 {
			var text string
			if json.Unmarshal(raw, &text) == nil {
				raw = []byte(text)
			}
			if json.Unmarshal(raw, &m) == nil {
				return m
			}
		} else if len(envelope.Metadata) > 0 {
			// Compatibility projections used by clients without the full blob.
			_ = json.Unmarshal(envelope.Metadata["thread_source"], &m.Source)
			_ = json.Unmarshal(envelope.Metadata["parent_thread_id"], &m.Parent)
			if m.Parent == "" {
				_ = json.Unmarshal(envelope.Metadata["x-codex-parent-thread-id"], &m.Parent)
			}
			_ = json.Unmarshal(envelope.Metadata["forked_from_thread_id"], &m.Forked)
			_ = json.Unmarshal(envelope.Metadata["installation_id"], &m.Installation)
			if m.Source != "" || m.Parent != "" || m.Forked != "" {
				return m
			}
		}
	}
	m = sessionMetadata{}
	if json.Unmarshal([]byte(h.Get("x-codex-turn-metadata")), &m) != nil {
		return sessionMetadata{}
	}
	return m
}

func requestCallKind(h http.Header, m sessionMetadata) string {
	if h.Get("x-openai-subagent") != "" || h.Get("x-openai-memgen-request") != "" {
		return callKind(h)
	}
	switch source := strings.TrimSpace(m.Source); source {
	case "", "user", "subagent":
		return callKind(h)
	default:
		if len(source) > 40 {
			source = source[:40]
		}
		return source
	}
}

// Only title helpers inherit their originating chat. A user-created fork is a
// separate conversation, even though it carries the same fork ancestry.
func titleParentSession(h http.Header, m sessionMetadata, kind string) string {
	if !isTitleKind(kind) {
		return ""
	}
	for _, id := range []string{m.Parent, m.Forked, h.Get("x-codex-parent-thread-id")} {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\t") || id == sessionOf(h) {
			continue
		}
		return id
	}
	return ""
}

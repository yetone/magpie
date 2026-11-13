package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
)

func isTitleKind(kind string) bool {
	return kind == "thread_title" || kind == "thread_title_reconsideration" || kind == "title_generation"
}

// isCommitKind: the Codex app's commit message, pull request text and
// both at once (commit_message, pull_request_message,
// commit_pull_request_message), each asked for in a schema of its own
func isCommitKind(kind string) bool {
	return kind == "commit_message" || kind == "pull_request_message" || kind == "commit_pull_request_message"
}

func isDescriptionKind(kind string) bool {
	return kind == "thread_description"
}

// isSummaryKind: the Codex app's one-line summary of a thread's latest
// message, shown under its title (thread_summary)
func isSummaryKind(kind string) bool {
	return kind == "thread_summary"
}

// isPredictionKind: the Codex app's guess at the user's next message,
// shown in its composer (composer_predictions, #1970)
func isPredictionKind(kind string) bool {
	return kind == "composer_predictions"
}

// Codex projects turn metadata into headers, but its canonical transport is
// client_metadata in the Responses body. Read only identity fields; never keep
// prompts or the rest of the metadata in the trace.
type sessionMetadata struct {
	Turn         string `json:"turn_id"`
	Thread       string `json:"thread_id"`
	Source       string `json:"thread_source"`
	Parent       string `json:"parent_thread_id"`
	Forked       string `json:"forked_from_thread_id"`
	Installation string `json:"installation_id"`
}

func requestSessionMetadata(h http.Header, body []byte) sessionMetadata {
	var metadata map[string]json.RawMessage
	var m sessionMetadata
	// Every Codex request asks this four or five times, so only the member
	// is decoded: decoding the whole body walked the conversation each time
	// (#3394). A long body is checked and split once (topMember), so it is
	// asked straight away: Codex sends client_metadata last, after the
	// conversation, and looking for the word walked all of it each time.
	if (len(body) >= validMemoMin || bodyHas(body, "client_metadata")) && metadataOf(body, &metadata) {
		if raw := metadata["x-codex-turn-metadata"]; len(raw) > 0 {
			var text string
			if json.Unmarshal(raw, &text) == nil {
				raw = []byte(text)
			}
			if json.Unmarshal(raw, &m) == nil {
				return m
			}
		} else if len(metadata) > 0 {
			_ = json.Unmarshal(metadata["turn_id"], &m.Turn)
			_ = json.Unmarshal(metadata["thread_id"], &m.Thread)
			// Compatibility projections used by clients without the full blob.
			_ = json.Unmarshal(metadata["thread_source"], &m.Source)
			_ = json.Unmarshal(metadata["parent_thread_id"], &m.Parent)
			if m.Parent == "" {
				_ = json.Unmarshal(metadata["x-codex-parent-thread-id"], &m.Parent)
			}
			_ = json.Unmarshal(metadata["forked_from_thread_id"], &m.Forked)
			_ = json.Unmarshal(metadata["installation_id"], &m.Installation)
			if m.Source != "" || m.Parent != "" || m.Forked != "" {
				return m
			}
		}
	}
	projectedTurn := m.Turn
	m = sessionMetadata{}
	if json.Unmarshal([]byte(h.Get("x-codex-turn-metadata")), &m) != nil {
		return sessionMetadata{Turn: projectedTurn}
	}
	if projectedTurn != "" {
		m.Turn = projectedTurn
	}
	return m
}

// metadataOf decodes body's client_metadata into md, as decoding body into
// a struct with that one field does: false when body isn't a JSON object or
// the member isn't an object or null.
func metadataOf(body []byte, md *map[string]json.RawMessage) bool {
	raw, found, ok := topMember(body, "client_metadata")
	if !ok {
		return false
	}
	return !found || json.Unmarshal(raw, md) == nil
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

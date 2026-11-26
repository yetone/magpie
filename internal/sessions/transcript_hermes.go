package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// hermesTranscript reads a Hermes Agent session's conversation from its
// rows in the profile's state.db (the session's Path is the database and
// "#" its id), as Hermes itself shows it: the rows eachMessage reads for
// the session's counts (a rewind's dropped rows and model-only scaffolding
// left out, a compaction's archived turns kept once), less the ones
// Hermes never paints (display_kind "hidden"). A row's content is
// Hermes's _encode_content: the text as typed, or "\x00json:" and its
// parts. A compaction's handoff is context, and the turn after it is said.
// Before this, Hermes's sessions had no transcript, so Show conversation,
// search and "Learn from conversations" passed every one over (CFK on
// Discord).
func hermesTranscript(s Session, add func(bool, Part) bool) error {
	i := strings.LastIndex(s.Path, "#")
	if i < 0 {
		return ErrNoTranscript
	}
	sid, err := url.QueryUnescape(s.Path[i+1:])
	if err != nil || sid == "" {
		return ErrNoTranscript
	}
	db, err := provider.OpenReadOnly(s.Path[:i])
	if err != nil {
		return err
	}
	defer db.Close()
	h := newHermesDB(db)
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	more := true
	return h.eachMessageOf(tx, sid, true, func(m hermesMessage) {
		if !more || m.displayKind == "hidden" {
			return
		}
		for _, p := range hermesParts(m) {
			if more = add(p.Role != "assistant", p); !more {
				return
			}
		}
	})
}

// hermesParts are what one of Hermes's rows said.
func hermesParts(m hermesMessage) []Part {
	switch m.role {
	case "user":
		return hermesSaid("user", m.content)
	case "assistant":
		var out []Part
		if strings.TrimSpace(m.reasoning) != "" {
			out = append(out, Part{Role: "assistant", Kind: "thinking", Text: m.reasoning})
		}
		out = append(out, hermesSaid("assistant", m.content)...)
		for _, t := range hermesTools(m.tools) {
			if t.Function.Name != "" {
				out = append(out, Part{Role: "assistant", Kind: "tool_use", Name: t.Function.Name, Text: hermesArguments(t.Function.Arguments)})
			}
		}
		return out
	case "tool":
		return []Part{{Role: "tool", Kind: "tool_result", Name: m.toolName, Text: hermesText(m.content)}}
	}
	return nil
}

// hermesSaid is a user's or assistant's content: its words, and an image
// part as one. A compaction's handoff (Hermes's summary of the turns
// before, in a user row or, to keep turns alternating, an assistant one)
// is context, the turn it carries said.
func hermesSaid(role, raw string) []Part {
	var out []Part
	text := raw
	if encoded, ok := strings.CutPrefix(raw, hermesJSONContentPrefix); ok {
		parts := gjson.Parse(encoded)
		if !parts.IsArray() {
			return []Part{textPart(role, encoded)}
		}
		var words []string
		for _, p := range parts.Array() {
			switch t := p.Get("type").String(); {
			case p.Type == gjson.String:
				words = append(words, p.String())
			case t == "text" || t == "input_text" || t == "output_text":
				words = append(words, p.Get("text").String())
			case t == "image_url" || t == "image" || t == "input_image":
				out = append(out, Part{Role: role, Kind: "image", Text: "[image]"})
			}
		}
		text = strings.Join(words, "\n")
	}
	if live, ok := hermesDisplayText(text); ok {
		summary := strings.Replace(text, live, "", 1)
		return append([]Part{{Role: role, Kind: "context", Text: summary}, textPart(role, live)}, out...)
	}
	if hermesStartsSummary(strings.TrimLeft(text, " \t\r\n")) {
		return append([]Part{{Role: role, Kind: "context", Text: text}}, out...)
	}
	if role == "assistant" {
		return append([]Part{{Role: role, Kind: "text", Text: text}}, out...)
	}
	return append([]Part{textPart(role, text)}, out...)
}

// hermesArguments is a tool call's arguments: OpenAI's JSON text in a
// string, as Hermes keeps them, or an object.
func hermesArguments(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if json.Valid([]byte(s)) {
			return pretty(json.RawMessage(s))
		}
		return s
	}
	return pretty(raw)
}

package gateway

import (
	"bytes"
	"cmp"
	"encoding/json"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// upstreamOf is the provider an aggregator's reply (a JSON body, or one
// event of its stream) says answered behind it, "" when it says none.
// OpenRouter names it in a top-level "provider" — a Chat body and every
// chunk of its stream, an Anthropic Messages body — and in a Messages
// stream's message_start, as message.provider (seen 2026-10-05: DeepInfra,
// StreamLake). Its Responses replies name none. ClinePass (api.cline.bot)
// names it in a Chat choice's provider_metadata, as
// gateway.routing.finalProvider: under delta in a stream's chunk, under
// message in one body (#2847, smart-lty). Only those places count: a
// choice's content is the model's, and providerAttempts inside routing
// lists every provider tried, not the one that answered.
func upstreamOf(b []byte) string {
	if !bodyHas(b, `"provider"`) && !bodyHas(b, `"finalProvider"`) {
		return ""
	}
	type routed struct {
		ProviderMetadata struct {
			Gateway struct {
				Routing struct {
					FinalProvider json.RawMessage `json:"finalProvider"`
				} `json:"routing"`
			} `json:"gateway"`
		} `json:"provider_metadata"`
	}
	var v struct {
		Provider json.RawMessage `json:"provider"`
		Message  struct {
			Provider json.RawMessage `json:"provider"`
		} `json:"message"`
		Response struct {
			Provider json.RawMessage `json:"provider"`
		} `json:"response"`
		Choices []struct {
			Delta   routed `json:"delta"`
			Message routed `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	raws := []json.RawMessage{v.Provider, v.Message.Provider, v.Response.Provider}
	for _, c := range v.Choices {
		raws = append(raws, c.Delta.ProviderMetadata.Gateway.Routing.FinalProvider, c.Message.ProviderMetadata.Gateway.Routing.FinalProvider)
	}
	for _, raw := range raws {
		var s string // a request's provider options echoed back are an object
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" && len(s) <= 64 {
				return s
			}
		}
	}
	return ""
}

// upstreamStop is why a reply (a JSON body, or one event of its stream)
// says it ended, in the upstream's own words: an Anthropic stop_reason, a
// Chat finish_reason, a Responses reply's status and the reason it was
// incomplete. Kept with the request, so a reply that ended too soon shows
// what the upstream said of it — a relay's "end_turn" after a few words,
// or nothing at all (蓝猫 on Discord). "" when this one says none.
func upstreamStop(b []byte) string {
	if !bodyHas(b, `stop_reason"`) && !bodyHas(b, `finish_reason"`) && !bodyHas(b, `"response.`) {
		return ""
	}
	var v struct {
		Type       string `json:"type"`
		StopReason string `json:"stop_reason"`
		Delta      struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Response struct {
			Status            string `json:"status"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	switch {
	case v.Delta.StopReason != "":
		return v.Delta.StopReason
	case v.StopReason != "":
		return v.StopReason
	case strings.HasPrefix(v.Type, "response.") && v.Response.Status != "" && v.Response.Status != "in_progress" && v.Response.Status != "queued":
		if d := v.Response.IncompleteDetails; d != nil && d.Reason != "" {
			return v.Response.Status + ": " + d.Reason
		}
		return v.Response.Status
	}
	for _, c := range v.Choices {
		if c.FinishReason != "" {
			return c.FinishReason
		}
	}
	return ""
}

// replyEnds says whether an upstream's event ends the reply, read as the
// protocol it was asked in, stop being what upstreamStop read of it: an
// Anthropic message_stop or error, or a message_delta's stop_reason; a
// Responses response.completed, response.incomplete, response.failed or
// error; a Gemini chunk's finishReason (geminiEnds); a Chat
// finish_reason. Another protocol's end, or Anthropic's events with their
// type left out, end nothing: the decoder reads nothing of them, and a
// stream told whole on one became a completed reply that said nothing,
// which the agent took as its turn's end and didn't ask again (#1933).
func replyEnds(proto provider.Protocol, data, stop string) bool {
	switch {
	case proto == provider.Anthropic:
		t := eventType(data)
		return t == "message_stop" || t == "error" || t == "message_delta" && stop != ""
	case proto == provider.Responses:
		t := eventType(data)
		return t == "error" || strings.HasPrefix(t, "response.") && (lastEvent(t) || stop != "")
	case geminiKin(proto):
		return stop != "" || geminiEnds([]byte(data))
	}
	return stop != ""
}

// eventType is an event's own "type", "" when it names none.
func eventType(data string) string {
	var v struct {
		Type json.RawMessage `json:"type"`
	}
	var t string
	if json.Unmarshal([]byte(data), &v) != nil || json.Unmarshal(v.Type, &t) != nil {
		return ""
	}
	return t
}

// carriedError is the error an upstream's event carries where its
// protocol's decoder can't read it: an error that is a string,
// {"type":"error","error":"…"} (#1933), or one whose other fields aren't
// the shape the protocol gives them. Read as nothing, the stream went on
// to end as a whole reply with nothing in it, and the agent didn't try
// again. ok is false for an event that carries no error.
func carriedError(data string) (ev Event, ok bool) {
	var v struct {
		Type     json.RawMessage `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return Event{}, false
	}
	var t string
	json.Unmarshal(v.Type, &t)
	var r struct {
		Error json.RawMessage `json:"error"`
	}
	json.Unmarshal(v.Response, &r)
	if t != "error" && t != "response.failed" && !carries(v.Error) && !carries(r.Error) {
		return Event{}, false
	}
	code, status, said := filterRefused(data)
	if status == 0 {
		// the status the error names for itself (#4855)
		status = cmp.Or(namedStatusOf(r.Error), namedStatusOf(v.Error))
	}
	return Event{Kind: KError, Text: streamErrorText(data, r.Error, v.Error), Code: code, Status: status, Said: said, Resets: saidResets(data, time.Now())}, true
}

// streamErrorText is what an upstream's error event says: the message of
// the first of errs that has one (an object's message or type, or a
// string), else the event as provider.APIError reads it. Never "": an
// error said with no words is still told as one.
func streamErrorText(data string, errs ...json.RawMessage) string {
	for _, raw := range errs {
		if !carries(raw) {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
			continue
		}
		var e struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(raw, &e) == nil {
			if m := strings.TrimSpace(e.Message); m != "" {
				return m
			}
			if e.Type != "" {
				return e.Type
			}
		}
	}
	return provider.APIError([]byte(data), "the upstream reported an error")
}

// saidResets is when the limit an upstream's error event refused the
// request for renews, as the event says it: resets_at (unix seconds) or
// resets_in_seconds on its error, as ChatGPT's usage_limit_reached and
// doubao-work's doubao_quota carry them. Zero when it says neither. Kept
// as the event's Resets, the account rests till then (resetsOn), and the
// error magpie writes in the client's own shape says it too (resetsSaid):
// read from neither, doubao-work's monthly allowance, said in its stream
// after reasoning, rested a quarter of an hour (aaron0x10 on X).
func saidResets(data string, now time.Time) time.Time {
	var v struct {
		Error    json.RawMessage `json:"error"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal([]byte(data), &v) != nil {
		return time.Time{}
	}
	var r struct {
		Error json.RawMessage `json:"error"`
	}
	json.Unmarshal(v.Response, &r)
	for _, raw := range []json.RawMessage{v.Error, r.Error} {
		var e struct {
			At int64 `json:"resets_at"`
			In int64 `json:"resets_in_seconds"`
		}
		if !carries(raw) || json.Unmarshal(raw, &e) != nil {
			continue
		}
		if t := time.Unix(e.At, 0); e.At > 0 && t.After(now) {
			return t
		}
		if e.In > 0 {
			return now.Add(time.Duration(e.In) * time.Second)
		}
	}
	return time.Time{}
}

// carries says whether a JSON value is there and not null.
func carries(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && string(s) != "null"
}

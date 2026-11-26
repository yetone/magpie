package gateway

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// An error event that says when its limit renews keeps that time, in
// every protocol's stream (Event.Resets), and the account rests till then
// by it (resetsOn). Read from neither, doubao-work's monthly allowance,
// said after reasoning, rested a quarter of an hour (aaron0x10 on X).
func TestStreamErrorSaysItsReset(t *testing.T) {
	at := time.Now().Add(20 * 24 * time.Hour).Truncate(time.Second)
	n := strconv.FormatInt(at.Unix(), 10)
	past := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	for _, c := range []struct {
		name  string
		proto provider.Protocol
		data  string
		want  time.Duration // from now; 0 for none, -1 for at
	}{
		{"chat", provider.Chat, `{"error":{"message":"豆包工作：本月额度已用完，下个月恢复为你服务。","type":"doubao_quota","status":429,"resets_at":` + n + `}}`, -1},
		{"chat in seconds", provider.Chat, `{"error":{"message":"quota","resets_in_seconds":90}}`, 90 * time.Second},
		// a shape the chunk can't be read as: carriedError's
		{"chat carried", provider.Chat, `{"error":{"message":{"text":"quota"},"resets_at":` + n + `}}`, -1},
		{"anthropic", provider.Anthropic, `{"type":"error","error":{"type":"rate_limit_error","message":"quota","resets_at":` + n + `}}`, -1},
		{"responses failed", provider.Responses, `{"type":"response.failed","response":{"status":"failed","error":{"code":"usage_limit_reached","message":"quota","resets_at":` + n + `}}}`, -1},
		{"responses error", provider.Responses, `{"type":"error","error":{"type":"usage_limit_reached","message":"quota","resets_at":` + n + `}}`, -1},
		{"past", provider.Chat, `{"error":{"message":"quota","resets_at":` + past + `}}`, 0},
		{"none", provider.Chat, `{"error":{"message":"quota"}}`, 0},
	} {
		var got []Event
		before := time.Now()
		if err := decoder(c.proto)(c.data, func(ev Event) {
			if ev.Kind == KError {
				got = append(got, ev)
			}
		}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != 1 {
			t.Fatalf("%s: errors %+v", c.name, got)
		}
		r := got[0].Resets
		switch {
		case c.want == -1:
			if !r.Equal(at) {
				t.Errorf("%s: resets %v, want %v", c.name, r, at)
			}
		case c.want == 0:
			if !r.IsZero() {
				t.Errorf("%s: resets %v, want none", c.name, r)
			}
		default:
			if d := r.Sub(before); d < c.want || d > c.want+time.Minute {
				t.Errorf("%s: resets in %v, want %v", c.name, d, c.want)
			}
		}
	}
}

// The error event magpie writes in the client's own shape says that time
// too: a stream held before its reply began is failed, and rested, from
// that event (holdWriter.scan, resetsAt), whatever the upstream's shape.
func TestStreamErrorWrittenSaysItsReset(t *testing.T) {
	at := time.Now().Add(20 * 24 * time.Hour).Truncate(time.Second)
	for _, proto := range []provider.Protocol{provider.Chat, provider.Anthropic, provider.Responses} {
		rec := httptest.NewRecorder()
		enc := encoder(proto, newSSEWriter(rec), &Request{Model: "m"})
		enc.event(Event{Kind: KError, Text: "豆包工作：本月额度已用完，下个月恢复为你服务。", Status: 429, Resets: at})
		out := []byte(rec.Body.String())
		var said []byte
		for len(out) > 0 {
			end := eventEnd(out)
			if end < 0 {
				break
			}
			if kind, _, msg := streamEvent(out[:end]); kind == eventError {
				said = []byte(msg)
			}
			out = out[end:]
		}
		if said == nil {
			t.Fatalf("%s: no error event in %s", proto, rec.Body.String())
		}
		if d := resetsAt(said, time.Now()); d < time.Until(at)-time.Minute || d > time.Until(at)+time.Minute {
			t.Errorf("%s: the error written resets in %v, want %v: %s", proto, d, time.Until(at), said)
		}
	}
}

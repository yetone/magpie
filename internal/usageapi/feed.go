package usageapi

import (
	"github.com/yetone/magpie/internal/usage"
)

// The request feed as GET /v1/magpie/usage/requests?cursor= tells it
// (rdsaltbo on X, #6060). See usage.FeedRead for how a change is found.

// Originals are the ids the sources themselves gave a call, each told by who
// gave it. A key that is absent is one the source did not give: magpie
// invents none, and its own id of the row is FeedRow.ID.
type Originals struct {
	// RequestID: the id the vendor gave the request, or, in a row read from
	// an agent's file, the one the agent recorded (Claude Code's req_…)
	RequestID string `json:"request_id,omitempty"`
	// ResponseID: in a row read from an agent's file, the id of the response
	// object the agent recorded (Codex's resp_…). A call through the gateway
	// has ClientResponseID instead
	ResponseID string `json:"response_id,omitempty"`
	// ClientResponseID: the response object id the gateway sent its client,
	// which may be magpie's own when the reply was translated
	ClientResponseID string `json:"client_response_id,omitempty"`
	// MessageID: the id the agent's own file gives the call's message
	// (Claude Code's msg_…), in a row read from that file
	MessageID string `json:"message_id,omitempty"`
	// SessionID: the conversation, as the agent names it
	SessionID string `json:"session_id,omitempty"`
	// NativeSessionID: the agent's own session header when X-Magpie-Session
	// named the conversation instead
	NativeSessionID string `json:"native_session_id,omitempty"`
}

// FeedRow is a row of the feed. Every row is one request: a call the
// gateway served, one read from an agent's file, one a client reported, or
// one another computer's magpie shared. None is a session or model summary.
type FeedRow struct {
	// ID is magpie's own, the same on every read and after a restart
	ID string `json:"id"`
	// Seq is the feed position of this version of the row
	Seq int64 `json:"seq"`
	// Origin: "gateway" (magpie served it), "session" (read from the agent's
	// own file; the gateway never saw it), "reported" (a client told
	// magpie), "" (a call magpie rejected itself, or its own test)
	Origin    string    `json:"origin"`
	Originals Originals `json:"original_ids"`
	Row
}

// Granularity is what one row is of. Always "request": the rows of the
// feed are single calls, never a session's or a model's total.
func (FeedRow) Granularity() string { return "request" }

// Gone is an id that is no row now: replaced by another row (a call from an
// agent's file the gateway turned out to have served) or taken out (a clear).
type Gone struct {
	ID  string `json:"id"`
	Seq int64  `json:"seq"`
}

// FeedResult is a read of the feed: the rows to upsert by id and the ids to
// drop (an id is in one of them), then the cursor to read on from.
type FeedResult struct {
	Rows       []FeedRow `json:"rows"`
	Deleted    []Gone    `json:"deleted"`
	NextCursor string    `json:"next_cursor"`
	HasMore    bool      `json:"has_more"`
}

// Feed reads the feed. key narrows it to one gateway key's calls.
func Feed(cursor string, f usage.Filter, limit int, key string) (FeedResult, error) {
	p, err := usage.FeedRead(usage.FeedRequest{Cursor: cursor, Limit: limit, Filter: f, Key: key})
	if err != nil {
		return FeedResult{}, err
	}
	names := newNamer()
	out := FeedResult{Rows: []FeedRow{}, Deleted: []Gone{}, NextCursor: p.Next, HasMore: p.More}
	for _, it := range p.Items {
		if it.Gone {
			out.Deleted = append(out.Deleted, Gone{it.ID, it.Seq})
			continue
		}
		r := it.Row
		fr := FeedRow{ID: it.ID, Seq: it.Seq, Row: names.row(r, map[string]string{r.CallerKeyID: r.CallerKeyName}, nil)}
		switch usage.SourceOf(r) {
		case usage.SourceGateway:
			fr.Origin = "gateway"
		case usage.SourceSession:
			fr.Origin = "session"
		case usage.SourceReported:
			fr.Origin = "reported"
		}
		fr.Originals = Originals{RequestID: r.RequestID, SessionID: r.Session, NativeSessionID: r.NativeSession}
		if r.Source == "log" {
			fr.Originals.ResponseID, fr.Originals.MessageID = r.ResponseID, r.MessageID
		} else {
			fr.Originals.ClientResponseID = r.ResponseID
		}
		out.Rows = append(out.Rows, fr)
	}
	return out, nil
}

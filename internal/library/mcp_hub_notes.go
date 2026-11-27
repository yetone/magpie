package library

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/yetone/magpie/internal/gateway"
)

// A call through the router whose agent asked to be told its progress, or
// its server's log lines (gateway.HubNoteOf), is sent to its server with a
// progressToken of magpie's own, one for each call, so two calls on one
// server, an agent's each or two agents', never take each other's: the
// server's notifications/progress under that token, and its
// notifications/message about that call, are handed to the call's
// gateway.HubNote, which tells them to its agent (#7920).
//
// Which call a log line is about: on a streamable HTTP server, the call
// whose POST's event stream it came on; on a command or an SSE server, whose
// replies share one stream, the one request waiting then, and none while
// two or more are, since it can't be told whose it is.

// callNotes is where one call's notifications from its server go.
type callNotes struct {
	token string
	note  gateway.HubNote
}

var progressTokens atomic.Int64

// newCallNotes is a call's notes, with a progressToken no other call has.
func newCallNotes(note gateway.HubNote) *callNotes {
	return &callNotes{token: fmt.Sprintf("magpie-%d", progressTokens.Add(1)), note: note}
}

type callNotesKey struct{}

func withNotes(ctx context.Context, n *callNotes) context.Context {
	return context.WithValue(ctx, callNotesKey{}, n)
}

func notesOf(ctx context.Context) *callNotes {
	n, _ := ctx.Value(callNotesKey{}).(*callNotes)
	return n
}

// progressTokenOf is the progressToken a notifications/progress names,
// when it is one magpie gives (a string).
func progressTokenOf(m *rpcMsg) string {
	var p struct {
		Token json.RawMessage `json:"progressToken"`
	}
	var tok string
	if json.Unmarshal(m.Params, &p) != nil || json.Unmarshal(p.Token, &tok) != nil {
		return ""
	}
	return tok
}

// take hands m to the call when it is about it: its progress, under its
// own token, or a log line. Anything else is let be.
func (n *callNotes) take(m *rpcMsg) {
	if n == nil {
		return
	}
	switch m.Method {
	case "notifications/progress":
		if progressTokenOf(m) != n.token {
			return
		}
	case "notifications/message":
	default:
		return
	}
	n.note(m.Method, m.Params)
}

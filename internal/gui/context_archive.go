package gui

// The context window card's text, and the reply beside it, read from the
// request archive once a request's body has left memory (wangbinyq on
// Discord: with the archive on, the card still said the text was gone, as
// magpie keeps only the last 10 requests' bodies in memory). The archive
// keeps the body as the agent sent it and the reply as the agent got it,
// their secrets taken out; gateway.ArchivedPromptText and
// gateway.ReplyPieces mask them as the body in memory is, so the archive
// shows nothing the memory's copy wouldn't.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// ctxGone says why a request's text can't be shown, for the card to say:
// "off" when the archive has no copy named for the request (it was off,
// or the route is gone), "missing" when it names one the archive doesn't
// have (not saved or uploaded yet, or removed past its size), "cut" when
// the archive kept only the start of it, "unread" when it can't be read
// or masked: nothing is shown unmasked in its place.
type ctxGone struct {
	Archive string `json:"archive"`
}

func writeGone(rw http.ResponseWriter, why string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(http.StatusNotFound)
	json.NewEncoder(rw).Encode(ctxGone{Archive: why})
}

// ctxRouteOf is request id's route, from the gateway this GUI runs or, for a
// request it no longer holds, the history of day (the page's ?day=).
func ctxRouteOf(r *http.Request, id int64) (gateway.Route, bool) {
	if gw := served.Load(); gw != nil {
		for _, route := range gw.Trace(r.Context(), 0, 0).Routes {
			if route.ID == id {
				return route, true
			}
		}
	}
	day, err := time.Parse("2006-01-02", r.URL.Query().Get("day"))
	if err != nil {
		return gateway.Route{}, false
	}
	return gateway.HistoryRoute(id, day)
}

// archivedCopy is a call as the archive kept it, read from this computer's
// archive, else the bucket, as the Routing page's archivePanel reads it
// (openArchive). The last one read is kept, in memory, so the items opened
// one by one read it once.
type archivedCopy struct {
	name     string
	from     provider.Protocol
	request  gateway.ArchivePart
	response gateway.ArchivePart
}

// lastCallFor is how long the last call read is kept
const lastCallFor = 5 * time.Minute

var lastCall struct {
	sync.Mutex
	c archivedCopy
}

var errNotArchived = errors.New("not archived")

func readArchived(ctx context.Context, archive string) (archivedCopy, error) {
	date, id, ok := strings.Cut(archive, "/")
	if !ok {
		return archivedCopy{}, errNotArchived
	}
	name, ok := gateway.ArchiveName(date, id)
	if !ok {
		return archivedCopy{}, errNotArchived
	}
	lastCall.Lock()
	if lastCall.c.name == name {
		c := lastCall.c
		lastCall.Unlock()
		return c, nil
	}
	lastCall.Unlock()
	body, size, err := openArchiveName(ctx, name)
	if err != nil {
		return archivedCopy{}, err
	}
	defer body.Close()
	if size > archiveReadMost {
		return archivedCopy{}, errArchiveCut
	}
	data, err := io.ReadAll(io.LimitReader(body, archiveReadMost+1))
	if err != nil {
		return archivedCopy{}, err
	}
	if len(data) > archiveReadMost {
		return archivedCopy{}, errArchiveCut
	}
	var a gateway.Archived
	if err := json.Unmarshal(data, &a); err != nil {
		return archivedCopy{}, errArchiveCut
	}
	c := archivedCopy{name: name, from: a.Call.From, request: a.Request, response: a.Response}
	lastCall.Lock()
	lastCall.c = c
	lastCall.Unlock()
	// let go of it once the card is likely closed: it can be two bodies
	// of the archive's 32 MiB
	time.AfterFunc(lastCallFor, func() {
		lastCall.Lock()
		if lastCall.c.name == name {
			lastCall.c = archivedCopy{}
		}
		lastCall.Unlock()
	})
	return c, nil
}

var errArchiveCut = errors.New("the archive's copy can't be read whole")

// openArchiveName opens the archive's file name, this computer's first;
// errNotArchived when neither it nor the bucket has it.
func openArchiveName(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	if body, size, err := localArchive().Open(ctx, name); !errors.Is(err, gateway.ErrNotArchived) {
		return body, size, err
	}
	b, ok := archiveBucket()
	if !ok {
		return nil, 0, errNotArchived
	}
	body, size, err := b.Open(ctx, name)
	if errors.Is(err, davsync.ErrNoObject) {
		return nil, 0, errNotArchived
	}
	return body, size, err
}

// archiveGone is the ctxGone of an error reading a route's archive; ""
// for one the page shows as it is (a bucket that failed to answer: not
// known to be gone).
func archiveGone(err error) string {
	switch {
	case errors.Is(err, errNotArchived):
		return "missing"
	case errors.Is(err, errArchiveCut):
		return "cut"
	}
	return ""
}

// archivedPromptText answers /api/context/text from the archive, for a
// request whose body has left memory.
func archivedPromptText(rw http.ResponseWriter, r *http.Request, id int64, kind string, withText bool) (gateway.PromptText, bool) {
	route, ok := ctxRouteOf(r, id)
	if !ok || route.Archive == "" {
		writeGone(rw, "off")
		return gateway.PromptText{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	c, err := readArchived(ctx, route.Archive)
	if err != nil {
		if why := archiveGone(err); why != "" {
			writeGone(rw, why)
		} else {
			fail(rw, err)
		}
		return gateway.PromptText{}, false
	}
	if c.request.Truncated {
		writeGone(rw, "cut")
		return gateway.PromptText{}, false
	}
	tokens := 0
	if route.Prompt != nil {
		tokens = route.Prompt.Tokens
	}
	pt, ok := gateway.ArchivedPromptText(c.from, []byte(c.request.Body), id, kind, withText, tokens)
	if !ok {
		writeGone(rw, "unread")
		return gateway.PromptText{}, false
	}
	return pt, true
}

// contextReplyAPI serves the reply to a request the card shows: from the
// gateway's Recent calls while it holds the call, else from the archive.
func contextReplyAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/context/reply", func(rw http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(rw, "invalid route id", http.StatusBadRequest)
			return
		}
		out := gateway.PromptReply{ID: id, From: "memory"}
		var (
			from provider.Protocol
			body []byte
			cut  bool
			have bool
		)
		if gw := served.Load(); gw != nil {
			from, body, cut, have = gw.ReplyBody(id)
		}
		if !have {
			route, ok := ctxRouteOf(r, id)
			if !ok || route.Archive == "" {
				writeGone(rw, "off")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
			defer cancel()
			c, err := readArchived(ctx, route.Archive)
			if err != nil {
				if why := archiveGone(err); why != "" {
					writeGone(rw, why)
				} else {
					fail(rw, err)
				}
				return
			}
			from, body, cut, out.From = c.from, []byte(c.response.Body), c.response.Truncated, "archive"
		}
		pieces, cut, ok := gateway.ReplyPieces(from, body, cut)
		if !ok {
			writeGone(rw, "unread")
			return
		}
		out.Pieces, out.Cut = pieces, cut
		rw.Header().Set("Cache-Control", "no-store")
		writeJSON(rw, out)
	})
}

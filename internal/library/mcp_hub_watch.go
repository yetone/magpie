package library

// Telling the hub's clients that its tools changed (jjiabdhhdhjiann on X:
// a tool added at the magpie this one has as a Remote magpie couldn't be
// used here until this magpie was restarted, as the hub kept that
// router's first list for good).
//
//   - The hub's Version changes with library.json and whenever a server's
//     tools are listed other than before (hubListsChanged); the gateway
//     tells each client holding the hub's event stream (gateway.hubEvents),
//     which lists its tools again.
//   - A server that is another magpie's router (isRouter) is held to by
//     that router's event stream (watch): told its tools changed, it is
//     listed again at once, and this hub's own clients are told when the
//     list did change, not before, so two magpies that are each other's
//     Remote magpie don't tell each other round. While no stream is open
//     (an older magpie that offers none, one being opened again) its list
//     is asked for again once it is routerFresh old, and a router that
//     doesn't answer then has its last list kept.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// hubGen counts the lists of the hub's servers that came out other than
// they were before.
var hubGen atomic.Int64

// hubListsChanged is that a server's tools were listed other than before.
func hubListsChanged() {
	hubGen.Add(1)
	gateway.HubChanged()
}

// Version is library.json as it is, and hubGen: what the hub gives
// anyone changes with one of them.
func (theHub) Version() string {
	h := fnv.New64a()
	b, _ := os.ReadFile(path())
	h.Write(b)
	return fmt.Sprintf("%x.%d", h.Sum64(), hubGen.Load())
}

// sameTools is whether two lists of a server's tools are the same.
func sameTools(a, b []map[string]json.RawMessage) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// routerFresh is how long another magpie's router's list is used without
// asking it again, while no event stream of it is open.
var routerFresh = 30 * time.Second

// isRouter is whether s is another magpie's router, reached over
// streamable HTTP: a Remote magpie's (Bridge), or one the user added.
func isRouter(s *Server) bool {
	if s.Transport != "http" || s.URL == "" {
		return false
	}
	u, err := url.Parse(s.URL)
	return err == nil && strings.HasPrefix(u.Path, gateway.HubMCPAll)
}

// unwatched is whether b's list is another magpie's router's that no
// event stream holds, listed routerFresh ago or more. b.mu is held.
func (b *backend) unwatched() bool {
	return b.router && !b.watching && time.Since(b.at) >= routerFresh
}

func (b *backend) setWatching(on bool) {
	b.mu.Lock()
	b.watching = on
	b.mu.Unlock()
}

// changed is the server saying its tools changed: they are listed again
// now, and the hub's clients are told if they did (tools).
func (b *backend) changed() {
	b.relist()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hubRelistWait)
		defer cancel()
		_, _ = b.tools(ctx)
	}()
}

// hubRelistWait is how long a list asked for after a server said its
// tools changed may take.
var hubRelistWait = 40 * time.Second

// watch holds the event stream of h, another magpie's router, open while
// h is connected: each notifications/tools/list_changed on it has the
// router listed again. A stream that ends is opened again, a second
// first, then twice as long each time up to a minute, and what the
// router said while it was closed isn't known, so it is listed again too.
// A router that offers no stream (an older magpie) is asked again when
// its list is routerFresh old (unwatched).
func (b *backend) watch(h *httpConn) {
	wait := time.Second
	for again := false; ; again = true {
		began := time.Now()
		err := h.c.listen(h.ended, func() {
			b.setWatching(true)
			if again {
				b.changed()
			}
		}, b.changed)
		b.setWatching(false)
		if h.ended.Err() != nil || errors.Is(err, errNoStream) {
			return
		}
		if time.Since(began) > time.Minute {
			wait = time.Second
		}
		select {
		case <-h.ended.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

// errNoStream is a server that offers no event stream of its own.
var errNoStream = errors.New("it offers no event stream")

// listen opens the session's event stream (Streamable HTTP's GET) and
// reads it until ctx ends or it does: opened once it is open, changed for
// each notifications/tools/list_changed.
func (c *httpSession) listen(ctx context.Context, opened, changed func()) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.s.URL, nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	for k, v := range c.hdr {
		req.Header[k] = slices.Clone(v)
	}
	session, version := c.session, c.version
	c.mu.Unlock()
	req.Header.Set("Accept", "text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", version)
	}
	res, err := checkClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusMethodNotAllowed || res.StatusCode/100 == 2 && !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return errNoStream
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("its event stream: %s", res.Status)
	}
	opened()
	for e := range sseEvents(res.Body, ctx.Done()) {
		if e.name != "" && e.name != "message" {
			continue
		}
		var m rpcMsg
		if json.Unmarshal([]byte(e.data), &m) == nil && m.Method == "notifications/tools/list_changed" {
			changed()
		}
	}
	return ctx.Err()
}

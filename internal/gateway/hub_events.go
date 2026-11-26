package gateway

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// The hub's event stream: Streamable HTTP's GET, which an MCP client
// opens after initialize for what the server says unasked. The hub says
// one thing there, notifications/tools/list_changed, whenever its
// Version changes, so a client lists its tools again: an agent sees a
// server added in the Library without starting again, and a magpie that
// has this one as a Remote magpie (library/bridge_mcp.go) lists this
// router's tools again rather than keep the ones it listed first, which
// it did until it was restarted (jjiabdhhdhjiann on X).

// hubWatchEvery is how often the hub's Version is read while a client
// holds the stream: the Library can be changed by another process (the
// window, the CLI), so it is looked at, not only told (HubChanged).
var hubWatchEvery = 2 * time.Second

// hubBeat is how often a quiet stream gets a comment, so a proxy or a
// tunnel between doesn't end it as idle.
var hubBeat = 25 * time.Second

const hubListChanged = "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n"

// hubWatchers are the streams open, each told when the Version changed;
// one goroutine reads it while there are any.
var hubWatchers = struct {
	sync.Mutex
	subs    map[chan struct{}]bool
	poke    chan struct{}
	last    string
	running bool
}{subs: map[chan struct{}]bool{}, poke: make(chan struct{}, 1)}

func hubVersion() string {
	if MCPHub == nil {
		return ""
	}
	return MCPHub.Version()
}

// hubWatch is a channel told when the hub's Version changes from now on,
// and the function that ends it.
func hubWatch() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	hubWatchers.Lock()
	if len(hubWatchers.subs) == 0 {
		hubWatchers.last = hubVersion()
	}
	hubWatchers.subs[c] = true
	start := !hubWatchers.running
	hubWatchers.running = true
	hubWatchers.Unlock()
	if start {
		go hubWatchLoop()
	}
	return c, func() {
		hubWatchers.Lock()
		delete(hubWatchers.subs, c)
		hubWatchers.Unlock()
	}
}

// HubChanged is the library telling the hub that a list it gives may have
// changed: its Version is read now, not at the next look.
func HubChanged() {
	select {
	case hubWatchers.poke <- struct{}{}:
	default:
	}
}

// hubWatchLoop reads the Version while a stream is open, and tells each
// when it changed. It ends with the last stream.
func hubWatchLoop() {
	tick := time.NewTicker(hubWatchEvery)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case <-hubWatchers.poke:
		}
		// MCPHub is read only while a stream is open (tests swap it)
		hubWatchers.Lock()
		if len(hubWatchers.subs) == 0 {
			hubWatchers.running = false
			hubWatchers.Unlock()
			return
		}
		hubWatchers.Unlock()
		v := hubVersion()
		hubWatchers.Lock()
		if v != hubWatchers.last && len(hubWatchers.subs) > 0 {
			hubWatchers.last = v
			for c := range hubWatchers.subs {
				select {
				case c <- struct{}{}:
				default:
				}
			}
		}
		hubWatchers.Unlock()
	}
}

// hubEvents holds the hub's event stream open: a list_changed whenever
// the hub's Version changes, a comment every hubBeat. It ends when the
// client goes or the gateway stops serving (streamsEnding).
func (s *Server) hubEvents(w http.ResponseWriter, r *http.Request) {
	changes, stop := hubWatch()
	defer stop()
	rc := http.NewResponseController(w)
	defer rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	if id := r.Header.Get("Mcp-Session-Id"); id != "" {
		h.Set("Mcp-Session-Id", id)
	}
	w.WriteHeader(http.StatusOK)
	send := func(text string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(w, text); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send(": magpie\n\n") {
		return
	}
	beat := time.NewTicker(hubBeat)
	defer beat.Stop()
	for {
		var text string
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsEnding():
			return
		case <-changes:
			text = hubListChanged
		case <-beat.C:
			text = ": \n\n"
		}
		if !send(text) {
			return
		}
	}
}

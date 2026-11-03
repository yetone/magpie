package gateway

import (
	"cmp"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// The route-events feed (#3387, joe21_eee on Discord, sywyyhykkk on
// GitHub): a display on the LAN, an ESP32 screen, shows every agent's
// requests as they are routed (started, each try, its first content,
// fallback, done) without knowing their sessions, which
// /v1/magpie/route asks for, and without the full trace, which names
// accounts, folders and errors and is told to this computer alone.
//
// GET routeSnapshotPath is the requests going now and a cursor; GET
// routeEventsPath?after=<cursor> streams each change after it as SSE. A
// route here is routeActivity, an allow list of fields, never a Route
// with some taken out. They are told as the usage log is: to this
// computer, and to another with an enabled gateway key while magpie is
// shared (a read-only key reads them too, readOnlyRoutes), never to a
// key held to a budget, models or accounts, which is told no one else's
// requests.
const (
	routeSnapshotPath = "/v1/magpie/route-events/snapshot"
	routeEventsPath   = "/v1/magpie/route-events"
	// routeEventsKeep is how many changes the feed keeps for a reader
	// that fell behind, or reconnects with the cursor it last had
	routeEventsKeep = 512
	// routeEventsBeat is how often an idle stream is sent a heartbeat,
	// and the longest a key turned off keeps an idle stream open
	routeEventsBeat = 15 * time.Second
)

// routeEventsRecheck is how often a stream with changes to send asks
// again whether its key is still on (a var for the tests)
var routeEventsRecheck = time.Second

// routeEventsRefused is what another machine is told while magpie isn't
// shared with it.
const routeEventsRefused = "magpie's routing events are told to another machine only when magpie is shared on the local network (Settings → Network and sharing → Share on local network) and the request carries an enabled gateway key (Authorization: Bearer <key> or x-api-key: <key>); a read-only key reads them too"

// routeActivity is what the feed tells of a request. ID is the route's,
// never a session's, an account's or a key's; Attempt counts from 1.
type routeActivity struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Agent    string    `json:"agent"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
	Attempt  int       `json:"attempt"`
	Fallback bool      `json:"fallback"`
	Phase    string    `json:"phase"`
	Status   int       `json:"status,omitempty"`
	Failed   bool      `json:"failed,omitempty"`
	Millis   int64     `json:"ms,omitempty"`
	TTFT     int64     `json:"ttft_ms,omitempty"`
}

// The phases a route is in.
const (
	phaseStarted    = "started"      // no try yet
	phaseAttempt    = "attempt"      // its last try is out
	phaseResponding = "responding"   // its last try's content is coming
	phaseTryDone    = "attempt_done" // its last try ended, the next not yet out
	phaseDone       = "done"         // answered, one way or the other
	phaseDropped    = "dropped"      // gone from the trace unfinished (drop, trim)
)

func activityOf(r *Route) routeActivity {
	a := routeActivity{ID: strconv.FormatInt(r.ID, 36), Time: r.Time, Agent: r.Agent,
		Phase: phaseStarted, Attempt: len(r.Tries)}
	if n := len(r.Tries); n > 0 {
		tr := r.Tries[n-1]
		a.Model, a.Status, a.Millis, a.TTFT = cmp.Or(tr.Served, tr.Model), tr.Status, tr.Millis, cmp.Or(tr.TTFT, tr.firstMs)
		a.Failed, a.Fallback, a.Phase = tr.Error != "" || tr.Status >= 400, n > 1, phaseAttempt
		// a try's ID is its seat's, which names an account or a key's
		// fingerprint: only the seat's provider is told
		for _, seat := range r.Order {
			if seat.ID == tr.ID && seat.Model == tr.Model {
				a.Provider = seat.Provider
				break
			}
		}
		if a.TTFT > 0 {
			a.Phase = phaseResponding
		}
		if tr.Done {
			a.Phase = phaseTryDone
		}
	}
	if r.Done {
		a.Phase, a.Status, a.Millis = phaseDone, r.Status, r.Millis
		a.Failed = r.Status >= 400 || r.Error != ""
	}
	return a
}

func activityChange(before, after routeActivity) string {
	switch {
	case after.Phase == phaseDone && before.Phase != phaseDone:
		return "request_finished"
	case after.Attempt > before.Attempt:
		return "attempt_started"
	case after.Phase == phaseTryDone && before.Phase != phaseTryDone:
		return "attempt_finished"
	case after.TTFT > 0 && before.TTFT == 0:
		return "first_content"
	default:
		return "route_changed"
	}
}

// firstContent is a try's firstToken.onFirst: it tells the route's last
// try, still going, that its first content came ms after it began.
func (t *trace) firstContent(r *Route) func(int64) {
	return func(ms int64) {
		t.update(r, func(r *Route) {
			if n := len(r.Tries); n > 0 && !r.Tries[n-1].Done {
				r.Tries[n-1].firstMs = max(ms, 1)
			}
		})
	}
}

type routeActivityEvent struct {
	Cursor string `json:"cursor"`
	Kind   string `json:"kind"`
	// Active is how many requests were going once this change was made
	Active int           `json:"active"`
	Route  routeActivity `json:"route"`
}

// routeActivityLog is the feed's last routeEventsKeep changes, a ring
// held under trace.mu: a request that begins and ends between two reads
// is told step by step, not as its end alone. epoch is this process's,
// so a cursor from before a restart is told to take a snapshot.
type routeActivityLog struct {
	epoch  string
	seq    int64
	events []routeActivityEvent
}

func (l *routeActivityLog) init() {
	if l.epoch == "" {
		l.epoch = rand.Text()
		l.events = make([]routeActivityEvent, routeEventsKeep)
	}
}

func (l *routeActivityLog) cursor() string {
	return l.epoch + ":" + strconv.FormatInt(l.seq, 10)
}

// after is every change after cursor; false when cursor isn't one of
// this process's, or the changes after it are no longer all kept.
func (l *routeActivityLog) after(cursor string) ([]routeActivityEvent, bool) {
	epoch, raw, ok := strings.Cut(cursor, ":")
	n, err := strconv.ParseInt(raw, 10, 64)
	if !ok || err != nil || epoch != l.epoch || n < 0 || n > l.seq || n < l.seq-routeEventsKeep {
		return nil, false
	}
	out := make([]routeActivityEvent, 0, l.seq-n)
	for i := n; i < l.seq; i++ {
		out = append(out, l.events[i%routeEventsKeep])
	}
	return out, true
}

// noteActivity records a change of r, under t.mu: none once r left the
// trace unfinished and the feed was told it went (noteDropped), though
// the request that holds r goes on writing to it.
func (t *trace) noteActivity(r *Route, kind string) {
	if r.offFeed {
		return
	}
	l := &t.activity
	l.init()
	l.seq++
	l.events[(l.seq-1)%routeEventsKeep] = routeActivityEvent{l.cursor(), kind, t.going(), activityOf(r)}
}

// noteDropped tells the feed r left the trace unfinished, under t.mu,
// after it was taken off t.routes.
func (t *trace) noteDropped(r *Route) {
	if r.offFeed {
		return
	}
	l := &t.activity
	l.init()
	l.seq++
	a := activityOf(r)
	a.Phase = phaseDropped
	l.events[(l.seq-1)%routeEventsKeep] = routeActivityEvent{l.cursor(), "request_dropped", t.going(), a}
	r.offFeed = true
}

// going counts the requests still going, under t.mu.
func (t *trace) going() int {
	n := 0
	for _, r := range t.routes {
		if !r.Done {
			n++
		}
	}
	return n
}

// routeEventsAllowed: r may read the feed. It is answered as the usage
// log's routes are (usageSummary), and refused otherwise.
func routeEventsAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !local(r) && !sharedWith(r) {
		writeError(w, provider.Chat, http.StatusForbidden, routeEventsRefused)
		return false
	}
	return !refuseHeld(w, r, "this gateway key is held to a budget, models or accounts, so it is told no one else's routing: GET /v1/magpie/usage/requests tells it its own calls")
}

// activitySnapshot is the requests going now, by id, and the cursor of
// the last change, read together.
func (t *trace) activitySnapshot() (string, []routeActivity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.activity.init()
	routes := []routeActivity{}
	for _, r := range t.routes {
		if !r.Done && !r.offFeed {
			routes = append(routes, activityOf(r))
		}
	}
	return t.activity.cursor(), routes
}

func (s *Server) routeSnapshot(w http.ResponseWriter, r *http.Request) {
	if !routeEventsAllowed(w, r) {
		return
	}
	cursor, routes := s.trace.activitySnapshot()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"cursor": cursor, "active": len(routes), "routes": routes})
}

// routeEvents streams the feed's changes after a cursor (Last-Event-ID,
// else ?after=). It ends when the client goes, when the gateway stops
// serving (streamsEnding: Shutdown would wait its whole drain for it),
// when the key it came with is turned off, removed or runs out, and when
// magpie stops being shared with a reader on another machine.
func (s *Server) routeEvents(w http.ResponseWriter, r *http.Request) {
	if !routeEventsAllowed(w, r) {
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	keyID, remote := access.Caller(r.Context()).KeyID, !local(r)
	// still: the reader may still be told the feed, read again at most
	// once routeEventsRecheck, as it reads the key store and settings
	// from disk under the lock every request's key check takes
	var checked time.Time
	still := func() bool {
		if time.Since(checked) < routeEventsRecheck {
			return true
		}
		checked = time.Now()
		return (keyID == "" || access.On(keyID)) && (!remote || remoteKeyed())
	}
	t := &s.trace
	beat := time.NewTicker(routeEventsBeat)
	defer beat.Stop()
	rc := http.NewResponseController(w)
	defer rc.SetWriteDeadline(time.Time{})
	started := false
	write := func(event, id string, data any) bool {
		b, err := json.Marshal(data)
		if err != nil {
			return false
		}
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if id != "" {
			if _, err = fmt.Fprintf(w, "id: %s\n", id); err != nil {
				return false
			}
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		return err == nil
	}
	for {
		t.mu.Lock()
		t.activity.init()
		events, ok := t.activity.after(cursor)
		if t.wake == nil {
			t.wake = make(chan struct{})
		}
		wake := t.wake
		t.mu.Unlock()
		if !ok {
			reset := map[string]string{"error": "snapshot_required", "snapshot": routeSnapshotPath}
			if started {
				write("reset", "", reset)
				rc.Flush()
			} else {
				writeJSON(w, http.StatusConflict, reset)
			}
			return
		}
		if !started {
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			if err := rc.Flush(); err != nil {
				return
			}
			started = true
		}
		if len(events) > 0 {
			if !still() {
				return
			}
			for _, event := range events {
				if !write("route", event.Cursor, event) {
					return
				}
				cursor = event.Cursor
			}
			if rc.Flush() != nil {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsEnding():
			return
		case <-wake:
		case <-beat.C:
			checked = time.Time{}
			if !still() || !write("heartbeat", "", struct{}{}) || rc.Flush() != nil {
				return
			}
		}
	}
}

// streamsEnding closes once the gateway stops serving.
func (s *Server) streamsEnding() <-chan struct{} {
	s.endingOnce.Do(func() { s.ending = make(chan struct{}) })
	return s.ending
}

// endStreams ends every stream waiting on streamsEnding.
func (s *Server) endStreams() {
	s.streamsEnding()
	s.endOnce.Do(func() { close(s.ending) })
}

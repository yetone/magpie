package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/lazyre"
)

// The page runtime's room on this computer (docs/subsystems/artifacts.md,
// "Live sharing"): whoever has an artifact's page open now, in another
// tab or window here. It speaks site/live.js's protocol, so runtime.js
// has one room for both: a page sends join, leave, emit and presence
// ({t, room, ref, topic, data, patch}) and hears hi, joined, peer, gone,
// msg and error. Everyone here is the artifact's owner, so every peer
// isMe, and a page's own socket is its sameTab. Nothing is kept: a room
// is the sockets open now, and goes with the last of them.

const (
	roomMaxMessage = 64 << 10
	roomMaxData    = 4096
	roomMaxDepth   = 8
	roomMaxRooms   = 16
	roomMaxPeers   = 200
	roomRate       = 40.0
	roomBurst      = 80.0
	// a page that reads its socket slower than this many messages behind
	// is dropped (it joins again)
	roomQueue = 256
)

var (
	roomNameRe  = lazyre.New(`^[a-z0-9][a-z0-9_.-]{0,47}$`)
	roomTopicRe = lazyre.New(`^[a-z][a-z0-9_.-]{0,47}$`)
)

// rooms holds every artifact's rooms, by its id.
var rooms = &roomHub{arts: map[string]*roomArt{}}

type roomHub struct {
	mu   sync.Mutex
	arts map[string]*roomArt
}

// roomArt is one artifact's rooms, "" its lobby.
type roomArt struct {
	rooms map[string]map[*roomConn]*roomMember
	seq   uint64
}

type roomMember struct {
	presence map[string]any
	updated  int64
	seq      uint64
}

// roomConn is one page's socket.
type roomConn struct {
	sid    string
	by     any // the viewer's id when the page declares user, else nil
	art    string
	out    chan []byte
	dead   chan struct{}
	once   sync.Once
	tokens float64
	at     time.Time
}

func (c *roomConn) drop() { c.once.Do(func() { close(c.dead) }) }

func (c *roomConn) send(m any) {
	b, err := roomJSON(m)
	if err != nil {
		return
	}
	select {
	case c.out <- b:
	case <-c.dead:
	default:
		// a page this far behind has lost messages: it joins again
		c.drop()
	}
}

func (c *roomConn) allowed() bool {
	now := time.Now()
	if c.at.IsZero() {
		c.tokens, c.at = roomBurst, now
	}
	c.tokens = min(roomBurst, c.tokens+now.Sub(c.at).Seconds()*roomRate)
	c.at = now
	if c.tokens < 1 {
		return false
	}
	c.tokens--
	return true
}

// roomJSON is JSON as a browser writes it: < > & as they are, so a size
// counted here is the one the page counted.
func roomJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// roomDepth is site/live.js's depthOf.
func roomDepth(v any, d int) int {
	if d > roomMaxDepth {
		return d
	}
	m := d + 1
	switch x := v.(type) {
	case map[string]any:
		for _, c := range x {
			m = max(m, roomDepth(c, d+1))
		}
	case []any:
		for _, c := range x {
			m = max(m, roomDepth(c, d+1))
		}
	default:
		return d
	}
	return m
}

func roomTooBig(v any) bool {
	if roomDepth(v, 0) > roomMaxDepth {
		return true
	}
	b, err := roomJSON(v)
	return err != nil || len(b) > roomMaxData
}

func (h *roomHub) peer(c *roomConn, m *roomMember, to *roomConn) map[string]any {
	p := m.presence
	if p == nil {
		p = map[string]any{}
	}
	return map[string]any{"peer": c.sid, "by": c.by, "isMe": true, "sameTab": c == to, "kind": "viewer", "guest": false, "presence": p, "updatedAt": m.updated}
}

// tell sends each member of room but skip what make gives them; h.mu is
// held.
func (h *roomHub) tell(room map[*roomConn]*roomMember, skip *roomConn, msg func(to *roomConn) any) {
	for to := range room {
		if to != skip {
			to.send(msg(to))
		}
	}
}

// handle is one message from c; it answers c and tells the room.
func (h *roomHub) handle(c *roomConn, raw []byte) {
	fail := func(code, msg string, ref any) {
		m := map[string]any{"t": "error", "code": code, "message": msg}
		if ref != nil {
			m["ref"] = ref
		}
		c.send(m)
	}
	if len(raw) > roomMaxMessage {
		fail("invalid_argument", "a message is text, at most 64 KiB", nil)
		return
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil || m == nil {
		fail("invalid_argument", "a message is a JSON object", nil)
		return
	}
	var ref any
	switch r := m["ref"].(type) {
	case json.Number, string:
		ref = r
	}
	name, _ := m["room"].(string)
	if name != "" && !roomNameRe.MatchString(name) {
		fail("invalid_argument", "a room's name is ^[a-z0-9][a-z0-9_.-]{0,47}$", ref)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	art := h.arts[c.art]
	if art == nil {
		art = &roomArt{rooms: map[string]map[*roomConn]*roomMember{}}
		h.arts[c.art] = art
	}
	room := art.rooms[name]
	cur := room[c]
	switch m["t"] {
	case "join":
		if cur == nil {
			n := 0
			for k, r := range art.rooms {
				if k != "" && r[c] != nil {
					n++
				}
			}
			if name != "" && n >= roomMaxRooms {
				fail("limit_reached", "a page is in 16 rooms at most", ref)
				return
			}
			if len(room) >= roomMaxPeers {
				fail("limit_reached", "a room holds 200 pages at most", ref)
				return
			}
			if room == nil {
				room = map[*roomConn]*roomMember{}
				art.rooms[name] = room
			}
			art.seq++
			cur = &roomMember{presence: map[string]any{}, updated: time.Now().UnixMilli(), seq: art.seq}
			room[c] = cur
			h.tell(room, c, func(to *roomConn) any {
				return map[string]any{"t": "peer", "room": name, "peer": h.peer(c, cur, to)}
			})
		}
		all := make([]*roomConn, 0, len(room))
		for p := range room {
			all = append(all, p)
		}
		sort.Slice(all, func(i, j int) bool { return room[all[i]].seq < room[all[j]].seq })
		peers := make([]any, 0, len(all))
		for _, p := range all {
			peers = append(peers, h.peer(p, room[p], c))
		}
		c.send(map[string]any{"t": "joined", "ref": ref, "room": name, "peers": peers})
	case "leave":
		if cur != nil {
			h.leave(art, name, c)
		}
	case "presence":
		if cur == nil {
			fail("invalid_argument", "join the room first", ref)
			return
		}
		patch, ok := m["patch"].(map[string]any)
		if !ok {
			fail("invalid_argument", "a presence patch is an object", ref)
			return
		}
		if !c.allowed() {
			fail("rate_limited", "presence is held to about 40 a second", ref)
			return
		}
		merged := make(map[string]any, len(cur.presence)+len(patch))
		for k, v := range cur.presence {
			merged[k] = v
		}
		for k, v := range patch {
			if k == "__proto__" || k == "constructor" || k == "prototype" {
				fail("invalid_argument", k+" can't be a key", ref)
				return
			}
			if v == nil {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		if roomTooBig(merged) {
			fail("invalid_argument", "presence is 4096 bytes of JSON at most, 8 deep", ref)
			return
		}
		cur.presence, cur.updated = merged, time.Now().UnixMilli()
		h.tell(room, c, func(to *roomConn) any {
			return map[string]any{"t": "peer", "room": name, "peer": h.peer(c, cur, to)}
		})
	case "emit":
		if cur == nil {
			fail("invalid_argument", "join the room first", ref)
			return
		}
		topic, _ := m["topic"].(string)
		if !roomTopicRe.MatchString(topic) {
			fail("invalid_argument", "a topic is ^[a-z][a-z0-9_.-]{0,47}$", ref)
			return
		}
		// everyone here is the owner, who sends on every topic
		data, has := m["data"]
		if has && roomTooBig(data) {
			fail("invalid_argument", "data is 4096 bytes of JSON at most, 8 deep", ref)
			return
		}
		if !c.allowed() {
			fail("rate_limited", "emits are held to about 40 a second", ref)
			return
		}
		h.tell(room, nil, func(to *roomConn) any {
			msg := map[string]any{"t": "msg", "room": name, "topic": topic, "from": map[string]any{"peer": c.sid, "by": c.by, "isMe": true, "sameTab": c == to, "kind": "viewer", "guest": false}}
			if has {
				msg["data"] = data
			}
			return msg
		})
	default:
		fail("invalid_argument", "no such message", ref)
	}
}

// leave takes c out of one room; h.mu is held.
func (h *roomHub) leave(art *roomArt, name string, c *roomConn) {
	room := art.rooms[name]
	delete(room, c)
	if len(room) == 0 {
		delete(art.rooms, name)
		return
	}
	h.tell(room, nil, func(*roomConn) any { return map[string]any{"t": "gone", "room": name, "peer": c.sid} })
}

// gone takes c out of every room.
func (h *roomHub) gone(c *roomConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	art := h.arts[c.art]
	if art == nil {
		return
	}
	for name, room := range art.rooms {
		if room[c] != nil {
			h.leave(art, name, c)
		}
	}
	if len(art.rooms) == 0 {
		delete(h.arts, c.art)
	}
}

// artifactLive is a page's room socket, GET <page>/_api/live?t=<token>.
// The page's origin is opaque (Origin: null) and its token is what lets
// it in, as for every call of its API, so the socket takes any origin.
// It ends when the page goes, the artifact does (4001), or the gateway
// stops serving.
func (s *Server) artifactLive(w http.ResponseWriter, r *http.Request) {
	a, n, ok := artifactAPIOpen(w, r, r.URL.Query().Get("t"), "room")
	if !ok {
		return
	}
	caps := a.Version(n).Capabilities
	if _, err := artifact.ParseRoomConfig(caps["room"]); err != nil {
		artifactAPIError(w, http.StatusForbidden, "not_granted", err.Error())
		return
	}
	var by any
	if caps.Has("user") {
		v, err := artifact.LocalViewer()
		if err != nil {
			artifactAPIError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
			return
		}
		by = v.ID
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(roomMaxMessage + 1)
	sid := make([]byte, 8)
	rand.Read(sid)
	c := &roomConn{sid: "p_" + hex.EncodeToString(sid), by: by, art: a.ID, out: make(chan []byte, roomQueue), dead: make(chan struct{})}
	defer rooms.gone(c)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c.send(map[string]any{"t": "hi", "sid": c.sid})

	go func() {
		defer c.drop()
		for {
			kind, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if kind == websocket.MessageText {
				rooms.handle(c, b)
			}
		}
	}()
	poll := time.NewTicker(artifactPollEvery)
	defer poll.Stop()
	for {
		select {
		case b := <-c.out:
			wctx, done := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Write(wctx, websocket.MessageText, b)
			done()
			if err != nil {
				return
			}
		case <-poll.C:
			if _, err := artifact.Get(a.ID); errors.Is(err, artifact.ErrNotFound) {
				ws.Close(4001, "the artifact is gone")
				return
			}
		case <-c.dead:
			// the page closed it, or fell behind and joins again
			ws.Close(websocket.StatusNormalClosure, "")
			return
		case <-s.streamsEnding():
			ws.Close(websocket.StatusGoingAway, "magpie is stopping")
			return
		case <-ctx.Done():
			return
		}
	}
}

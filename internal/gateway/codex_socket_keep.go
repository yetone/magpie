package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

// A conversation's socket to the ChatGPT backend is kept between its
// turns, and a turn that carries on from the last one goes on it as only
// its new input after previous_response_id, as Codex's own client does it
// (codex-rs core/src/client.rs: WebsocketSession, get_incremental_items).
// With store false the backend holds a response for the socket it was
// made on, so the id is only ever named on that socket.
//
// A socket is kept for a turn whose request names its conversation
// (session-id or thread-id, or else prompt_cache_key) and answered
// response.completed with an id; under a key of the host, the sign-in
// (a digest of Authorization and chatgpt-account-id), the conversation's
// ids and the subagent headers, so another account's turn never finds it.
// The next turn of that key takes it out of codexKeepPool, so a second
// turn of the conversation at the same time opens its own socket, and the
// last to end is the one kept. A kept socket is let go after codexKeepIdle
// unused, at codexKeepAge (the backend ends a connection at 60 minutes),
// and the oldest past codexKeepMax.
//
// The turn goes as a delta when every field but input, client_metadata
// and stream_options is as the last turn sent it, and its input starts
// with the last turn's input and then that turn's output items
// (response.output_item.done, in order), each item compared as codexSocketItem
// has it. Anything else goes whole on the kept socket, as Codex sends it.
// What is compared is the body as it goes, after masking and every
// translation, so a turn masked otherwise than the last goes whole.
//
// A kept socket that fails before the reply is handed on (it was closed
// while kept, the write fails, an error frame comes first, such as
// previous_response_not_found or the connection limit) is closed, and the
// turn goes whole on a new socket, and from there over HTTP, as a turn
// with no kept socket does.

var (
	// codexKeepIdle is how long a kept socket waits for its next turn
	codexKeepIdle = 10 * time.Minute
	// codexKeepAge is how long a socket is used, from its handshake
	codexKeepAge = 55 * time.Minute
)

// codexKeepMax is how many sockets are kept at most.
const codexKeepMax = 32

var (
	codexKeepMu   sync.Mutex
	codexKeepPool = map[string]*codexKept{}
)

// codexKept is a socket to the backend and what its last turn sent and
// got back.
type codexKept struct {
	c       *websocket.Conn
	cancel  context.CancelFunc
	key     string // "" for a turn that names no conversation
	host    string
	header  http.Header // the handshake's, as each reply carries them
	opened  time.Time
	used    time.Time
	once    sync.Once
	timer   *time.Timer
	fields  map[string]string
	input   []itemSum
	output  []itemSum
	replyID string
}

// close ends the socket at once.
func (k *codexKept) close() {
	k.once.Do(func() {
		k.c.CloseNow()
		k.cancel()
	})
}

// closeNormally ends the socket with a normal close, as a turn's end.
func (k *codexKept) closeNormally() {
	k.once.Do(func() {
		go func() {
			k.c.Close(websocket.StatusNormalClosure, "")
			k.cancel()
		}()
	})
}

// codexKeepKey is req's conversation's key in codexKeepPool, "" for a
// request that names none.
func codexKeepKey(req *http.Request, body []byte) string {
	sid, tid := req.Header.Get("Session-Id"), req.Header.Get("Thread-Id")
	if sid == "" && tid == "" {
		// Codex's prompt_cache_key is its thread's id
		if sid = gjson.GetBytes(body, "prompt_cache_key").String(); sid == "" {
			return ""
		}
	}
	h := sha256.New()
	for _, v := range []string{
		req.URL.Scheme, req.URL.Host, req.URL.Path, req.Header.Get("Authorization"), req.Header.Get("Chatgpt-Account-Id"),
		// a Responses provider's key may be said in either (#5781)
		req.Header.Get("X-Api-Key"), req.Header.Get("Api-Key"),
		sid, tid, req.Header.Get("X-Openai-Subagent"), req.Header.Get("X-Codex-Parent-Thread-Id"),
	} {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// codexKeepTake takes key's kept socket out of the pool, nil for none.
func codexKeepTake(key string) *codexKept {
	codexKeepMu.Lock()
	k := codexKeepPool[key]
	if k != nil {
		delete(codexKeepPool, key)
		k.timer.Stop()
	}
	codexKeepMu.Unlock()
	if k != nil && time.Since(k.opened) >= codexKeepAge {
		k.close()
		return nil
	}
	return k
}

// codexKeepPut keeps k for its conversation's next turn, in place of one
// kept before it.
func codexKeepPut(k *codexKept) {
	if k.key == "" || time.Since(k.opened) >= codexKeepAge {
		k.close()
		return
	}
	k.used = time.Now()
	codexKeepMu.Lock()
	old := codexKeepPool[k.key]
	if old != nil {
		old.timer.Stop()
	}
	codexKeepPool[k.key] = k
	k.timer = time.AfterFunc(codexKeepIdle, func() { codexKeepDrop(k) })
	var evict *codexKept
	if len(codexKeepPool) > codexKeepMax {
		for _, e := range codexKeepPool {
			if evict == nil || e.used.Before(evict.used) {
				evict = e
			}
		}
		delete(codexKeepPool, evict.key)
		evict.timer.Stop()
	}
	codexKeepMu.Unlock()
	if old != nil && old != k {
		old.close()
	}
	if evict != nil {
		evict.close()
	}
}

// codexKeepDrop lets k go if it is still kept.
func codexKeepDrop(k *codexKept) {
	codexKeepMu.Lock()
	kept := codexKeepPool[k.key] == k
	if kept {
		delete(codexKeepPool, k.key)
	}
	codexKeepMu.Unlock()
	if kept {
		k.close()
	}
}

// codexKeepDrain lets every kept socket go.
func codexKeepDrain() {
	codexKeepMu.Lock()
	all := make([]*codexKept, 0, len(codexKeepPool))
	for key, k := range codexKeepPool {
		k.timer.Stop()
		all = append(all, k)
		delete(codexKeepPool, key)
	}
	codexKeepMu.Unlock()
	for _, k := range all {
		k.close()
	}
}

// codexKeptCount is how many sockets are kept.
func codexKeptCount() int {
	codexKeepMu.Lock()
	defer codexKeepMu.Unlock()
	return len(codexKeepPool)
}

// codexTurnShape is what of a turn the next one is compared with.
type codexTurnShape struct {
	fields map[string]string
	input  []json.RawMessage
	sums   []itemSum
}

// codexShape is body's shape, false for a body that is not continued
// from: one whose input isn't a list of items, or that names a previous
// response itself.
func codexShape(body []byte) (*codexTurnShape, bool) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil, false
	}
	if _, ok := top["previous_response_id"]; ok {
		return nil, false
	}
	var input []json.RawMessage
	if json.Unmarshal(top["input"], &input) != nil {
		return nil, false
	}
	s := &codexTurnShape{fields: map[string]string{}, input: input, sums: make([]itemSum, len(input))}
	for k, v := range top {
		switch k {
		case "input", "client_metadata", "stream_options":
			continue
		}
		var b bytes.Buffer
		if json.Compact(&b, v) != nil {
			return nil, false
		}
		s.fields[k] = b.String()
	}
	for i, it := range input {
		sum, ok := codexSocketItem(it)
		if !ok {
			return nil, false
		}
		s.sums[i] = sum
	}
	return s, true
}

// codexSocketItem is an item's digest as the backend and Codex both have it:
// with neither its own id nor status (Codex sends its items back without
// ids the backend gave), and nothing null, an empty list, annotations or
// logprobs at any depth (Codex keeps none of them).
func codexSocketItem(raw []byte) (itemSum, bool) {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return itemSum{}, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		return itemSum{}, false
	}
	delete(m, "id")
	delete(m, "status")
	b, err := json.Marshal(codexSocketItemBare(m))
	if err != nil {
		return itemSum{}, false
	}
	return sha256.Sum256(b), true
}

func codexSocketItemBare(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k == "annotations" || k == "logprobs" || e == nil {
				delete(x, k)
				continue
			}
			if l, ok := e.([]any); ok && len(l) == 0 {
				delete(x, k)
				continue
			}
			x[k] = codexSocketItemBare(e)
		}
	case []any:
		for i, e := range x {
			x[i] = codexSocketItemBare(e)
		}
	}
	return v
}

// delta is body as its new input after previous_response_id, when s
// carries on from k's last turn.
func (k *codexKept) delta(body []byte, s *codexTurnShape) ([]byte, bool) {
	if k.replyID == "" || k.fields == nil || len(s.fields) != len(k.fields) {
		return nil, false
	}
	for f, v := range k.fields {
		if s.fields[f] != v {
			return nil, false
		}
	}
	n := len(k.input) + len(k.output)
	if len(s.sums) < n {
		return nil, false
	}
	for i, sum := range k.input {
		if s.sums[i] != sum {
			return nil, false
		}
	}
	for i, sum := range k.output {
		if s.sums[len(k.input)+i] != sum {
			return nil, false
		}
	}
	in := gjson.GetBytes(body, "input")
	if !in.IsArray() || in.Index <= 0 {
		return nil, false
	}
	rest := []byte{'['}
	for i, it := range s.input[n:] {
		if i > 0 {
			rest = append(rest, ',')
		}
		rest = append(rest, it...)
	}
	rest = append(rest, ']')
	out := make([]byte, 0, len(body)-len(in.Raw)+len(rest))
	out = append(out, body[:in.Index]...)
	out = append(out, rest...)
	out = append(out, body[in.Index+len(in.Raw):]...)
	id, _ := json.Marshal(k.replyID)
	return withField(out, "previous_response_id", id), true
}

// withField is body, a JSON object, with key set to raw ahead of the rest.
func withField(body []byte, key string, raw []byte) []byte {
	rest := bytes.TrimSpace(body[1:])
	out := make([]byte, 0, len(body)+len(key)+len(raw)+4)
	out = append(out, '{')
	k, _ := json.Marshal(key)
	out = append(out, k...)
	out = append(out, ':')
	out = append(out, raw...)
	if len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	return append(out, rest...)
}

// codexReplyItem is a response.output_item.done frame's item's digest.
func codexReplyItem(frame []byte) (itemSum, bool) {
	it := gjson.GetBytes(frame, "item")
	if !it.IsObject() {
		return itemSum{}, false
	}
	return codexSocketItem([]byte(it.Raw))
}

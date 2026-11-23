package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Copilot's Chat API sends a reply's reasoning sealed, as
// reasoning_opaque beside the reply's tool calls, and its own client puts
// it back on that assistant message the next turn (VS Code Copilot Chat's
// copilotChatEndpoint: out.reasoning_opaque on every assistant message
// with one). Taken back, Copilot opens it into the model's earlier
// reasoning: probed live on gemini-4-hydrogen, the second turn counted 275
// prompt tokens with it and 227 without, and 227 with a broken one, each
// answered 200 (Copilot drops one it can't open). No agent of Chat keeps
// a field it doesn't know, and no other protocol has a place for it, so
// every client lost its model's reasoning between tool turns
// (@yuushabu_buyinn on X).
//
// magpie keeps it by the tool call it came with, one file per account,
// model and call id under its folder (copilotOpaqueDir), and puts it back
// on the assistant message that carries that call when it goes to the
// same account's same model again. A message with reasoning_opaque of its
// own keeps it. A reply with no tool call has nothing to know its message
// by and is not kept.
const (
	opaqueKeyBytes = 16
	// opaqueKeep: a conversation not taken up again in this long goes on
	// without its old reasoning, as before
	opaqueKeep = 14 * 24 * time.Hour
	// opaqueLargest is the largest opaque kept; Copilot's was 1156
	// characters for 48 reasoning tokens
	opaqueLargest = 1 << 20
	// opaqueWholeMost is how much of a reply sent whole is read for it
	opaqueWholeMost = 8 << 20
)

var (
	copilotOpaqueDir = func() string { return filepath.Join(settings.Dir(), "copilot-reasoning") }

	opaquePruneMu   sync.Mutex
	opaquePrunedAt  time.Time
	opaquePruneTick = time.Hour
)

// opaqueKey names the file of call id's opaque from user's model.
func opaqueKey(user, model, id string) string {
	h := sha256.Sum256([]byte(user + "\x00" + model + "\x00" + id))
	return base64.RawURLEncoding.EncodeToString(h[:opaqueKeyBytes])
}

// keepOpaque stores opaque for call id of user's model, written whole
// (temp-and-rename) so a reader never sees half of one.
func keepOpaque(user, model, id, opaque string) {
	if id == "" || opaque == "" || len(opaque) > opaqueLargest {
		return
	}
	dir := copilotOpaqueDir()
	path := filepath.Join(dir, opaqueKey(user, model, id))
	if b, err := os.ReadFile(path); err == nil && string(b) == opaque {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return
	}
	_, werr := tmp.WriteString(opaque)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
		return
	}
	pruneOpaques(dir)
}

// keptOpaque is the opaque stored for call id of user's model, "" for
// none. A read keeps it from being pruned.
func keptOpaque(user, model, id string) string {
	if id == "" {
		return ""
	}
	path := filepath.Join(copilotOpaqueDir(), opaqueKey(user, model, id))
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	now := time.Now()
	os.Chtimes(path, now, now)
	return string(b)
}

// pruneOpaques removes, at most once per opaquePruneTick, the opaques not
// read or written for opaqueKeep and temp files a write left behind.
func pruneOpaques(dir string) {
	opaquePruneMu.Lock()
	if time.Since(opaquePrunedAt) < opaquePruneTick {
		opaquePruneMu.Unlock()
		return
	}
	opaquePrunedAt = time.Now()
	opaquePruneMu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		age := time.Since(info.ModTime())
		if age > opaqueKeep || (e.Name()[0] == '.' && age > time.Hour) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// copilotOpaqueOn is whether p's request on to carries Copilot's sealed
// reasoning: a Copilot account's Chat API.
func copilotOpaqueOn(p provider.Provider, to provider.Protocol) bool {
	return to == provider.Chat && copilotAccount(p)
}

// withCopilotOpaque is body with each assistant message that has tool
// calls and no reasoning_opaque given the one kept for its calls, from
// p's account's same model. Every other message keeps its bytes, and a
// body with nothing kept comes back as it was.
func withCopilotOpaque(p provider.Provider, to provider.Protocol, body []byte) []byte {
	if !copilotOpaqueOn(p, to) || !bodyHas(body, `"tool_calls"`) {
		return body
	}
	user, model := p.Account.User, modelOf(body)
	return editMessages(body, []string{"tool_calls"}, func(m map[string]json.RawMessage) bool {
		var role string
		decodeJSON(m["role"], &role)
		if role != "assistant" {
			return false
		}
		if _, own := m["reasoning_opaque"]; own {
			return false
		}
		var calls []struct {
			ID string `json:"id"`
		}
		if decodeJSON(m["tool_calls"], &calls) != nil {
			return false
		}
		for _, c := range calls {
			if o := keptOpaque(user, model, c.ID); o != "" {
				m["reasoning_opaque"], _ = marshalPlain(o)
				return true
			}
		}
		return false
	})
}

// heardCopilotOpaque has res's reply, a 200 to body on p's Copilot Chat
// API, read for its reasoning_opaque as it is passed on, and kept by its
// tool calls (copilotOpaqueTap).
func heardCopilotOpaque(p provider.Provider, to provider.Protocol, body []byte, res *http.Response) {
	if !copilotOpaqueOn(p, to) || res == nil || res.Body == nil || res.StatusCode != http.StatusOK {
		return
	}
	res.Body = &copilotOpaqueTap{ReadCloser: res.Body, user: p.Account.User, model: modelOf(body)}
}

// copilotOpaqueTap passes a Copilot Chat reply on as it comes, and keeps
// each choice's reasoning_opaque by that choice's tool call ids: in a
// stream once both have come (the opaque came with the first call, live,
// and the last one sent is what VS Code puts back), in a reply sent whole
// at its end.
type copilotOpaqueTap struct {
	io.ReadCloser
	user, model string
	buf         []byte
	whole, sure bool
	done        bool
	choices     map[int64]*opaqueChoice
}

type opaqueChoice struct {
	opaque string
	ids    []string
	kept   map[string]string
}

func (t *copilotOpaqueTap) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 && !t.done {
		t.buf = append(t.buf, p[:n]...)
		t.look()
	}
	if err == io.EOF && !t.done && t.whole {
		t.done = true
		t.readWhole(t.buf)
		t.buf = nil
	}
	return n, err
}

// look tells a stream from a reply sent whole by its first byte, and reads
// a stream's whole lines.
func (t *copilotOpaqueTap) look() {
	if !t.sure {
		b := bytes.TrimLeft(t.buf, " \t\r\n")
		if len(b) == 0 {
			return
		}
		t.sure, t.whole = true, b[0] == '{'
	}
	if t.whole {
		if len(t.buf) > opaqueWholeMost {
			t.done, t.buf = true, nil
		}
		return
	}
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(t.buf[:i])
		t.buf = t.buf[i+1:]
		data, ok := bytes.CutPrefix(line, []byte("data:"))
		if ok && (bodyHas(data, `"reasoning_opaque"`) || bodyHas(data, `"tool_calls"`)) {
			t.readChunk(bytes.TrimSpace(data))
		}
	}
	if len(t.buf) > opaqueLargest+opaqueWholeMost {
		// a line no stream sends: the rest isn't read
		t.done, t.buf = true, nil
	}
}

func (t *copilotOpaqueTap) choice(i int64) *opaqueChoice {
	if t.choices == nil {
		t.choices = map[int64]*opaqueChoice{}
	}
	c := t.choices[i]
	if c == nil {
		c = &opaqueChoice{kept: map[string]string{}}
		t.choices[i] = c
	}
	return c
}

func (t *copilotOpaqueTap) readChunk(data []byte) {
	gjson.GetBytes(data, "choices").ForEach(func(_, ch gjson.Result) bool {
		c := t.choice(ch.Get("index").Int())
		d := ch.Get("delta")
		if o := d.Get("reasoning_opaque"); o.Type == gjson.String && o.Str != "" {
			c.opaque = o.Str
		}
		d.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			if id := call.Get("id").Str; id != "" {
				c.ids = append(c.ids, id)
			}
			return true
		})
		t.keep(c)
		return true
	})
}

func (t *copilotOpaqueTap) readWhole(b []byte) {
	gjson.GetBytes(b, "choices").ForEach(func(_, ch gjson.Result) bool {
		c := t.choice(ch.Get("index").Int())
		m := ch.Get("message")
		c.opaque = m.Get("reasoning_opaque").Str
		m.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
			if id := call.Get("id").Str; id != "" {
				c.ids = append(c.ids, id)
			}
			return true
		})
		t.keep(c)
		return true
	})
}

// keep writes c's opaque for each of its calls not kept with it yet.
func (t *copilotOpaqueTap) keep(c *opaqueChoice) {
	if c.opaque == "" {
		return
	}
	for _, id := range c.ids {
		if c.kept[id] == c.opaque {
			continue
		}
		keepOpaque(t.user, t.model, id, c.opaque)
		c.kept[id] = c.opaque
	}
}

package sessions

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
)

// DeepSeek Harness (dsh) keeps a session in a folder of its own, under
// $DSH_HOME/sessions/ (~/.dsh), in a folder per working directory: its
// events a line each, in session.jsonl, zstd frames appended a batch at a
// time (session.jsonl.zstd) unless compression is off. A newer format is
// session.v<N>.jsonl[.zstd]; the file of an older one stays behind when
// the session is carried over, so only the latest is read. The first line
// is the header — the session's id, cwd and time made, and for a
// subagent's its parent's id (parentSession, delegationDepth 1 and more) —
// then an event per line: {type, seq, time, data}. A "user/message" is a
// prompt typed when its source's kind is "user" (else instructions, a
// plugin's, a subagent's report…), a "session/title" names the session,
// an "assistant/message" carries its model and its usage — input
// without the cache, output with the reasoning — and a "tool/call" is a
// tool called, {name, arguments}; a skill is a call of the skill tool,
// or, typed as /<skill>, a "user/message" whose source is
// {kind: "skill-invocation", name: <skill>}. A session forked from
// another starts with that one's events: before format 2 the first
// seedLength of them, after it those up to a "session/end-seed" said to be
// inherited. They count where they were first written. dsh has no command
// that picks up a session by its id.

// DshDir is dsh's folder: $DSH_HOME, else ~/.dsh.
func DshDir() string {
	if d := appdir.Getenv("DSH_HOME"); d != "" {
		return expandHome(d)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dsh")
}

// session.jsonl, session.v3.jsonl.zstd, …
var dshName = lazyre.New(`^session(?:\.v(\d+))?\.jsonl(\.zstd)?$`)

// dshHead is a session file's first line.
type dshHead struct {
	Type            string `json:"type"`
	Version         int    `json:"version"`
	ID              string `json:"id"`
	Cwd             string `json:"cwd"`
	CreatedAt       int64  `json:"createdAt"`
	ParentSession   string `json:"parentSession"`
	DelegationDepth int    `json:"delegationDepth"`
	SeedLength      int64  `json:"seedLength"`
	IsSeeded        bool   `json:"isSeeded"`
}

// dshHeads keeps the headers read, by path: a file's never changes.
var (
	dshMu    sync.Mutex
	dshHeads = map[string]dshHead{}
)

func dshFiles() []file {
	if !Reads("dsh") {
		return nil
	}
	dirs, _ := filepath.Glob(filepath.Join(DshDir(), "sessions", "*", "*"))
	var out []file
	parent := map[string]string{}
	seen := map[string]bool{}
	dshMu.Lock()
	defer dshMu.Unlock()
	for _, d := range dirs {
		es, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		// the latest format's file, the one written last of its two kinds
		var f file
		gen := -1
		for _, e := range es {
			m := dshName.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			c := file{agent: "dsh", path: filepath.Join(d, e.Name()), main: true}
			if !stat(&c) || n < gen || n == gen && !c.mod.After(f.mod) {
				continue
			}
			f, gen = c, n
		}
		if gen < 0 {
			continue
		}
		h, ok := dshHeads[f.path]
		if !ok {
			if h, ok = dshReadHead(f.path); !ok {
				continue
			}
			dshHeads[f.path] = h
		}
		seen[f.path] = true
		f.w().sid = h.ID
		if h.DelegationDepth > 0 {
			parent[h.ID] = h.ParentSession
		}
		out = append(out, f)
	}
	for p := range dshHeads {
		if !seen[p] {
			delete(dshHeads, p)
		}
	}
	for i := range out {
		root := out[i].m().sid
		for n := 0; parent[root] != "" && n < 64; n++ {
			root = parent[root]
		}
		out[i].key = "dsh:" + root
		out[i].main = parent[out[i].m().sid] == ""
	}
	return out
}

// dshSessionDir is the folder dsh keeps a session file's session in, all
// of it that session's own: <sessions>/<project>/<the id, escaped>/, with
// the session's generations and its session.lock. "" when the file isn't
// in the folder its id names, so a delete never takes another's.
func dshSessionDir(f file) string {
	if f.agent != "dsh" || f.m().sid == "" {
		return ""
	}
	d := filepath.Dir(f.path)
	if filepath.Base(d) != dshSegment(f.m().sid) || filepath.Dir(filepath.Dir(d)) != filepath.Join(DshDir(), "sessions") {
		return ""
	}
	return d
}

// dshSegment is a session id as dsh names its folder (encodeSegment in
// dsh-session-persistence-jsonl): A–Z, a–z, 0–9, '.', '_' and '-' kept,
// every other UTF-16 unit ~XXXX, and "." and ".." escaped whole.
func dshSegment(id string) string {
	switch id {
	case ".":
		return "~002E"
	case "..":
		return "~002E~002E"
	}
	var b strings.Builder
	for _, u := range utf16.Encode([]rune(id)) {
		if u < 0x80 && (u >= 'A' && u <= 'Z' || u >= 'a' && u <= 'z' || u >= '0' && u <= '9' || u == '.' || u == '_' || u == '-') {
			b.WriteByte(byte(u))
		} else {
			fmt.Fprintf(&b, "~%04X", u)
		}
	}
	return b.String()
}

// dshCacheRecord is the row dsh's session listing caches of a session
// (dsh-session-projection-cache, on dsh-storage-json's per-record layout):
// <dsh>/storages/session_projcache/sessions/<id>.json. "" for an id that
// layout can't hold.
func dshCacheRecord(id string) string {
	if !safeID.MatchString(id) {
		return ""
	}
	return filepath.Join(DshDir(), "storages", "session_projcache", "sessions", id+".json")
}

// dshOpen reads a session file's lines, decompressed.
func dshOpen(path string) (io.ReadCloser, error) { return openLines(path) }

func dshReadHead(path string) (dshHead, bool) {
	var h dshHead
	r, err := dshOpen(path)
	if err != nil {
		return h, false
	}
	defer r.Close()
	b, _ := bufio.NewReaderSize(r, 64<<10).ReadBytes('\n')
	if json.Unmarshal(bytes.TrimSpace(b), &h) != nil || h.Type != "session" || h.ID == "" {
		return h, false
	}
	return h, true
}

// dshResponse is what the vendor's own reply named: the model and provider
// that answered, under a streamed reply's replay state.
type dshResponse struct {
	Model         string `json:"model"`
	Provider      string `json:"provider"`
	ResponseModel string `json:"responseModel"`
	// ResponseID is the id of the reply object the vendor's own response
	// carried, as dsh read it: the same id magpie's gateway wrote on the
	// route that served it (Route.ResponseID)
	ResponseID string `json:"responseId"`
}

// dshReplay is a reply's replay state, where dsh keeps what the vendor's own
// reply named: it is the only place a swapped model shows (the provider
// answered with another).
type dshReplay struct {
	Response dshResponse `json:"response"`
}

// dshSource is the source of an assistant/message: the model and provider dsh
// was told to use, and, under ReplayState, the replay state's record of the
// response.
type dshSource struct {
	Model       string    `json:"model"`
	Provider    string    `json:"provider"`
	ReplayState dshReplay `json:"replayState"`
}

// dshEvent is an event line: packed rows of a streamed reply's chunks carry
// neither type nor time of their own and are passed over.
type dshEvent struct {
	Type string `json:"type"`
	Seq  *int64 `json:"seq"`
	Time int64  `json:"time"`
	Data struct {
		Content json.RawMessage `json:"content"`
		Source  struct {
			Kind string `json:"kind"`
			// a skill-invocation's skill
			Name string `json:"name"`
		} `json:"source"`
		// a tool/call's tool and its arguments, a JSON string
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Title     string `json:"title"`
		Inherited bool   `json:"inherited"`
		// Messages are a title call's (session/title-llm-request)
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Message struct {
			Source dshSource `json:"source"`
		} `json:"message"`
		Usage *struct {
			Input      int `json:"inputTokens"`
			Output     int `json:"outputTokens"`
			CacheRead  int `json:"cacheReadTokens"`
			CacheWrite int `json:"cacheWriteTokens"`
		} `json:"usage"`
	} `json:"data"`
}

// dshTurnKept is how many of a turn's newest user messages a session keeps
// (TurnConvs), as many as the gateway reads off a request (dshLastConvs):
// the one typed and the runtime context dsh sends after it, and a few more
// for a file whose dsh writes no turn/start.
const dshTurnKept = 4

var dshWanted = [][]byte{[]byte(`"user/message"`), []byte(`"session/title"`), []byte(`"session/title-llm-request"`), []byte(`"assistant/message"`), []byte(`"session/end-seed"`), []byte(`"tool/call"`), []byte(`"turn/start"`)}

// dshCarry is what a read of a dsh session file carries on with: where it
// read to and the header it read first.
type dshCarry struct {
	Tail *tail   `json:"tail,omitempty"`
	Head dshHead `json:"head"`
}

// parseDsh reads a session file. dsh only appends to one, a line at a time
// or, packed, a frame of lines at a time, so a file read before is read on
// from where that read left off: a packed one from the frame it left off
// in. A file cut short or replaced is read whole again, and a line or frame
// not yet written whole waits for the next read.
func parseDsh(f file, old *state) *state {
	var s *state
	var c dshCarry
	var off, skip int64
	if old != nil && old.more().Dsh != nil && old.more().Dsh.Tail.holds(f.path) {
		s, c = old.clone(), *old.more().Dsh
		off, skip = c.Tail.Off, c.Tail.Skip
	} else {
		s = &state{}
	}
	s.Size, s.Mod = f.size, f.mod.UnixNano()
	headed := c.Tail != nil
	ok := true
	each := func(b []byte) bool {
		b = bytes.TrimSpace(b)
		if len(b) == 0 || len(b) > maxLine {
			return true
		}
		if !headed {
			headed = true
			if json.Unmarshal(b, &c.Head) != nil || c.Head.Type != "session" {
				ok = false
				return false
			}
			s.ID, s.Cwd = c.Head.ID, c.Head.Cwd
			s.saw(ms(c.Head.CreatedAt), f.main)
			return true
		}
		dshCount(s, &c.Head, b, f.main)
		return true
	}
	var next, nextSkip, end int64
	var err error
	if packed(f.path) {
		next, nextSkip, end, err = scanFrames(f.path, off, skip, each)
	} else {
		next, err = scanAt(f.path, off, nil, func(b []byte, _, _ int64) bool { return each(b) })
		end = next
	}
	if !ok {
		// not a session's file: only its size and time
		return &state{Size: f.size, Mod: f.mod.UnixNano()}
	}
	c.Tail = nil
	if err == nil {
		c.Tail = markTail(f.path, next, nextSkip, end)
	}
	s.own().Dsh = &c
	return s
}

// dshCount counts one event of a session file after its header.
func dshCount(s *state, h *dshHead, b []byte, main bool) {
	var e dshEvent
	if dshWant(b) {
		if json.Unmarshal(b, &e) != nil || e.Type == "" {
			return
		}
	} else if t := dshNum(b, "time"); t > 0 {
		// only its time and place looked at
		e.Time = t
		if n := dshNum(b, "seq"); n >= 0 {
			e.Seq = &n
		}
	} else {
		return
	}
	switch e.Type {
	case "user/message":
		if main && s.Title == "" && s.more().First == "" && e.Data.Source.Kind == "user" {
			s.own().Convs = append(s.more().Convs, dshConvs(e.Data.Content)...)
			if t := ccText(e.Data.Content); strings.HasPrefix(t, "<") {
				s.own().First = untagged(t)
			} else {
				s.Title = title(t)
			}
		}
		if main {
			// the messages of the turn under way, as its file has them
			// before the request goes out: the newest of them are what a
			// request still answering carried (TurnConvs, ownerLast). One
			// spelling is enough, the gateway writes the request's that
			// way (dshLastConvs); a file with no turn/start keeps its
			// newest few
			if c := dshConvs(e.Data.Content); len(c) > 0 {
				s.own().TurnConvs = append(s.more().TurnConvs, c[0])
				if n := len(s.more().TurnConvs); n > dshTurnKept {
					s.own().TurnConvs = slices.Clone(s.more().TurnConvs[n-dshTurnKept:])
				}
			}
		}
	case "turn/start":
		if main {
			s.own().TurnConvs = nil
		}
	case "session/title":
		if t := title(e.Data.Title); t != "" {
			s.own().Named = t
		}
	case "session/title-llm-request":
		if main {
			for _, m := range e.Data.Messages {
				if m.Role == "user" {
					s.own().Convs = append(s.more().Convs, dshConvs(m.Content)...)
					break
				}
			}
		}
	case "session/end-seed":
		if h.Version >= 2 && h.IsSeeded && e.Data.Inherited {
			// the events a fork was seeded with end here: what was
			// counted of them is its source's. What they opened is
			// kept: dsh writes no parentSession for a fork, so the
			// openings its seed carried are all that says the fork
			// came from them (SeedConvs)
			n := &state{ID: s.ID, Cwd: s.Cwd, Title: s.Title, Size: s.Size, Mod: s.Mod,
				StateMore: moreOf(StateMore{First: s.more().First, Named: s.more().Named,
					SeedConvs: append(append([]string(nil), s.more().SeedConvs...), s.more().Convs...)})}
			n.saw(ms(h.CreatedAt), main)
			*s = *n
			return
		}
	}
	if h.Version < 2 && e.Seq != nil && *e.Seq < h.SeedLength {
		return
	}
	if e.Type == "assistant/message" && main {
		if src := e.Data.Message.Source; dshGatewayProvider(src) {
			if id := src.ReplayState.Response.ResponseID; id != "" {
				s.own().ResponseIDs = append(s.more().ResponseIDs, id)
			}
		}
	}
	at := ms(e.Time)
	s.saw(at, main)
	switch {
	case e.Type == "tool/call":
		s.tool(at, e.Data.Name, dshSkill(e.Data.Name, e.Data.Arguments))
	case e.Type == "user/message" && e.Data.Source.Kind == "skill-invocation":
		// a /<skill> typed: dsh puts the skill's text in after the prompt
		s.tool(at, "", e.Data.Source.Name)
	}
	if u := e.Data.Usage; e.Type == "assistant/message" && u != nil && e.Data.Message.Source.Model != "" {
		s.use(dateOf(at), e.Data.Message.Source.Model, Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
	}
}

// dshSkill is the skill a call of dsh's skill tool brings in: its
// arguments are {"name": <skill>}.
func dshSkill(tool, args string) string {
	if tool != "skill" {
		return ""
	}
	var a struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return ""
	}
	return a.Name
}

func dshWant(b []byte) bool {
	for _, w := range dshWanted {
		if bytes.Contains(b, w) {
			return true
		}
	}
	return false
}

// dshNum is the number an event's key (its first of the name) holds,
// without decoding the rest of it; -1 for none.
func dshNum(b []byte, key string) int64 {
	i := bytes.Index(b, []byte(`"`+key+`":`))
	if i < 0 {
		return -1
	}
	rest := b[i+len(key)+3:]
	j := 0
	for j < len(rest) && j < 20 && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	n, err := strconv.ParseInt(string(rest[:j]), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// dshConvs are the conversation ids magpie's gateway gives the requests a
// message starts (gateway's conversationID). dsh sends no session id on
// its chat-completions and messages routes, so the gateway keys each such
// request by the hash of its first user message as sent, and the Context
// page finds the session only through it (Kumo31 on Discord). A message
// of one text part goes out as a plain string, or, as the last user
// message on the messages route, as that part with a cache mark: each
// spelling is hashed. Anything else (an image, several parts) gives none.
// That message is what the user typed, which the gateway hashes alone
// (conversationOpening), without the runtime context dsh sends after it.
func dshConvs(content json.RawMessage) []string {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil || len(parts) != 1 || parts[0].Type != "text" || parts[0].Text == "" {
		return nil
	}
	t := jsString(parts[0].Text)
	out := make([]string, 0, 3)
	for _, m := range []string{
		`{"role":"user","content":` + t + `}`,
		`{"role":"user","content":[{"type":"text","text":` + t + `}]}`,
		`{"role":"user","content":[{"type":"text","text":` + t + `,"cache_control":{"type":"ephemeral"}}]}`,
	} {
		sum := sha256.Sum256([]byte(m))
		out = append(out, "magpie-"+hex.EncodeToString(sum[:12]))
	}
	return out
}

// jsString is a string as JavaScript's JSON.stringify writes it, the way
// dsh's requests carry it: unlike encoding/json it leaves <, >, &, U+2028
// and U+2029 as they are.
func jsString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

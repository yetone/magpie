package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/appdir"
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
// and an "assistant/message" carries its model and its usage — input
// without the cache, output with the reasoning. A session forked from
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
var dshName = regexp.MustCompile(`^session(?:\.v(\d+))?\.jsonl(\.zstd)?$`)

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
		f.sid = h.ID
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
		root := out[i].sid
		for n := 0; parent[root] != "" && n < 64; n++ {
			root = parent[root]
		}
		out[i].key = "dsh:" + root
		out[i].main = parent[out[i].sid] == ""
	}
	return out
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
		} `json:"source"`
		Title     string `json:"title"`
		Inherited bool   `json:"inherited"`
		Message   struct {
			Source struct {
				Model string `json:"model"`
			} `json:"source"`
		} `json:"message"`
		Usage *struct {
			Input      int `json:"inputTokens"`
			Output     int `json:"outputTokens"`
			CacheRead  int `json:"cacheReadTokens"`
			CacheWrite int `json:"cacheWriteTokens"`
		} `json:"usage"`
	} `json:"data"`
}

var dshWanted = [][]byte{[]byte(`"user/message"`), []byte(`"session/title"`), []byte(`"assistant/message"`), []byte(`"session/end-seed"`)}

// parseDsh reads a session file whole: compressed, it can't be read on from
// the middle of a frame.
func parseDsh(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano()}
	r, err := dshOpen(f.path)
	if err != nil {
		return s
	}
	defer r.Close()
	br := bufio.NewReaderSize(r, 1<<20)
	var h dshHead
	var evs []dshEvent
	for first := true; ; first = false {
		b, err := br.ReadBytes('\n')
		if err != nil {
			// a line still being written, or a frame cut short
			if !errors.Is(err, io.EOF) || len(bytes.TrimSpace(b)) == 0 {
				break
			}
		}
		if b = bytes.TrimSpace(b); len(b) > 0 && len(b) <= maxLine {
			if first {
				if json.Unmarshal(b, &h) != nil || h.Type != "session" {
					return s
				}
			} else if dshWant(b) {
				var e dshEvent
				if json.Unmarshal(b, &e) == nil && e.Type != "" {
					evs = append(evs, e)
				}
			} else if t := dshNum(b, "time"); t > 0 {
				// only its time and place looked at
				e := dshEvent{Time: t}
				if n := dshNum(b, "seq"); n >= 0 {
					e.Seq = &n
				}
				evs = append(evs, e)
			}
		}
		if err != nil {
			break
		}
	}
	s.ID, s.Cwd = h.ID, h.Cwd
	// the events a fork was seeded with
	cut := -1
	if h.Version >= 2 && h.IsSeeded {
		for i, e := range evs {
			if e.Type == "session/end-seed" && e.Data.Inherited {
				cut = i
			}
		}
	}
	s.saw(ms(h.CreatedAt), f.main)
	for i, e := range evs {
		inherited := i <= cut || h.Version < 2 && e.Seq != nil && *e.Seq < h.SeedLength
		switch e.Type {
		case "user/message":
			if f.main && s.Title == "" && s.First == "" && e.Data.Source.Kind == "user" {
				if t := ccText(e.Data.Content); strings.HasPrefix(t, "<") {
					s.First = untagged(t)
				} else {
					s.Title = title(t)
				}
			}
		case "session/title":
			if t := title(e.Data.Title); t != "" {
				s.Named = t
			}
		}
		if inherited {
			continue
		}
		at := ms(e.Time)
		s.saw(at, f.main)
		if u := e.Data.Usage; e.Type == "assistant/message" && u != nil && e.Data.Message.Source.Model != "" {
			s.use(dateOf(at), e.Data.Message.Source.Model, Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
		}
	}
	return s
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

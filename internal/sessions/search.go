package sessions

// Searching what was said: the Sessions page's filter finds a session by a
// word of its conversation, not only by its title, folder, id or model
// (YOU on Discord: gateway sessions are named ses_…, and what was said is
// how they are remembered). Each session's own file is read only as far as
// its first match; a file that doesn't hold the words at all, as bytes, is
// left after one pass without decoding a line of it.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Hit is where a session said what was searched for: a few words either
// side of the match, on one line.
type Hit struct {
	Role   string `json:"role"`
	Kind   string `json:"kind"`
	Before string `json:"before"`
	Match  string `json:"match"`
	After  string `json:"after"`
}

const (
	hitBefore = 36  // runes kept before a match
	hitAfter  = 120 // and after it
)

// errFound stops a read at the first match a reader would want.
var errFound = errors.New("found")

// fold is a text lowered rune by rune, so a rune's index in it is its index
// in the text itself.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// NeedleOf is a query as it is matched: lowered, its spaces made one.
func NeedleOf(q string) string {
	return fold(strings.Join(strings.Fields(q), " "))
}

// hitIn finds needle (from NeedleOf) in a part's text, its spaces taken as
// one, and cuts the snippet round it.
func hitIn(p Part, needle string) (Hit, bool) {
	if needle == "" || p.Text == "" {
		return Hit{}, false
	}
	text := []rune(strings.Join(strings.Fields(p.Text), " "))
	low := fold(string(text))
	i := strings.Index(low, needle)
	if i < 0 {
		return Hit{}, false
	}
	at := utf8.RuneCountInString(low[:i])
	n := utf8.RuneCountInString(needle)
	from, to := max(0, at-hitBefore), min(len(text), at+n+hitAfter)
	h := Hit{Role: p.Role, Kind: p.Kind, Before: string(text[from:at]), Match: string(text[at : at+n]), After: string(text[at+n : to])}
	if from > 0 {
		h.Before = "…" + strings.TrimLeft(h.Before, " ")
	}
	if to < len(text) {
		h.After = strings.TrimRight(h.After, " ") + "…"
	}
	return h, true
}

// said says whether a part is something the user or the model said, a
// match there worth more than one in a tool's output or the model's thoughts.
func said(p Part) bool {
	return p.Kind == "text" && (p.Role == "user" || p.Role == "assistant")
}

// searched says whether a part is read at all: not the context an agent
// sends before every request, nor an image.
func searched(p Part) bool {
	return p.Role != "system" && p.Kind != "context" && p.Kind != "image"
}

// matcher keeps the best hit of the parts it is told: the first said, else
// the first anywhere.
type matcher struct {
	needle string
	hit    Hit
	found  bool
}

// add takes a part; false once the hit can't be bettered.
func (m *matcher) add(p Part) bool {
	if !searched(p) {
		return true
	}
	if m.found && !said(p) {
		return true
	}
	h, ok := hitIn(p, m.needle)
	if !ok {
		return true
	}
	m.hit, m.found = h, true
	return !said(p)
}

// rawNeedle is what of the needle an agent's file must hold byte for byte,
// when it can be told: its longest word, of words with nothing JSON
// escapes. (A space in the query may be a line break or a run of them in
// the file, but each word is there as typed.) Otherwise "", and the file is
// read whole.
func rawNeedle(needle string) string {
	for _, r := range needle {
		if r < 0x20 || r == '"' || r == '\\' || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029 || r == utf8.RuneError {
			return ""
		}
		// a letter whose capital lowers to more or fewer bytes, or a
		// writer that escapes what isn't ASCII, could hide it from bytes
		if r >= utf8.RuneSelf && !unicode.Is(unicode.Han, r) && !unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return ""
		}
	}
	long := ""
	for _, w := range strings.Fields(needle) {
		if len(w) > len(long) {
			long = w
		}
	}
	return long
}

// mayHold says whether a file can hold needle at all, read as bytes in
// lowered chunks; a packed file or one not read says yes.
func mayHold(ctx context.Context, path, needle string) bool {
	if needle == "" || packed(path) {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	nb := []byte(needle)
	keep := len(nb) - 1
	buf := make([]byte, scanBuffer(f, 0)+keep)
	have := 0
	for {
		if ctx.Err() != nil {
			return true
		}
		n, err := io.ReadFull(f, buf[have:])
		have += n
		if containsFold(buf[:have], nb) {
			return true
		}
		if err != nil {
			return !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)
		}
		copy(buf, buf[have-keep:have])
		have = keep
	}
}

// containsFold says whether b holds needle, A–Z in b taken as a–z; needle
// is lowered already, and what in it isn't ASCII is caseless (rawNeedle).
// It copies nothing: candidates are found by the first byte in either case.
func containsFold(b, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	first := needle[0]
	upper := first
	if 'a' <= first && first <= 'z' {
		upper = first - 'a' + 'A'
	}
	for len(b) >= len(needle) {
		i := bytes.IndexByte(b[:len(b)-len(needle)+1], first)
		if upper != first {
			if k := bytes.IndexByte(b[:len(b)-len(needle)+1], upper); k >= 0 && (i < 0 || k < i) {
				i = k
			}
		}
		if i < 0 {
			return false
		}
		if equalFoldASCII(b[i:i+len(needle)], needle) {
			return true
		}
		b = b[i+1:]
	}
	return false
}

// equalFoldASCII says whether a, with A–Z lowered, is lower.
func equalFoldASCII(a, lower []byte) bool {
	for i, c := range a {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

// SearchSession finds needle (from NeedleOf) in what a session said, read
// from its agent's own file; ErrNoTranscript for an agent whose
// conversations aren't read.
func SearchSession(ctx context.Context, s Session, needle string) (Hit, bool, error) {
	if !Reads(s.Agent) {
		return Hit{}, false, ErrNotRead
	}
	if !HasTranscript(s.Agent) || s.Path == "" {
		return Hit{}, false, ErrNoTranscript
	}
	raw := rawNeedle(needle)
	if linesFile(s) && !mayHold(ctx, s.Path, raw) {
		return Hit{}, false, nil
	}
	var keep func([]byte) bool
	if raw != "" {
		// only a line holding every word is decoded: in a long session
		// most lines are tool output that doesn't, and a part is a line's
		var words [][]byte
		for _, w := range strings.Fields(needle) {
			words = append(words, []byte(w))
		}
		keep = func(b []byte) bool {
			for _, w := range words {
				if !containsFold(b, w) {
					return false
				}
			}
			return true
		}
	}
	m := matcher{needle: needle}
	err := readTranscriptOf(s, keep, func(_ bool, p Part) bool {
		return ctx.Err() == nil && m.add(p)
	})
	if err == nil {
		err = ctx.Err()
	}
	return m.hit, m.found, err
}

// linesFile says whether s.Path is the file its conversation is written in,
// so that one without the needle's bytes can be passed over unread. An
// OpenCode session's is its info (the messages are elsewhere) or a database,
// and a Cursor chat's a database too, whose newest pages are in its -wal.
// An Antigravity app conversation's is its database, the transcript beside
// it.
func linesFile(s Session) bool {
	switch s.Agent {
	case "opencode", "antigravity":
		return false
	case "cursor":
		return strings.HasSuffix(s.Path, ".jsonl")
	}
	return true
}

// SearchGateway finds needle in what magpie recorded of a gateway session,
// the parts as its transcript shows them.
func SearchGateway(ctx context.Context, agent, id, needle string) (Hit, bool, error) {
	if err := ctx.Err(); err != nil {
		return Hit{}, false, err
	}
	t, err := GatewayTranscript(agent, id)
	if err != nil {
		return Hit{}, false, err
	}
	m := matcher{needle: needle}
	for _, p := range t.Parts {
		if !m.add(p) {
			break
		}
	}
	return m.hit, m.found, nil
}

// ---- a gateway session's title ----------------------------------------------

// gatewayTitles keeps a recorded session's title by the file it was read
// from: its first line is written once and never again.
var gatewayTitles sync.Map // path -> string

const gatewayTitleLines = 8

// GatewayTitle is what a gateway session was first asked, made one short
// line, from the first turns magpie recorded of it; "" when it recorded
// none or nothing the user wrote.
func GatewayTitle(agent, id string) string { return GatewayTitler()(agent, id) }

// GatewayTitler is GatewayTitle for a listing's many sessions: whether
// magpie recorded anything at all is asked once, not once a session. A
// Stat and the folder's name for each of ~5000 gateway sessions on each
// poll were ~80 MB of 90 s of polls (#2278).
func GatewayTitler() func(agent, id string) string {
	root := gatewayDir()
	if _, err := os.Stat(root); err != nil {
		// nothing recorded at all, as for most
		return func(string, string) string { return "" }
	}
	return func(agent, id string) string { return gatewayTitleIn(root, agent, id) }
}

func gatewayTitleIn(root, agent, id string) string {
	if agent == "" || id == "" {
		return ""
	}
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	now := time.Now().UTC()
	for d := 7; d >= 0; d-- {
		dir := filepath.Join(root, now.AddDate(0, 0, -d).Format(time.DateOnly), gatewayIdentity(agent, id))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if e.Type().IsRegular() && (strings.HasSuffix(e.Name(), ".json") || e.Name() == "turns.jsonl") {
				names = append(names, e.Name())
			}
		}
		// the per-turn files of before are older than a day's turns.jsonl
		sort.Slice(names, func(i, j int) bool {
			if (names[i] == "turns.jsonl") != (names[j] == "turns.jsonl") {
				return names[j] == "turns.jsonl"
			}
			return names[i] < names[j]
		})
		for _, name := range names {
			path := filepath.Join(dir, name)
			if t, ok := gatewayTitles.Load(path); ok {
				if t != "" {
					return t.(string)
				}
				continue
			}
			t, whole := gatewayFileTitle(path, agent, id)
			if t != "" || whole {
				gatewayTitles.Store(path, t)
			}
			if t != "" {
				return t
			}
		}
	}
	return ""
}

// gatewayFileTitle reads a recorded file's first turns for the user's first
// words; whole says the answer won't change as the file grows.
func gatewayFileTitle(path, agent, id string) (title string, whole bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false
	}
	end, err := gatewayCompleteSize(f, info.Size())
	if err != nil {
		return "", false
	}
	scan := bufio.NewScanner(io.LimitReader(f, end))
	scan.Buffer(make([]byte, 64<<10), gatewayTurnMax+1)
	lines := 0
	for scan.Scan() {
		lines++
		var turn GatewayTurn
		if json.Unmarshal(scan.Bytes(), &turn) == nil && turn.Agent == agent && turn.Session == id {
			if t := firstAsked(turn.Input); t != "" {
				return t, true
			}
		}
		if lines >= gatewayTitleLines {
			return "", true
		}
	}
	return "", !strings.HasSuffix(path, ".jsonl")
}

// firstAsked is the first thing the user wrote in a request: not a tag an
// agent wraps its context in, nor an image.
func firstAsked(parts []Part) string {
	for _, p := range parts {
		if p.Role != "user" || p.Kind != "text" {
			continue
		}
		s := strings.TrimSpace(p.Text)
		if s == "" || strings.HasPrefix(s, "<") || strings.HasPrefix(s, "# AGENTS.md") || s == "[image]" {
			continue
		}
		if t := untagged(s); t != "" {
			return t
		}
	}
	return ""
}

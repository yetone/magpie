package claudetui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A message is checked whole once Claude Code has it: the words its
// transcript holds of it against those pasted. A console that loses part
// of a paste (Windows 10's own ConPTY drops a long paste's marks and some
// of its blocks, #5205) gives Claude Code the rest, or the whole taken as
// many pastes with tags in the middle of its words, and the model answers
// that with nothing said. One that isn't whole ends the turn as
// Unpastable, before any of its reply, so the bridge asks it again as
// claude -p, which is sent the message itself.
// What Claude Code makes of the message once it has it is its own: a
// path it took as an image, as it says, isn't missing (sameWordsBut).

// wholeWait is how long a message whose words no row holds yet waits for
// another row, in case the one read wasn't its.
const wholeWait = 2 * time.Second

// words reads a message's words one space apart, as Claude Code keeps what
// is pasted whatever it trims, wraps, numbers or takes out: a run of white
// space, a paste tag or an image mark is a space, none at the ends, and a
// format character (a zero-width space) is nothing. It reads in place, so
// two messages are compared without a copy of either.
type words struct {
	s     string
	i     int
	began bool // a letter was read
	space bool // a space comes before the next letter
}

// next is the next rune, ' ' for a space, -1 at the end.
func (w *words) next() rune {
	for w.i < len(w.s) {
		c := w.s[w.i]
		if c == '<' || c == '[' {
			if n := markAt(w.s[w.i:]); n > 0 {
				w.i += n
				w.space = w.began
				continue
			}
		}
		r, size := rune(c), 1
		if c >= utf8.RuneSelf {
			r, size = utf8.DecodeRuneInString(w.s[w.i:])
		}
		switch {
		case 0x3000 < r && r < 0xfeff:
			// CJK and the rest between: no white space, no format
			// character (unicode.IsSpace, unicode.Cf)
			if w.space {
				w.space = false
				return ' '
			}
		case c < utf8.RuneSelf && asciiSpace[c] || c >= utf8.RuneSelf && unicode.IsSpace(r):
			w.i += size
			w.space = w.began
			continue
		case c >= utf8.RuneSelf && unicode.Is(unicode.Cf, r):
			w.i += size
			continue
		case w.space:
			w.space = false
			return ' '
		}
		w.i += size
		w.began = true
		return r
	}
	return -1
}

// asciiSpace is unicode.IsSpace's white space below 0x80.
var asciiSpace = [utf8.RuneSelf]bool{'\t': true, '\n': true, '\v': true, '\f': true, '\r': true, ' ': true}

// markAt is the length of the paste tag (<pasted_content id="…">, or its
// end) or image mark ([Image #3]) s begins with, 0 for none.
func markAt(s string) int {
	if s == "" || s[0] != '<' && s[0] != '[' {
		return 0
	}
	var head, tail string
	var in func(byte) bool
	switch {
	case strings.HasPrefix(s, `<pasted_content id="`):
		head, tail, in = `<pasted_content id="`, `">`, isWord
	case strings.HasPrefix(s, `</pasted_content id="`):
		head, tail, in = `</pasted_content id="`, `">`, isWord
	case strings.HasPrefix(s, "[Image #"):
		head, tail, in = "[Image #", "]", isDigit
	default:
		return 0
	}
	n := len(head)
	for n < len(s) && in(s[n]) {
		n++
	}
	if n == len(head) || !strings.HasPrefix(s[n:], tail) {
		return 0
	}
	return n + len(tail)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
func isWord(c byte) bool  { return isDigit(c) || c == '_' || 'a' <= c|0x20 && c|0x20 <= 'z' }

// sameWords is whether a and b have the same words, read as words reads
// them; it stops at the first that differs.
func sameWords(a, b string) bool {
	x, y := words{s: a}, words{s: b}
	for {
		r := x.next()
		if r != y.next() {
			return false
		}
		if r < 0 {
			return true
		}
	}
}

// sameWordsBut is sameWords with any of taken, each once, left out of a
// where b doesn't hold it: an image file's path Claude Code took as the
// image, as it says in a row after the message's (imageSources). n is
// how many were left out. With none taken it is sameWords, no copy made.
func sameWordsBut(a, b string, taken []string) (same bool, n int) {
	if len(taken) == 0 {
		return sameWords(a, b), 0
	}
	used := make([]bool, len(taken))
	x, y := words{s: a}, words{s: b}
	for {
		if end := takenAt(x, y, taken, used); end > 0 {
			x.i, x.space = end, x.began
			n++
			continue
		}
		r := x.next()
		if r != y.next() {
			return false, n
		}
		if r < 0 {
			return true, n
		}
	}
}

// takenAt is where in x's words one of taken not yet used ends, quoted or
// not, when it is x's next word whole and y doesn't go on with it (Claude
// Code left it there): 0 when none is.
func takenAt(x, y words, taken []string, used []bool) int {
	at := x
	switch r := at.next(); {
	case r == ' ':
		// at.i is the next word's first byte
	case r < 0 || x.began:
		return 0
	default:
		at.i -= utf8.RuneLen(r)
	}
	s := x.s[at.i:]
	for k, p := range taken {
		end := 0
		switch {
		case used[k] || p == "":
			continue
		case strings.HasPrefix(s, p):
			end = len(p)
		case len(s) > len(p)+1 && (s[0] == '"' || s[0] == '\'') && strings.HasPrefix(s[1:], p) && s[1+len(p)] == s[0]:
			end = len(p) + 2
		default:
			continue
		}
		if end < len(s) && !(s[end] < utf8.RuneSelf && asciiSpace[s[end]]) || sameUpTo(x, y, at.i+end) {
			continue
		}
		used[k] = true
		return at.i + end
	}
	return 0
}

// sameUpTo is whether y goes on as x does till x has read up to stop.
func sameUpTo(x, y words, stop int) bool {
	for x.i < stop {
		if r := x.next(); r < 0 || r != y.next() {
			return false
		}
	}
	return true
}

// letters counts s's characters as words reads them, its spaces aside.
func letters(s string) int {
	w, n := words{s: s}, 0
	for r := w.next(); r >= 0; r = w.next() {
		if r != ' ' {
			n++
		}
	}
	return n
}

// messageCut is a message Claude Code recorded otherwise than it was
// pasted: got and sent count its characters, white space aside, and
// gotImages and sentImages its images when its words were whole.
type messageCut struct{ got, sent, gotImages, sentImages int }

func (e *messageCut) Error() string {
	switch {
	case e.gotImages < e.sentImages:
		return fmt.Sprintf("Claude Code recorded %d of the message's %d images%s", e.gotImages, e.sentImages, cutHint())
	case e.got < e.sent:
		return fmt.Sprintf("Claude Code recorded %d of the message's %d characters%s", e.got, e.sent, cutHint())
	}
	return "Claude Code recorded the message otherwise than it was pasted" + cutHint()
}

// heardRow is what the transcript recorded of a message: its words, and
// how many images it holds, -1 where a row doesn't say (a queued
// message's, which is its words alone).
type heardRow struct {
	words  string
	images int
}

// wholeIn is nil when one of the messages the transcript recorded since one
// was sent (said) has the words pasted (sent) and its images, or when it
// recorded none it could read. Claude Code takes a pasted image file's
// path as the image, by rules of its own, and says which it took (taken):
// those are left out of the words, and their images out of the count.
func wholeIn(sent string, images int, said []heardRow, taken []string) error {
	if len(said) == 0 {
		return nil
	}
	got, gotImages, whole, unexplained := 0, images, false, false
	for _, h := range said {
		if extra := h.images - images; extra > 0 && pathsIn(sent, taken) < extra {
			unexplained = true
			continue
		}
		same, n := sameWordsBut(sent, h.words, taken)
		if !same {
			got = max(got, letters(h.words))
			continue
		}
		if h.images < 0 || h.images-n >= images {
			return nil
		}
		whole, gotImages = true, max(0, h.images-n)
	}
	if unexplained {
		return errUnexplained
	}
	cut := &messageCut{got: got, sent: letters(sent), gotImages: gotImages, sentImages: images}
	if whole {
		cut.got = cut.sent
	}
	return cut
}

// errUnexplained is a row with more images than were pasted, for which
// Claude Code hasn't said (yet) that it took as many of the paste's
// paths: one it doesn't say, says late or says otherwise (a path it
// made its own) isn't judged, rather than failed each time it is sent.
var errUnexplained = errors.New("Claude Code took words of the message as images it didn't name")

// pathsIn counts the paths of taken that sent holds.
func pathsIn(sent string, taken []string) int {
	n := 0
	for _, p := range taken {
		if p != "" && strings.Contains(sent, p) {
			n++
		}
	}
	return n
}

// checkWhole is wholeIn for a message just sent, given wholeWait for the
// row that is its, and for what Claude Code says it took as images.
func (s *shim) checkWhole(sent string, images int) error {
	s.hold()
	by := time.After(wholeWait)
	for {
		s.rowsMu.Lock()
		said, taken := slices.Clone(s.said), slices.Clone(s.taken)
		s.rowsMu.Unlock()
		err := wholeIn(sent, images, said, taken)
		if err == nil {
			return s.unhold(nil)
		}
		select {
		case <-s.rowsCh:
			continue
		case <-by:
		case <-s.exited:
			if !errors.Is(err, errUnexplained) {
				err = fmt.Errorf("%w; Claude Code ended (%v): %s", err, s.exit, s.lastScreen())
			}
		}
		if errors.Is(err, errUnexplained) {
			err = nil
		}
		return s.unhold(err)
	}
}

// heard keeps what a message that reached the transcript holds while one
// sent is checked (said isn't nil from send to checkWhole's end); a row of
// one comes before its sawRow. row is read only then.
func (s *shim) heard(row func() heardRow) {
	s.rowsMu.Lock()
	checking := s.said != nil
	s.rowsMu.Unlock()
	if !checking {
		return
	}
	h := row()
	if h.words == "" && h.images <= 0 {
		return
	}
	s.rowsMu.Lock()
	if s.said != nil {
		s.said = append(s.said, h)
	}
	s.rowsMu.Unlock()
}

// heardTaken keeps the image files' paths Claude Code says, in a row after
// a message's, it took as the images, while one sent is checked, and has
// it checked again.
func (s *shim) heardTaken(raw json.RawMessage) {
	s.rowsMu.Lock()
	checking := s.said != nil
	s.rowsMu.Unlock()
	if !checking {
		return
	}
	paths := imageSources(raw)
	if len(paths) == 0 {
		return
	}
	s.rowsMu.Lock()
	if s.said != nil {
		s.taken = append(s.taken, paths...)
	}
	s.rowsMu.Unlock()
	select {
	case s.rowsCh <- struct{}{}:
	default:
	}
}

// heardAll ends what heard and heardTaken keep, as send returns.
func (s *shim) heardAll() {
	s.rowsMu.Lock()
	s.said, s.taken = nil, nil
	s.rowsMu.Unlock()
}

// heardTaken tells an emitter that checks messages whole (shim) of a meta
// row, which may say what Claude Code took as images.
func (t *translator) heardTaken(raw json.RawMessage) {
	if h, ok := t.out.(interface{ heardTaken(json.RawMessage) }); ok {
		h.heardTaken(raw)
	}
}

// imageSource begins a text block of the meta row Claude Code records
// after a message for each image file's path it took as the image:
// "[Image: source: C:\a\x.png]" (2.1.319).
const imageSource = "[Image: source: "

// imageSources is the paths a meta row says were taken as images.
func imageSources(raw json.RawMessage) []string {
	if !bytes.Contains(raw, []byte(imageSource)) {
		return nil
	}
	var m struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &m)
	var paths []string
	for _, b := range m.Content {
		if p, ok := strings.CutPrefix(b.Text, imageSource); ok && b.Type == "text" && strings.HasSuffix(p, "]") {
			paths = append(paths, p[:len(p)-1])
		}
	}
	return paths
}

// heard tells an emitter that checks messages whole (shim) what one that
// reached the transcript holds.
func (t *translator) heard(row func() heardRow) {
	if h, ok := t.out.(interface{ heard(func() heardRow) }); ok {
		h.heard(row)
	}
}

// promptRow is what a user row's message holds: its text, or its text
// blocks one after another, and its image blocks.
func promptRow(raw json.RawMessage) heardRow {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return heardRow{}
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return heardRow{words: s}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(m.Content, &blocks)
	var h heardRow
	var b strings.Builder
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			b.WriteString(bl.Text)
			b.WriteString("\n")
		case "image":
			h.images++
		}
	}
	h.words = b.String()
	return h
}

// tell writes v past what is held (hold): a turn's failure.
func (s *shim) tell(v any) {
	b, _ := json.Marshal(v)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = s.out.Write(append(b, '\n'))
}

// hold keeps what Claude Code answers while its message is checked: it
// asks Anthropic as soon as it records the message (25ms after), and a
// quick model answered words cut, its result let out, within the time
// waited for the message's other rows (wholeWait): the check's error came
// after the turn had ended (Haiku, 64K on Windows 10's console).
//
// What sawRow began holding is kept: a reply read before the check began
// stays in it. What a failed check dropped (io.Discard) is held anew.
func (s *shim) hold() {
	s.outMu.Lock()
	if _, ok := s.held.(*bytes.Buffer); !ok {
		s.held = new(bytes.Buffer)
	}
	s.outMu.Unlock()
}

// letHeld lets out what is held for a message that was never checked:
// send failed after sawRow began holding (a check's own end, unhold, has
// already ended the hold either way).
func (s *shim) letHeld() {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if b, ok := s.held.(*bytes.Buffer); ok {
		_, _ = s.out.Write(b.Bytes())
		s.held = nil
	}
}

// unhold lets out what was held when the message was whole (err nil),
// and drops it, and all after it, when it wasn't: the turn's failure is
// told past it (tell).
func (s *shim) unhold(err error) error {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if err != nil {
		s.held = io.Discard
		return err
	}
	if b, ok := s.held.(*bytes.Buffer); ok {
		_, _ = s.out.Write(b.Bytes())
	}
	s.held = nil
	return nil
}

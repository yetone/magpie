package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A model can fall into a loop it never leaves: DeepSeek's reasoning went
// on as "Now." "Writing." "Go." "Let me write." "OK." "Producing." … over
// and over, with a word or two changing between rounds, until the reply's
// token limit, and dsh and Pi waited on it the whole time (#1359,
// Moody-Sin). loopGuard reads the reply as the agent is sent it, in any of
// the protocols, and says when its reasoning or its text has become such a
// loop, for the gateway to end the reply with an error the agent can act
// on (holdWriter.cutLoop) rather than let it run on.
//
// The reply is cut into units, its lines and sentences. A loop is a long
// run of units that are nearly all the same few: of the last loopWindow
// units of reasoning, or of text, no more than loopDistinct different
// ones. Ordinary writing doesn't come near it, even when lines repeat:
// code, a table, a list or a log differs from line to line in its names,
// numbers and cells, and a unit with no word in it (a brace, a rule, a
// row of 0x00 or of numbers) isn't counted at all. The cycle need not be exact,
// as the reporter's wasn't ("Here." "Produce." one round, "Final." or
// "Producing." the next).
type loopGuard struct {
	pending []byte // what came of an event not yet whole
	think   unitRun
	text    unitRun
	// spoke: the reply has said something the agent acts on, a text or a
	// call, which a reply asked again would say twice
	spoke bool
}

const (
	// loopWindow is how many units of a channel are looked at: 256 of the
	// reporter's lines are about 600 tokens, a moment of a reply that runs
	// to its limit of tens of thousands.
	loopWindow = 256
	// loopDistinct is the most different units the window may hold and
	// still be a loop: the reporter's has 9.
	loopDistinct = 24
	// unitMost is how long a unit grows without a break before it is
	// taken whole, and unitKept how much of it is told apart.
	unitMost = 4096
	unitKept = 256
	// pendingMost bounds an event that never ends, which isn't read.
	pendingMost = 1 << 20
)

// unitRun is one channel's units: its reasoning's, or its text's.
type unitRun struct {
	cur  strings.Builder // the unit being written
	stop bool            // cur ends in a sentence's stop: a space after it ends the unit
	// lettered: cur has a letter in it, for a stop after it to end a
	// sentence
	lettered bool
	ring     [loopWindow]uint64
	n        int // units seen
	counts   map[uint64]*unitCount
	// runOn is how many units in a row were cut at unitMost, with no break
	// in them, as the same few words over and over (runOnWords)
	runOn int
}

type unitCount struct {
	n    int
	text string // as it was first written, for the error to quote
	at   int    // when it first came, to quote them in order
}

// loopTrip is what loopGuard found: which channel looped, and on what.
type loopTrip struct {
	reasoning bool
	units     []string // the different units, the most frequent first
	window    int
	// runOn: the loop had no break in it, and units are its words and
	// window how many characters it ran to
	runOn bool
}

// message is what the agent and the logs are told.
func (t loopTrip) message() string {
	what := "text"
	if t.reasoning {
		what = "reasoning"
	}
	quoted := make([]string, 0, 6)
	for i, u := range t.units {
		if i == 6 {
			quoted = append(quoted, "…")
			break
		}
		quoted = append(quoted, fmt.Sprintf("%q", u))
	}
	if t.runOn {
		return fmt.Sprintf(loopGuardSaid+": the model's %s was stuck in a loop, its last %d characters the same over and over without a break (%s), and would have run on to its output limit. Ask again, or set Settings › Models › Replies › Stop looping replies off",
			what, t.window, strings.Join(quoted, " "))
	}
	return fmt.Sprintf(loopGuardSaid+": the model's %s was stuck in a loop, its last %d lines and sentences all among the same %d (%s), and would have run on to its output limit. Ask again, or set Settings › Models › Replies › Stop looping replies off",
		what, t.window, len(t.units), strings.Join(quoted, " "))
}

// loopGuardSaid begins what the loop guard tells the agent of a reply it
// ended: magpie's words, not a vendor's (magpieSaid).
const loopGuardSaid = "magpie ended this reply"

// feed reads b, the next of the reply as the agent is sent it.
func (g *loopGuard) feed(b []byte) (loopTrip, bool) {
	g.pending = append(g.pending, b...)
	for {
		end := eventEnd(g.pending)
		if end < 0 {
			break
		}
		ev := g.pending[:end]
		think, text := eventWords(ev)
		g.pending = g.pending[end:]
		if think != "" {
			if t, ok := g.think.add(think); ok {
				t.reasoning = true
				return t, true
			}
		}
		if !g.spoke {
			content, said, reasoning, _ := timingKind(ev)
			g.spoke = said || content && !reasoning
		}
		if text != "" {
			if t, ok := g.text.add(text); ok {
				return t, true
			}
		}
	}
	if len(g.pending) == 0 {
		g.pending = nil
	} else if len(g.pending) > pendingMost {
		g.pending = nil
	}
	return loopTrip{}, false
}

// eventWords is what one event of a reply says, in any protocol: its
// reasoning and its text. Calls, starts, stops and usage say nothing here.
func eventWords(ev []byte) (think, text string) {
	if !bytes.Contains(ev, []byte(`delta`)) && !bytes.Contains(ev, []byte(`candidates`)) {
		return "", "" // most events that aren't content, without reading them
	}
	var data []byte
	for _, ln := range bytes.Split(ev, []byte("\n")) {
		ln = bytes.TrimRight(ln, "\r")
		if bytes.HasPrefix(ln, []byte("data:")) {
			data = append(data, bytes.TrimSpace(ln[5:])...)
		}
	}
	if len(data) == 0 || data[0] != '{' {
		return "", ""
	}
	var v struct {
		Type    string          `json:"type"`
		Delta   json.RawMessage `json:"delta"`
		Choices []struct {
			Delta struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
				Reasoning        json.RawMessage `json:"reasoning"`
			} `json:"delta"`
		} `json:"choices"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(data, &v) != nil {
		return "", ""
	}
	str := func(raw json.RawMessage) string {
		var s string
		if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	switch v.Type {
	case "content_block_delta": // Anthropic
		var d struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		if json.Unmarshal(v.Delta, &d) == nil {
			switch d.Type {
			case "text_delta":
				return "", d.Text
			case "thinking_delta":
				return d.Thinking, ""
			}
		}
		return "", ""
	case "response.output_text.delta": // Responses
		return "", str(v.Delta)
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		return str(v.Delta), ""
	case "":
	default:
		return "", ""
	}
	var th, tx strings.Builder
	for _, c := range v.Choices { // Chat Completions
		th.WriteString(cmpStr(str(c.Delta.ReasoningContent), str(c.Delta.Reasoning)))
		tx.WriteString(str(c.Delta.Content))
	}
	for _, c := range v.Candidates { // Gemini
		for _, p := range c.Content.Parts {
			if p.Thought {
				th.WriteString(p.Text)
			} else {
				tx.WriteString(p.Text)
			}
		}
	}
	return th.String(), tx.String()
}

// cmpStr is the first of a and b that isn't empty: a Chat delta carries
// its reasoning in one field or the other, as the vendor names it.
func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// add reads s, the next of the channel.
func (r *unitRun) add(s string) (loopTrip, bool) {
	for _, c := range s {
		switch {
		case c == '\n':
			if t, ok := r.end(); ok {
				return t, true
			}
			continue
		case r.stop && unicode.IsSpace(c):
			if t, ok := r.end(); ok {
				return t, true
			}
			continue
		}
		r.cur.WriteRune(c)
		switch c {
		case '.', '!', '?':
			// a sentence's stop, not a list's number (1.) or a decimal
			r.stop = r.lettered
		case '。', '！', '？':
			if t, ok := r.end(); ok {
				return t, true
			}
			continue
		default:
			r.stop = false
			r.lettered = r.lettered || unicode.IsLetter(c)
		}
		if r.cur.Len() >= unitMost {
			// a unit this long has had no break: a loop with none in it
			// ("mcp mcp mcp …", #1489) is never 256 of them before the
			// reply's limit, so it is read on its own
			if words, ok := runOnWords(r.cur.String()); ok {
				if r.runOn++; r.runOn >= runOnUnits {
					return loopTrip{units: words, window: r.runOn * unitMost, runOn: true}, true
				}
			} else {
				r.runOn = 0
			}
			if t, ok := r.end(); ok {
				return t, true
			}
			continue
		}
	}
	return loopTrip{}, false
}

// end takes the unit written so far, and says whether the window has
// become a loop with it.
func (r *unitRun) end() (loopTrip, bool) {
	raw := r.cur.String()
	if r.cur.Len() < unitMost {
		// a break; a long unit before it that still runs on keeps the count
		// (a loop broken into lines longer than unitMost)
		if len(raw) < unitMost/8 {
			r.runOn = 0
		} else if _, ok := runOnWords(raw); !ok {
			r.runOn = 0
		}
	}
	r.cur.Reset()
	r.stop, r.lettered = false, false
	key, ok := unitKey(raw)
	if !ok {
		return loopTrip{}, false
	}
	h := fnv.New64a()
	h.Write([]byte(key))
	sum := h.Sum64()
	if r.counts == nil {
		r.counts = map[uint64]*unitCount{}
	}
	slot := r.n % loopWindow
	if r.n >= loopWindow {
		old := r.ring[slot]
		if c := r.counts[old]; c != nil {
			if c.n--; c.n == 0 {
				delete(r.counts, old)
			}
		}
	}
	r.ring[slot] = sum
	c := r.counts[sum]
	if c == nil {
		text := strings.TrimSpace(raw)
		if utf8.RuneCountInString(text) > 40 {
			text = string([]rune(text)[:40]) + "…"
		}
		c = &unitCount{text: text, at: r.n}
		r.counts[sum] = c
	}
	c.n++
	r.n++
	if r.n < loopWindow || len(r.counts) > loopDistinct {
		return loopTrip{}, false
	}
	all := make([]*unitCount, 0, len(r.counts))
	for _, c := range r.counts {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].at < all[j].at
	})
	t := loopTrip{window: loopWindow}
	for _, c := range all {
		t.units = append(t.units, c.text)
	}
	return t, true
}

// runOnUnits is how many units cut at unitMost in a row, each the same
// few words over and over, are a loop: some 2,000 tokens of it.
const runOnUnits = 2

// runOnWords says whether s, written with no break, is the same few words
// over and over ("mcp mcp mcp …", #1489, or "mcpmcpmcp…"), and which. A
// long line of prose, code, JSON, base64 or a table row has many different
// words (its names, numbers and cells) or none.
func runOnWords(s string) ([]string, bool) {
	if !hasWord(s) {
		return nil, false
	}
	// one piece repeated, a word or a few with no space between
	for p := 1; p <= 64 && 4*p <= len(s); p++ {
		i := p
		for i < len(s) && s[i] == s[i-p] {
			i++
		}
		if i == len(s) {
			start := 0
			for start < p && !utf8.RuneStart(s[start]) {
				start++
			}
			// a few rounds of it, which a word cut anywhere is whole in
			end := min(start+3*p, len(s))
			for end < len(s) && !utf8.RuneStart(s[end]) {
				end++
			}
			return []string{strings.TrimSpace(s[start:end])}, true
		}
	}
	counts := map[string]int{}
	var order []string
	words := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_'
	}) {
		words++
		if counts[w]++; counts[w] == 1 {
			order = append(order, w)
		}
		if len(counts) > runOnDistinct {
			return nil, false
		}
	}
	if words*32 < len(s) {
		return nil, false // a few long words: not read as a loop
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	return order, true
}

// runOnDistinct is the most different words a run-on loop has.
const runOnDistinct = 8

// unitKey is what tells a unit apart from others, and whether it counts:
// its words, without case or its closing stop. A unit with no word in it
// — a brace, a rule, box drawing, a row of numbers or hex — is no
// sentence, and is left out.
func unitKey(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimRightFunc(s, func(c rune) bool {
		return strings.ContainsRune(".!?。！？,，;；:：…", c) || unicode.IsSpace(c)
	})
	if !hasWord(s) {
		return "", false
	}
	s = strings.ToLower(s)
	if len(s) > unitKept {
		s = s[:unitKept]
	}
	return s, true
}

// hasWord says whether s has a word in it: a run of letters with no digit
// in it (Now, Writing, 好), not 0x00, v2 or a hash.
func hasWord(s string) bool {
	letters, digits := false, false
	for _, c := range s + " " {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' {
			letters = letters || unicode.IsLetter(c)
			digits = digits || unicode.IsDigit(c)
			continue
		}
		if letters && !digits {
			return true
		}
		letters, digits = false, false
	}
	return false
}

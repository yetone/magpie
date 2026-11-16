package gateway

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// A client may send its conversation back with the images and files of
// earlier messages taken out (#1382): Hana's pre-step hook replaces each
// image before the last reply with a line of text, in the tool result or
// user message that held it, so pi-ai's Chat Completions body no longer
// has the user message of the tool results' images ("Attached image(s)
// from tool result:" and the images) and each result's text has the line
// added to its end. The run that had the conversation was given the
// images, and its Claude Code still holds them: told the conversation
// anew, it would write all of it to the prompt cache again, at each step
// after one that read an image. lostMedia tells this rewrite from any
// other by its shape, not the client's words for it.
//
// A client may also send the images back in a later request, and take
// them out again in the next (Yqihotaru on X): its history keeps them,
// and only some of its requests take them out. mediaMoved tells either
// way apart from any other change, in any turn of the conversation.

// wordPart is a part of a message as hashMessages reads it: its kind and
// its words, and for a tool result how many images it carried beside them.
type wordPart struct {
	kind  Kind
	words string
	media int
}

// messageParts is each message's parts as hashMessages reads them, those
// with no words left out. A call is told by its place among the
// conversation's calls, and a result by the call it answers.
func messageParts(msgs []Message) [][]wordPart {
	out := make([][]wordPart, len(msgs))
	n, calls := 0, map[string]int{} // a call's id → its place
	for i, m := range msgs {
		for _, p := range m.Parts {
			var s string
			media := 0
			switch p.Kind {
			case Text:
				s = p.Text
			case ToolCall:
				n++
				calls[p.ID] = n
				s = fmt.Sprintf("\x01call %s #%d", p.Name, n)
			case ToolResult:
				k := "?"
				if n, ok := calls[p.CallID]; ok {
					k = fmt.Sprint(n)
				}
				s = fmt.Sprintf("\x01result #%s %s", k, p.Text)
				media = len(p.Images)
			case File:
				s = fmt.Sprintf("\x01file %s %d %s", p.MediaType, len(p.Data), p.URL)
			case Image:
				s = fmt.Sprintf("\x01image %d %s", len(p.Data), p.URL)
			default:
				continue
			}
			if words := strings.Join(strings.Fields(s), " "); words != "" {
				out[i] = append(out[i], wordPart{p.Kind, words, media})
			}
		}
	}
	return out
}

// heardPart is a wordPart as a run keeps it: the hash and length of its
// words. words is set only on the conversation compared with what a run
// kept, which is a request's own. tail is a tool result's last words, as
// a run keeps them too: a request that sent the images back has a
// result's words without the text its client had added in their place,
// and the kept words are those with it (grown).
type heardPart struct {
	kind  Kind
	n     int
	sum   [sha256.Size]byte
	media int
	words string
	tail  string
}

// heardTail is how much of a tool result's words a run keeps: room for
// the lines a client adds in place of a result's images.
const heardTail = 2048

// heardRun is a role's messages in a row, as hashMessages runs them
// together: how a reply is split into messages is left out.
type heardRun struct {
	role  string
	parts []heardPart
}

// heardRuns is msgs as lostMedia compares them; sealed keeps no words.
//
// A reply's texts in a row are one part, their words with no whitespace,
// as messageWords reads them: the run keeps its reply as the one text it
// said, and the client as Anthropic's stream told it, a text block for
// each run of words between thinking blocks (#5705). Compared block by
// block, the next turn whose client had also taken out an earlier image
// (omp keeps 5 in a request to a provider it has no budget for) was none
// the run had, and went to a new Claude Code told all of it from a
// transcript (Yqihotaru on X). A user's texts stay apart: the text an
// image left between them is what lostFromRun reads.
func heardRuns(msgs []Message, sealed bool) []heardRun {
	type run struct {
		role  string
		parts []wordPart
	}
	var runs []run
	for i, parts := range messageParts(msgs) {
		if len(parts) == 0 {
			continue
		}
		if len(runs) == 0 || runs[len(runs)-1].role != msgs[i].Role {
			runs = append(runs, run{role: msgs[i].Role})
		}
		r := &runs[len(runs)-1]
		for _, p := range parts {
			if r.role == "assistant" && p.kind == Text {
				p.words = strings.Join(strings.Fields(p.words), "")
				if k := len(r.parts) - 1; k >= 0 && r.parts[k].kind == Text {
					r.parts[k].words += p.words
					continue
				}
			}
			r.parts = append(r.parts, p)
		}
	}
	out := make([]heardRun, len(runs))
	for i, r := range runs {
		out[i].role = r.role
		for _, p := range r.parts {
			h := heardPart{kind: p.kind, n: len(p.words), sum: sha256.Sum256([]byte(p.words)), media: p.media}
			if !sealed {
				h.words = p.words
			} else if p.kind == ToolResult {
				h.tail = p.words[max(0, len(p.words)-heardTail):]
			}
			out[i].parts = append(out[i].parts, h)
		}
	}
	return out
}

func isMedia(k Kind) bool { return k == Image || k == File }

// lostMedia says now, a request's conversation (heardRuns unsealed), is
// was, the one a run had (sealed), with images or files taken out, and
// that alone, as far as was goes: now may go on after it. Each image or
// file of was is in now, or gone, or text is in its place; a tool result
// may have text added to its end when it carried images or the user
// message of images after it went; that message, a text and images right
// after the results, may go whole when a result before it had text added.
// Nothing else changed, and something was taken out. A conversation told
// to another run, compacted or edited, is never this.
func lostMedia(was, now []heardRun) bool {
	if len(now) < len(was) {
		return false
	}
	lost := 0
	for i, w := range was {
		if now[i].role != w.role {
			return false
		}
		n, ok := lostFromRun(w.parts, now[i].parts)
		if !ok {
			return false
		}
		lost += n
	}
	return lost > 0
}

// mediaMoved says now, a request's conversation (heardRuns unsealed), is
// kept, the one a run had (sealed), with images or files taken out of it
// or put back into it, as lostMedia tells them, a role's run of messages
// at a time, and nothing else changed: now may go on after it. A client
// that keeps its history's images and takes them out of some requests
// (Yqihotaru on X) sends back what the run had been told, then what it
// was told the turn before.
func mediaMoved(kept, now []heardRun) bool {
	if len(now) < len(kept) {
		return false
	}
	moved := 0
	for i, k := range kept {
		if now[i].role != k.role {
			return false
		}
		if n, ok := lostFromRun(k.parts, now[i].parts); ok {
			moved += n
		} else if n, ok := lostFromRun(now[i].parts, k.parts); ok {
			moved += n // put back
		} else {
			return false
		}
	}
	return moved > 0
}

// lostFromRun is lostMedia for a role's run of messages: how many images
// or files were taken out of was to make now, and whether that is all
// that changed.
func lostFromRun(was, now []heardPart) (int, bool) {
	lost, j := 0, 0
	grew := false // a result of the results just before had text added
	for i, w := range was {
		switch {
		case j < len(now) && same(w, now[j]):
			if w.kind != ToolResult {
				grew = false
			}
			j++
		case isMedia(w.kind):
			// gone, or the text in its place: up to the part of was's
			// that is no image or file next
			lost++
			next := i + 1
			for next < len(was) && isMedia(was[next].kind) {
				next++
			}
			for j < len(now) && now[j].kind == Text && (next == len(was) || !same(was[next], now[j])) {
				j++
			}
		case w.kind == ToolResult && j < len(now) && now[j].kind == ToolResult && w.media > now[j].media && grown(w, now[j]):
			// its images, text in their place
			lost += w.media - now[j].media
			grew = true
			j++
		case w.kind == ToolResult && j < len(now) && now[j].kind == ToolResult && w.media == now[j].media && mediaAfter(was, i) && grown(w, now[j]):
			// the images sent after the results, text in their place
			grew = true
			j++
		case w.kind == Text && grew && i > 0 && was[i-1].kind == ToolResult && i+1 < len(was) && isMedia(was[i+1].kind):
			// the line before the results' images, gone with them
		default:
			return 0, false
		}
	}
	return lost, j == len(now)
}

// mediaAfter says the results was[i] is among are followed by images or
// files: the user message a Chat Completions client sends a tool's images
// in, a line of text before them.
func mediaAfter(was []heardPart, i int) bool {
	k := i + 1
	for k < len(was) && was[k].kind == ToolResult {
		k++
	}
	if k < len(was) && was[k].kind == Text {
		k++
	}
	return k < len(was) && isMedia(was[k].kind)
}

// same says now's part is w.
func same(w, now heardPart) bool {
	return w.kind == now.kind && w.n == now.n && w.sum == now.sum
}

// grown says now's words are w's with text added to their end: read from
// now's words when it has them, else, for a part a run kept (sealed),
// from w's words and the end of now's (tail).
func grown(w, now heardPart) bool {
	if now.n <= w.n {
		return false
	}
	if now.words != "" {
		return w.sum == sha256.Sum256([]byte(now.words[:w.n]))
	}
	d := now.n - w.n
	if len(w.words) != w.n || d > len(now.tail) {
		return false
	}
	return now.sum == sha256.Sum256([]byte(w.words+now.tail[len(now.tail)-d:]))
}

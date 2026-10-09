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
// kept, which is a request's own.
type heardPart struct {
	kind  Kind
	n     int
	sum   [sha256.Size]byte
	media int
	words string
}

// heardRun is a role's messages in a row, as hashMessages runs them
// together: how a reply is split into messages is left out.
type heardRun struct {
	role  string
	parts []heardPart
}

// heardRuns is msgs as lostMedia compares them; sealed keeps no words.
func heardRuns(msgs []Message, sealed bool) []heardRun {
	var out []heardRun
	for i, parts := range messageParts(msgs) {
		if len(parts) == 0 {
			continue
		}
		if len(out) == 0 || out[len(out)-1].role != msgs[i].Role {
			out = append(out, heardRun{role: msgs[i].Role})
		}
		r := &out[len(out)-1]
		for _, p := range parts {
			h := heardPart{kind: p.kind, n: len(p.words), sum: sha256.Sum256([]byte(p.words)), media: p.media}
			if !sealed {
				h.words = p.words
			}
			r.parts = append(r.parts, h)
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
		case w.kind == ToolResult && j < len(now) && now[j].kind == ToolResult && w.media > now[j].media && grown(w, now[j].words):
			// its images, text in their place
			lost += w.media - now[j].media
			grew = true
			j++
		case w.kind == ToolResult && j < len(now) && now[j].kind == ToolResult && w.media == now[j].media && mediaAfter(was, i) && grown(w, now[j].words):
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
	return w.kind == now.kind && w.n == len(now.words) && w.sum == sha256.Sum256([]byte(now.words))
}

// grown says words are w's with text added to their end.
func grown(w heardPart, words string) bool {
	return len(words) > w.n && w.sum == sha256.Sum256([]byte(words[:w.n]))
}

package sessions

import (
	"fmt"
	"strings"
	"unicode"
)

// A search over every agent's sessions, and a session read, as text an
// agent reads: the sessions MCP server's search_sessions and
// read_session (cryingLiu on X) and `magpie sessions search` / `show`
// (yansheng1003 on X) both write it, each saying how to ask it for more
// in its own words (TextHow).

// TextHow is how the reader of a text asks for more, in the words of
// the tool or command that wrote it.
type TextHow struct {
	// More: how to ask for more hits ("ask for a higher limit")
	More string
	// Read: how to read a hit's session, a sentence
	Read string
	// From: how to read s on from message n
	From func(s Session, n int) string
	// Whole: how to read s from message n with each message whole, said
	// under a message cut to its first partMax characters
	Whole func(s Session, n int) string
}

const (
	// TextReadChars is how much of a session SessionText gives when not
	// told, and TextReadMost the most it gives
	TextReadChars = 20_000
	TextReadMost  = 100_000
	// textToolChars is what is shown of a tool's call or answer
	textToolChars = 400
	// textLead is how many messages before the found one a read with
	// find starts
	textLead = 3
)

// SessionRef is how a session is named in these texts: agent:id.
func SessionRef(s Session) string { return s.Agent + ":" + s.ID }

// SaidText is what a search found, as text: each session (agent:id,
// title, folder, when) and the words round where it said needle.
func SaidText(found SaidFound, needle string, how TextHow) string {
	var b strings.Builder
	still := found.Listing || found.Indexed < found.Total
	if len(found.Hits) == 0 && still {
		fmt.Fprintf(&b, "No session searched so far says %q.", needle)
	} else if len(found.Hits) == 0 {
		fmt.Fprintf(&b, "No session says %q.", needle)
	} else if len(found.Hits) == 1 {
		fmt.Fprintf(&b, "1 session says %q:\n", needle)
	} else {
		fmt.Fprintf(&b, "%d sessions say %q, the newest first:\n", len(found.Hits), needle)
	}
	for i, h := range found.Hits {
		ss := h.Session
		fmt.Fprintf(&b, "\n%d. %s", i+1, SessionRef(ss))
		if t := cutLine(ss.Title, 120); t != "" {
			fmt.Fprintf(&b, " · %s", t)
		}
		b.WriteString("\n   ")
		if ss.Cwd != "" {
			b.WriteString(ss.Cwd + " · ")
		}
		b.WriteString(ss.Last.Local().Format("2006-01-02 15:04"))
		fmt.Fprintf(&b, "\n   %s: %s[%s]%s\n", h.Hit.Role, strings.TrimLeft(snipTail(flat(h.Hit.Before), 200), " "), flat(h.Hit.Match), strings.TrimRight(snipHead(flat(h.Hit.After), 300), " "))
	}
	if found.Cut {
		fmt.Fprintf(&b, "\nThere may be more: %s, or more words.", how.More)
	}
	if found.Listing {
		b.WriteString("\nStill indexing: the sessions to search are still being found; ask again in a minute.")
	} else if found.Total > 0 && found.Indexed < found.Total {
		fmt.Fprintf(&b, "\nStill indexing: %d of %d sessions searched so far; ask again in a minute for the rest.", found.Indexed, found.Total)
	}
	if len(found.Hits) > 0 && how.Read != "" {
		b.WriteString("\n" + how.Read)
	}
	return b.String()
}

// ReadAsk is what part of a session SessionText (and ReadTranscript)
// gives: from message From on, or, with Find, from a few messages before
// where those words were said; MaxChars of it at most; each message
// whole with Whole, else cut to its first partMax characters.
type ReadAsk struct {
	Find     string
	From     int
	MaxChars int
	Whole    bool
}

// readAsked is the transcript ask reads, and the message its text starts
// at: From when asked, else a few before the first that says Find.
func readAsked(s Session, ask ReadAsk) (Transcript, int, error) {
	from := max(ask.From, 0)
	if needle := NeedleOf(ask.Find); needle != "" && from == 0 {
		tr, err := transcriptAt(s, needle, ask.Whole)
		if err != nil {
			return tr, 0, err
		}
		return tr, max(tr.From, textNear(tr, needle)-textLead), nil
	}
	// read from where it is asked: a "more" past a long session's first
	// transcriptMax characters read the same cut start again
	tr, err := transcriptAfter(s, from, ask.Whole)
	return tr, from, err
}

// textChars is the most characters a read gives, asked for most.
func textChars(most int) int {
	if most <= 0 {
		most = TextReadChars
	}
	return min(max(most, 1000), TextReadMost)
}

// ReadTranscript is the part of s's conversation ask asks for, as
// SessionText reads it, for a reader that takes the parts themselves
// (magpie sessions show --json): from message From on, else, with Find,
// as TranscriptAt reads it. With MaxChars it holds the parts up to that
// many characters of text (at least one), starting where SessionText's
// text would, and is Cut when more follow; Next says where they start.
// Without it, it holds what one read does, transcriptMax.
func ReadTranscript(s Session, ask ReadAsk) (Transcript, error) {
	tr, from, err := readAsked(s, ask)
	if err != nil || ask.MaxChars <= 0 {
		return tr, err
	}
	if skip := from - tr.From; skip > 0 && skip <= len(tr.Parts) {
		tr.Parts, tr.From = tr.Parts[skip:], from
	}
	most, size := textChars(ask.MaxChars), 0
	for i, p := range tr.Parts {
		if size += len(p.Text); size > most && i > 0 {
			tr.Parts, tr.Cut = tr.Parts[:i], true
			break
		}
	}
	return tr, nil
}

// SessionText is s's conversation as text: the prompts, the replies and
// the tools called, shortened, each message numbered.
func SessionText(s Session, ask ReadAsk, how TextHow) (string, error) {
	tr, from, err := readAsked(s, ask)
	if err != nil {
		return "", err
	}
	most := textChars(ask.MaxChars)

	var b strings.Builder
	b.WriteString(SessionRef(s))
	if t := cutLine(s.Title, 200); t != "" {
		b.WriteString(" · " + t)
	}
	b.WriteString("\n")
	if s.Cwd != "" {
		b.WriteString(s.Cwd + " · ")
	}
	fmt.Fprintf(&b, "%s to %s\n", s.Start.Local().Format("2006-01-02 15:04"), s.Last.Local().Format("2006-01-02 15:04"))
	if from > tr.From {
		fmt.Fprintf(&b, "(messages before %d left out)\n", from)
	} else if tr.From > 0 {
		fmt.Fprintf(&b, "(messages before %d left out)\n", tr.From)
	}
	end := tr.From
	for i, p := range tr.Parts {
		n := tr.From + i
		if n < from {
			continue
		}
		line := partText(n, p, ask.Whole)
		if p.Cut > 0 && !ask.Whole && how.Whole != nil && line != "" && (p.Kind == "text" || p.Kind == "context") {
			line += fmt.Sprintf("(cut: %d more characters of this message; %s)\n", p.Cut, how.Whole(s, n))
		}
		if line == "" {
			end = n + 1
			continue
		}
		if b.Len()+len(line) > most && end > from {
			fmt.Fprintf(&b, "\n(more: %s)", how.From(s, n))
			return b.String(), nil
		}
		b.WriteString(line)
		end = n + 1
	}
	if tr.Cut {
		fmt.Fprintf(&b, "\n(the rest is longer than is read at once: %s)", how.From(s, end))
	}
	return b.String(), nil
}

// textNear is the number of the first part that says needle, or 0.
func textNear(tr Transcript, needle string) int {
	for i, p := range tr.Parts {
		if (p.Kind == "text" || p.Kind == "context") && strings.Contains(NeedleOf(p.Text), needle) {
			return tr.From + i
		}
	}
	return 0
}

// partText is one part as SessionText shows it: a tool's call or answer
// on one line, shortened, unless whole.
func partText(n int, p Part, whole bool) string {
	text := strings.TrimSpace(p.Text)
	if whole && text != "" {
		switch p.Kind {
		case "tool_use":
			return fmt.Sprintf("\n[%d] %s called %s:\n%s\n", n, p.Role, p.Name, text)
		case "tool_result":
			return fmt.Sprintf("[%d] tool answered:\n%s\n", n, text)
		}
	}
	switch p.Kind {
	case "text", "context":
		if text == "" {
			return ""
		}
		return fmt.Sprintf("\n[%d] %s:\n%s\n", n, p.Role, text)
	case "thinking":
		return ""
	case "tool_use":
		return fmt.Sprintf("\n[%d] %s called %s: %s\n", n, p.Role, p.Name, cutLine(text, textToolChars))
	case "tool_result":
		return fmt.Sprintf("[%d] tool answered: %s\n", n, cutLine(text, textToolChars))
	case "image":
		return fmt.Sprintf("\n[%d] %s: (an image)\n", n, p.Role)
	}
	return ""
}

// cutLine is text on one line, cut to most runes.
func cutLine(text string, most int) string {
	return snipHead(strings.Join(strings.Fields(text), " "), most)
}

// flat is text on one line, each run of white space one space, its ends
// kept: the words round a match stay apart from it.
func flat(text string) string {
	var b strings.Builder
	space := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

// snipHead is text's first most runes; snipTail its last.
func snipHead(text string, most int) string {
	if r := []rune(text); len(r) > most {
		return string(r[:most]) + "…"
	}
	return text
}

func snipTail(text string, most int) string {
	if r := []rune(text); len(r) > most {
		return "…" + string(r[len(r)-most:])
	}
	return text
}

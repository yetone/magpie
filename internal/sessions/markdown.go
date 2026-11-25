package sessions

// A session's whole conversation as a Markdown file, to keep or to read
// elsewhere (#1276): what the Sessions page's Show conversation shows, read
// again from the agent's own file without the page's cuts.

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"strings"
	"time"
)

// Kept is what of a conversation a Markdown file keeps.
type Kept int

const (
	// KeepAll keeps every part: thinking, tools and context too.
	KeepAll Kept = iota
	// KeepSaid keeps only what the user and the agent said to each other
	// (usrname1833 on Discord: an export to read the conversation, not
	// its work): their words and images, no thinking, tool call or
	// result, and nothing the agent put in itself.
	KeepSaid
)

// keeps says whether a file that keeps k has p in it.
func (k Kept) keeps(p Part) bool {
	if k != KeepSaid {
		return true
	}
	return (p.Role == "user" || p.Role == "assistant") && (p.Kind == "" || p.Kind == "text" || p.Kind == "image")
}

// WriteMarkdown writes s's conversation, read whole from its file, as
// Markdown: a heading with what the session is, then each turn under who
// said it. Thinking, a tool's result and what the agent put in itself are
// folded (<details>); a tool's call is its name and arguments. name is the
// agent as the reader knows it. The file is only read.
func WriteMarkdown(w io.Writer, s Session, name string) error {
	return WriteMarkdownKept(w, s, name, KeepAll)
}

// WriteMarkdownKept is WriteMarkdown keeping only what keep keeps.
func WriteMarkdownKept(w io.Writer, s Session, name string, keep Kept) error {
	if !HasTranscript(s.Agent) || s.Path == "" {
		return ErrNoTranscript
	}
	return writeMarkdown(w, s, name, "File", s.Path, false, keep, func(each func(Part) bool) error {
		return readTranscript(s, func(_ bool, p Part) bool { return each(p) })
	})
}

// WriteGatewayMarkdown writes a session magpie's gateway recorded (t, from
// GatewayTranscript) as WriteMarkdown writes one read from the agent's own
// file: for a session whose agent ran on another machine, as one that
// reaches this magpie through a remote magpie does (akic404 on Discord),
// there is no file here, only what the gateway kept. It says so, and says
// when older turns or the end of a long part weren't kept.
func WriteGatewayMarkdown(w io.Writer, s Session, name string, t Transcript) error {
	return WriteGatewayMarkdownKept(w, s, name, t, KeepAll)
}

// WriteGatewayMarkdownKept is WriteGatewayMarkdown keeping only what keep
// keeps.
func WriteGatewayMarkdownKept(w io.Writer, s Session, name string, t Transcript, keep Kept) error {
	return writeMarkdown(w, s, name, "Recorded by", "magpie's gateway", t.Cut, keep, func(each func(Part) bool) error {
		for _, p := range t.Parts {
			if !each(p) {
				break
			}
		}
		return nil
	})
}

func writeMarkdown(w io.Writer, s Session, name, fromKey, from string, cut bool, keep Kept, parts func(func(Part) bool) error) error {
	b := bufio.NewWriter(w)
	title := strings.TrimSpace(s.Title)
	if title == "" {
		title = "(no prompt)"
	}
	fmt.Fprintf(b, "# %s\n\n", oneLine(title))
	meta := func(k, v string) {
		if v != "" {
			fmt.Fprintf(b, "- **%s:** %s\n", k, v)
		}
	}
	meta("Agent", name)
	meta("Session ID", "`"+s.ID+"`")
	meta("Folder", s.Cwd)
	meta("Started", when(s.Start))
	meta("Last active", when(s.Last))
	var models []string
	for _, m := range s.Models {
		models = append(models, m.Model)
	}
	meta("Models", strings.Join(models, ", "))
	meta(fromKey, from)
	if cut {
		b.WriteString("\n*Not all of this session was kept: what follows is what is left of it.*\n")
	}
	if keep == KeepSaid {
		b.WriteString("\n*Only what the user and the agent said: thinking and tool calls are left out.*\n")
	}
	b.WriteString("\n---\n")
	who := ""
	said := false
	err := parts(func(p Part) bool {
		p.Text = strings.TrimSpace(p.Text)
		if p.Text == "" || !keep.keeps(p) {
			return true
		}
		said = true
		// a tool's result is told under the turn that called it
		if r := p.Role; r != "tool" && r != who {
			who = r
			head := "User"
			if r == "assistant" {
				head = "Assistant"
			}
			fmt.Fprintf(b, "\n## %s\n", head)
		}
		b.WriteString("\n")
		switch p.Kind {
		case "thinking":
			folded(b, "Thinking", p.Text, false)
		case "tool_use":
			lang := ""
			if strings.HasPrefix(p.Text, "{") || strings.HasPrefix(p.Text, "[") {
				lang = "json"
			}
			fmt.Fprintf(b, "**Tool call: %s**\n\n", oneLine(cmp.Or(p.Name, "tool")))
			fenced(b, lang, p.Text)
		case "tool_result":
			label := "Tool result"
			if p.Name != "" {
				label += ": " + oneLine(p.Name)
			}
			folded(b, label, p.Text, true)
		case "context":
			folded(b, "Context", p.Text, true)
		case "image":
			b.WriteString("*[image]*\n")
		default:
			b.WriteString(p.Text + "\n")
		}
		if p.Cut > 0 {
			fmt.Fprintf(b, "\n*… %d more characters weren't kept.*\n", p.Cut)
		}
		return true
	})
	if err != nil {
		return err
	}
	if !said {
		b.WriteString("\n*Nothing was said in this session.*\n")
	}
	return b.Flush()
}

// folded is a <details> block: its words as they are, or, code, in a fence
// (a tool's output and the agent's own notes hold tags and Markdown of their
// own, which would otherwise be drawn as this file's).
func folded(b *bufio.Writer, summary, text string, code bool) {
	fmt.Fprintf(b, "<details>\n<summary>%s</summary>\n\n", htmlEscape(summary))
	if code {
		fenced(b, "", text)
	} else {
		b.WriteString(text + "\n")
	}
	b.WriteString("\n</details>\n")
}

// fenced is text in a code fence longer than any run of backticks in it.
func fenced(b *bufio.Writer, lang, text string) {
	n, run := 3, 0
	for _, r := range text {
		if r == '`' {
			run++
			n = max(n, run+1)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", n)
	fmt.Fprintf(b, "%s%s\n%s\n%s\n", f, lang, text, f)
}

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05 -07:00")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/yetone/magpie/internal/sessions"
)

// magpie's sessions (cryingLiu on X) are an MCP server an agent the user
// picks in Library → MCP can mount, so it can find what was said in any
// agent's session on this computer and read it: search_sessions and
// read_session, Streamable HTTP at SessionsMCPPath, served as
// SearchMCPPath is (serveMCP), to the agents on this computer alone, and
// given as `magpie mcp sessions`. It reads only the agents Settings ›
// Sessions lets magpie read (sessions.Reads), as the Sessions page does.

// SessionsMCPPath is where the sessions MCP server is served.
const SessionsMCPPath = "/mcp/magpie/sessions"

const (
	sessionsServerName = "magpie-sessions"
	sessionsSearchTool = "search_sessions"
	sessionsReadTool   = "read_session"
)

const (
	// sessionsReadChars is how much of a session read_session gives when
	// not told, and sessionsReadMost the most it gives
	sessionsReadChars = 20_000
	sessionsReadMost  = 100_000
	// sessionsToolChars is what is shown of a tool's call or answer
	sessionsToolChars = 400
)

func (s *Server) sessionsMCP(w http.ResponseWriter, r *http.Request) {
	// what was said in this computer's agents' sessions: only an agent on
	// this computer reads it. Another computer's gateway key and a web
	// page, even one Settings lists, are turned away.
	if !local(r) || fromBrowser(r) {
		http.Error(w, "magpie's sessions MCP server answers the agents on this computer alone", http.StatusForbidden)
		return
	}
	serveMCP(w, r, "magpie's sessions", sessionsServerName, 1<<20, s.sessionsMCPCall)
}

func sessionsMCPTools() []any {
	return []any{
		map[string]any{
			"name":  sessionsSearchTool,
			"title": "Search sessions",
			"description": "Searches what was said (the user's prompts and the agents' replies) in the sessions of every coding agent on this computer: Claude Code, Codex and the others magpie reads. " +
				"Use it to find an earlier conversation, a decision or a fix from another session or another agent. Words match by their start; the newest sessions come first. Read one with " + sessionsReadTool + ".",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "The words to find, as they were said"},
					"agent": map[string]any{"type": "string", "description": "Only this agent's sessions, by magpie's id for it (claude, codex, gemini, opencode, …)"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
				},
				"required": []string{"query"},
			},
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		},
		map[string]any{
			"name":  sessionsReadTool,
			"title": "Read session",
			"description": "Reads a session's conversation as text: the prompts, the replies, and the tools called, shortened. " +
				"With find, it starts a few messages before where those words were said; from goes on where the last read ended.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session":   map[string]any{"type": "string", "description": "The session, as " + sessionsSearchTool + " names it (agent:id)"},
					"find":      map[string]any{"type": "string", "description": "Start a few messages before where these words were said"},
					"from":      map[string]any{"type": "integer", "minimum": 0, "description": "Start at this message, as a read before says"},
					"max_chars": map[string]any{"type": "integer", "minimum": 1000, "maximum": sessionsReadMost, "default": sessionsReadChars},
				},
				"required": []string{"session"},
			},
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		},
	}
}

type sessionsArgs struct {
	Query    string    `json:"query"`
	Agent    string    `json:"agent"`
	Limit    flexCount `json:"limit"`
	Session  string    `json:"session"`
	Find     string    `json:"find"`
	From     flexCount `json:"from"`
	MaxChars flexCount `json:"max_chars"`
}

func (s *Server) sessionsMCPCall(r *http.Request, m mcpMessage) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpVersion(m),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": sessionsServerName, "title": "Magpie Sessions", "version": Version},
			"instructions":    "What was said in every coding agent's sessions on this computer. Search it for an earlier conversation, decision or fix, in this agent or another, and read the session it is in.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": sessionsMCPTools()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		var a sessionsArgs
		if len(p.Arguments) > 0 {
			if err := json.Unmarshal(p.Arguments, &a); err != nil {
				return toolResult("the arguments don't read: "+err.Error(), true), nil
			}
		}
		var said string
		var err error
		switch p.Name {
		case sessionsSearchTool:
			said, err = sessionsSearch(r, a)
		case sessionsReadTool:
			said, err = sessionsRead(a)
		default:
			return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name + ": this server has " + sessionsSearchTool + " and " + sessionsReadTool}
		}
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(said, false), nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + m.Method}
}

// searchSaid is sessions.SearchSaid; a variable for tests.
var searchSaid = sessions.SearchSaid

func sessionsSearch(r *http.Request, a sessionsArgs) (string, error) {
	needle := sessions.NeedleOf(a.Query)
	if needle == "" {
		return "", errors.New("query is empty: say the words to find")
	}
	limit := min(max(int(a.Limit), 0), 50)
	if limit == 0 {
		limit = 10
	}
	found, err := searchSaid(r.Context(), needle, strings.TrimSpace(a.Agent), limit)
	if errors.Is(err, sessions.ErrNoWords) {
		return "", errors.New("the query has no words to find (letters or digits)")
	}
	if err != nil {
		return "", err
	}
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
		fmt.Fprintf(&b, "\n%d. %s:%s", i+1, ss.Agent, ss.ID)
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
		b.WriteString("\nThere may be more: ask for a higher limit, or more words.")
	}
	if found.Listing {
		b.WriteString("\nStill indexing: the sessions to search are still being found; ask again in a minute.")
	} else if found.Total > 0 && found.Indexed < found.Total {
		fmt.Fprintf(&b, "\nStill indexing: %d of %d sessions searched so far; ask again in a minute for the rest.", found.Indexed, found.Total)
	}
	if len(found.Hits) > 0 {
		b.WriteString("\nRead one with " + sessionsReadTool + "(session, find).")
	}
	return b.String(), nil
}

func sessionsRead(a sessionsArgs) (string, error) {
	agent, id, ok := strings.Cut(strings.TrimSpace(a.Session), ":")
	if !ok || agent == "" || id == "" {
		return "", errors.New("session is agent:id, as " + sessionsSearchTool + " names it")
	}
	if !sessions.Reads(agent) {
		return "", sessions.ErrNotRead
	}
	s, found := sessions.Find(agent, id)
	if !found {
		return "", fmt.Errorf("no session %s", a.Session)
	}
	var tr sessions.Transcript
	var err error
	from := max(int(a.From), 0)
	if needle := sessions.NeedleOf(a.Find); needle != "" && from == 0 {
		tr, err = sessions.TranscriptAt(s, needle)
		if err == nil {
			from = max(tr.From, sessionsNear(tr, needle)-sessionsLead)
		}
	} else {
		tr, err = sessions.TranscriptOf(s)
	}
	if err != nil {
		return "", err
	}
	most := int(a.MaxChars)
	if most <= 0 {
		most = sessionsReadChars
	}
	most = min(max(most, 1000), sessionsReadMost)

	var b strings.Builder
	fmt.Fprintf(&b, "%s:%s", s.Agent, s.ID)
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
		line := sessionsPart(n, p)
		if line == "" {
			end = n + 1
			continue
		}
		if b.Len()+len(line) > most && end > from {
			fmt.Fprintf(&b, "\n(more: %s with from=%d)", sessionsReadTool, n)
			return b.String(), nil
		}
		b.WriteString(line)
		end = n + 1
	}
	if tr.Cut {
		fmt.Fprintf(&b, "\n(the rest is longer than is read at once: %s with from=%d)", sessionsReadTool, end)
	}
	return b.String(), nil
}

// sessionsLead is how many messages before the found one a read with
// find starts.
const sessionsLead = 3

// sessionsNear is the number of the first part that says needle, or 0.
func sessionsNear(tr sessions.Transcript, needle string) int {
	for i, p := range tr.Parts {
		if (p.Kind == "text" || p.Kind == "context") && strings.Contains(sessions.NeedleOf(p.Text), needle) {
			return tr.From + i
		}
	}
	return 0
}

// sessionsPart is one part as read_session shows it.
func sessionsPart(n int, p sessions.Part) string {
	text := strings.TrimSpace(p.Text)
	switch p.Kind {
	case "text", "context":
		if text == "" {
			return ""
		}
		return fmt.Sprintf("\n[%d] %s:\n%s\n", n, p.Role, text)
	case "thinking":
		return ""
	case "tool_use":
		return fmt.Sprintf("\n[%d] %s called %s: %s\n", n, p.Role, p.Name, cutLine(text, sessionsToolChars))
	case "tool_result":
		return fmt.Sprintf("[%d] tool answered: %s\n", n, cutLine(text, sessionsToolChars))
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

package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
					"max_chars": map[string]any{"type": "integer", "minimum": 1000, "maximum": sessions.TextReadMost, "default": sessions.TextReadChars},
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

// sessionsHow is how the MCP server's texts say to ask for more.
var sessionsHow = sessions.TextHow{
	More: "ask for a higher limit",
	Read: "Read one with " + sessionsReadTool + "(session, find).",
	From: func(_ sessions.Session, n int) string { return fmt.Sprintf("%s with from=%d", sessionsReadTool, n) },
}

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
	return sessions.SaidText(found, needle, sessionsHow), nil
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
	return sessions.SessionText(s, sessions.ReadAsk{Find: a.Find, From: int(a.From), MaxChars: int(a.MaxChars)}, sessionsHow)
}

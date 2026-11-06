package sessions

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Claude Code writes one line per event: the prompts as "user" lines, each
// block of a reply as an "assistant" line carrying the whole message's
// usage (so a message's later blocks repeat it), and titles of its own.

type ccLine struct {
	Type        string `json:"type"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Cwd         string `json:"cwd"`
	SessionID   string `json:"sessionId"`
	UUID        string `json:"uuid"`
	RequestID   ccStr  `json:"requestId"`
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	Summary     string `json:"summary"`
	Entrypoint  string `json:"entrypoint"`
	Message     struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input      int              `json:"input_tokens"`
			Output     int              `json:"output_tokens"`
			CacheRead  int              `json:"cache_read_input_tokens"`
			CacheWrite int              `json:"cache_creation_input_tokens"`
			Creation   *ccCacheCreation `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
	// a compaction's summary, the first message of what follows it
	IsCompactSummary bool `json:"isCompactSummary"`
}

var (
	ccAssistant = []byte(`"type":"assistant"`)
	ccUser      = []byte(`"type":"user"`)
	ccTitle     = []byte(`"type":"ai-title"`)
	ccCustom    = []byte(`"type":"custom-title"`)
	ccSummary   = []byte(`"type":"summary"`)
	ccCwd       = []byte(`"cwd":"`)
	ccEntry     = []byte(`"entrypoint":"`)
)

var (
	ccMessage = []byte(`"message":{`)
	ccAttach  = []byte(`"attachment":{`)
	ccRole    = []byte(`"role":"`)
	ccModel   = []byte(`"model":"`)
	ccID      = []byte(`"id":"`)
	ccUsage   = []byte(`"usage":{`)
	ccSession = []byte(`"sessionId":"`)
	ccTop     = []byte(`{"type":"`)
)

// claudeLine reads a line, telling from its first bytes what it is: most of
// a long session is tool results and attachments, read for their time
// alone, and a reply's usage is decoded without its content.
func claudeLine(s *state, b []byte, main bool) {
	h := b[:min(len(b), 1024)]
	if bytes.HasPrefix(h, ccTop) {
		// a line of Claude Code's own: a title, a summary, a mode…
		switch typeAfter(h, ccTop) {
		case "user", "assistant":
			// a message laid out type first
		case "ai-title", "custom-title", "summary":
			claudeFull(s, b, main)
			return
		case "relocated":
			ccRelocated(s, b, main)
			return
		default:
			s.saw(tsAt(b, true), main)
			return
		}
	}
	m, a := bytes.Index(h, ccMessage), bytes.Index(h, ccAttach)
	if a >= 0 && (m < 0 || a < m) {
		s.saw(tsAt(b, true), main)
		ccWhere(s, b)
		return
	}
	if m < 0 {
		claudeFull(s, b, main)
		return
	}
	// Claude Code's own order: a reply opens with its model and id, a
	// prompt with its role; a message written another way is read whole
	msg := h[m+len(ccMessage):]
	switch {
	case bytes.HasPrefix(msg, ccModel) && typeAfter(msg, ccRole) == "assistant":
		at := tsAt(b, true)
		s.saw(at, main)
		ccWhere(s, b)
		ccLink(s, h, b, main)
		ccReply(s, at, msg, b, main)
	case bytes.HasPrefix(msg, ccRole) && typeAfter(msg, ccRole) == "user":
		if main && !ccToolResult(msg) {
			// a prompt, or something Claude Code put in: read whole
			claudeFull(s, b, main)
			return
		}
		s.saw(tsAt(b, true), main)
		ccWhere(s, b)
		ccLink(s, h, b, main)
	default:
		claudeFull(s, b, main)
	}
}

var ccSidechain = []byte(`"isSidechain":true`)

// ccLink notes a message of the file's own conversation by its uuid, the
// first as Root and the last as Leaf. Claude Code copies a conversation
// into a new file under the same uuids when it forks one (--fork-session,
// /branch) or moves it to a background session, so a file whose Leaf
// another file holds is a copy of that one's conversation (ccCopies). The
// uuid is the line's own: a tool's input or result can hold one too.
func ccLink(s *state, h, b []byte, main bool) {
	if !main || bytes.Contains(h, ccSidechain) {
		return
	}
	if u := gjson.GetBytes(b, "uuid"); u.Type == gjson.String {
		s.link(u.String())
	}
}

// link notes uuid as the file's latest message. Only a uuid as Claude
// Code makes them, random, tells a copy: a file whose writer numbers its
// messages, or names them "u" as tests do, shares them with every other.
func (s *state) link(uuid string) {
	if !ccRandomUUID(uuid) {
		return
	}
	if s.Root == "" {
		s.Root = uuid
	}
	s.Leaf = uuid
}

// ccRandomUUID reports whether u is a version 4 uuid, as Claude Code's
// message uuids are.
func ccRandomUUID(u string) bool {
	if len(u) != 36 || u[14] != '4' {
		return false
	}
	for i := 0; i < 36; i++ {
		c := u[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// ccWhere takes the session's folder and id from the end of a line, while
// they are not known, and the entrypoint from the line the folder was
// found in: each line names both, and a file of an older Claude Code that
// names no entrypoint isn't searched for one on every line.
func ccWhere(s *state, b []byte) {
	if s.Cwd == "" {
		s.Cwd = strAt(b, ccCwd)
		if s.Cwd != "" {
			s.Entry = strAt(b, ccEntry)
		}
	}
	if s.ID == "" {
		s.ID = strAt(b, ccSession)
	}
}

var ccRelocatedCwd = []byte(`"relocatedCwd":"`)

// ccRelocated reads the line Claude Code's /cd adds when it moves a session
// to another folder: it moves the file into that folder's project and
// writes {"type":"relocated","relocatedCwd":…}, while the lines before keep
// the old cwd. The session is then the new folder's, the last move's.
func ccRelocated(s *state, b []byte, main bool) {
	s.saw(tsAt(b, true), main)
	if !main {
		return
	}
	if c := strAt(b, ccRelocatedCwd); c != "" {
		s.Cwd = c
	}
}

// ccStale reports whether a Claude Code session's kept parse was made by a
// magpie that didn't read /cd's relocated lines, and the folder it names is
// not the project the file is in: the session was moved with /cd, and its
// parse names the folder it left. Read again once, the parse is current.
func ccStale(f file, s *state) bool {
	if f.agent != "claude" || !f.main || s == nil || s.CdRead || s.Cwd == "" {
		return false
	}
	return !ccInProject(filepath.Base(filepath.Dir(f.path)), s.Cwd)
}

// ccInProject reports whether dir is the name Claude Code gives cwd's
// project folder: cwd with each UTF-16 unit not a letter or digit as "-",
// past 200 of them cut and a hash added.
func ccInProject(dir, cwd string) bool {
	n := claudeSlug(cwd)
	if len(n) <= 200 {
		return dir == n
	}
	return strings.HasPrefix(dir, n[:200]+"-")
}

// strAt is the string after the last key in b.
func strAt(b, key []byte) string {
	i := lastIndex(b, key)
	if i < 0 {
		return ""
	}
	rest := b[i+len(key)-1:]
	for j := 1; j < len(rest); j++ {
		switch rest[j] {
		case '\\':
			j++
		case '"':
			var v string
			if json.Unmarshal(rest[:j+1], &v) != nil {
				return ""
			}
			return v
		}
	}
	return ""
}

var (
	ccContent = []byte(`"content":`)
	ccResult1 = []byte(`[{"tool_use_id"`)
	ccResult2 = []byte(`[{"type":"tool_result"`)
	ccToolUse = []byte(`{"type":"tool_use"`)
)

// ccToolResult tells a user message that is a tool's result from its head.
func ccToolResult(msg []byte) bool {
	i := bytes.Index(msg, ccContent)
	if i < 0 {
		return false
	}
	c := msg[i+len(ccContent):]
	return bytes.HasPrefix(c, ccResult1) || bytes.HasPrefix(c, ccResult2)
}

// ccTools finds Claude Code's tool blocks (type first) and decodes their
// identity without retaining their arguments. Advancing over the whole block
// also prevents a nested tool-shaped object in its input from being counted.
func ccTools(s *state, m *claudeContribution, at time.Time, b []byte) {
	for rest := b; ; {
		i := bytes.Index(rest, ccToolUse)
		if i < 0 {
			return
		}
		var tool struct {
			ID, Name string
			Input    struct{ Skill any }
		}
		end := objectEnd(rest[i:])
		if end < 0 || json.Unmarshal(rest[i:i+end], &tool) != nil {
			return
		}
		rest = rest[i+end:]
		skill, _ := tool.Input.Skill.(string)
		if tool.Name != "Skill" {
			skill = ""
		}
		ccTool(s, m, at, tool.ID, tool.Name, skill)
	}
}

// objectEnd is the length of the JSON object b starts with, or -1 when
// it doesn't close. It only matches brackets outside strings; the object
// is checked when it is decoded. A json.Decoder over the rest of the line
// copied all of it, for each tool block and each usage: the most a cold
// start allocated (1.6 GB of 11.6 GB in its first 2 minutes).
func objectEnd(b []byte) int {
	if len(b) == 0 || b[0] != '{' {
		return -1
	}
	depth, in := 0, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case in:
			if c == '\\' {
				i++
			} else if c == '"' {
				in = false
			}
		case c == '"':
			in = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// ccReply counts a reply's usage: its model and id lead the message, its
// usage follows the content.
func ccReply(s *state, at time.Time, msg, b []byte, main bool) {
	model, id := typeAfter(msg, ccModel), typeAfter(msg, ccID)
	// Decode just the outer identity: a tool's input may itself contain a
	// requestId, which must never become the identity of this response.
	identity := gjson.GetBytes(b, "requestId")
	request := ""
	if identity.Type == gjson.String {
		request = identity.String()
	}
	m := ccUsageState(&s.Claude).message(id, request)
	defer s.Claude.finish(m)

	i := lastIndex(b, ccUsage)
	if i < 0 {
		i = len(b)
	}
	ccTools(s, m, at, b[:i])
	if i == len(b) || model == "<synthetic>" {
		return
	}
	var u ccUsageFields
	usage := b[i+len(ccUsage)-1:]
	if end := objectEnd(usage); end < 0 || ccDecodeUsage(usage[:end], &u) != nil {
		return
	}
	ccCount(s, m, at, model, ccTokens(u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Creation), main)
}

// claudeFull reads a line whole.
func claudeFull(s *state, b []byte, main bool) {
	at := tsAt(b, true)
	s.saw(at, main)
	want := bytes.Contains(b, ccAssistant) ||
		main && bytes.Contains(b, ccUser) ||
		main && (bytes.Contains(b, ccTitle) || bytes.Contains(b, ccCustom) || bytes.Contains(b, ccSummary)) ||
		s.Cwd == "" && bytes.Contains(b, ccCwd)
	if !want {
		return
	}
	// its content, a prompt's pasted text and images, is read where the
	// line holds it, not copied for each line (#2278)
	var l ccLine
	content, err := ccDecodeLess(b, &l)
	if err != nil {
		return
	}
	if content != nil {
		l.Message.Content = content
	}
	if s.Cwd == "" && l.Cwd != "" {
		s.Cwd, s.Entry = l.Cwd, l.Entrypoint
	}
	if s.ID == "" && l.SessionID != "" {
		s.ID = l.SessionID
	}
	if main && !l.IsSidechain && (l.Type == "user" || l.Type == "assistant") {
		s.link(l.UUID)
		if l.IsCompactSummary && l.Type == "user" && ccRandomUUID(l.UUID) && !slices.Contains(s.Compacts, l.UUID) {
			s.Compacts = append(s.Compacts, l.UUID)
		}
	}
	switch l.Type {
	case "ai-title":
		if l.AITitle != "" {
			s.Named = title(l.AITitle)
		}
	case "custom-title":
		// named by the user (/rename): before the one Claude Code made
		if main && l.CustomTitle != "" {
			s.Custom = title(l.CustomTitle)
		}
	case "summary":
		if l.Summary != "" && s.Named == "" {
			s.Named = title(l.Summary)
		}
	case "user":
		if main && !l.IsMeta && !l.IsSidechain {
			p := ccPrompt(l.Message.Content)
			if p != "" {
				s.day(dateOf(at)).Prompts++
			}
			if s.Title == "" {
				s.Title = p
				if s.Title == "" && s.First == "" {
					s.First = untagged(ccText(l.Message.Content))
				}
			}
		}
	case "assistant":
		m := ccUsageState(&s.Claude).message(l.Message.ID, string(l.RequestID))
		defer s.Claude.finish(m)
		var blocks []struct {
			Type, ID, Name string
			Input          struct{ Skill any }
		}
		if json.Unmarshal(l.Message.Content, &blocks) == nil {
			for _, c := range blocks {
				if c.Type == "tool_use" {
					skill, _ := c.Input.Skill.(string)
					if c.Name != "Skill" {
						skill = ""
					}
					ccTool(s, m, at, c.ID, c.Name, skill)
				}
			}
		}
		u := l.Message.Usage
		if u == nil || l.Message.Model == "<synthetic>" {
			return
		}
		ccCount(s, m, at, l.Message.Model, ccTokens(u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Creation), main)
	}
}

// ccText is the text of a user message, "" for a tool's result.
func ccText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) != nil {
		var blocks []struct{ Type, Text string }
		if json.Unmarshal(content, &blocks) != nil {
			return ""
		}
		var parts []string
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return ""
			}
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, " ")
	}
	return strings.TrimSpace(text)
}

var (
	ccCommand = regexp.MustCompile(`<command-name>\s*([^<]*?)\s*</command-name>`)
	ccArgs    = regexp.MustCompile(`<command-args>\s*([^<]*?)\s*</command-args>`)
)

// ccPrompt is the words of a prompt someone typed, or "" for what Claude
// Code put in the conversation itself (tool results, command output).
func ccPrompt(content json.RawMessage) string {
	text := ccText(content)
	if m := ccCommand.FindStringSubmatch(text); m != nil {
		// a slash command: its name and what was typed after it
		cmd := m[1]
		if a := ccArgs.FindStringSubmatch(text); a != nil && a[1] != "" {
			cmd += " " + a[1]
		}
		return title(cmd)
	}
	if text == "" || strings.HasPrefix(text, "<") || strings.HasPrefix(text, "[Request interrupted") || strings.HasPrefix(text, "Caveat:") {
		return ""
	}
	return title(text)
}

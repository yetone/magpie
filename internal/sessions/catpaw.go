package sessions

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/yetone/magpie/internal/provider"
)

// CatPaw (Meituan's CatPaw IDE, "CatPawAI", a VS Code fork whose agent is
// its built-in mt-idekit.mt-idekit-code extension) keeps its agent's
// conversations in ~/.sankuai/CatPawAI/sqliteDB/globalCache.sqlite on
// every system (the external tenant's dbFolderPath, under the home
// folder), SQLite in WAL mode, not encrypted. CatPaw 2.x's
// HistoryServiceAdapter writes two tables there (its main.*.js):
//
//	t_conversations  a conversation: conversation_id, parent_conversation_id
//	                 (a subagent's conversation names its parent's), title
//	                 ("新对话" or "对话 <date>" until its first prompt, then
//	                 that prompt cut to 47 characters and "..."), workspace_id
//	                 (the window's first folder, or its .code-workspace
//	                 file, "defaultWorkspace" with none), create_time and
//	                 update_time in ms
//	t_ui_messages    its messages in order (create_time, then id):
//	                 conversation_id, message_id, message_type (user_prompt,
//	                 text, thinking, tool, error, …), and content, the
//	                 message object as JSON: a prompt's content, a reply's
//	                 content and its response's tokenUsage, responseId and
//	                 actualUseModelName (a response's thinking and tools
//	                 carry the same three), a tool's blockData (name,
//	                 params) and toolResultData
//
// A conversation is a session: its prompts, replies and tools, and the
// tokens of each response once (by responseId). A subagent's
// conversation is filed under its parent's: its tools and tokens count
// there, its prompt (the parent's instruction) is no prompt of the
// user's.
//
// magpie only reads it, and never deletes a conversation: it is rows in a
// database CatPaw keeps open, which only CatPaw changes safely, and it has
// no command to pick one up again, so its sessions are listed read only.

// CatPawDir is the folder of CatPaw's database.
func CatPawDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".sankuai", "CatPawAI", "sqliteDB")
}

func catpawDB() string {
	d := CatPawDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "globalCache.sqlite")
}

// catpawConv is what t_conversations says of a conversation listed.
type catpawConv struct {
	db        *sql.DB
	title     string
	workspace string
}

// catpawMsg is what magpie reads of a message's content.
type catpawMsg struct {
	Content    string          `json:"content"`
	ResponseID string          `json:"responseId"`
	Model      string          `json:"actualUseModelName"`
	Usage      *catpawUsage    `json:"tokenUsage"`
	ToolName   string          `json:"tooluseName"`
	Original   string          `json:"originalToolName"`
	Result     json.RawMessage `json:"toolResultData"`
	Pretty     json.RawMessage `json:"toolResultPretty"`
	Expand     string          `json:"expandContent"`
	Block      *struct {
		Name   string          `json:"name"`
		Params json.RawMessage `json:"params"`
	} `json:"blockData"`
	Thinking *struct {
		Content string `json:"content"`
	} `json:"thinkingData"`
	Editor *struct {
		Text  string `json:"contentText"`
		Model string `json:"selectedModelName"`
	} `json:"submitEditorState"`
}

// catpawUsage is a response's tokenUsage, as the model's stream gave it.
// CatPaw reads cache reads from cache_read_tokens, cacheReadTokens or
// OpenAI's prompt_tokens_details.cached_tokens, and cache writes from
// cache_write_tokens or cacheWriteTokens (its tracing's p()).
type catpawUsage struct {
	Prompt      int  `json:"prompt_tokens"`
	Completion  int  `json:"completion_tokens"`
	CacheRead   *int `json:"cache_read_tokens"`
	CacheReadC  *int `json:"cacheReadTokens"`
	CacheWrite  *int `json:"cache_write_tokens"`
	CacheWriteC *int `json:"cacheWriteTokens"`
	Details     *struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// tokens are the usage in magpie's terms. A cache read named on its own
// is apart from prompt_tokens (Anthropic's way, and CatPaw's total adds
// the two); one read from prompt_tokens_details is inside it (OpenAI's).
func (u *catpawUsage) tokens() Tokens {
	if u == nil {
		return Tokens{}
	}
	t := Tokens{Input: max(0, u.Prompt), Output: max(0, u.Completion)}
	switch {
	case u.CacheRead != nil:
		t.CacheRead = max(0, *u.CacheRead)
	case u.CacheReadC != nil:
		t.CacheRead = max(0, *u.CacheReadC)
	case u.Details != nil:
		t.CacheRead = max(0, u.Details.Cached)
		t.Input = max(0, t.Input-t.CacheRead)
	}
	if w := cmp.Or(u.CacheWrite, u.CacheWriteC); w != nil {
		t.CacheWrite = max(0, *w)
	}
	return t
}

func (t Tokens) sum() int { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

func catpawTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// catpawFiles are CatPaw's conversations, one a session: its path the
// database's and #<conversation id>, its size the bytes of its messages
// (its subagents' with them), its time the latest message, and rev all
// that tells it changed. A subagent's conversation is not one of its
// own, and a conversation with no prompt (one opened and left) is left
// out, as CatPaw's own history leaves them.
func catpawFiles() []file {
	if !Reads("catpaw") {
		return nil
	}
	path := catpawDB()
	if path == "" || !fileExists(path) {
		return nil
	}
	db := openDB(path)
	if db == nil {
		return nil
	}
	cc, mc := hermesColumns(db, "t_conversations"), hermesColumns(db, "t_ui_messages")
	if !cc["conversation_id"] || !cc["parent_conversation_id"] || !cc["title"] || !cc["workspace_id"] ||
		!mc["conversation_id"] || !mc["message_type"] || !mc["content"] || !mc["create_time"] {
		return nil
	}
	rows, err := db.Query(`WITH m AS (
		SELECT COALESCE(NULLIF(c.parent_conversation_id, ''), m.conversation_id) AS root,
			m.conversation_id = COALESCE(NULLIF(c.parent_conversation_id, ''), m.conversation_id) AS own,
			m.message_type AS type, COALESCE(m.create_time, 0) AS at, COALESCE(m.update_time, 0) AS up,
			COALESCE(octet_length(m.content), 0) AS size
		FROM t_ui_messages m LEFT JOIN t_conversations c ON c.conversation_id = m.conversation_id),
	g AS (SELECT root, COUNT(*) AS n, MAX(MAX(at), MAX(up)) AS last, SUM(size) AS size,
			SUM(CASE WHEN own AND type = 'user_prompt' THEN 1 ELSE 0 END) AS prompts
		FROM m GROUP BY root)
	SELECT g.root, g.n, g.last, g.size, COALESCE(c.title, ''), COALESCE(c.workspace_id, ''), COALESCE(c.update_time, 0)
	FROM g LEFT JOIN t_conversations c ON c.conversation_id = g.root
	WHERE g.prompts > 0 AND COALESCE(c.parent_conversation_id, '') = ''`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []file
	for rows.Next() {
		var id, name, ws string
		var n, last, size, updated int64
		if rows.Scan(&id, &n, &last, &size, &name, &ws, &updated) != nil || id == "" {
			continue
		}
		out = append(out, file{
			agent:    "catpaw",
			key:      "catpaw:" + id,
			path:     path + "#" + url.QueryEscape(id),
			main:     true,
			size:     size,
			mod:      modOf(catpawTime(last)),
			readOnly: true,
			more: &fileMore{
				sid:    id,
				catpaw: &catpawConv{db: db, title: name, workspace: ws},
				rev: strings.Join([]string{strconv.FormatInt(n, 10), strconv.FormatInt(last, 10), strconv.FormatInt(size, 10),
					strconv.FormatInt(updated, 10), name, ws}, "|"),
			},
		})
	}
	return out
}

// catpawTitle is a conversation's title, "" while CatPaw holds one of its
// own in its place (isTemporaryTitle: "新对话", "对话 <date>", or its own
// cut of the first prompt, which magpie cuts itself).
func catpawTitle(t string) string {
	t = strings.TrimSpace(t)
	if t == "新对话" || strings.HasPrefix(t, "对话 ") ||
		(len(utf16.Encode([]rune(t))) == 50 && strings.HasSuffix(t, "...")) {
		return ""
	}
	return title(t)
}

// catpawCwd is the folder of a conversation's workspace_id: the folder
// itself, or the one a .code-workspace file is in; none for CatPaw's
// "defaultWorkspace", a window with no folder.
func catpawCwd(ws string) string {
	ws = strings.TrimSpace(ws)
	if ws == "" || ws == "defaultWorkspace" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(ws), ".code-workspace") {
		if i := strings.LastIndexAny(ws, `/\`); i > 0 {
			return ws[:i]
		}
	}
	return ws
}

// catpawPrompt is a prompt's words: its content, else what the editor held.
func catpawPrompt(m catpawMsg) string {
	if strings.TrimSpace(m.Content) != "" || m.Editor == nil {
		return m.Content
	}
	return m.Editor.Text
}

func catpawTool(m catpawMsg) string {
	if m.Block != nil && m.Block.Name != "" {
		return m.Block.Name
	}
	return cmp.Or(m.Original, m.ToolName)
}

// parseCatPaw reads one conversation and its subagents' in a read
// transaction, a snapshot while CatPaw writes on.
func parseCatPaw(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano(), StateMore: moreOf(StateMore{DBRevision: f.m().rev})}
	cv := f.m().catpaw
	if cv == nil {
		return s
	}
	sid := f.m().sid
	tx, err := cv.db.BeginTx(context.Background(), nil)
	if err != nil {
		return &state{}
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT conversation_id, message_type, COALESCE(create_time, 0), COALESCE(content, '') FROM t_ui_messages
		WHERE conversation_id = ? OR conversation_id IN (SELECT conversation_id FROM t_conversations WHERE parent_conversation_id = ?)
		ORDER BY create_time, id`, sid, sid)
	if err != nil {
		return &state{}
	}
	type response struct {
		date  string
		model string
		t     Tokens
	}
	var order []string
	responses := map[string]*response{}
	first, picked := "", ""
	replied := true // a reply a prompt: a turn speaks between its tools
	for rows.Next() {
		var conv, typ, raw string
		var at int64
		if rows.Scan(&conv, &typ, &at, &raw) != nil {
			continue
		}
		var m catpawMsg
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		t := catpawTime(at)
		own := conv == sid
		s.saw(t, own)
		switch typ {
		case "user_prompt":
			if !own {
				continue // the parent's instruction to its subagent
			}
			text := catpawPrompt(m)
			if strings.TrimSpace(text) == "" {
				continue
			}
			s.day(dateOf(t)).Prompts++
			if first == "" {
				first = title(text)
			}
			if m.Editor != nil && m.Editor.Model != "" {
				picked = m.Editor.Model
			}
			replied = false
		case "text":
			if own && !replied && strings.TrimSpace(m.Content) != "" {
				s.day(dateOf(t)).Replies++
				replied = true
			}
		case "tool":
			s.tool(t, catpawTool(m), "")
		}
		// a response's thinking, text and tools carry its usage each:
		// once a response, the largest it was seen at (a stream's last)
		u := m.Usage.tokens()
		if u.zero() {
			continue
		}
		key := m.ResponseID
		if key == "" {
			key = fmt.Sprintf("%s|%d|%d|%d|%d", conv, u.Input, u.Output, u.CacheRead, u.CacheWrite)
		}
		r := responses[key]
		if r == nil {
			r = &response{date: dateOf(t)}
			responses[key] = r
			order = append(order, key)
		}
		if u.sum() > r.t.sum() {
			r.t = u
		}
		r.model = cmp.Or(strings.TrimSpace(m.Model), r.model, picked)
	}
	rows.Close()
	if rows.Err() != nil {
		return &state{}
	}
	for _, k := range order {
		r := responses[k]
		s.use(r.date, r.model, r.t)
	}
	s.Title = cmp.Or(catpawTitle(cv.title), first)
	s.Cwd = catpawCwd(cv.workspace)
	s.own().First = first
	return s
}

// catpawTranscript reads a conversation as CatPaw shows it, in one read
// transaction: the prompts, the replies, the thinking, and each tool with
// what it was given and what it answered. A subagent's own conversation,
// CatPaw's notices (errors, a cancel, a summary's boundary) and the
// context it attaches are not said.
func catpawTranscript(s Session, add func(bool, Part) bool) error {
	i := strings.LastIndex(s.Path, "#")
	if i < 0 || s.ID == "" {
		return ErrNoTranscript
	}
	db, err := provider.OpenReadOnly(s.Path[:i])
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT message_type, COALESCE(content, '') FROM t_ui_messages WHERE conversation_id = ? ORDER BY create_time, id`, s.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, raw string
		if err := rows.Scan(&typ, &raw); err != nil {
			return err
		}
		var m catpawMsg
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		more := true
		switch typ {
		case "user_prompt":
			if text := catpawPrompt(m); strings.TrimSpace(text) != "" {
				more = add(true, textPart("user", text))
			}
		case "text":
			if strings.TrimSpace(m.Content) != "" {
				more = add(false, Part{Role: "assistant", Kind: "text", Text: m.Content})
			}
		case "thinking":
			if m.Thinking != nil && strings.TrimSpace(m.Thinking.Content) != "" {
				more = add(false, Part{Role: "assistant", Kind: "thinking", Text: m.Thinking.Content})
			}
		case "tool":
			name := catpawTool(m)
			if name == "" {
				continue
			}
			in := ""
			if m.Block != nil {
				in = pretty(m.Block.Params)
			}
			if strings.TrimSpace(in) == "" || in == "null" {
				in = name
			}
			more = add(false, Part{Role: "assistant", Kind: "tool_use", Name: name, Text: in})
			out := ""
			switch {
			case len(m.Result) > 0 && string(m.Result) != "null":
				out = resultText(m.Result)
			case len(m.Pretty) > 0 && string(m.Pretty) != "null":
				out = resultText(m.Pretty)
			default:
				out = m.Expand
			}
			if more && strings.TrimSpace(out) != "" {
				more = add(true, Part{Role: "tool", Kind: "tool_result", Name: name, Text: out})
			}
		}
		if !more {
			return nil
		}
	}
	return rows.Err()
}

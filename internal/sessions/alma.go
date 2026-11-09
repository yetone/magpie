package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Alma (the desktop AI chat app) keeps its chats in one SQLite database,
// chat_threads.db, in its data folder — Electron's userData for the app
// "alma": ~/Library/Application Support/alma on a Mac, $XDG_CONFIG_HOME/alma
// (~/.config/alma) on Linux, %APPDATA%\alma on Windows. It is in WAL mode
// and written all the while Alma runs.
//
//	chat_threads   id, title ("New Chat" until it has one), model
//	               ("<provider id>:<model>"), created_at, updated_at (ISO),
//	               workspace_id (→ workspaces.path, the folder it works in),
//	               is_incognito, metadata ({"activePath":[…],"isCron":…})
//	chat_messages  id, thread_id, parent_id/slot_id/depth (a tree: an edited
//	               prompt or a reply asked again is a sibling), message (the
//	               AI SDK's UIMessage as JSON: id, role, parts — text,
//	               reasoning, file, step-start, tool-<Name> with its input and
//	               output, dynamic-tool with toolName), timestamp, metadata
//	               ({"usage":…,"isCompactionIndicator":…}), and
//	               parent_tool_call_id on a subagent's messages
//	usage_records  one row per reply: thread_id, model, provider_id, and its
//	               input_tokens (cache reads and writes included),
//	               cached_input_tokens, cache_write_input_tokens and
//	               output_tokens (reasoning included), timestamp
//	aux_usage_records  the same for side work (titles, memory), by thread
//
// magpie only reads it, through a read-only connection, and never deletes
// or moves a chat: a chat is rows in Alma's live database, with triggers,
// a full-text index and other tables pointing at it, which only Alma can
// change safely. Alma has no command to pick a chat up again, so its
// sessions are listed read only.

// AlmaDir is Alma's data folder.
func AlmaDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "alma")
}

func almaDB() string {
	d := AlmaDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "chat_threads.db")
}

// almaStore is Alma's database while a listing reads it, and the columns
// its version has.
type almaStore struct {
	db                            *sql.DB
	threads, messages, workspaces map[string]bool
	usage, aux                    map[string]bool
}

func almaTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	return t
}

// almaCronTitle starts the title of a chat a scheduled task of Alma's runs in.
const almaCronTitle = "⏰ Cron:"

// almaFiles are Alma's chats, one a session: its path the database's and
// #<thread id>, its size the bytes of its messages, its time the latest
// change seen, and rev all that tells a chat changed — the database's
// files change with every write to any chat, so a chat is read again only
// when its own rows did.
func almaFiles() []file {
	if !Reads("alma") {
		return nil
	}
	path := almaDB()
	if path == "" || !fileExists(path) {
		return nil
	}
	db := openDB(path)
	if db == nil {
		return nil
	}
	st := &almaStore{
		db:         db,
		threads:    hermesColumns(db, "chat_threads"),
		messages:   hermesColumns(db, "chat_messages"),
		workspaces: hermesColumns(db, "workspaces"),
		usage:      hermesColumns(db, "usage_records"),
		aux:        hermesColumns(db, "aux_usage_records"),
	}
	if !st.threads["id"] || !st.messages["thread_id"] || !st.messages["message"] {
		return nil
	}
	usage := "0, ''"
	if st.usage["thread_id"] {
		usage = "(SELECT COUNT(*) FROM usage_records u WHERE u.thread_id = t.id), " +
			"COALESCE((SELECT MAX(timestamp) FROM usage_records u WHERE u.thread_id = t.id), '')"
	}
	// incognito chats are left out, and so are the chats Alma's scheduled
	// tasks run in — thousands of them, which would bury every other
	// session — told the way Alma tells them: metadata.isCron, or a title
	// starting "⏰ Cron:" (the title is set before the flag)
	var conds []string
	if st.threads["is_incognito"] {
		conds = append(conds, "COALESCE(t.is_incognito, 0) = 0")
	}
	if st.threads["metadata"] {
		conds = append(conds, "COALESCE(json_extract(CASE WHEN json_valid(t.metadata) THEN t.metadata END, '$.isCron'), 0) = 0")
	}
	if st.threads["title"] {
		conds = append(conds, "instr(COALESCE(t.title, ''), '"+almaCronTitle+"') <> 1")
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	// created_at and the thread id are in an index of Alma's; the bytes are
	// read from each row's header, not its text
	q := "SELECT t.id, " + almaExpr(st.threads, "t", "updated_at", "''") +
		", COALESCE(m.n, 0), COALESCE(m.last, ''), COALESCE(m.bytes, 0), " + usage +
		" FROM chat_threads t LEFT JOIN (SELECT thread_id, COUNT(*) n, MAX(" + almaColumn(st.messages, "created_at", "timestamp") +
		") last, SUM(octet_length(message)) bytes FROM chat_messages GROUP BY thread_id) m ON m.thread_id = t.id" + where
	rows, err := db.Query(q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []file
	for rows.Next() {
		var id, updated, last, usageLast string
		var n, bytes, usageN int64
		if rows.Scan(&id, &updated, &n, &last, &bytes, &usageN, &usageLast) != nil || id == "" {
			continue
		}
		if n == 0 && usageN == 0 {
			continue // a chat opened and left: nothing was said in it
		}
		mod := almaTime(updated)
		for _, t := range []string{last, usageLast} {
			if at := almaTime(t); at.After(mod) {
				mod = at
			}
		}
		out = append(out, file{
			agent:    "alma",
			key:      "alma:" + id,
			path:     path + "#" + url.QueryEscape(id),
			main:     true,
			size:     bytes,
			mod:      mod,
			readOnly: true,
			more: &fileMore{
				sid:  id,
				alma: st,
				rev:  strings.Join([]string{updated, strconv.FormatInt(n, 10), last, strconv.FormatInt(bytes, 10), strconv.FormatInt(usageN, 10), usageLast}, "|"),
			},
		})
	}
	return out
}

// almaExpr is a column of the table aliased as alias, else fallback when
// its version has no such column.
func almaExpr(cols map[string]bool, alias, name, fallback string) string {
	if cols[name] {
		return "COALESCE(" + alias + "." + name + ", " + fallback + ")"
	}
	return fallback
}

// almaColumn is the first of the names the table has, an empty string
// literal for none.
func almaColumn(cols map[string]bool, names ...string) string {
	for _, n := range names {
		if cols[n] {
			return n
		}
	}
	return "''"
}

// almaMessage is what magpie reads of a message: who said it, and its parts'
// kinds, words and tools.
type almaMessage struct {
	Role  string `json:"role"`
	Parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ToolName string          `json:"toolName"`
		Input    json.RawMessage `json:"input"`
	} `json:"parts"`
}

type almaMeta struct {
	Indicator bool `json:"isCompactionIndicator"`
}

// almaModel is a model as Alma names it, "<provider id>:<model>" (a
// plugin's "plugin:<plugin>:<provider>:<model>"), without Alma's provider.
func almaModel(model, providerID string) string {
	m := strings.TrimSpace(model)
	if providerID == "" {
		providerID, _, _ = strings.Cut(m, ":")
	}
	rest, ok := strings.CutPrefix(m, providerID+":")
	if !ok {
		return m
	}
	if providerID == "plugin" {
		// <plugin>:<its provider>:<model>
		if parts := strings.SplitN(rest, ":", 3); len(parts) == 3 && parts[2] != "" {
			return parts[2]
		}
	}
	if rest == "" {
		return m
	}
	return rest
}

// parseAlma reads one chat in a read transaction, a snapshot while Alma
// writes on.
func parseAlma(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano(), StateMore: moreOf(StateMore{DBRevision: f.m().rev})}
	st := f.m().alma
	if st == nil {
		return s
	}
	tx, err := st.db.BeginTx(context.Background(), nil)
	if err != nil {
		return &state{}
	}
	defer tx.Rollback()

	cwd := "''"
	join := ""
	if st.threads["workspace_id"] && st.workspaces["id"] && st.workspaces["path"] {
		cwd, join = "COALESCE(w.path, '')", " LEFT JOIN workspaces w ON w.id = t.workspace_id"
	}
	var named, created string
	if err := tx.QueryRow("SELECT "+almaExpr(st.threads, "t", "title", "''")+", "+almaExpr(st.threads, "t", "created_at", "''")+", "+cwd+
		" FROM chat_threads t"+join+" WHERE t.id = ?", f.m().sid).Scan(&named, &created, &s.Cwd); err != nil {
		return &state{}
	}
	if named = title(named); named != "New Chat" {
		s.Title = named
	}
	s.saw(almaTime(created), false)

	sub := "''"
	if st.messages["parent_tool_call_id"] {
		sub = "COALESCE(parent_tool_call_id, '')"
	}
	order := almaColumn(st.messages, "timestamp", "created_at")
	rows, err := tx.Query("SELECT message, "+hermesExpr(st.messages, "timestamp", "''")+", "+hermesExpr(st.messages, "metadata", "'{}'")+", "+sub+
		" FROM chat_messages WHERE thread_id = ? ORDER BY "+order+", rowid", f.m().sid)
	if err != nil {
		return &state{}
	}
	first := ""
	for rows.Next() {
		var raw, at, meta, parent string
		if rows.Scan(&raw, &at, &meta, &parent) != nil {
			continue
		}
		var m almaMessage
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		var md almaMeta
		_ = json.Unmarshal([]byte(meta), &md)
		t := almaTime(at)
		main := parent == ""
		s.saw(t, main)
		if md.Indicator {
			continue // the note a compaction leaves, not something said
		}
		d := s.day(dateOf(t))
		switch m.Role {
		case "user":
			if !main {
				continue
			}
			d.Prompts++
			if first == "" {
				for _, p := range m.Parts {
					if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
						first = title(p.Text)
						break
					}
				}
			}
		case "assistant":
			if main {
				d.Replies++
			}
			for _, p := range m.Parts {
				name := ""
				if n, ok := strings.CutPrefix(p.Type, "tool-"); ok {
					name = n
				} else if p.Type == "dynamic-tool" {
					name = p.ToolName
				}
				if name == "" {
					continue
				}
				skill := ""
				if name == "Skill" {
					var in struct {
						Skill string `json:"skill"`
					}
					if json.Unmarshal(p.Input, &in) == nil {
						skill = in.Skill
					}
				} else if strings.EqualFold(name, "Read") {
					var in struct {
						FilePath string `json:"file_path"`
						Path     string `json:"path"`
					}
					if json.Unmarshal(p.Input, &in) == nil {
						skill = routedSkill(in.FilePath + in.Path)
					}
				}
				s.tool(t, name, skill)
			}
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return &state{}
	}
	if s.Title == "" {
		s.Title = first
	}
	s.own().First = first

	for _, table := range []struct {
		name string
		cols map[string]bool
	}{{"usage_records", st.usage}, {"aux_usage_records", st.aux}} {
		if !table.cols["thread_id"] {
			continue
		}
		q := "SELECT " + hermesSelect(table.cols, []string{"model", "provider_id", "timestamp"}, "''") + ", " +
			hermesSelect(table.cols, []string{"input_tokens", "cached_input_tokens", "cache_write_input_tokens", "output_tokens"}, "0") +
			" FROM " + table.name + " WHERE thread_id = ?"
		rows, err := tx.Query(q, f.m().sid)
		if err != nil {
			return &state{}
		}
		for rows.Next() {
			var model, provider, at string
			var in, cached, written, out int
			if rows.Scan(&model, &provider, &at, &in, &cached, &written, &out) != nil {
				continue
			}
			t := almaTime(at)
			if t.IsZero() {
				t = s.Last
			}
			if t.After(s.Last) {
				s.Last = t
			}
			s.use(dateOf(t), almaModel(model, provider), Tokens{Input: max(0, in-cached-written), Output: out, CacheRead: cached, CacheWrite: written})
		}
		rows.Close()
		if rows.Err() != nil {
			return &state{}
		}
	}
	return s
}

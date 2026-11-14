package sessions

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/provider"
)

// OpenMausBot (milind-soni/OpenMausBot, a chat app whose bots run on agent
// CLIs or on a model API) keeps its data in $OMB_DATA_DIR, else
// ~/.openmausbot on every system (server/config.ts's DATA_DIR):
//
//	bots.json      the bots, each with its tasks — one a conversation
//	               (thread): threadId, title ("New thread" until it has
//	               one), createdAt/updatedAt (ms), and cwd once a turn has
//	               pinned the folder it works in (server/store.ts)
//	messages.db    SQLite, in WAL mode, written while it runs: messages
//	               (thread_id, id, at in ms, role user|bot, kind text|
//	               activity|digest|options|…, text, json), an activity's
//	               json holding the tool it ran as tool.name
//	               (server/message-db.ts)
//	usage/<YYYY-MM>.jsonl  one row a turn: threadId, driverKind, model,
//	               input (cache reads and writes included), cachedInput,
//	               cacheWrite, output, at (server/usage-ledger.ts)
//
// A conversation is a session: its prompts, replies and tools from
// messages.db, its title and folder from bots.json, its tokens from the
// usage rows. Only the rows of a bot that runs on a model API (the
// drivers in ombAPIDrivers) count tokens here: a bot on Claude Code, Codex,
// Pi or another agent CLI runs that CLI, which keeps the turn in its own
// session as well, so its tokens are counted there (Claude Code's and
// Codex's in ~/.claude and ~/.codex) and would be counted twice.
//
// magpie only reads it, and never deletes a conversation: it is rows in a
// database OpenMausBot keeps open, which only OpenMausBot changes safely,
// and it has no command to pick one up again, so its sessions are listed
// read only.

// OpenMausBotDir is OpenMausBot's data folder.
func OpenMausBotDir() string {
	if d := strings.TrimSpace(appdir.Getenv("OMB_DATA_DIR")); d != "" {
		return expandHome(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".openmausbot")
}

func ombDB() string {
	d := OpenMausBotDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "messages.db")
}

// ombAPIDrivers are OpenMausBot's drivers that call a model's API
// themselves (server/agents-bridge-switch.ts's CHAT_RUNTIME_DRIVERS): no
// other app keeps their turns, so their tokens are counted here.
var ombAPIDrivers = []string{"cerebras", "greenference", "modelgo", "grok", "groq", "meta", "minimax", "mistral", "openai-compat"}

// ombTask is what magpie reads of a conversation in bots.json.
type ombTask struct {
	Title string
	Cwd   string
}

// ombUse is one of usage/*.jsonl's rows.
type ombUse struct {
	At         string `json:"at"`
	ThreadID   string `json:"threadId"`
	DriverKind string `json:"driverKind"`
	Model      string `json:"model"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	Cached     int    `json:"cachedInput"`
	Written    int    `json:"cacheWrite"`
}

// ombStore is OpenMausBot's data while a listing reads it.
type ombStore struct {
	db    *sql.DB
	tasks map[string]ombTask
	usage map[string][]ombUse
}

// ombRead is a file read once and kept until it changes: bots.json and
// the usage files are read on every Sessions poll otherwise.
type ombRead[T any] struct {
	size int64
	mod  time.Time
	v    T
}

var (
	ombMu        sync.Mutex
	ombTasksRead = map[string]ombRead[map[string]ombTask]{}
	ombUsageRead = map[string]ombRead[map[string][]ombUse]{}
)

// ombCached is read(path), or what it gave last while the file's size and
// time are the same.
func ombCached[T any](cache map[string]ombRead[T], path string, read func([]byte) T) (T, bool) {
	var zero T
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		delete(cache, path)
		return zero, false
	}
	if c, ok := cache[path]; ok && c.size == info.Size() && c.mod.Equal(info.ModTime()) {
		return c.v, true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return zero, false
	}
	v := read(b)
	cache[path] = ombRead[T]{size: info.Size(), mod: info.ModTime(), v: v}
	return v, true
}

// ombTasks are bots.json's conversations by thread id. A file that can't
// be parsed (OpenMausBot writes it whole) gives none, and the sessions
// keep their first prompts as titles.
func ombTasks(b []byte) map[string]ombTask {
	var bots []struct {
		Cwd   *string `json:"cwd"`
		Tasks []struct {
			ThreadID string  `json:"threadId"`
			Title    string  `json:"title"`
			Cwd      *string `json:"cwd"`
		} `json:"tasks"`
	}
	if json.Unmarshal(b, &bots) != nil {
		return nil
	}
	out := map[string]ombTask{}
	for _, bot := range bots {
		for _, t := range bot.Tasks {
			if t.ThreadID == "" {
				continue
			}
			task := ombTask{Title: t.Title}
			if t.Cwd != nil {
				task.Cwd = *t.Cwd
			}
			out[t.ThreadID] = task
		}
	}
	return out
}

// ombUsage are a usage file's rows by thread id; a line that doesn't
// parse (one being written) is skipped.
func ombUsage(b []byte) map[string][]ombUse {
	out := map[string][]ombUse{}
	for line := range bytes.Lines(b) {
		var u ombUse
		if json.Unmarshal(line, &u) != nil || u.ThreadID == "" {
			continue
		}
		out[u.ThreadID] = append(out[u.ThreadID], u)
	}
	return out
}

func ombTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// openmausbotFiles are OpenMausBot's conversations, one a session: its
// path the database's and #<thread id>, its size the bytes of its
// messages, its time the latest message or usage row, and rev all that
// tells it changed — the database's files change with every write to any
// conversation. A conversation with no prompt and no usage (a bot's
// greeting alone) is left out.
func openmausbotFiles() []file {
	if !Reads("openmausbot") {
		return nil
	}
	path := ombDB()
	if path == "" || !fileExists(path) {
		return nil
	}
	db := openDB(path)
	if db == nil {
		return nil
	}
	cols := hermesColumns(db, "messages")
	if !cols["thread_id"] || !cols["at"] || !cols["role"] || !cols["kind"] || !cols["json"] {
		return nil
	}
	dir := filepath.Dir(path)
	st := &ombStore{db: db, usage: map[string][]ombUse{}}
	ombMu.Lock()
	st.tasks, _ = ombCached(ombTasksRead, filepath.Join(dir, "bots.json"), ombTasks)
	months, _ := filepath.Glob(filepath.Join(dir, "usage", "*.jsonl"))
	slices.Sort(months)
	for m := range ombUsageRead {
		if !slices.Contains(months, m) {
			delete(ombUsageRead, m)
		}
	}
	for _, m := range months {
		rows, _ := ombCached(ombUsageRead, m, ombUsage)
		for id, r := range rows {
			st.usage[id] = append(st.usage[id], r...)
		}
	}
	ombMu.Unlock()

	rows, err := db.Query("SELECT thread_id, COUNT(*), MAX(at), SUM(octet_length(json)), " +
		"SUM(CASE WHEN role = 'user' AND kind = 'text' THEN 1 ELSE 0 END) FROM messages GROUP BY thread_id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []file
	for rows.Next() {
		var id string
		var n, last, size, prompts int64
		if rows.Scan(&id, &n, &last, &size, &prompts) != nil || id == "" {
			continue
		}
		use := st.usage[id]
		if prompts == 0 && len(use) == 0 {
			continue
		}
		mod := ombTime(last)
		usageLast := ""
		for _, u := range use {
			if u.At > usageLast {
				usageLast = u.At
			}
		}
		if t := almaTime(usageLast); t.After(mod) {
			mod = t
		}
		task := st.tasks[id]
		out = append(out, file{
			agent:    "openmausbot",
			key:      "openmausbot:" + id,
			path:     path + "#" + url.QueryEscape(id),
			main:     true,
			size:     size,
			mod:      modOf(mod),
			readOnly: true,
			more: &fileMore{
				sid: id,
				omb: st,
				rev: strings.Join([]string{strconv.FormatInt(n, 10), strconv.FormatInt(last, 10), strconv.FormatInt(size, 10),
					strconv.Itoa(len(use)), usageLast, task.Title, task.Cwd}, "|"),
			},
		})
	}
	return out
}

// parseOpenMausBot reads one conversation in a read transaction, a
// snapshot while OpenMausBot writes on.
func parseOpenMausBot(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano(), StateMore: moreOf(StateMore{DBRevision: f.m().rev})}
	st := f.m().omb
	if st == nil {
		return s
	}
	sid := f.m().sid
	tx, err := st.db.BeginTx(context.Background(), nil)
	if err != nil {
		return &state{}
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT at, role, kind, COALESCE(text, ''), json FROM messages WHERE thread_id = ? ORDER BY at, rowid", sid)
	if err != nil {
		return &state{}
	}
	first := ""
	turns := map[string]bool{}
	for rows.Next() {
		var at int64
		var role, kind, text, raw string
		if rows.Scan(&at, &role, &kind, &text, &raw) != nil {
			continue
		}
		t := ombTime(at)
		s.saw(t, true)
		switch {
		case role == "user" && kind == "text":
			s.day(dateOf(t)).Prompts++
			if first == "" && strings.TrimSpace(text) != "" {
				first = title(text)
			}
		case role == "bot" && kind == "text":
			// a turn may say something before a tool and again after it:
			// one reply a turn. A bot's greeting, before anything was
			// asked, is no reply.
			var m struct {
				TurnID string `json:"turnId"`
			}
			_ = json.Unmarshal([]byte(raw), &m)
			if m.TurnID != "" {
				if turns[m.TurnID] {
					continue
				}
				turns[m.TurnID] = true
			} else if first == "" {
				continue
			}
			s.day(dateOf(t)).Replies++
		case role == "bot" && kind == "activity":
			var m struct {
				Tool struct {
					Name string `json:"name"`
				} `json:"tool"`
			}
			if json.Unmarshal([]byte(raw), &m) == nil && m.Tool.Name != "" {
				s.tool(t, m.Tool.Name, "")
			}
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return &state{}
	}
	task := st.tasks[sid]
	if named := title(task.Title); named != "" && named != "New thread" {
		s.Title = named
	} else {
		s.Title = first
	}
	s.Cwd = task.Cwd
	s.own().First = first

	for _, u := range st.usage[sid] {
		t := almaTime(u.At)
		if t.IsZero() {
			t = s.Last
		}
		if t.After(s.Last) {
			s.Last = t
		}
		if !slices.Contains(ombAPIDrivers, u.DriverKind) {
			continue // counted in the agent CLI's own session
		}
		cached, written := max(0, u.Cached), max(0, u.Written)
		s.use(dateOf(t), strings.TrimSpace(u.Model), Tokens{Input: max(0, u.Input-cached-written), Output: max(0, u.Output), CacheRead: cached, CacheWrite: written})
	}
	return s
}

// ombTranscript reads a conversation as OpenMausBot shows it, in one read
// transaction: the person's messages, the bot's lines (an options card's
// words too, as MausBot's first message is one), and each tool a turn ran
// with what it was given and what it answered. A line OpenMausBot itself
// adds (a notice, a queue or engine move) is an activity with no item of
// a turn and isn't said; nor is a turn's digest, which repeats the reply.
func ombTranscript(s Session, add func(bool, Part) bool) error {
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
	rows, err := tx.Query("SELECT role, kind, COALESCE(text, ''), json FROM messages WHERE thread_id = ? ORDER BY at, rowid", s.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var role, kind, text, raw string
		if err := rows.Scan(&role, &kind, &text, &raw); err != nil {
			return err
		}
		more := true
		switch {
		case role == "user" && kind == "text":
			more = add(true, textPart("user", text))
		case role == "bot" && (kind == "text" || kind == "options"):
			more = add(false, Part{Role: "assistant", Kind: "text", Text: text})
		case role == "bot" && kind == "activity":
			var m struct {
				Tool struct {
					Name    string          `json:"name"`
					Summary string          `json:"summary"`
					Input   json.RawMessage `json:"input"`
					Output  json.RawMessage `json:"output"`
					ItemID  string          `json:"itemId"`
				} `json:"tool"`
			}
			if json.Unmarshal([]byte(raw), &m) != nil || m.Tool.Name == "" || m.Tool.ItemID == "" {
				continue
			}
			in := pretty(m.Tool.Input)
			if strings.TrimSpace(in) == "" || in == "null" {
				// OpenMausBot kept no arguments: its one-line summary, else the name
				in = cmp.Or(m.Tool.Summary, m.Tool.Name)
			}
			more = add(false, Part{Role: "assistant", Kind: "tool_use", Name: m.Tool.Name, Text: in})
			if more && len(m.Tool.Output) > 0 && string(m.Tool.Output) != "null" {
				more = add(true, Part{Role: "tool", Kind: "tool_result", Name: m.Tool.Name, Text: resultText(m.Tool.Output)})
			}
		}
		if !more {
			return nil
		}
	}
	return rows.Err()
}

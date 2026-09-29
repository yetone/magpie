package sessions

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// OpenCode keeps a session as rows, not a file of lines: since 1.2 in its
// SQLite database (opencode.db: session, message and part tables, each
// message and part a JSON document), before that as JSON files under
// storage/ — session/<project>/<id>.json, message/<session>/<id>.json and
// part/<message>/<id>.json. The database is made from those files once,
// which stay behind, so where there is a database only it is read. An
// assistant message carries its model and its tokens, input without the
// cache and output without the reasoning. A subagent's work is a session
// of its own whose parent is the session it ran in, and counts there.
//
// OpenCode 2 keeps its sessions in the same database but in tables of its
// own (packages/core/src/session/sql.ts): session_v2, the session row as
// before (directory, a title that may be null, parent_id, time_created and
// _updated), and session_message, one row a message with its kind in type
// (user, assistant, synthetic, system, compaction, idle, …), its order in
// seq and the rest in data. A user message's data is its prompt, {text,
// files, …}, with no parts of its own; an assistant's has model {id,
// providerID, variant}, content (its text, reasoning and tools), cost,
// tokens {input, output, reasoning, cache {read, write}} counted as before,
// and time {created, completed} (packages/schema/src/session-message.ts,
// token-usage.ts). A compaction's data has its own tokens. On its first
// start OpenCode 2 copies every old session into these tables
// (packages/core/src/database/v1-migration.bun.ts, which records kv
// migration.v1-v2 {"phase":"completed"} when done) and leaves the old
// tables behind unwritten, so once it has, only the new tables are read;
// while it is under way, an old session not yet copied is read from the
// old ones.

// OpenCodeDir is OpenCode's data folder: $XDG_DATA_HOME/opencode, else
// ~/.local/share/opencode — on Windows too, where OpenCode keeps it there.
func OpenCodeDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "opencode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode")
}

// openCodeDB is OpenCode's database: $OPENCODE_DB (a path in the data
// folder unless absolute), else opencode.db there.
func openCodeDB() string {
	if p := os.Getenv("OPENCODE_DB"); p != "" && p != ":memory:" {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(OpenCodeDir(), p)
	}
	return filepath.Join(OpenCodeDir(), "opencode.db")
}

// ocInfo is a session as OpenCode keeps it, a row or a file.
type ocInfo struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentID"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Time      struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

// ocMessage is a message, a JSON document in either store.
type ocMessage struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	// ZCode's newer messages say modelId, which the key matches too (the
	// decoder takes a key in any case when none is spelt as it is)
	ModelID string `json:"modelID"`
	Time    struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Path *struct {
		Cwd string `json:"cwd"`
	} `json:"path"`
	Tokens *struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	// ZCode: who a message is from; a prompt someone typed is a real_user's
	Semantics *struct {
		Origin string `json:"origin"`
	} `json:"semantics"`
}

// ocPart is a part of a message: only its text is looked at, for a title.
type ocPart struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
	Ignored   bool   `json:"ignored"`
}

// ocStore is where OpenCode keeps its sessions: its database or its files.
type ocStore interface {
	info(sid string) (ocInfo, bool)
	messages(sid string) [][]byte // in no set order
	parts(mid string) [][]byte    // in order
}

func ms(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(n)
}

// openCodeFiles are OpenCode's sessions, one "file" each: in the database
// its path is the database's and #<session id>, its size the count of its
// messages and its time that of the latest change to one; from the files
// its own file's path, the sum of its messages' sizes and the latest time.
func openCodeFiles() []file {
	if db := openCodeDB(); fileExists(db) {
		return openCodeDBFiles("opencode", db)
	}
	return openCodeJSONFiles(filepath.Join(OpenCodeDir(), "storage"))
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// ocRoots gives each session the key of the session it ran under, the one
// at the top: a subagent's subagent counts there too.
func ocRoots(out []file, parent map[string]string) []file {
	for i := range out {
		root := out[i].sid
		for n := 0; parent[root] != "" && n < 64; n++ {
			root = parent[root]
		}
		out[i].key = out[i].agent + ":" + root
		out[i].main = parent[out[i].sid] == ""
	}
	return out
}

// ---- the database -------------------------------------------------------------

// dbs are the databases open while List or Stats reads them, closed when
// they are done (closeDBs).
var dbs = map[string]*sql.DB{}

func openDB(path string) *sql.DB {
	if db := dbs[path]; db != nil {
		return db
	}
	db, err := provider.OpenReadOnly(path)
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(4)
	dbs[path] = db
	return db
}

func closeDBs() {
	for p, db := range dbs {
		db.Close()
		delete(dbs, p)
	}
}

type ocDB struct{ db *sql.DB }

// openCodeDBFiles are the sessions in OpenCode's database, or ZCode's.
func openCodeDBFiles(agent, path string) []file {
	db := openDB(path)
	if db == nil {
		return nil
	}
	var store ocStore = ocDB{db}
	if agent == "zcode" {
		store = zcDB{ocDB{db}}
	}
	const q = `SELECT s.id, COALESCE(s.parent_id, ''), s.time_updated, COUNT(m.id), COALESCE(MAX(m.time_updated), 0)
		FROM %s s LEFT JOIN %s m ON m.session_id = s.id %s GROUP BY s.id`
	parent := map[string]string{}
	old := fmt.Sprintf(q, "session", "message", "")
	var out []file
	if agent == "opencode" && ocHasTable(db, "session_v2") {
		out = ocDBRows(db, fmt.Sprintf(q, "session_v2", "session_message", ""), agent, path, ocV2DB{db}, parent)
		switch {
		case !ocHasTable(db, "session") || ocMigrated(db):
			old = ""
		default: // being copied over: what isn't yet
			old = fmt.Sprintf(q, "session", "message", "WHERE s.id NOT IN (SELECT id FROM session_v2)")
		}
	}
	if old != "" {
		out = append(out, ocDBRows(db, old, agent, path, store, parent)...)
	}
	return ocRoots(out, parent)
}

func ocDBRows(db *sql.DB, query, agent, path string, store ocStore, parent map[string]string) []file {
	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []file
	for rows.Next() {
		var id, par string
		var updated, n, last int64
		if rows.Scan(&id, &par, &updated, &n, &last) != nil {
			continue
		}
		parent[id] = par
		out = append(out, file{agent: agent, path: path + "#" + id, sid: id, oc: store, size: n, mod: ms(max(updated, last))})
	}
	return out
}

func ocHasTable(db *sql.DB, name string) bool {
	var n int
	return db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n) == nil && n > 0
}

// ocMigrated is whether OpenCode 2 has copied every old session over.
func ocMigrated(db *sql.DB) bool {
	var b []byte
	if db.QueryRow(`SELECT value FROM kv WHERE key = 'migration.v1-v2'`).Scan(&b) != nil {
		return false
	}
	var v struct {
		Phase string `json:"phase"`
	}
	return json.Unmarshal(b, &v) == nil && v.Phase == "completed"
}

func (s ocDB) info(sid string) (ocInfo, bool) {
	var i ocInfo
	err := s.db.QueryRow(`SELECT id, COALESCE(parent_id, ''), directory, title, time_created, time_updated FROM session WHERE id = ?`, sid).
		Scan(&i.ID, &i.ParentID, &i.Directory, &i.Title, &i.Time.Created, &i.Time.Updated)
	return i, err == nil
}

func (s ocDB) rows(query, arg string) [][]byte {
	rows, err := s.db.Query(query, arg)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) == nil {
			out = append(out, b)
		}
	}
	return out
}

func (s ocDB) messages(sid string) [][]byte {
	return s.rows(`SELECT data FROM message WHERE session_id = ?`, sid)
}

func (s ocDB) parts(mid string) [][]byte {
	return s.rows(`SELECT data FROM part WHERE message_id = ? ORDER BY id`, mid)
}

// ---- OpenCode 2's tables ------------------------------------------------------

// ocV2DB is OpenCode 2's session_v2 and session_message. Its messages are
// given as the older ones are, to be read the same way.
type ocV2DB struct{ db *sql.DB }

func (s ocV2DB) info(sid string) (ocInfo, bool) {
	var i ocInfo
	err := s.db.QueryRow(`SELECT id, COALESCE(parent_id, ''), directory, COALESCE(title, ''), time_created, time_updated FROM session_v2 WHERE id = ?`, sid).
		Scan(&i.ID, &i.ParentID, &i.Directory, &i.Title, &i.Time.Created, &i.Time.Updated)
	return i, err == nil
}

// messages are a session's prompts, replies and compactions: a reply's
// model is its model's id, a compaction's (which may name none) the one
// last replied with.
func (s ocV2DB) messages(sid string) [][]byte {
	rows, err := s.db.Query(`SELECT id, type, data FROM session_message
		WHERE session_id = ? AND type IN ('user', 'assistant', 'compaction') ORDER BY seq`, sid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out [][]byte
	last := ""
	for rows.Next() {
		var id, typ string
		var b []byte
		if rows.Scan(&id, &typ, &b) != nil {
			continue
		}
		var m ocMessage
		var v struct {
			Model *struct {
				ID string `json:"id"`
			} `json:"model"`
		}
		if json.Unmarshal(b, &m) != nil || json.Unmarshal(b, &v) != nil {
			continue
		}
		m.ID, m.Role, m.ModelID = id, "assistant", last
		if v.Model != nil && v.Model.ID != "" {
			m.ModelID = v.Model.ID
		}
		switch typ {
		case "user":
			m.Role, m.ModelID, m.Tokens = "user", "", nil
		case "assistant":
			last = m.ModelID
		}
		if o, err := json.Marshal(m); err == nil {
			out = append(out, o)
		}
	}
	return out
}

// parts are a prompt's words, which OpenCode 2 keeps in the prompt itself.
func (s ocV2DB) parts(mid string) [][]byte {
	var b []byte
	if s.db.QueryRow(`SELECT data FROM session_message WHERE id = ? AND type = 'user'`, mid).Scan(&b) != nil {
		return nil
	}
	var u struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(b, &u) != nil {
		return nil
	}
	p, _ := json.Marshal(ocPart{Type: "text", Text: u.Text})
	return [][]byte{p}
}

// ---- the files ----------------------------------------------------------------

type ocFiles struct{ root string } // storage/

func openCodeJSONFiles(root string) []file {
	infos, _ := filepath.Glob(filepath.Join(root, "session", "*", "*.json"))
	store := ocFiles{root}
	var out []file
	parent := map[string]string{}
	for _, p := range infos {
		b, err := os.ReadFile(p)
		var i ocInfo
		if err != nil || json.Unmarshal(b, &i) != nil || i.ID == "" {
			continue
		}
		f := file{agent: "opencode", path: p, sid: i.ID, oc: store}
		if !stat(&f) {
			continue
		}
		// a message's file is written again as its reply goes on
		f.size = 0
		if es, err := os.ReadDir(filepath.Join(root, "message", i.ID)); err == nil {
			for _, e := range es {
				if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
					f.size += fi.Size()
					if fi.ModTime().After(f.mod) {
						f.mod = fi.ModTime()
					}
				}
			}
		}
		parent[i.ID] = i.ParentID
		out = append(out, f)
	}
	return ocRoots(out, parent)
}

func (s ocFiles) info(sid string) (ocInfo, bool) {
	ps, _ := filepath.Glob(filepath.Join(s.root, "session", "*", sid+".json"))
	for _, p := range ps {
		var i ocInfo
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &i) == nil {
			return i, true
		}
	}
	return ocInfo{}, false
}

func (s ocFiles) docs(dir string) [][]byte {
	ps, _ := filepath.Glob(filepath.Join(s.root, dir, "*.json"))
	sort.Strings(ps)
	var out [][]byte
	for _, p := range ps {
		if b, err := os.ReadFile(p); err == nil {
			out = append(out, b)
		}
	}
	return out
}

func (s ocFiles) messages(sid string) [][]byte { return s.docs(filepath.Join("message", sid)) }
func (s ocFiles) parts(mid string) [][]byte    { return s.docs(filepath.Join("part", mid)) }

// ---- reading a session --------------------------------------------------------

// parseOpenCode reads a session whole: a message's usage is written in
// place as its reply goes on, so there is no reading on from before.
func parseOpenCode(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano()}
	if f.oc == nil {
		return s
	}
	info, ok := f.oc.info(f.sid)
	if !ok {
		return s
	}
	s.ID, s.Cwd = info.ID, info.Directory
	if t := title(info.Title); t != "" && !ocDefaultTitle(t) {
		s.Named = t
	}
	var msgs []ocMessage
	for _, b := range f.oc.messages(f.sid) {
		var m ocMessage
		if json.Unmarshal(b, &m) == nil {
			msgs = append(msgs, m)
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].Time.Created != msgs[j].Time.Created {
			return msgs[i].Time.Created < msgs[j].Time.Created
		}
		return msgs[i].ID < msgs[j].ID
	})
	s.saw(ms(info.Time.Created), f.main)
	for _, m := range msgs {
		at := ms(m.Time.Created)
		s.saw(at, f.main)
		switch m.Role {
		case "user":
			if typed := m.Semantics == nil || m.Semantics.Origin == "" || m.Semantics.Origin == "real_user"; typed && f.main && s.Title == "" && s.First == "" {
				ocTitle(s, f.oc.parts(m.ID))
			}
		case "assistant":
			if done := ms(m.Time.Completed); !done.IsZero() {
				s.saw(done, f.main)
				at = done
			}
			if s.Cwd == "" && m.Path != nil {
				s.Cwd = m.Path.Cwd
			}
			if t := m.Tokens; t != nil && m.ModelID != "" {
				u := Tokens{Input: t.Input, Output: t.Output + t.Reasoning, CacheRead: t.Cache.Read, CacheWrite: t.Cache.Write}
				if f.agent == "zcode" {
					// the AI SDK's counts: input with the cache, output with the reasoning
					u.Input, u.Output = max(0, t.Input-t.Cache.Read-t.Cache.Write), t.Output
				}
				s.use(dateOf(at), m.ModelID, u)
			}
		}
	}
	return s
}

// ocTitle takes a session's title from its first prompt: the words typed,
// not what OpenCode put in beside them (a file read, a command's template).
func ocTitle(s *state, parts [][]byte) {
	var typed, other []string
	for _, b := range parts {
		var p ocPart
		if json.Unmarshal(b, &p) != nil || p.Type != "text" || p.Ignored || strings.TrimSpace(p.Text) == "" {
			continue
		}
		if p.Synthetic {
			other = append(other, p.Text)
		} else {
			typed = append(typed, p.Text)
		}
	}
	if t := strings.Join(typed, " "); strings.HasPrefix(strings.TrimSpace(t), "<") {
		s.First = untagged(t)
	} else {
		s.Title = title(t)
	}
	if s.Title == "" && s.First == "" {
		s.First = untagged(strings.Join(other, " "))
	}
}

// ocDefaultTitle is the title OpenCode gives a session before it names it.
func ocDefaultTitle(t string) bool {
	return strings.HasPrefix(t, "New session - ") || strings.HasPrefix(t, "Child session - ")
}

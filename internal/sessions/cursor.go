package sessions

import (
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"net/url"

	"github.com/yetone/magpie/internal/appdir"
)

// Cursor's CLI (cursor-agent) keeps a chat in a folder of its own under its
// config folder's chats/, in a folder per working directory named by the
// MD5 of its path: chats/<md5(cwd)>/<chat id>/. store.db there is a SQLite
// file of two tables, as its run store makes them
// (cursor-sdk-local-runtime's sqlite-blob-store): meta (key, value), whose
// one row "0" is the chat's metadata as JSON written in hex — agentId, name
// (its title; "New Agent" until it has one), createdAt (milliseconds),
// lastUsedModel, latestRootBlobId (hex) and, for a subagent's chat,
// subagentInfo {parentAgentId, rootParentAgentId, toolCallId, typeName} —
// and blobs (id, data), content-addressed by the hex of their SHA-256. The
// root blob is a ConversationStateStructure (agent.v1, protobuf): field 1,
// root_prompt_messages_json, the ids of the conversation's messages in
// order; field 13, summary_archives, the ids of the archives a compaction
// left (ConversationSummaryArchive, whose field 1 names the messages it
// summed up); field 26 the time the conversation started. A message's blob
// is the AI SDK's message as JSON: role (system, user, assistant, tool) and
// content, words or parts (text, reasoning, tool-call with toolName, …).
// What the user typed is in <user_query> in a user message, beside the
// context Cursor puts in it (<user_info>, <rules>, …). Newer builds write a
// meta.json beside store.db too: title, createdAtMs, updatedAtMs,
// hasConversation, isSubagent and the cwd, which store.db doesn't name. The
// requests go to Cursor's servers only, which keep what they cost, so a
// chat here says nothing of tokens. `cursor-agent --resume <id>` picks one
// up again, in its folder: the chats are looked up by it.

// CursorDir is Cursor's CLI config folder: $CURSOR_CONFIG_DIR, else cursor
// in $XDG_CONFIG_HOME, else ~/.cursor.
func CursorDir() string {
	if d := strings.TrimSpace(appdir.Getenv("CURSOR_CONFIG_DIR")); d != "" {
		return d
	}
	if d := strings.TrimSpace(appdir.Getenv("XDG_CONFIG_HOME")); d != "" {
		return filepath.Join(d, "cursor")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cursor")
}

// cursorDataDir is where Cursor keeps its projects' files, the chats'
// transcripts among them: $CURSOR_DATA_DIR, else ~/.cursor.
func cursorDataDir() string {
	if d := strings.TrimSpace(appdir.Getenv("CURSOR_DATA_DIR")); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cursor")
}

var cursorSlugBad = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// cursorTranscripts are the transcripts Cursor writes of a chat, from its
// store, under projects/<the cwd made a slug>/agent-transcripts/: <id>/
// (its own and its subagents'), and <id>.jsonl and <id>.txt of older builds.
func cursorTranscripts(cwd, id string) []string {
	slug := strings.Trim(cursorSlugBad.ReplaceAllString(cwd, "-"), "-")
	if slug == "" || !safeID.MatchString(id) {
		return nil
	}
	dir := filepath.Join(cursorDataDir(), "projects", slug, "agent-transcripts")
	return []string{filepath.Join(dir, id), filepath.Join(dir, id+".jsonl"), filepath.Join(dir, id+".txt")}
}

// cursorMetaFile is a chat's meta.json.
type cursorMetaFile struct {
	Title      string `json:"title"`
	Created    int64  `json:"createdAtMs"`
	Updated    int64  `json:"updatedAtMs"`
	Subagent   bool   `json:"isSubagent"`
	Cwd        string `json:"cwd"`
	HasConvers bool   `json:"hasConversation"`
}

func readCursorMetaFile(dir string) (cursorMetaFile, bool) {
	var m cursorMetaFile
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	return m, err == nil && json.Unmarshal(b, &m) == nil
}

// cursorMeta is a chat's row "0" in store.db's meta.
type cursorMeta struct {
	AgentID   string `json:"agentId"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	Model     string `json:"lastUsedModel"`
	Root      string `json:"latestRootBlobId"`
	Subagent  *struct {
		Parent     string `json:"parentAgentId"`
		RootParent string `json:"rootParentAgentId"`
	} `json:"subagentInfo"`
}

// readCursorMeta reads row "0": JSON in hex, or (should a build write it so)
// the JSON itself.
func readCursorMeta(db *sql.DB) (cursorMeta, bool) {
	var m cursorMeta
	var v string
	if db.QueryRow(`SELECT value FROM meta WHERE key = '0'`).Scan(&v) != nil {
		return m, false
	}
	v = strings.TrimSpace(v)
	b := []byte(v)
	if !strings.HasPrefix(v, "{") {
		d, err := hex.DecodeString(v)
		if err != nil {
			return m, false
		}
		b = d
	}
	return m, json.Unmarshal(b, &m) == nil
}

// cursorParents keeps, by a chat's store, the chat a subagent's ran under,
// so the stores are opened again only when they change.
var cursorParents struct {
	sync.Mutex
	m map[string]cursorParent
}

type cursorParent struct {
	size int64
	mod  time.Time
	of   string
}

// cursorParentOf is the chat a subagent's chat ran under, "" for one of the
// user's own. A meta.json that says it isn't a subagent's is taken at its
// word; otherwise store.db is asked.
func cursorParentOf(f file) string {
	dir := filepath.Dir(f.path)
	if m, ok := readCursorMetaFile(dir); ok && !m.Subagent {
		return ""
	}
	cursorParents.Lock()
	defer cursorParents.Unlock()
	if p, ok := cursorParents.m[f.path]; ok && p.size == f.size && p.mod.Equal(f.mod) {
		return p.of
	}
	of := ""
	if db, done, err := openCursorStore(f.path); err == nil {
		if m, ok := readCursorMeta(db); ok && m.Subagent != nil {
			of = m.Subagent.RootParent
			if of == "" {
				of = m.Subagent.Parent
			}
		}
		done()
	}
	if cursorParents.m == nil {
		cursorParents.m = map[string]cursorParent{}
	}
	cursorParents.m[f.path] = cursorParent{f.size, f.mod, of}
	return of
}

// openCursorStore opens a chat's store.db to read it without writing a byte
// in Cursor's folder. store.db is in WAL mode, and SQLite opening it even
// read-only makes its -wal and -shm files beside it when they aren't there
// (Cursor deletes them when it closes the chat) and writes its read marks
// in -shm when they are; both would change the folder and make the chat
// look written to a moment ago. A store with no log to replay is opened
// immutable, which touches nothing; one with a log is read from a copy of
// the two files, in a folder of magpie's own that close removes.
func openCursorStore(path string) (db *sql.DB, closeDB func(), err error) {
	open := func(p, query string) (*sql.DB, error) {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(p), RawQuery: query}
		if len(u.Path) >= 2 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			return nil, err
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, err
		}
		return db, nil
	}
	if fi, err := os.Stat(path + "-wal"); err != nil || fi.Size() == 0 {
		db, err := open(path, "immutable=1")
		if err != nil {
			return nil, nil, err
		}
		return db, func() { db.Close() }, nil
	}
	tmp, err := os.MkdirTemp("", "magpie-cursor-")
	if err != nil {
		return nil, nil, err
	}
	gone := func() { os.RemoveAll(tmp) }
	copied := filepath.Join(tmp, "store.db")
	for _, side := range []string{"", "-wal"} {
		if err := copyFile(path+side, copied+side); err != nil {
			gone()
			return nil, nil, err
		}
	}
	db, err = open(copied, "_pragma=busy_timeout(3000)")
	if err != nil {
		gone()
		return nil, nil, err
	}
	return db, func() { db.Close(); gone() }, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func cursorFiles() []file {
	paths, _ := filepath.Glob(filepath.Join(CursorDir(), "chats", "*", "*", "store.db"))
	var out []file
	for _, p := range paths {
		dir := filepath.Dir(p)
		id := filepath.Base(dir)
		if !safeID.MatchString(id) {
			continue
		}
		f := file{agent: "cursor", key: "cursor:" + id, path: p, main: true}
		if !stat(&f) {
			continue
		}
		// what isn't in store.db yet is in its write-ahead log; the title
		// and the cwd are in meta.json
		for _, side := range []string{p + "-wal", filepath.Join(dir, "meta.json")} {
			m := file{path: side}
			if stat(&m) {
				f.size += m.size
				if m.mod.After(f.mod) {
					f.mod = m.mod
				}
			}
		}
		if parent := cursorParentOf(f); parent != "" && parent != id && safeID.MatchString(parent) {
			f.key, f.main = "cursor:"+parent, false
		}
		out = append(out, f)
	}
	return out
}

// pbFields reads a protobuf message's fields of one number that are bytes
// (wire type 2), and its varints of another, without its schema.
func pbFields(b []byte, bytesField, varField int) (vals [][]byte, varint uint64) {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return
		}
		b = b[n:]
		num, typ := int(key>>3), key&7
		switch typ {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return
			}
			b = b[n:]
			if num == varField {
				varint = v
			}
		case 1:
			if len(b) < 8 {
				return
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return
			}
			if num == bytesField {
				vals = append(vals, b[n:n+int(l)])
			}
			b = b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return
			}
			b = b[4:]
		default:
			return
		}
	}
	return
}

// cursorMessage is a message as the AI SDK keeps it.
type cursorMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type cursorPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ToolName string `json:"toolName"`
}

// cursorMessages are a chat's messages in order: those compactions summed
// up first, then the conversation's own.
func cursorMessages(db *sql.DB, root string) []cursorMessage {
	blob := func(id []byte) []byte {
		var data []byte
		if db.QueryRow(`SELECT data FROM blobs WHERE id = ?`, hex.EncodeToString(id)).Scan(&data) != nil {
			return nil
		}
		return data
	}
	rootID, err := hex.DecodeString(root)
	if err != nil || len(rootID) == 0 {
		return nil
	}
	state := blob(rootID)
	if state == nil {
		return nil
	}
	var ids [][]byte
	archives, _ := pbFields(state, 13, 0)
	for _, a := range archives {
		if data := blob(a); data != nil {
			summed, _ := pbFields(data, 1, 0)
			ids = append(ids, summed...)
		}
	}
	own, _ := pbFields(state, 1, 0)
	ids = append(ids, own...)
	var out []cursorMessage
	for _, id := range ids {
		var m cursorMessage
		if data := blob(id); data != nil && json.Unmarshal(data, &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// cursorStarted is when the conversation of a root blob started, if it says.
func cursorStarted(db *sql.DB, root string) time.Time {
	id, err := hex.DecodeString(root)
	if err != nil || len(id) == 0 {
		return time.Time{}
	}
	var data []byte
	if db.QueryRow(`SELECT data FROM blobs WHERE id = ?`, hex.EncodeToString(id)).Scan(&data) != nil {
		return time.Time{}
	}
	_, at := pbFields(data, -1, 26)
	return ms(int64(at))
}

var (
	cursorQuery = regexp.MustCompile(`(?s)<user_query>(.*?)</user_query>`)
	// the context Cursor puts in a user message beside what was typed
	cursorContext = regexp.MustCompile(`(?s)<(user_info|project_layout|rules|always_applied_workspace_rules|agent_requestable_workspace_rules|user_rules|agent_skills|available_skills|open_and_recently_viewed_files|system_reminder|system-reminder|instructions_update|mcp_instructions|mcp_file_system|mcp_file_system_servers|git_status|agent_transcripts|cursor_rules_context|attached_files|system_notification|task_notification|agent_notification)(?:\s[^>]*)?>.*?</(user_info|project_layout|rules|always_applied_workspace_rules|agent_requestable_workspace_rules|user_rules|agent_skills|available_skills|open_and_recently_viewed_files|system_reminder|system-reminder|instructions_update|mcp_instructions|mcp_file_system|mcp_file_system_servers|git_status|agent_transcripts|cursor_rules_context|attached_files|system_notification|task_notification|agent_notification)>`)
)

// cursorParts are a message's content as parts: words are one text part.
func cursorParts(raw json.RawMessage) []cursorPart {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []cursorPart{{Type: "text", Text: s}}
	}
	var parts []cursorPart
	json.Unmarshal(raw, &parts)
	return parts
}

// cursorTyped is what was typed in a user message: its <user_query>, else
// its words out of the context Cursor put in it; context alone is none.
func cursorTyped(parts []cursorPart) (typed, other string) {
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	all := strings.Join(texts, "\n")
	if qs := cursorQuery.FindAllStringSubmatch(all, -1); len(qs) > 0 {
		var q []string
		for _, m := range qs {
			q = append(q, m[1])
		}
		return strings.TrimSpace(strings.Join(q, "\n")), ""
	}
	rest := strings.TrimSpace(cursorContext.ReplaceAllString(all, ""))
	if strings.HasPrefix(rest, "<") {
		return "", rest
	}
	return rest, ""
}

// parseCursor reads a chat's store whole.
func parseCursor(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano()}
	dir := filepath.Dir(f.path)
	last := f.mod
	if m, ok := readCursorMetaFile(dir); ok {
		s.Cwd = m.Cwd
		if f.main {
			s.Named = cursorTitle(m.Title)
		}
		s.saw(ms(m.Created), f.main)
		if u := ms(m.Updated); !u.IsZero() {
			last = u
		}
	}
	db, done, err := openCursorStore(f.path)
	if err != nil {
		s.saw(last, f.main)
		return s
	}
	defer done()
	meta, ok := readCursorMeta(db)
	if !ok {
		s.saw(last, f.main)
		return s
	}
	s.ID = filepath.Base(dir)
	if s.Named == "" && f.main {
		s.Named = cursorTitle(meta.Name)
	}
	s.saw(ms(meta.CreatedAt), f.main)
	s.saw(cursorStarted(db, meta.Root), f.main)
	s.saw(last, f.main)
	// the messages carry no time of their own: they count on the day the
	// chat was last at work
	date := dateOf(s.Last)
	for _, m := range cursorMessages(db, meta.Root) {
		parts := cursorParts(m.Content)
		switch m.Role {
		case "user":
			typed, other := cursorTyped(parts)
			if typed == "" {
				if f.main && s.Title == "" && s.First == "" && other != "" {
					s.First = untagged(other)
				}
				continue
			}
			if f.main {
				s.day(date).Prompts++
				if s.Title == "" {
					s.Title = title(typed)
				}
			}
		case "assistant":
			if f.main {
				s.day(date).Replies++
			}
			for _, p := range parts {
				if p.Type == "tool-call" {
					s.tool(s.Last, p.ToolName, "")
				}
			}
		}
	}
	return s
}

// cursorTitle is Cursor's title for a chat, none while it has the one it
// starts with.
func cursorTitle(t string) string {
	if t = title(t); t == "New Agent" {
		return ""
	}
	return t
}

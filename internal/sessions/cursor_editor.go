package sessions

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cursor's editor stores older chats in cursorDiskKV, and newer chats as
// project transcripts. These are read only: cursor-agent cannot resume them.
// A chat the editor keeps can't be deleted from outside it: Cursor's own
// delete clears the chat from several stores in state.vscdb and from the
// list of chats it holds in memory and writes back while it runs. A
// transcript whose chat the editor no longer keeps (loose) is files of its
// own, and is deleted as a CLI chat's transcripts are (cursorEditorPaths).
type cursorEditorMeta struct {
	db       *sql.DB
	revision string
	ID       string `json:"composerId"`
	Name     string `json:"name"`
	Created  int64  `json:"createdAt"`
	Updated  int64  `json:"lastUpdatedAt"`
	Headers  []struct {
		ID string `json:"bubbleId"`
	} `json:"fullConversationHeadersOnly"`
	Workspace struct {
		URI struct {
			Path string `json:"fsPath"`
		} `json:"uri"`
	} `json:"workspaceIdentifier"`
}

// Share one read-only snapshot across all editor chats in a listing. Opening
// the WAL-backed global database separately for every chat copies it N times.
func openCursorEditorDB(path string) *sql.DB {
	if db := dbs[path]; db != nil {
		return db
	}
	db, done, err := openCursorEditorStore(path)
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(4)
	dbs[path], dbCleanup[path] = db, done
	return db
}

// openCursorEditorStore opens the editor's state.vscdb to read it. While
// Cursor runs, its own processes share the store as readers of its -wal
// through its -shm, and magpie reads it as one more, in place: a copy of a
// store of several GB was written to the temp folder each time Cursor had
// written since the last, ~10.6 GB every few minutes on Windows, where no
// clone makes a copy free (Echo-Huang0010, #1988). Nothing magpie shows is
// read from the store's file times, so a reader's mark in -shm moves no
// chat; each chat carries its own (lastUpdatedAt). With no -shm beside a
// live -wal there is no Cursor running to share it with, and it is opened
// as a chat's store is (openCursorStore).
func openCursorEditorStore(path string) (*sql.DB, func(), error) {
	// copies an older magpie left, which no copy made here would sweep
	cursorSnaps.sweep.Do(sweepCursorSnaps)
	wal, werr := os.Stat(path + "-wal")
	_, serr := os.Stat(path + "-shm")
	if !cursorReadInPlace || werr != nil || wal.Size() == 0 || serr != nil {
		return openCursorStore(path)
	}
	db, err := openSQLite(path, "mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return openCursorStore(path)
	}
	cursorInPlace.Add(1)
	return db, func() { db.Close() }, nil
}

// cursorComposersQuery lists the editor's chats by a range on the key's
// index. LIKE is case-insensitive, so SQLite can't use the index for it and
// reads the whole table: every bubble and checkpoint, often several GB. A
// live store is read from a fresh copy whenever Cursor has written, and a
// fresh copy (an APFS clone on macOS) isn't in the page cache, so every
// listing read the whole store from disk again and Sessions never finished
// loading (#1988). ';' is the character after ':'.
const cursorComposersQuery = "SELECT value FROM cursorDiskKV WHERE key >= 'composerData:' AND key < 'composerData;'"

func cursorEditorDB() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Cursor", "User", "globalStorage", "state.vscdb")
}

func cursorEditorFiles(cli []file) []file {
	if !Reads("cursor") {
		return nil
	}
	seen := map[string]bool{}
	for _, f := range cli {
		seen[strings.TrimPrefix(f.key, "cursor:")] = true
		seen[filepath.Base(filepath.Dir(f.path))] = true
	}
	metas := map[string]*cursorEditorMeta{}
	var out []file
	p := cursorEditorDB()
	// read: the editor's chats are known, so a transcript none of them is
	// is Cursor's no longer (a store that can't be read knows nothing)
	read := !fileExists(p)
	if !read {
		if db := openCursorEditorDB(p); db != nil {
			rows, err := db.Query(cursorComposersQuery)
			if err == nil {
				read = true
				for rows.Next() {
					var b []byte
					var m cursorEditorMeta
					if rows.Scan(&b) != nil || json.Unmarshal(b, &m) != nil || !safeID.MatchString(m.ID) {
						continue
					}
					m.db, m.revision = db, hashHead(string(b))
					metas[m.ID] = &m
				}
				read = rows.Err() == nil
				rows.Close()
			}
		}
	}
	root := filepath.Join(cursorDataDir(), "projects")
	// Only top-level conversations; subagents are not additional user chats.
	for _, pattern := range []string{"*/agent-transcripts/*/*.jsonl", "*/agent-transcripts/*.jsonl"} {
		paths, _ := filepath.Glob(filepath.Join(root, pattern))
		for _, path := range paths {
			id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
			if !safeID.MatchString(id) || seen[id] {
				continue
			}
			f := file{agent: "cursor", key: "cursor:" + id, path: path, main: true, readOnly: true, more: &fileMore{sid: id, editor: metas[id], loose: read && metas[id] == nil}}
			if !stat(&f) || !cursorEditorHasMessages(path) {
				continue
			}
			if f.m().editor != nil {
				f.w().rev = f.m().editor.revision
			}
			out = append(out, f)
			seen[id] = true
		}
	}
	for id, m := range metas {
		if seen[id] || len(m.Headers) == 0 {
			continue
		}
		b, _ := json.Marshal(m)
		out = append(out, file{agent: "cursor", key: "cursor:" + id, path: p + "#" + id, main: true, readOnly: true, size: int64(len(b)), mod: modOf(ms(max(m.Created, m.Updated))), more: &fileMore{sid: id, editor: m, rev: m.revision}})
	}
	return out
}

// Failed exports are not conversations, including in the management
// page's counts, which are computed before full parsing.
func cursorEditorHasMessages(path string) bool {
	found := false
	_ = readCursorEditor(path, func(role, text string, _ time.Time) bool {
		if role == "user" {
			text, _ = cursorTyped([]cursorPart{{Type: "text", Text: text}})
		}
		found = (role == "user" || role == "assistant") && strings.TrimSpace(text) != ""
		return !found
	})
	return found
}

func parseCursorEditor(f file) *state {
	s := &state{ID: f.m().sid, Size: f.size, Mod: f.mod.UnixNano(), StateMore: moreOf(StateMore{DBRevision: f.m().rev})}
	if m := f.m().editor; m != nil {
		s.own().Named = cursorTitle(m.Name)
		s.Cwd = m.Workspace.URI.Path
		s.saw(ms(m.Created), false)
		s.saw(ms(m.Updated), false)
	}
	// Project transcripts have no timestamps: their existing modification
	// time is the only activity date available, never the time of this scan.
	s.saw(f.mod.Time(), false)
	message := func(role, text string, at time.Time) bool {
		if at.IsZero() {
			at = f.mod.Time()
		}
		s.saw(at, false)
		if role == "user" {
			typed, _ := cursorTyped([]cursorPart{{Type: "text", Text: text}})
			if typed != "" {
				s.day(dateOf(at)).Prompts++
				if s.Title == "" {
					s.Title = title(typed)
				}
			}
		} else if role == "assistant" && strings.TrimSpace(text) != "" {
			s.day(dateOf(at)).Replies++
		}
		return true
	}
	if _, _, ok := cursorEditorDatabasePath(f.path); ok && f.m().editor != nil && f.m().editor.db != nil {
		_ = editorMessages(f.m().editor.db, f.m().sid, message)
	} else {
		_ = readCursorEditor(f.path, message)
	}
	if len(s.Days) == 0 {
		// A failed transcript export may contain only an error record,
		// while the editor metadata still has a name for the empty chat.
		s.own().Named = ""
	}
	return s
}

func editorMessages(db *sql.DB, id string, add func(string, string, time.Time) bool) error {
	var raw []byte
	if err := db.QueryRow("SELECT value FROM cursorDiskKV WHERE key = ?", "composerData:"+id).Scan(&raw); err != nil {
		return err
	}
	var meta cursorEditorMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	for _, h := range meta.Headers {
		if err := db.QueryRow("SELECT value FROM cursorDiskKV WHERE key = ?", "bubbleId:"+id+":"+h.ID).Scan(&raw); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return err
		}
		var b struct {
			Type    int    `json:"type"`
			Text    string `json:"text"`
			Created string `json:"createdAt"`
		}
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		role := ""
		if b.Type == 1 {
			role = "user"
		} else if b.Type == 2 {
			role = "assistant"
		}
		at, _ := time.Parse(time.RFC3339Nano, b.Created)
		if at.IsZero() {
			at = ms(max(meta.Created, meta.Updated))
		}
		if role != "" && !add(role, b.Text, at) {
			break
		}
	}
	return nil
}

func readCursorEditor(path string, add func(string, string, time.Time) bool) error {
	if dbPath, id, ok := cursorEditorDatabasePath(path); ok {
		db, done, err := openCursorEditorStore(dbPath)
		if err != nil {
			return err
		}
		defer done()
		return editorMessages(db, id, add)
	}
	_, err := scanAt(path, 0, nil, func(b []byte, _, _ int64) bool {
		var m struct {
			Role    string `json:"role"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &m) != nil {
			return true
		}
		var texts []string
		for _, p := range cursorParts(m.Message.Content) {
			if p.Type == "text" {
				texts = append(texts, p.Text)
			}
		}
		return add(m.Role, strings.Join(texts, ""), time.Time{})
	})
	return err
}

func cursorEditorDatabasePath(path string) (string, string, bool) {
	i := strings.LastIndex(path, "#")
	if i < 0 || !strings.HasSuffix(path[:i], ".vscdb") || !safeID.MatchString(path[i+1:]) {
		return "", "", false
	}
	return path[:i], path[i+1:], true
}

func cursorTranscript(path string, add func(bool, Part) bool) error {
	emit := func(role, text string, _ time.Time) bool {
		if role == "user" {
			text, _ = cursorTyped([]cursorPart{{Type: "text", Text: text}})
		}
		if role != "user" && role != "assistant" {
			return true
		}
		return add(role == "user", textPart(role, text))
	}
	_, _, editorDB := cursorEditorDatabasePath(path)
	if strings.HasSuffix(path, ".jsonl") || editorDB {
		return readCursorEditor(path, emit)
	}
	db, done, err := openCursorStore(path)
	if err != nil {
		return err
	}
	defer done()
	meta, ok := readCursorMeta(db)
	if !ok {
		return ErrNoTranscript
	}
	for _, m := range cursorMessages(db, meta.Root) {
		if m.Role == "system" {
			// the agent's own prompt, not said in the conversation
			continue
		}
		if m.Role == "user" {
			typed, _ := cursorTyped(cursorParts(m.Content))
			if !emit(m.Role, typed, time.Time{}) {
				return nil
			}
			continue
		}
		for _, p := range cursorTranscriptParts(m) {
			if !add(false, p) {
				return nil
			}
		}
	}
	return nil
}

func cursorTranscriptParts(m cursorMessage) []Part {
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []Part{textPart(m.Role, text)}
	}
	var blocks []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ToolName string          `json:"toolName"`
		Input    json.RawMessage `json:"input"`
		Args     json.RawMessage `json:"args"`
		Result   json.RawMessage `json:"result"`
		Output   json.RawMessage `json:"output"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var parts []Part
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, textPart(m.Role, b.Text))
		case "reasoning":
			parts = append(parts, Part{Role: m.Role, Kind: "thinking", Text: b.Text})
		case "tool-call":
			args := b.Input
			if len(args) == 0 {
				args = b.Args
			}
			parts = append(parts, Part{Role: m.Role, Kind: "tool_use", Name: b.ToolName, Text: pretty(args)})
		case "tool-result":
			result := b.Result
			if len(result) == 0 {
				result = b.Output
			}
			parts = append(parts, Part{Role: "tool", Kind: "tool_result", Name: b.ToolName, Text: resultText(result)})
		}
	}
	return parts
}

// cursorEditorPaths are what a delete moves of a loose editor transcript:
// the transcripts Cursor wrote of the chat in its agent-transcripts folder,
// <id>/ (its own and its subagents') and <id>.jsonl and <id>.txt. A chat
// the editor keeps, or a file that isn't such a transcript, has none.
func cursorEditorPaths(f file, id string) []string {
	if !f.m().loose || !safeID.MatchString(id) || !strings.HasSuffix(f.path, ".jsonl") {
		return nil
	}
	dir := filepath.Dir(f.path)
	if filepath.Base(dir) == id {
		dir = filepath.Dir(dir)
	}
	if filepath.Base(dir) != "agent-transcripts" {
		return nil
	}
	return []string{filepath.Join(dir, id), filepath.Join(dir, id+".jsonl"), filepath.Join(dir, id+".txt")}
}

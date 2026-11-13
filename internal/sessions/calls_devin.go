package sessions

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/provider"
)

// Devin CLI's calls (aohan0905 on X: Usage counted none of them). Devin's
// CLI (3000.11.234, tried in a sandbox HOME with a real account) keeps its
// sessions beside its sign-in, in <data>/devin/cli, where <data> is
// $XDG_DATA_HOME or ~/.local/share (%APPDATA% on Windows, as its
// credentials.toml), or $DEVIN_SESSIONS_DIR when set:
//
//   - sessions.db, SQLite: a sessions row each (id, working_directory, …,
//     last_activity_at in unix seconds), and message_nodes, the chat's
//     messages as a tree, each a chat_message JSON;
//   - with DEVIN_SESSION_STORE=files, sessions/<id>/log.jsonl and meta.json
//     instead, a {"kind":"node","node":{…,"message":{…}}} line a message.
//     On its first run that store copies sessions.db's sessions in and
//     leaves the database where it was, so a session can be in both.
//
// An assistant message carries its call in metadata: request_id,
// generation_model (the variant id, swe-2-high, effort and all), created_at,
// and metrics with input_tokens (less what was read from the cache),
// output_tokens, cache_read_tokens, cache_creation_tokens, ttft_ms and
// total_time_ms. Devin writes each reply as two nodes with one request_id,
// so a call is counted once by it, across both stores too (Msg). Devin
// talks to its own backend, never through magpie's gateway, so there is no
// gateway record to pair a call with.

// DevinCLIDirs are the folders Devin's CLI may keep sessions in.
func DevinCLIDirs() []string {
	var out []string
	if d := appdir.Getenv("DEVIN_SESSIONS_DIR"); d != "" {
		out = append(out, filepath.Join(d, "cli"))
	}
	d := filepath.Join(filepath.Dir(provider.DevinCredentialsPath()), "cli")
	if len(out) == 0 || out[0] != d {
		out = append(out, d)
	}
	return out
}

// devinCallList is the last listing of each sessions.db, kept while the
// database and its WAL are as they were.
var devinCallList struct {
	sync.Mutex
	m map[string]devinListing
}

type devinListing struct {
	stamp string
	files []file
}

// devinCallFiles are Devin's sessions as call sources: a folder store's
// log.jsonl, or a database's session as db#id. A session in both stores is
// listed once, from the one written to last.
func devinCallFiles() []file {
	if !Reads("devin") {
		return nil
	}
	var out []file
	at := map[string]int{} // a session's id → its file in out
	put := func(id string, f file) {
		if i, ok := at[id]; ok {
			if f.mod > out[i].mod {
				out[i] = f
			}
			return
		}
		at[id] = len(out)
		out = append(out, f)
	}
	for _, dir := range DevinCLIDirs() {
		ents, _ := os.ReadDir(filepath.Join(dir, "sessions"))
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(dir, "sessions", e.Name(), "log.jsonl")
			fi, err := os.Stat(p)
			if err != nil || fi.IsDir() {
				continue
			}
			put(e.Name(), file{agent: "devin", key: "devin:" + e.Name(), path: p, main: true, size: fi.Size(), mod: modOf(fi.ModTime())})
		}
		for _, f := range devinDBFiles(filepath.Join(dir, "sessions.db")) {
			put(strings.TrimPrefix(f.key, "devin:"), f)
		}
	}
	return out
}

// devinDBFiles are a sessions.db's sessions, as db#id.
func devinDBFiles(path string) []file {
	if !fileExists(path) {
		return nil
	}
	stamp := ocStamp(path)
	devinCallList.Lock()
	defer devinCallList.Unlock()
	if l, ok := devinCallList.m[path]; ok && stamp != "" && l.stamp == stamp {
		return append([]file(nil), l.files...)
	}
	var out []file
	ok := withOCCallDB(path, func(db *sql.DB) {
		rows, err := db.Query(`SELECT s.id, s.last_activity_at, COUNT(m.row_id), COALESCE(MAX(m.row_id), 0)
			FROM sessions s LEFT JOIN message_nodes m ON m.session_id = s.id GROUP BY s.id`)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var last, n, top int64
			if rows.Scan(&id, &last, &n, &top) != nil {
				continue
			}
			// a message added or taken away changes the size, as a line
			// added to a file does
			out = append(out, file{agent: "devin", key: "devin:" + id, path: path + "#" + id, main: true, size: n<<32 | top, mod: modOf(time.Unix(last, 0))})
		}
	})
	if !ok {
		return nil
	}
	if devinCallList.m == nil {
		devinCallList.m = map[string]devinListing{}
	}
	devinCallList.m[path] = devinListing{stamp, out}
	return append([]file(nil), out...)
}

// devinMessage is a chat_message as Devin writes it, the fields a call needs.
type devinMessage struct {
	Role     string `json:"role"`
	Metadata struct {
		RequestID string `json:"request_id"`
		Model     string `json:"generation_model"`
		CreatedAt string `json:"created_at"`
		Metrics   *struct {
			TTFT       *int64 `json:"ttft_ms"`
			Total      *int64 `json:"total_time_ms"`
			Input      *int   `json:"input_tokens"`
			Output     *int   `json:"output_tokens"`
			CacheRead  *int   `json:"cache_read_tokens"`
			CacheWrite *int   `json:"cache_creation_tokens"`
		} `json:"metrics"`
	} `json:"metadata"`
}

// readDevinCalls reads a session's calls whole, from its log.jsonl or its
// rows of sessions.db.
func readDevinCalls(f file) (*callFile, bool) {
	st := &callFile{Agent: "devin", Path: f.path, Session: strings.TrimPrefix(f.key, "devin:")}
	var cwd string
	var msgs [][]byte
	if i := strings.LastIndex(f.path, "#"); i >= 0 && !strings.HasSuffix(f.path, ".jsonl") {
		path, sid := f.path[:i], f.path[i+1:]
		if st.Session == "" {
			st.Session = sid
		}
		ok := withOCCallDB(path, func(db *sql.DB) {
			_ = db.QueryRow(`SELECT COALESCE(working_directory, '') FROM sessions WHERE id = ?`, sid).Scan(&cwd)
			rows, err := db.Query(`SELECT chat_message FROM message_nodes
				WHERE session_id = ? AND json_extract(chat_message, '$.role') = 'assistant' ORDER BY row_id`, sid)
			if err != nil {
				return
			}
			defer rows.Close()
			for rows.Next() {
				var b []byte
				if rows.Scan(&b) == nil {
					msgs = append(msgs, b)
				}
			}
		})
		if !ok {
			return st, false
		}
	} else {
		var ok bool
		if msgs, ok = devinLogMessages(f.path); !ok {
			return st, false
		}
		var meta struct {
			Cwd string `json:"working_directory"`
		}
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(f.path), "meta.json")); err == nil && json.Unmarshal(b, &meta) == nil {
			cwd = meta.Cwd
		}
		if st.Session == "" {
			st.Session = filepath.Base(filepath.Dir(f.path))
		}
	}
	seen := map[string]bool{}
	for _, b := range msgs {
		var m devinMessage
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		c, ok := devinCall(st, m)
		if !ok || seen[c.Msg] {
			continue
		}
		seen[c.Msg] = true
		c.Cwd = st.str(cwd)
		st.Calls = append(st.Calls, c)
	}
	sort.SliceStable(st.Calls, func(i, j int) bool { return st.Calls[i].Time.Before(st.Calls[j].Time) })
	return st, true
}

var devinRequest = []byte(`"request_id"`)

// devinLogMessages are the messages of a folder store's log.jsonl nodes.
func devinLogMessages(path string) ([][]byte, bool) {
	r, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer r.Close()
	var out [][]byte
	br := bufio.NewReaderSize(r, scanBuffer(r, 0))
	for {
		b, err := br.ReadBytes('\n')
		if err != nil && (!errors.Is(err, io.EOF) || len(bytes.TrimSpace(b)) == 0) {
			break
		}
		b = bytes.TrimSpace(b)
		if len(b) > 0 && len(b) <= maxLine && bytes.Contains(b, devinRequest) {
			var line struct {
				Kind string `json:"kind"`
				Node struct {
					Message json.RawMessage `json:"message"`
				} `json:"node"`
			}
			if json.Unmarshal(b, &line) == nil && line.Kind == "node" && len(line.Node.Message) > 0 {
				out = append(out, line.Node.Message)
			}
		}
		if err != nil {
			break
		}
	}
	return out, true
}

// devinCall is an assistant message as a call; one with no metrics (a
// message Devin made itself) is none.
func devinCall(st *callFile, m devinMessage) (Call, bool) {
	md := m.Metadata
	if m.Role != "assistant" || md.RequestID == "" || md.Metrics == nil || md.Metrics.Input == nil || md.Metrics.Output == nil {
		return Call{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, md.CreatedAt)
	if err != nil {
		return Call{}, false
	}
	model, effort := provider.DevinModelEffort(md.Model)
	c := Call{Agent: "devin", Time: t, Session: st.Session, Model: st.str(model), Effort: st.str(effort),
		Tokens: Tokens{Input: *md.Metrics.Input, Output: *md.Metrics.Output}}
	if p := md.Metrics.CacheRead; p != nil {
		c.CacheRead = *p
	}
	if p := md.Metrics.CacheWrite; p != nil {
		c.CacheWrite = *p
	}
	if c.Tokens.zero() {
		return Call{}, false
	}
	if p := md.Metrics.TTFT; p != nil {
		c.TTFT = *p
	}
	if p := md.Metrics.Total; p != nil {
		c.Millis = *p
	}
	c.Msg = "devin:" + md.RequestID
	return c, true
}

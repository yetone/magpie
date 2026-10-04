package sessions

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// OpenCode's own calls (#680): each assistant message is one, with its
// tokens, model and provider, read as the Sessions page reads OpenCode
// (opencode.go): its database — OpenCode 2's session_v2/session_message or
// the older session/message — else the JSON files under storage/. A
// session is a call source of its own, its size the count of its messages
// and its time the latest change to one, so only a session that changed is
// read again, and what it holds is kept in the call cache as a file's is.
//
// A reply through magpie's gateway (provider "magpie", or "dial" as magpie
// was once called) the gateway has logged already: it is left out, not
// matched afterwards.

// ocGatewayProvider is whether an OpenCode provider id is magpie's gateway.
func ocGatewayProvider(id string) bool { return id == "magpie" || id == "dial" }

// ocCallList is the last listing of OpenCode's database, kept while the
// database and its WAL are as they were.
var ocCallList struct {
	sync.Mutex
	stamp string
	files []file
}

// openCodeCallFiles are OpenCode's sessions as call sources.
func openCodeCallFiles() []file {
	path := openCodeDB()
	if !fileExists(path) {
		return openCodeJSONFiles(filepath.Join(OpenCodeDir(), "storage"))
	}
	stamp := ocStamp(path)
	ocCallList.Lock()
	defer ocCallList.Unlock()
	if stamp != "" && stamp == ocCallList.stamp {
		return append([]file(nil), ocCallList.files...)
	}
	var out []file
	// the size is the sum of the messages' times as well as their count, so
	// an older message written again tells too, not only the latest
	withOCCallDB(path, func(db *sql.DB) {
		out = openCodeDBFilesIn("opencode", path, db, "COUNT(m.id) + COALESCE(SUM(m.time_updated), 0)")
	})
	for i := range out {
		out[i].oc = nil // the handle is not kept; a read opens it again
	}
	ocCallList.stamp, ocCallList.files = stamp, out
	return append([]file(nil), out...)
}

// ocStamp is what tells OpenCode's database changed: it and its WAL's sizes
// and times.
func ocStamp(path string) string {
	var b strings.Builder
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%d:%d;", fi.Size(), fi.ModTime().UnixNano())
		} else {
			b.WriteString("-;")
		}
	}
	return path + "|" + b.String()
}

// ocCallDBs are the databases open for reading calls: a source is read on
// its own (ReadCallSource), and many at once, so a handle is shared and
// closed once nothing has used it for a while.
var ocCallDBs = struct {
	sync.Mutex
	m map[string]*ocCallHandle
}{m: map[string]*ocCallHandle{}}

type ocCallHandle struct {
	db    *sql.DB
	users int
	idle  *time.Timer
}

const ocCallIdle = 10 * time.Second

func withOCCallDB(path string, fn func(*sql.DB)) bool {
	ocCallDBs.Lock()
	h := ocCallDBs.m[path]
	if h == nil {
		db, err := provider.OpenReadOnly(path)
		if err != nil {
			ocCallDBs.Unlock()
			return false
		}
		db.SetMaxOpenConns(4)
		h = &ocCallHandle{db: db}
		ocCallDBs.m[path] = h
	}
	if h.idle != nil {
		h.idle.Stop()
		h.idle = nil
	}
	h.users++
	ocCallDBs.Unlock()
	defer func() {
		ocCallDBs.Lock()
		defer ocCallDBs.Unlock()
		if h.users--; h.users > 0 {
			return
		}
		h.idle = time.AfterFunc(ocCallIdle, func() {
			ocCallDBs.Lock()
			defer ocCallDBs.Unlock()
			if h.users == 0 && ocCallDBs.m[path] == h {
				h.db.Close()
				delete(ocCallDBs.m, path)
			}
		})
	}()
	fn(h.db)
	return true
}

// resetOCCalls forgets the listing and closes the handles nothing is using.
func resetOCCalls() {
	ocCallList.Lock()
	ocCallList.stamp, ocCallList.files = "", nil
	ocCallList.Unlock()
	ocCallDBs.Lock()
	defer ocCallDBs.Unlock()
	for p, h := range ocCallDBs.m {
		if h.users == 0 {
			if h.idle != nil {
				h.idle.Stop()
			}
			h.db.Close()
			delete(ocCallDBs.m, p)
		}
	}
}

// readOpenCodeCalls reads a session's calls whole: a message's tokens are
// written in place as its reply goes on, so there is no reading on.
func readOpenCodeCalls(f file) (*callFile, bool) {
	st := &callFile{Agent: f.agent, Path: f.path, Session: strings.TrimPrefix(f.key, f.agent+":")}
	var info ocInfo
	var msgs [][]byte
	if strings.HasSuffix(f.path, ".json") && fileExists(f.path) {
		// storage/session/<project>/<id>.json
		store := ocFiles{filepath.Dir(filepath.Dir(filepath.Dir(f.path)))}
		sid := strings.TrimSuffix(filepath.Base(f.path), ".json")
		info, _ = store.info(sid)
		msgs = store.messages(sid)
	} else {
		i := strings.LastIndex(f.path, "#")
		if i < 0 {
			return st, false
		}
		path, sid := f.path[:i], f.path[i+1:]
		if !withOCCallDB(path, func(db *sql.DB) {
			var store ocStore = ocDB{db}
			if ocHasTable(db, "session_v2") {
				var n int
				if db.QueryRow(`SELECT COUNT(*) FROM session_v2 WHERE id = ?`, sid).Scan(&n) == nil && n > 0 {
					store = ocV2DB{db}
				}
			}
			info, _ = store.info(sid)
			msgs = store.messages(sid)
		}) {
			return st, false
		}
	}
	if st.Session == "" {
		st.Session = info.ID
	}
	for _, b := range msgs {
		var m ocMessage
		if json.Unmarshal(b, &m) != nil || m.Role != "assistant" || ocGatewayProvider(m.ProviderID) {
			continue
		}
		if c, ok := ocCall(st, m, info.Directory); ok {
			st.Calls = append(st.Calls, c)
		}
	}
	sort.SliceStable(st.Calls, func(i, j int) bool {
		if !st.Calls[i].Time.Equal(st.Calls[j].Time) {
			return st.Calls[i].Time.Before(st.Calls[j].Time)
		}
		return st.Calls[i].Msg < st.Calls[j].Msg
	})
	return st, true
}

// ocCall is an assistant message as a call: its tokens as Codex's are told,
// the output with the reasoning in it; one that ended in an error with no
// tokens is a failed call, unless it was only stopped.
func ocCall(st *callFile, m ocMessage, dir string) (Call, bool) {
	created, done := ms(m.Time.Created), ms(m.Time.Completed)
	at := done
	if at.IsZero() {
		at = created
	}
	if at.IsZero() {
		return Call{}, false
	}
	c := Call{Time: at, Agent: "opencode", Session: st.Session, Model: st.str(m.ModelID), Upstream: st.str(m.ProviderID),
		Cwd: st.str(dir), File: st.Path, Msg: m.ID}
	if m.Path != nil && m.Path.Cwd != "" {
		c.Cwd = st.str(m.Path.Cwd)
	}
	// the effort picked for the prompt in OpenCode's model menu, which each
	// of its replies carries; "default" is none picked (#680)
	if m.Variant != "" && m.Variant != "default" {
		c.Effort = st.str(m.Variant)
	}
	if !done.IsZero() && !created.IsZero() && done.After(created) {
		c.Millis = done.Sub(created).Milliseconds()
	}
	if t := m.Tokens; t != nil {
		c.Tokens = Tokens{Input: t.Input, Output: t.Output + t.Reasoning, CacheRead: t.Cache.Read, CacheWrite: t.Cache.Write}
		c.Reasoning = t.Reasoning
	}
	if !c.Tokens.zero() {
		return c, c.Model != ""
	}
	kind, text := ocError(m.Error)
	if kind == "" || kind == "MessageAbortedError" || kind == "aborted" {
		return Call{}, false
	}
	c.Error, c.ErrorText = st.str(kind), firstChars(text, 400)
	return c, true
}

// ocError is the kind and message of a reply's error: OpenCode's
// {name, data: {message}}, or OpenCode 2's {type, message}.
func ocError(raw json.RawMessage) (kind, text string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	var e struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Message string `json:"message"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return "unknown", ""
	}
	kind = e.Name
	if kind == "" {
		kind = e.Type
	}
	if kind == "" {
		kind = "unknown"
	}
	text = e.Data.Message
	if text == "" {
		text = e.Message
	}
	return kind, text
}

package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// Qoder's CLI named its session in session-id on each request to a
// provider of the user's own up to 1.1.81. 1.1.82 (11-03) sends none,
// and nothing else in a request says whose it is: no header, no metadata
// (coderlee233 on Discord, qoderclicn 1.1.82). Its requests then had no
// session, so the Sessions page lost the conversation's title and showed
// each title and suggestion call as a row of its own.
//
// Qoder still writes each session to its folder as before (sessions.QoderDir:
// projects/<project>/<id>.jsonl, a subagent's in
// <id>/subagents/agent-<agent>.jsonl with the parent's sessionId), and it
// writes the prompt the user typed (a "user" line with humanInput, origin
// kind human) before it asks the model. So the session of a request from
// Qoder that names none is the one whose typed prompts the request holds:
//
//   - a turn holds each prompt of its session, in a user message's text
//     (the <system-reminder> blocks beside it are Qoder's, not typed);
//   - a subagent's turn holds the Agent tool's prompt, its file's first
//     line, and its file names the parent session;
//   - a title call holds the latest prompt, or "prompt\nreply\nprompt…",
//     and a suggestion call "User: prompt\n\nAssistant: reply".
//
// The session holding most of the request's text wins, and of those the
// file written last: a new session's "hi" against an older one's. The id
// found goes on the request as session-id, the header 1.1.81 sent, so the
// request is recorded, titled and grouped as an older Qoder's was. A
// request that names a session (an older Qoder, X-Magpie-Session) is left
// as it came. Only a request from this computer is matched: another's
// sessions are in another folder.

// qoderRecent is how long since a session file was last written it is
// still matched: each turn writes its prompt first, so a live session's
// file is always newer than this.
const qoderRecent = 24 * time.Hour

// qoderRescan is how long a listing of Qoder's folders is used before it
// is read again; a request no file matches lists them again at once.
const qoderRescan = time.Second

// qoderTitleWait is how long a side call (no tools: a title, a suggestion)
// no file matches is held for its session's file: Qoder asks the first
// title the moment the prompt is sent, and was seen ~0.1s before it wrote
// the session's file.
const qoderTitleWait = 600 * time.Millisecond

// qoderPromptsKept bounds the prompts kept of one file.
const qoderPromptsKept = 500

// qoderFile is what is read of one session file: the session it is, and
// the prompts typed in it, read up to off (the end of its last whole line).
type qoderFile struct {
	size    int64
	mod     time.Time
	off     int64
	session string
	prompts []string
}

// qoderIndex is the session files of one Qoder folder.
type qoderIndex struct {
	root   string // its projects folder
	mu     sync.Mutex
	listed time.Time
	files  map[string]*qoderFile
}

var qoderIndexes = struct {
	sync.Mutex
	by map[string]*qoderIndex
}{by: map[string]*qoderIndex{}}

// qoderIndexOf is the index of dir, one per folder.
func qoderIndexOf(dir string) *qoderIndex {
	qoderIndexes.Lock()
	defer qoderIndexes.Unlock()
	x := qoderIndexes.by[dir]
	if x == nil {
		x = &qoderIndex{root: dir, files: map[string]*qoderFile{}}
		qoderIndexes.by[dir] = x
	}
	return x
}

// qoderSession is r with the session of Qoder's it is a turn of named in
// session-id, when it comes from Qoder (qoder, qoder-cn) on this computer
// and names none. Otherwise, or when no session of Qoder's holds it, r as
// it came.
func qoderSession(r *http.Request, from provider.Protocol, body []byte) *http.Request {
	if from != provider.Chat && from != provider.Anthropic {
		return r
	}
	c := callerOf(r)
	if c.agent != "qoder" && c.agent != "qoder-cn" {
		return r
	}
	if sessionOf(r.Header) != "" || !local(r) {
		return r
	}
	asked := qoderAsked(body)
	if len(asked) == 0 {
		return r
	}
	wait := time.Duration(0)
	if len(gjson.GetBytes(body, "tools").Array()) == 0 {
		wait = qoderTitleWait
	}
	id := qoderIndexOf(filepath.Join(sessions.QoderDir(c.agent), "projects")).find(r.Context(), asked, wait)
	if id == "" {
		return r
	}
	r.Header.Set("session-id", id)
	if c.native[0] == "" {
		// a remote magpie it is passed on to records it the same
		c.native = [2]string{"session-id", id}
	}
	return r.WithContext(context.WithValue(r.Context(), callerCtx{}, c))
}

// qoderAsked is the text of a request's user messages, without Qoder's
// <system-reminder> blocks: what of it the user (or, for a subagent, the
// Agent tool) wrote, or a side call's transcript. Tool results are not
// in it (a Chat tool message, an Anthropic tool_result block).
func qoderAsked(body []byte) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !strings.HasPrefix(s, "<system-reminder>") {
			out = append(out, s)
		}
	}
	for _, m := range gjson.GetBytes(body, "messages").Array() {
		if m.Get("role").String() != "user" {
			continue
		}
		content := m.Get("content")
		if content.Type == gjson.String {
			add(content.String())
			continue
		}
		for _, b := range content.Array() {
			if b.Get("type").String() == "text" {
				add(b.Get("text").String())
			}
		}
	}
	return out
}

// find is the session the request asked holds the prompts of, "" when
// none. A request no file matches lists the folder again; a side call is
// given up to wait for its file to be written.
func (x *qoderIndex) find(ctx context.Context, asked []string, wait time.Duration) string {
	deadline := time.Now().Add(wait)
	for force := false; ; force = true {
		if id := x.match(asked, force); id != "" {
			return id
		}
		if force && !time.Now().Before(deadline) {
			return ""
		}
		if force {
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

// match reads what is new in the folder's recent files and answers the
// session holding most of asked, of those the file written last.
func (x *qoderIndex) match(asked []string, relist bool) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.refresh(relist)
	var best *qoderFile
	bestN := 0
	for _, f := range x.files {
		if f.session == "" {
			continue
		}
		n := 0
		for _, p := range f.prompts {
			if slices.ContainsFunc(asked, func(a string) bool { return strings.Contains(a, p) }) {
				n++
			}
		}
		if n > bestN || n == bestN && n > 0 && f.mod.After(best.mod) {
			best, bestN = f, n
		}
	}
	if best == nil {
		return ""
	}
	return best.session
}

// refresh lists the folder again when the listing is old or relist asks,
// and reads what was added to each recent file since it was last read.
func (x *qoderIndex) refresh(relist bool) {
	now := time.Now()
	if !relist && now.Sub(x.listed) < qoderRescan {
		return
	}
	x.listed = now
	seen := map[string]bool{}
	for path, info := range qoderFiles(x.root, now) {
		seen[path] = true
		f := x.files[path]
		if f == nil || info.Size() < f.off {
			f = &qoderFile{}
			x.files[path] = f
		}
		f.size, f.mod = info.Size(), info.ModTime()
		if f.size > f.off {
			f.read(path)
		}
	}
	for path := range x.files {
		if !seen[path] {
			delete(x.files, path)
		}
	}
}

// qoderFiles are the session files under root written in the last
// qoderRecent: projects/<project>/<id>.jsonl, and the subagents' of a
// session whose own file is recent (a subagent is started from a turn,
// which wrote its parent's file first).
func qoderFiles(root string, now time.Time) map[string]os.FileInfo {
	out := map[string]os.FileInfo{}
	if root == "" {
		return out
	}
	projects, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	recent := func(path string) os.FileInfo {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) > qoderRecent {
			return nil
		}
		return info
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(root, p.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			path := filepath.Join(dir, name)
			info := recent(path)
			if info == nil {
				continue
			}
			out[path] = info
			subs := filepath.Join(dir, strings.TrimSuffix(name, ".jsonl"), "subagents")
			agents, err := os.ReadDir(subs)
			if err != nil {
				continue
			}
			for _, a := range agents {
				if n := a.Name(); !a.IsDir() && strings.HasPrefix(n, "agent-") && strings.HasSuffix(n, ".jsonl") {
					if info := recent(filepath.Join(subs, n)); info != nil {
						out[filepath.Join(subs, n)] = info
					}
				}
			}
		}
	}
	return out
}

// read takes the whole lines written to path since f.off: the session
// they name, and each prompt typed. A line still being written is left
// for the next read.
func (f *qoderFile) read(path string) {
	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	if _, err := fh.Seek(f.off, io.SeekStart); err != nil {
		return
	}
	br := bufio.NewReaderSize(fh, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			// a line without its newline is not whole yet
			return
		}
		f.off += int64(len(line))
		f.line(line)
	}
}

// line takes one line of a session file.
func (f *qoderFile) line(line []byte) {
	if gjson.GetBytes(line, "type").String() != "user" {
		return
	}
	v := gjson.ParseBytes(line)
	if f.session == "" {
		f.session = boundedSession(v.Get("sessionId").String())
	}
	var typed string
	if h := v.Get("humanInput.text"); h.Exists() {
		typed = h.String()
	} else if v.Get("origin.kind").String() == "human" {
		content := v.Get("message.content")
		if content.Type == gjson.String {
			typed = content.String()
		} else {
			var parts []string
			for _, b := range content.Array() {
				if b.Get("type").String() == "text" {
					parts = append(parts, b.Get("text").String())
				}
			}
			typed = strings.Join(parts, "\n")
		}
	}
	if typed = strings.TrimSpace(typed); typed == "" {
		return
	}
	f.prompts = append(f.prompts, typed)
	if len(f.prompts) > qoderPromptsKept {
		f.prompts = slices.Delete(f.prompts, 0, len(f.prompts)-qoderPromptsKept)
	}
}

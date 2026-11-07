package sessions

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/dirent"
	"github.com/yetone/magpie/internal/provider"
)

// The Antigravity app's (the desktop IDE's, agent "antigravity") own
// conversations, on the Sessions and Usage pages, read where the app keeps
// them whether or not it reached magpie's gateway (#2190, yygutn; xxooioo
// on Discord). The app keeps each conversation as a SQLite database,
// ~/.gemini/antigravity/conversations/<conversation id>.db, written in WAL
// mode while it runs. Its messages are encrypted at rest; the figures read
// here are not. As Antigravity 2.23.0 on macOS writes them (yygutn's real
// rows on #2190; the app isn't installed on any machine magpie is tried on):
//
//	steps         idx, metadata BLOB, …: one row a step
//	gen_metadata  idx, data BLOB, …: one row a generator (a model's call)
//
// steps.metadata is a plain protobuf message:
//
//	1   the step's start (google.protobuf.Timestamp: 1 seconds, 2 nanos)
//	9   the model call's usage, on a model's step:
//	      2 input tokens, less what was read from the cache
//	      3 output tokens
//	      4 tokens written to the cache
//	      5 tokens read from the cache
//	      11 the vendor's response id
//	20  where the step sits: 3 the gen_metadata idx of its generator
//	    (absent for generator 0)
//
// gen_metadata.data's field 1 is the message asked of the model, whose
// field 19 is the model's id ("gemini-3.8-flash-n", counted as
// gemini-3.8-flash), else 21 or 22 (cc-bar's reader). Nothing else of either column is read. Each call is the app's,
// under the model its generator names; one sent through magpie is the
// gateway's to count, matched to the gateway's record by its tokens and
// time as an agy step is (usage's agyGateway: the app sends no session
// header).
//
// What the app calls a conversation is its row of
// conversation_summaries.db beside conversations/, the database agy keeps
// (agy.go): the app's language server is agy's, and Antigravity 2.29.0's
// makes the same table in ~/.gemini/antigravity on its first start. title
// is the name the app gives it, preview what it lists the conversation as
// before that (the user's own words), and workspace_uris the folders open
// when it was started, the first local one its folder on the Sessions page
// (agyFolder). Without the row, the steps' text being encrypted, a
// conversation has no name and no folder (xxooioo on Discord).
//
// A session is listed read only: the app has no command to pick a
// conversation up again from a terminal.

// AntigravityAppDir is the Antigravity app's data folder,
// ~/.gemini/antigravity.
func AntigravityAppDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity")
}

// antigravityConversations is the folder of the app's conversation databases.
func antigravityConversations() string {
	return filepath.Join(AntigravityAppDir(), "conversations")
}

// antigravityFiles are the app's conversations, one database a session.
func antigravityFiles() []file { return antigravityFilesIn(antigravityConversations()) }

// antigravityFilesIn are the conversation databases in dir. A file's size
// and time are the database's and its write-ahead log's together, so a
// write that is in the log alone still reads the conversation again; rev
// is its summary row, so a title the app gives it later does too.
func antigravityFilesIn(dir string) []file {
	if !Reads("antigravity") {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		return nil
	}
	rows := agySummariesOf(filepath.Dir(dir))
	var out []file
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".db")
		if !ok || !safeID.MatchString(id) || dirent.IsFolder(dir, e) {
			continue
		}
		f := file{agent: "antigravity", key: "antigravity:" + id, path: filepath.Join(dir, e.Name()), main: true, readOnly: true}
		if !stat(&f) {
			continue
		}
		if fi, err := os.Stat(f.path + "-wal"); err == nil {
			f.size += fi.Size()
			if fi.ModTime().After(f.mod) {
				f.mod = fi.ModTime()
			}
		}
		if r, ok := rows[id]; ok {
			m := f.w()
			m.agy, m.rev = &r, agyRev(r)
		}
		out = append(out, f)
	}
	return out
}

// antigravityCallFiles are the same databases as call sources.
func antigravityCallFiles() []file { return antigravityFiles() }

// agAppCall is one model call of a conversation.
type agAppCall struct {
	step     int64
	at       time.Time
	model    string
	tokens   Tokens
	response string
}

// errAgApp is a protobuf that can't be read.
var errAgApp = errors.New("antigravity: unreadable metadata")

// antigravityRead reads a conversation's calls, in one read transaction so
// a step and its generator come from one snapshot while the app writes on.
// A call whose response id came before is the later step of that response,
// and replaces it.
func antigravityRead(path string) ([]agAppCall, error) {
	db, err := provider.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	models := map[int64]string{}
	if rows, err := tx.Query("SELECT idx, data FROM gen_metadata WHERE data IS NOT NULL"); err == nil {
		for rows.Next() {
			var idx int64
			var b []byte
			if rows.Scan(&idx, &b) == nil {
				if m := agAppModel(b); m != "" {
					models[idx] = m
				}
			}
		}
		rows.Close()
	}
	rows, err := tx.Query("SELECT idx, metadata FROM steps WHERE metadata IS NOT NULL ORDER BY idx")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []agAppCall
	byResponse := map[string]int{}
	for rows.Next() {
		var idx int64
		var b []byte
		if rows.Scan(&idx, &b) != nil {
			continue
		}
		c, gen, ok := agAppStep(b)
		if !ok {
			continue
		}
		c.step, c.model = idx, models[gen]
		if i, seen := byResponse[c.response]; seen && c.response != "" {
			out[i] = c
			continue
		}
		if c.response != "" {
			byResponse[c.response] = len(out)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// agAppStep is a step's model call and its generator, ok when the step is
// a model's call with tokens and a time.
func agAppStep(b []byte) (c agAppCall, gen int64, ok bool) {
	var usage []byte
	err := pbWalk(b, func(num int, v uint64, d []byte) {
		switch num {
		case 1:
			if d != nil {
				c.at = pbTime(d)
			}
		case 9:
			usage = d
		case 20:
			_ = pbWalk(d, func(n int, v uint64, d []byte) {
				if n == 3 && d == nil {
					gen = int64(v)
				}
			})
		}
	})
	if err != nil || usage == nil || c.at.IsZero() {
		return c, 0, false
	}
	if pbWalk(usage, func(num int, v uint64, d []byte) {
		if d == nil {
			switch num {
			case 2:
				c.tokens.Input = int(v)
			case 3:
				c.tokens.Output = int(v)
			case 4:
				c.tokens.CacheWrite = int(v)
			case 5:
				c.tokens.CacheRead = int(v)
			}
		} else if num == 11 {
			c.response = string(d)
		}
	}) != nil || c.tokens.zero() {
		return c, 0, false
	}
	return c, gen, true
}

// agAppModel is the model a generator's message names: its field 19, else
// 21, else 22.
func agAppModel(b []byte) string {
	var named [3]string
	_ = pbWalk(b, func(num int, v uint64, d []byte) {
		if num != 1 || d == nil {
			return
		}
		_ = pbWalk(d, func(n int, v uint64, d []byte) {
			if d == nil {
				return
			}
			switch n {
			case 19:
				named[0] = strings.TrimSpace(string(d))
			case 21:
				named[1] = strings.TrimSpace(string(d))
			case 22:
				named[2] = strings.TrimSpace(string(d))
			}
		})
	})
	for _, m := range named {
		if m != "" && len(m) <= 200 {
			if base, ok := GeminiDeployment(m); ok {
				return base
			}
			return m
		}
	}
	return ""
}

// GeminiDeployment is the Gemini model a Google deployment's name stands
// for, and whether m is such a name: a gemini-* id with one letter after
// its last hyphen. Google names the deployment serving gemini-3.8-flash
// "gemini-3.8-flash-n" (the owner's real Antigravity account, b2432754e)
// and the one serving gemini-4-argon "gemini-4-argon-d" (oioio's
// Antigravity database, 11-06): ids no request can ask for, which are that
// model, at its price. No Gemini model's own id ends in a lone letter
// (-lite, -nano, -pro, -001 and -exp are longer), and other makers' ids
// are left as written.
func GeminiDeployment(m string) (string, bool) {
	i := strings.LastIndexByte(m, '-')
	if i < 0 || len(m)-i != 2 || m[i+1] < 'a' || m[i+1] > 'z' {
		return m, false
	}
	base := m[:i]
	if !strings.HasPrefix(base, "gemini-") || len(base) == len("gemini-") {
		return m, false
	}
	return base, true
}

// pbTime is a google.protobuf.Timestamp, zero when it has no seconds.
func pbTime(b []byte) time.Time {
	var sec, nanos uint64
	_ = pbWalk(b, func(num int, v uint64, d []byte) {
		switch {
		case d != nil:
		case num == 1:
			sec = v
		case num == 2:
			nanos = v
		}
	})
	if sec == 0 || sec > 1<<40 || nanos >= 1e9 {
		return time.Time{}
	}
	return time.Unix(int64(sec), int64(nanos))
}

// pbWalk walks a protobuf message's fields, handing each to field: a
// varint's value, or a length-delimited field's bytes (non-nil). Fixed-width
// fields are skipped.
func pbWalk(b []byte, field func(num int, v uint64, d []byte)) error {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 || tag>>3 == 0 {
			return errAgApp
		}
		b = b[n:]
		num := int(tag >> 3)
		switch tag & 7 {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return errAgApp
			}
			b = b[n:]
			field(num, v, nil)
		case 1:
			if len(b) < 8 {
				return errAgApp
			}
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || l > uint64(len(b)-n) {
				return errAgApp
			}
			field(num, 0, b[n:n+int(l)])
			b = b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return errAgApp
			}
			b = b[4:]
		default:
			return errAgApp
		}
	}
	return nil
}

// parseAntigravity reads a conversation for the Sessions page: its time and
// each model's tokens. A database that can't be read is no session yet.
func parseAntigravity(f file) *state {
	calls, err := antigravityRead(f.path)
	if err != nil {
		return &state{}
	}
	s := &state{Size: f.size, Mod: f.mod.UnixNano(), DBRevision: f.m().rev}
	if r := f.m().agy; r != nil {
		s.Named, s.Title, s.Cwd = title(r.Title), title(r.Preview), r.Cwd
	}
	for _, c := range calls {
		s.saw(c.at, true)
		s.use(dateOf(c.at), agAppModelOr(c.model), c.tokens)
		s.day(dateOf(c.at)).Replies++
	}
	return s
}

// agAppModelOr is the model a call is counted under: its generator's, else
// the agent's own name.
func agAppModelOr(m string) string {
	if m == "" {
		return "antigravity"
	}
	return m
}

// readAntigravityCalls reads a conversation's calls whole: the app writes
// its steps' rows over again as they finish.
func readAntigravityCalls(f file) (*callFile, bool) {
	st := &callFile{Agent: "antigravity", Path: f.path, Session: strings.TrimPrefix(f.key, "antigravity:")}
	calls, err := antigravityRead(f.path)
	if err != nil {
		return st, false
	}
	for _, c := range calls {
		st.Calls = append(st.Calls, Call{
			Agent: "antigravity", Time: c.at, Session: st.Session, Model: c.model, Tokens: c.tokens,
			Msg: st.str("antigravity:" + st.Session + ":" + strconv.FormatInt(c.step, 10)),
		})
	}
	return st, true
}

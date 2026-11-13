package sessions

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Antigravity CLI's (agy's) calls (#2097, parkavenue9639: a call agy made
// without magpie was counted nowhere). Each conversation keeps a transcript
// of its steps in
// ~/.gemini/antigravity-cli/brain/<conversation id>/.system_generated/logs/transcript_full.jsonl,
// one JSON object a line. A model's step, as agy 1.2.16 writes it (tried in
// a sandbox against a fake Gemini endpoint):
//
//	{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE",
//	 "created_at":"2026-10-14T06:12:41Z","input_tokens":702,"cache_read_tokens":300,
//	 "output_tokens":9,"content":"…"}
//
// Those are the step's own counts: input_tokens is the prompt less what was
// read from the cache, and output_tokens holds the thoughts as well as the
// answer, as the gateway counts a Gemini reply. created_at is when the step
// began, to the second. A transcript an older agy wrote has the same lines
// without the token fields; they are left out, not counted as zero.
//
// The step names no model, nor the account. The model is in the
// conversation's database, conversations/<conversation id>.db beside
// brain/, the layout the Antigravity app keeps (antigravity_app.go: agy's
// language server is the app's): a step's metadata field 20.3 is its
// generator's gen_metadata idx, and the generator's field 1.19 the served
// model's id ("gemini-4-argon"; agy 1.5.6's real rows on #3147 and #4805).
// A generator also names the step it answered in its field 1.20, a list of
// {1 key, 2 value}: "last_step_index" n is step n+1, which stands when the
// step's own row can't be read. A call whose model can't be read keeps
// none (DvnT662 on X: Usage → Requests showed agy's own calls with no
// model). The provider stays unknown. A step agy sent through magpie is
// the gateway's to count: usage's matcher pairs it with the gateway's
// record by its tokens and time (agy sends no session header).

// AgyDir is agy's data folder, ~/.gemini/antigravity-cli.
func AgyDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli")
}

// agyCallFiles are each conversation's transcript_full.jsonl. A file's size
// and time take in its conversation's database and write-ahead log, so a
// model agy writes there after the step is read still reads the calls
// again.
func agyCallFiles() []file {
	out := agyTranscripts(AgyDir())
	for i := range out {
		for _, p := range agyDBFiles(out[i].path) {
			if fi, err := os.Stat(p); err == nil {
				out[i].size += fi.Size()
				if fi.ModTime().After(out[i].mod.Time()) {
					out[i].mod = modOf(fi.ModTime())
				}
			}
		}
	}
	return out
}

// agyConversationDB is the database of the conversation whose transcript
// is at transcript: <data folder>/conversations/<id>.db for
// <data folder>/brain/<id>/.system_generated/logs/transcript_full.jsonl.
func agyConversationDB(transcript string) string {
	conv := filepath.Dir(filepath.Dir(filepath.Dir(transcript)))
	return filepath.Join(filepath.Dir(filepath.Dir(conv)), "conversations", filepath.Base(conv)+".db")
}

// agyDBFiles are the database of transcript's conversation and its
// write-ahead log.
func agyDBFiles(transcript string) []string {
	db := agyConversationDB(transcript)
	return []string{db, db + "-wal"}
}

// agyStepModels is the model of each step of a conversation's database,
// by the step's index; nil when the database can't be read.
func agyStepModels(path string) map[int]string {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := provider.OpenReadOnly(path)
	if err != nil {
		return nil
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	out := map[int]string{}
	gens := map[int64]string{}
	rows, err := tx.Query("SELECT idx, data FROM gen_metadata WHERE data IS NOT NULL")
	if err != nil {
		return nil
	}
	for rows.Next() {
		var idx int64
		var b []byte
		if rows.Scan(&idx, &b) != nil {
			continue
		}
		m := agAppModel(b)
		if m == "" {
			continue
		}
		gens[idx] = m
		if step, ok := agyGenStep(b); ok {
			out[step] = m
		}
	}
	rows.Close()
	rows, err = tx.Query("SELECT idx, metadata FROM steps WHERE metadata IS NOT NULL")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var idx int64
		var b []byte
		if rows.Scan(&idx, &b) != nil {
			continue
		}
		// a step's place names its generator, generator 0 by leaving it
		// out (as antigravityRead reads it)
		var gen int64
		if pbWalk(b, func(num int, v uint64, d []byte) {
			if num == 20 && d != nil {
				_ = pbWalk(d, func(n int, v uint64, d []byte) {
					if n == 3 && d == nil {
						gen = int64(v)
					}
				})
			}
		}) != nil {
			continue
		}
		if m, ok := gens[gen]; ok {
			out[int(idx)] = m
		}
	}
	return out
}

// agyGenStep is the step a generator answered: its "last_step_index" n,
// in field 1.20, is step n+1.
func agyGenStep(b []byte) (int, bool) {
	step, ok := 0, false
	_ = pbWalk(b, func(num int, v uint64, d []byte) {
		if num != 1 || d == nil {
			return
		}
		_ = pbWalk(d, func(n int, v uint64, d []byte) {
			if n != 20 || d == nil {
				return
			}
			var key, val string
			_ = pbWalk(d, func(n int, v uint64, d []byte) {
				switch {
				case d == nil:
				case n == 1:
					key = string(d)
				case n == 2:
					val = string(d)
				}
			})
			if key == "last_step_index" {
				if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && i >= 0 {
					step, ok = i+1, true
				}
			}
		})
	})
	return step, ok
}

type agyStep struct {
	Step      *int   `json:"step_index"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`
	Input     *int   `json:"input_tokens"`
	CacheRead *int   `json:"cache_read_tokens"`
	Output    *int   `json:"output_tokens"`
}

var agyTokens = []byte(`"output_tokens"`)

// readAgyCalls reads a transcript whole: agy may write a step's line again
// as it finishes, and a step is counted once by its index.
func readAgyCalls(f file) (*callFile, bool) {
	r, err := os.Open(f.path)
	if err != nil {
		return &callFile{Path: f.path, Agent: "agy"}, false
	}
	defer r.Close()
	st := &callFile{Agent: "agy", Path: f.path, Session: strings.TrimPrefix(f.key, "agy:")}
	at := map[int]int{} // a step's index → its call in st.Calls
	br := bufio.NewReaderSize(r, scanBuffer(r, 0))
	for {
		b, err := br.ReadBytes('\n')
		if err != nil && (!errors.Is(err, io.EOF) || len(bytes.TrimSpace(b)) == 0) {
			break
		}
		b = bytes.TrimSpace(b)
		if len(b) > 0 && len(b) <= maxLine && bytes.Contains(b, agyTokens) {
			var s agyStep
			if json.Unmarshal(b, &s) == nil {
				if c, ok := agyCall(st, s); ok {
					if i, seen := at[*s.Step]; seen {
						st.Calls[i] = c
					} else {
						at[*s.Step] = len(st.Calls)
						st.Calls = append(st.Calls, c)
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	if len(at) > 0 {
		if models := agyStepModels(agyConversationDB(f.path)); len(models) > 0 {
			for step, i := range at {
				st.Calls[i].Model = models[step]
			}
		}
	}
	return st, true
}

// agyCall is a model's step as a call.
func agyCall(st *callFile, s agyStep) (Call, bool) {
	if s.Source != "MODEL" || s.Step == nil || s.Input == nil || s.Output == nil {
		return Call{}, false
	}
	t, err := time.Parse(time.RFC3339, s.CreatedAt)
	if err != nil {
		return Call{}, false
	}
	c := Call{Agent: "agy", Time: t, Session: st.Session, Tokens: Tokens{Input: *s.Input, Output: *s.Output}}
	if s.CacheRead != nil {
		c.CacheRead = *s.CacheRead
	}
	if c.Tokens.zero() {
		return Call{}, false
	}
	c.Msg = st.str("agy:" + st.Session + ":" + strconv.Itoa(*s.Step))
	return c, true
}

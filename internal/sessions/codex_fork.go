package sessions

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A Codex fork (the CLI's /fork, the app's fork, a spawn_agent with the
// parent's context) starts a rollout of its own and writes every item of
// its parent's into it first, token_count events included, as they were
// (codex-rs record_initial_history, InitialHistory::Forked). Each copied
// line is stamped when the fork wrote it, so a copied checkpoint is the
// parent's call again, at the fork's time, and read as one it counted the
// parent's whole history a second time on the day of the fork (Sky,
// skyhuang1994: a week of 5.1B gpt-5.4 tokens where the machine had spent
// ~0.27B). The fork's own session_meta names the parent (forked_from_id),
// and a checkpoint the parent's rollout holds, its running total and its
// last call alike, is a copy: the parent's file counts it. A fork of a fork
// holds its grandparent's lines through its parent's file, which holds
// them too. When the parent's rollout can't be found or read, the fork's
// lines are read as before.
//
// The copies are the head of the fork's rollout and nothing after it: a
// fork goes on working, and its own calls are its own whatever their
// numbers (Sky, skyhuang1994: forks that went on working lost their own
// usage). The fork's first turn the parent never had, a turn_id of its
// own, ends the copies. A rollout with no turn ids is matched by
// checkpoint alone: its running total, which a fork's own call moves past
// the parent's, keeps a later call of either from matching.

// cxCheckpoint is a token_count event's two counters, as written.
type cxCheckpoint struct{ Total, Last cxUsage }

// forkedFrom notes, from the first session_meta of the rollout at path,
// the parent the rollout was forked from, and reads that parent's
// checkpoints.
func (s *codexUsageState) forkedFrom(path, parent string) {
	if parent == "" || path == "" {
		return
	}
	if p := codexRolloutOf(path, parent); p != "" {
		s.Copied, s.CopiedTurns = codexCheckpointsOf(p)
	}
}

// ownTurn ends the copies at a turn of the fork's own.
func (s *codexUsageState) ownTurn(turn string) {
	if turn != "" && s.Copied != nil && s.CopiedTurns != nil && !s.CopiedTurns[turn] {
		s.Copied, s.CopiedTurns = nil, nil
	}
}

// copied says whether a checkpoint is one of the parent's, copied in.
func (s *codexUsageState) copied(total, last *cxUsage) bool {
	if len(s.Copied) == 0 || total == nil || last == nil {
		return false
	}
	return s.Copied[cxCheckpoint{*total, *last}]
}

// codexForks keeps the checkpoints of the parents last read, by path,
// size and time: forks a spawn_agent makes are of one parent, often many
// at a time.
var codexForks struct {
	sync.Mutex
	kept []codexForkParent
}

type codexForkParent struct {
	path  string
	size  int64
	mod   time.Time
	marks map[cxCheckpoint]bool
	turns map[string]bool
}

const codexForksKept = 8

// codexCheckpointsOf are the token_count checkpoints of the rollout at
// path, and its turn ids (nil when it names none); nil when it can't be
// read whole.
func codexCheckpointsOf(path string) (map[cxCheckpoint]bool, map[string]bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil
	}
	codexForks.Lock()
	for _, k := range codexForks.kept {
		if k.path == path && k.size == fi.Size() && k.mod.Equal(fi.ModTime()) {
			codexForks.Unlock()
			return k.marks, k.turns
		}
	}
	codexForks.Unlock()
	marks := map[cxCheckpoint]bool{}
	var turns map[string]bool
	err = eachLine(path, func(b []byte) {
		h := b[:min(len(b), 1024)]
		top, payload := typeAfter(h, cxType), typeAfter(h, cxPayload)
		if top == "turn_context" || top == "event_msg" && payload == "task_started" {
			var l struct {
				Payload struct {
					TurnID string `json:"turn_id"`
				} `json:"payload"`
			}
			if json.Unmarshal(b, &l) == nil && l.Payload.TurnID != "" {
				if turns == nil {
					turns = map[string]bool{}
				}
				turns[l.Payload.TurnID] = true
			}
			return
		}
		if top != "event_msg" || payload != "token_count" || bytes.Contains(h, cxInfo) {
			return
		}
		total, ok := cxUsageIn(b, cxTotal)
		if !ok {
			return
		}
		if last, ok := cxUsageIn(b, cxLast); ok {
			marks[cxCheckpoint{*total, *last}] = true
		}
	})
	if err != nil {
		return nil, nil
	}
	codexForks.Lock()
	codexForks.kept = append(codexForks.kept, codexForkParent{path, fi.Size(), fi.ModTime(), marks, turns})
	if n := len(codexForks.kept); n > codexForksKept {
		codexForks.kept = append([]codexForkParent(nil), codexForks.kept[n-codexForksKept:]...)
	}
	codexForks.Unlock()
	return marks, turns
}

// codexRolloutOf is the rollout of thread id in the Codex folder that
// holds the rollout at path, "" when there is none. Codex files a rollout
// under sessions/YYYY/MM/DD, the local day it began, and a thread id is a
// UUIDv7, which carries that time: the days around it are looked in
// first, then every day, then archived_sessions.
func codexRolloutOf(path, id string) string {
	home := codexHomeOf(path)
	if home == "" || strings.ContainsAny(id, `/\*?[`) {
		return ""
	}
	name := "rollout-*-" + id + "*.jsonl*"
	match := func(pattern string) string {
		found, _ := filepath.Glob(pattern)
		for _, p := range found {
			if m := rolloutName.FindStringSubmatch(filepath.Base(p)); m != nil && m[1] == id && p != path {
				return p
			}
		}
		return ""
	}
	sessions := filepath.Join(home, "sessions")
	if at, ok := uuidV7Time(id); ok {
		for _, d := range []int{0, -1, 1} {
			day := at.AddDate(0, 0, d).Local()
			if p := match(filepath.Join(sessions, day.Format("2006"), day.Format("01"), day.Format("02"), name)); p != "" {
				return p
			}
		}
	}
	if p := match(filepath.Join(sessions, "*", "*", "*", name)); p != "" {
		return p
	}
	return match(filepath.Join(home, "archived_sessions", name))
}

// uuidV7Time is the time a UUIDv7 was made, its first 48 bits.
func uuidV7Time(id string) (time.Time, bool) {
	h := strings.ReplaceAll(id, "-", "")
	if len(h) != 32 || h[12] != '7' {
		return time.Time{}, false
	}
	b, err := hex.DecodeString(h[:12])
	if err != nil {
		return time.Time{}, false
	}
	var ms int64
	for _, c := range b {
		ms = ms<<8 | int64(c)
	}
	return time.UnixMilli(ms), true
}

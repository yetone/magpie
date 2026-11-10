package usage

import (
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Clearing the Usage page's figures (CrazyMouse_TT on X: a local model
// deployed again and benchmarked, its requests, speed and cache wanted from
// nothing). A clear is kept in settings (settings.UsageCleared) and read
// over whatever the pages count, never written into usage.jsonl: the log
// keeps every call, so a key's spending limit (package budget), the
// allowances' window holds and the gateway's own reads of it (Visit) still
// count the calls cleared, and the agents' session files and other
// computers' shared days, which magpie can't rewrite, are cleared the same
// way as its own log.

// clearSet is the user's clears as last read, the usage aliases a model's
// clear takes in, and a key for the caches built on what they hide.
type clearSet struct {
	marks []settings.UsageClear
	alias func(string) string
	// all is the latest clear of everything, zero for none
	all time.Time
	key string
}

var clearedAs atomic.Pointer[clearSet]

// readCleared reads the user's clears again, for a pass over the usage:
// each record of the pass is then judged by what was read here
// (clearedNow). It reads settings.json, so it is called before any lock a
// reader of the usage waits on.
func readCleared() *clearSet {
	marks := settings.UsageClearedNow()
	names := ""
	if len(marks) > 0 {
		names = provider.UsageNamesKey()
	}
	b, _ := json.Marshal(struct {
		M []settings.UsageClear
		N string
	}{marks, names})
	key := string(b)
	if cur := clearedAs.Load(); cur != nil && cur.key == key {
		return cur
	}
	c := &clearSet{marks: marks, key: key}
	if len(marks) > 0 {
		c.alias = provider.UsageAlias()
	}
	for _, m := range marks {
		if m.Provider == "" && m.Model == "" && m.Time().After(c.all) {
			c.all = m.Time()
		}
	}
	clearedAs.Store(c)
	return c
}

// clearedKey is what the caches of counted records key on.
func clearedKey() string { return readCleared().key }

// clearedNow is the clears the last readCleared read; nil before any.
func clearedNow() *clearSet { return clearedAs.Load() }

// hides is whether the user cleared r from the usage pages: a clear of
// everything, or of its provider's model, made at or after its time. r's
// provider is the one it is shown under (renameBy) and its model is taken
// by its own id or by the usage alias it is counted under, as the
// Overview's Models list names it.
func (c *clearSet) hides(r *Record) bool {
	if c == nil || len(c.marks) == 0 {
		return false
	}
	if !c.all.IsZero() && !r.Time.After(c.all) {
		return true
	}
	model := ""
	for _, m := range c.marks {
		if m.Model == "" || r.Time.After(m.Time()) || m.Provider != r.Provider {
			continue
		}
		if r.Model == m.Model {
			return true
		}
		if c.alias != nil {
			if model == "" {
				model = c.alias(r.Model)
			}
			if model == c.alias(m.Model) {
				return true
			}
		}
	}
	return false
}

// clearedSince is when the usage counted begins after a clear of
// everything: first, or the clear's time when it is later.
func (c *clearSet) clearedSince(first time.Time) time.Time {
	if c == nil || c.all.IsZero() || !first.IsZero() && first.After(c.all) {
		return first
	}
	return c.all
}

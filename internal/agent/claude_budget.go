package agent

import (
	"cmp"
	"encoding/json"
	"strconv"
	"strings"
)

// While Claude Desktop is on magpie, its Code tab runs Claude Code on
// ~/.claude/settings.json with every model Desktop lists, and Claude Code
// takes one window (claudeContextEnv) for all of those it doesn't know.
// The least of them held every one to it: one 131K image model beside a
// 500K GPT ran the GPT at 131K too (#2025). So magpie tells Claude Code
// the largest window instead, and holds each model below it to its own
// with modelSettings.<id>.autoCompactWindow, which Claude Code (2.1.296)
// looks up by the id it was handed (lowercased, "[1m]" dropped) and takes
// before the top-level autoCompactWindow. CLAUDE_CODE_AUTO_COMPACT_WINDOW
// would take precedence over both, so magpie's working window
// (settings.WorkingWindow) goes in the top-level autoCompactWindow
// instead while it does this.

// claudeBudget is a model Claude Code may run on through magpie while
// Claude Desktop is on it.
type claudeBudget struct {
	id     string // as Claude Code is handed it: its key under modelSettings
	window int    // its window, 0 when unknown
	long   bool   // Claude Code takes it for 1M by its id ("[1m]")
	// the auto-compact window magpie gives it: 0 for the top-level one
	// (base), below 0 for none, its whole window
	compact int
	// Claude Code runs it as a Claude model it knows (a Desktop tier's
	// pick, behavesAs: gateway.DesktopBehavesAs), and so at that model's
	// window, not at the one it is told: it always gets its own entry
	behaves bool
}

// claudeLong is the window Claude Code takes a "[1m]" model for.
const claudeLong = 1000000

// budgetKey is a modelSettings key as Claude Code matches it.
func budgetKey(id string) string { return strings.ToLower(strings.TrimSuffix(id, "[1m]")) }

// budgetKeyOK says id can go under modelSettings as a key path magpie's
// JSON editor writes: a dot is escaped, gjson's other path characters
// aren't, so a model whose id has one gets no entry.
func budgetKeyOK(id string) bool {
	return id != "" && !strings.ContainsAny(id, `*?|#@\"`)
}

// budgetPath is the key path of id's auto-compact window.
func budgetPath(id string) string {
	return "modelSettings." + strings.ReplaceAll(strings.TrimSuffix(id, "[1m]"), ".", `\.`) + ".autoCompactWindow"
}

// claudePlan is the window to tell Claude Code (top) and the
// auto-compact window to give each model whose own differs from what it
// would run at without one (entries, by id without "[1m]"). base is the
// top-level autoCompactWindow in the file, magpie's or the user's, 0 for
// none; theirs are the user's own modelSettings windows by budgetKey,
// which magpie never writes over. A model the user gave a window past its
// own ("auto" too, which runs it to the one Claude Code is told) holds
// top to its own window, as magpie can't put theirs right.
func claudePlan(models []claudeBudget, base int, theirs map[string]string) (top int, entries map[string]int) {
	for _, m := range models {
		switch {
		case !m.long && m.window > top:
			top = m.window
		case m.long:
			// a long one's window is the one told too, so it runs to its
			// own should Claude Code stop taking it for 1M (the 1M beta
			// turned away once, CLAUDE_CODE_DISABLE_1M_CONTEXT), and
			// nothing written reads as holding it to a smaller model's: a
			// 1M model Desktop lists by its 1M id alone beside a 272K GPT
			// ran at 272K (dumplings on Discord, Full window on)
			top = max(top, cmp.Or(m.window, claudeLong))
		}
	}
	for _, m := range models {
		v, ok := theirs[budgetKey(m.id)]
		if !ok || m.long || m.window <= 0 {
			continue
		}
		if n, err := strconv.Atoi(v); err != nil || n > m.window {
			top = min(top, m.window)
		}
	}
	if top <= 0 {
		return 0, nil
	}
	entries = map[string]int{}
	seen := map[string]string{}
	for _, m := range models {
		k := budgetKey(m.id)
		if _, own := theirs[k]; own || !budgetKeyOK(m.id) {
			continue
		}
		assumed := top
		if m.long {
			assumed = claudeLong
		}
		want := assumed
		if m.window > 0 && !m.long {
			want = min(m.window, top)
		}
		switch {
		case m.compact > 0:
			want = min(want, m.compact)
		case m.compact == 0 && base > 0:
			want = min(want, base)
		}
		had := assumed
		if base > 0 {
			had = min(base, assumed)
		}
		if want == had && !m.behaves {
			continue
		}
		id := strings.TrimSuffix(m.id, "[1m]")
		if was, dup := seen[k]; dup {
			// two ids Claude Code matches as one: the smaller window
			if want < entries[was] {
				entries[was] = want
			}
			continue
		}
		seen[k] = id
		entries[id] = want
	}
	return top, entries
}

// budgetsRead is the modelSettings entries' autoCompactWindow in raw, by
// key as written: a number's digits, a string's text.
func budgetsRead(raw string) map[string]string {
	var per map[string]map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &per) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range per {
		w, ok := v["autoCompactWindow"]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(w, &s) != nil {
			s = string(w)
		}
		out[k] = s
	}
	return out
}

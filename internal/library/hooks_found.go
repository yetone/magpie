package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"
)

// Hooks the agents run that magpie didn't give them (@InkCoalW on X: read
// and manage the hooks an agent already uses, and clean them up): an entry
// in an agent's own hooks file that no record of magpie's claims, as the
// user or another tool wrote it. The Hooks tab lists them under "In your
// agents", as the MCP and Skills tabs list the servers and skills the
// agents have of their own. One can be brought into the library, from then
// on magpie's like any hook it wrote (unticked, edited, given to the other
// agents), or taken out of the agent's file, the file copied to the backups
// first, so Backups puts it back.

// FoundHook is one entry of an agent's hooks file that magpie didn't write.
type FoundHook struct {
	// Key names the entry for ImportHooks and RemoveFoundHooks: the agent,
	// its event, the entry's text and which of the same entries it is
	Key   string `json:"key"`
	Agent string `json:"agent"`
	// Event is the agent's own name for it ("PostToolUse")
	Event   string `json:"event"`
	Matcher string `json:"matcher,omitempty"`
	// Commands are what it runs: a group can hold several, and a handler
	// that isn't a command is named by its type, "(prompt)"
	Commands []string `json:"commands"`
	// Timeout in seconds, when the entry sets one
	Timeout int    `json:"timeout,omitempty"`
	File    string `json:"file"`
	// Hook is what bringing it in makes it; nil when the library can't
	// keep it as it is written (Why: "several", "type" or "shape"), and
	// then it can only be removed
	Hook *Hook  `json:"hook,omitempty"`
	Why  string `json:"why,omitempty"`
	// Joins is the library hook it is the same as, for another agent:
	// bringing it in ticks this agent for that hook
	Joins string `json:"joins,omitempty"`
	// Own: written by a tool magpie runs from another tab ("rtk", RTK's
	// installer), which takes it out there; the page doesn't list it
	Own string `json:"own,omitempty"`

	entry json.RawMessage
	canon string // the entry written one way, as the records compare it
}

// foundOwner is the tool of magpie's another tab that writes a hook with
// this command: RTK's installer writes "rtk hook <agent>" (rtk.go), Gemini's
// as rtk-hook-gemini.
func foundOwner(cmds []string) string {
	for _, c := range cmds {
		if strings.Contains(c, "rtk hook ") || strings.Contains(c, "rtk-hook-") {
			return "rtk"
		}
	}
	return ""
}

// foundHooksOf lists the hooks of these agents that no record of magpie's
// claims. A file that can't be read is left out: what it holds is unknown,
// and it is said on the library's hooks where it matters.
func foundHooksOf(l *Library, targets []*Target) []FoundHook {
	out := []FoundHook{}
	for _, t := range targets {
		if t.Gone || t.Agent.ID == "dsh" {
			// dsh's magpie-hooks.json is magpie's own file, every entry of it
			continue
		}
		sp, _ := hookSpecOf(t)
		if sp == nil {
			continue
		}
		evs, lists, err := hookLists(sp)
		if err != nil {
			continue
		}
		claimed := map[string]map[int]bool{}
		for _, rec := range l.applied(t.Agent.ID).Hooks {
			c := canonical(rec.Entry)
			for i, e := range lists[rec.Event] {
				if !claimed[rec.Event][i] && canonical(e) == c {
					if claimed[rec.Event] == nil {
						claimed[rec.Event] = map[int]bool{}
					}
					claimed[rec.Event][i] = true
					break
				}
			}
		}
		for _, ev := range evs {
			seen := map[string]int{}
			for i, e := range lists[ev] {
				if claimed[ev][i] {
					continue
				}
				c := canonical(e)
				n := seen[c]
				seen[c]++
				f := foundHook(l, t.Agent.ID, sp, ev, e)
				f.Key, f.canon = foundKey(t.Agent.ID, ev, c, n), c
				out = append(out, f)
			}
		}
	}
	return out
}

func foundKey(agent, ev, canon string, n int) string {
	s := sha256.Sum256([]byte(agent + "\x00" + ev + "\x00" + canon + "\x00" + strconv.Itoa(n)))
	return hex.EncodeToString(s[:8])
}

// hookLists is every event's list in the agent's hooks file, the events in
// the file's order.
func hookLists(sp *hookSpec) ([]string, map[string][]json.RawMessage, error) {
	lists := map[string][]json.RawMessage{}
	var evs []string
	if sp.toml {
		for _, ev := range sp.native {
			x, err := grokHookBlocks(sp.path, ev)
			if err != nil {
				return nil, nil, err
			}
			if len(x) > 0 {
				evs = append(evs, ev)
				lists[ev] = x
			}
		}
		return evs, lists, nil
	}
	doc, err := readHooksFile(sp.path)
	if err != nil || doc == nil {
		return nil, lists, err
	}
	var failed error
	gjson.GetBytes(doc, "hooks").ForEach(func(k, v gjson.Result) bool {
		if !v.IsArray() {
			return true // not a list of hooks: nothing the agent runs, left as it is
		}
		var list []json.RawMessage
		if err := json.Unmarshal([]byte(v.Raw), &list); err != nil {
			failed = err
			return false
		}
		evs = append(evs, k.String())
		lists[k.String()] = list
		return true
	})
	return evs, lists, failed
}

// foundHook reads one entry: what it runs, and the library hook it would
// be, checked by writing that hook back the way magpie writes it: only an
// entry that comes out the same is brought in, so nothing of the user's
// (a second command, async, a status message) is lost on the next write.
func foundHook(l *Library, id string, sp *hookSpec, ev string, e json.RawMessage) FoundHook {
	f := FoundHook{Agent: id, Event: ev, File: sp.path, Commands: []string{}, entry: e}
	var group map[string]any
	if sp.toml {
		blk, _ := grokBlockText(e)
		var doc map[string]any
		if toml.Unmarshal([]byte(blk), &doc) == nil {
			hs, _ := doc["hooks"].(map[string]any)
			list, _ := hs[ev].([]any)
			if len(list) == 1 {
				group, _ = list[0].(map[string]any)
			}
		}
	} else {
		_ = json.Unmarshal(e, &group)
	}
	var handlers []map[string]any
	if sp.flat {
		if group != nil {
			handlers = []map[string]any{group}
		}
	} else if group != nil {
		hs, _ := group["hooks"].([]any)
		for _, h := range hs {
			if m, ok := h.(map[string]any); ok {
				handlers = append(handlers, m)
			}
		}
	}
	if group != nil {
		f.Matcher, _ = group["matcher"].(string)
	}
	why := ""
	for _, h := range handlers {
		typ, _ := h["type"].(string)
		cmd, _ := h["command"].(string)
		if (typ == "" || typ == "command") && cmd != "" {
			f.Commands = append(f.Commands, cmd)
		} else {
			if typ == "" {
				typ = "?"
			}
			f.Commands = append(f.Commands, "("+typ+")")
			why = "type"
		}
	}
	if len(handlers) == 1 {
		f.Timeout = numberOf(handlers[0]["timeout"])
		if sp.ms {
			f.Timeout /= 1000
		}
	}
	f.Own = foundOwner(f.Commands)
	switch {
	case why != "":
	case len(handlers) != 1:
		why = "several"
	default:
		if h := adoptable(id, sp, ev, e, f.Matcher, f.Commands[0], numberOf(handlers[0]["timeout"])); h != nil {
			f.Hook = h
			for _, x := range l.Hooks {
				if x.Event == h.Event && x.Matcher == h.Matcher && x.Command == h.Command && x.Timeout == h.Timeout && hookTakes(x, id) {
					f.Joins = x.Name
					h.Name = x.Name
					break
				}
			}
			if f.Joins == "" {
				h.Name = freeHookName(l, h.Command)
			}
		} else {
			why = "shape"
		}
	}
	f.Why = why
	return f
}

func numberOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int64:
		return int(n)
	}
	return 0
}

// adoptable is the library hook entry e is, under event ev of the agent, or
// nil when no hook magpie writes comes out as e: a portable one when its
// event and matcher are the portable one's, else the agent's own event as
// written.
func adoptable(id string, sp *hookSpec, ev string, e json.RawMessage, matcher, cmd string, timeout int) *Hook {
	if sp.ms {
		if timeout%1000 != 0 {
			return nil
		}
		timeout /= 1000
	}
	var tries []Hook
	for _, p := range HookEvents {
		if sp.events[p] != ev {
			continue
		}
		switch {
		case matcher == "":
			tries = append(tries, Hook{Event: p})
		case slices.Contains(toolEvents, p):
			for _, k := range HookTools {
				if sp.tools[k] == matcher {
					tries = append(tries, Hook{Event: p, Matcher: k})
				}
			}
		case p == "session-start":
			tries = append(tries, Hook{Event: p, Matcher: matcher})
			if g := grokStarts[matcher]; g != nil {
				tries = append(tries, Hook{Event: p, Matcher: strings.Join(g, "|")})
			}
		}
	}
	if slices.Contains(sp.native, ev) {
		tries = append(tries, Hook{Event: id + ":" + ev, Matcher: matcher})
	}
	want := canonical(e)
	for _, h := range tries {
		h.Name, h.Command, h.Timeout, h.Agents = "found", cmd, timeout, []string{id}
		if h.check() != nil || !hookTakes(&h, id) {
			continue
		}
		if canonical(sp.entry(&h, ev)) == want {
			return &h
		}
	}
	return nil
}

// freeHookName is a name for a hook running cmd that the library doesn't
// have yet: the program it runs, "say" for "say done".
func freeHookName(l *Library, cmd string) string {
	base := "hook"
	for _, w := range strings.Fields(cmd) {
		if strings.Contains(w, "=") && !strings.ContainsAny(w, "/\\") {
			continue // FOO=1 before the program
		}
		w = strings.Trim(w, `"'`)
		w = filepath.Base(strings.ReplaceAll(w, `\`, "/"))
		w = strings.TrimSuffix(w, filepath.Ext(w))
		var b strings.Builder
		for _, r := range strings.ToLower(w) {
			if r < 128 && (r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if s := strings.Trim(b.String(), "-_"); s != "" {
			base = s
		}
		break
	}
	if len(base) > 40 {
		base = base[:40]
	}
	name := base
	for n := 2; l.hook(name) != nil; n++ {
		name = base + "-" + strconv.Itoa(n)
	}
	return name
}

func pickFound(found []FoundHook, keys []string) ([]FoundHook, error) {
	if len(keys) == 0 {
		return nil, errors.New("no hooks named")
	}
	var out []FoundHook
	for _, k := range slices.Compact(slices.Sorted(slices.Values(keys))) {
		i := slices.IndexFunc(found, func(f FoundHook) bool { return f.Key == k })
		if i < 0 {
			return nil, errors.New("that hook is no longer in the agent's file: it changed since the page was drawn")
		}
		out = append(out, found[i])
	}
	return out, nil
}

// ImportHooks brings the hooks named into the library: each is the
// library's from then on, given to the agent it was found in, the entry
// there left exactly as it is. One the same as a library hook given to
// other agents ticks this agent for that hook.
func ImportHooks(keys []string) (*Result, error) {
	return change(func(l *Library) error {
		found, err := pickFound(foundHooksOf(l, Targets()), keys)
		if err != nil {
			return err
		}
		for _, f := range found {
			if f.Hook == nil {
				return fmt.Errorf("magpie can't keep %s's %s hook as it is written: it can only be removed", f.Agent, f.Event)
			}
			// the same hook in the library, one brought in a moment ago
			// from another agent too, takes this agent as well
			var h *Hook
			for _, x := range l.Hooks {
				if x.Event == f.Hook.Event && x.Matcher == f.Hook.Matcher && x.Command == f.Hook.Command && x.Timeout == f.Hook.Timeout && hookTakes(x, f.Agent) {
					h = x
					break
				}
			}
			if h == nil {
				c := *f.Hook
				c.Name = freeHookName(l, c.Command)
				h = &c
				l.Hooks = append(l.Hooks, h)
			}
			h.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(h.Agents), f.Agent)))))
			a := l.applied(f.Agent)
			a.Hooks = append(a.Hooks, HookEntry{Name: h.Name, Event: f.Event, Entry: f.entry})
		}
		return nil
	})
}

// foundSentinel names the record of a found hook being taken out, which
// no library hook can be called.
const foundSentinel = "\x00found"

// RemoveFoundHooks takes the hooks named out of their agents' files, each
// file copied to the backups first: Backups puts one back. The rest of
// each file stays as it is, and Codex's trust of the hooks after one moves
// with them.
func RemoveFoundHooks(keys []string) (*Result, error) {
	return change(func(l *Library) error {
		all := foundHooksOf(l, Targets())
		found, err := pickFound(all, keys)
		if err != nil {
			return err
		}
		b := l.kept
		if b == nil {
			b = newBackups()
			l.kept = b
		}
		byAgent := map[string][]FoundHook{}
		for _, f := range found {
			byAgent[f.Agent] = append(byAgent[f.Agent], f)
		}
		for _, id := range slices.Sorted(func(yield func(string) bool) {
			for k := range byAgent {
				if !yield(k) {
					return
				}
			}
		}) {
			t := targetByID(id)
			if t == nil {
				return fmt.Errorf("%s isn't here", id)
			}
			sp, why := hookSpecOf(t)
			if sp == nil {
				return errors.New(why)
			}
			a := l.applied(id)
			var want []*Hook
			for _, h := range l.Hooks {
				if slices.Contains(h.Agents, id) {
					want = append(want, h)
				}
			}
			recs := slices.Clone(a.Hooks)
			for _, f := range byAgent[id] {
				recs = append(recs, HookEntry{Name: foundSentinel, Event: f.Event, Entry: f.entry})
			}
			// a failure of the found entries' own, or of the whole file,
			// stops it; a library hook's own (an event the agent lacks) is
			// the sync's to say
			var failed, first error
			w := &hookWrite{id: id, sp: sp, want: want, recs: recs, gone: t.Gone,
				keep: func(path string) error { return b.keep(id, path) },
				fail: func(what string, err error) {
					if first == nil {
						first = err
					}
					if failed == nil && (what == "hooks" || what == "hook:"+foundSentinel) {
						failed = err
					}
				}}
			if id == "codex" {
				w.rekeyCfg = filepath.Join(codexDir(), "config.toml")
			}
			got, _, ok := w.run()
			if !ok {
				if failed == nil {
					failed = first
				}
				if failed == nil {
					failed = fmt.Errorf("%s couldn't be written", sp.path)
				}
				return fmt.Errorf("%s: %w", t.Agent.Name, failed)
			}
			if failed != nil {
				return fmt.Errorf("%s: %w", t.Agent.Name, failed)
			}
			a.Hooks = slices.DeleteFunc(got, func(r HookEntry) bool { return r.Name == foundSentinel })
		}
		// read back: as many of each entry as there were, less those taken
		// out, or the one left is said, not reported gone
		count := func(fs []FoundHook) map[string]int {
			m := map[string]int{}
			for _, f := range fs {
				m[f.Agent+"\x00"+f.Event+"\x00"+f.canon]++
			}
			return m
		}
		before, after, gone := count(all), count(foundHooksOf(l, Targets())), count(found)
		for _, f := range found {
			k := f.Agent + "\x00" + f.Event + "\x00" + f.canon
			if after[k] > before[k]-gone[k] {
				return fmt.Errorf("the %s hook is still in %s", f.Event, f.File)
			}
		}
		return nil
	})
}

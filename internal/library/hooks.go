package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
)

// Hooks (#1790) are commands an agent runs at a point of its turn: when
// it ends (a phone notification), after it edits a file (a formatter).
// One is kept once and written into each agent ticked for it, in that
// agent's own file and shape:
//
//	Claude Code  <claude dir>/settings.json  hooks.<Event>: [{matcher, hooks: [{type, command, timeout}]}]
//	Codex        <codex home>/hooks.json     the same shape
//	Gemini CLI   ~/.gemini/settings.json     the same shape, timeout in ms
//	Cursor       ~/.cursor/hooks.json        hooks.<event>: [{command, timeout, matcher}], version 1
//
// A portable event ("turn-end") is the same moment in each agent, under
// each one's own name; an agent's own event ("codex:PermissionRequest")
// is given to that agent alone. What magpie wrote is recorded entry by
// entry (Applied.Hooks), and only an entry still exactly as magpie wrote
// it is ever taken out: one the user changed is theirs from then on, and
// the user's own hooks around it stay as they are.

// Hook is a command the agents ticked for it run at Event.
type Hook struct {
	Name string `json:"name"`
	// Description says what the hook is for, in any language (#2011):
	// the name is letters, digits, - and _, which says little to someone
	// who reads Chinese. Only the library keeps it; no agent is given it.
	Description string `json:"description,omitempty"`
	// Event is a portable event (HookEvents), or an agent's own,
	// "<agent>:<Event>" as that agent names it
	Event string `json:"event"`
	// Matcher narrows a tool event to some tools: for a portable event
	// "shell" or "edit" (each agent's own tool names for them), for an
	// agent's own event the agent's own matcher; "" is every tool. A
	// session-start hook is narrowed to the starts it runs at instead
	// (#5604): one or more of HookStarts joined by "|", "compact"
	Matcher string   `json:"matcher,omitempty"`
	Command string   `json:"command"`
	Timeout int      `json:"timeout,omitempty"` // seconds; 0 is the agent's own
	Agents  []string `json:"agents"`
}

// HookEntry is one entry magpie wrote into an agent's hooks: the hook it
// is of, the agent's event it is under, and the entry exactly as written.
type HookEntry struct {
	Name  string          `json:"name"`
	Event string          `json:"event"`
	Entry json.RawMessage `json:"entry"`
}

// HookEvents are the portable events, in the order the page offers them.
var HookEvents = []string{"turn-end", "after-tool", "before-tool", "prompt", "session-start"}

// HookTools are the kinds of tools a portable tool event can be narrowed to.
var HookTools = []string{"shell", "edit"}

// HookStarts are the starts a portable session-start hook can be narrowed
// to (#5604, NEKO-CwC), in Claude Code's words for them (its SessionStart
// source): a new session, one resumed, one after /clear, one after its
// context was compacted, and a forked one.
var HookStarts = []string{"startup", "resume", "clear", "compact", "fork"}

// startSpec is how an agent's session start can be narrowed: the starts it
// has (a hook narrowed to others is one it would never run), and how its
// matcher names a set of them.
type startSpec struct {
	sources []string
	// one: its matcher is one source, compared whole (Gemini CLI's
	// hookPlanner.matchesTrigger is matcher === trigger)
	one bool
	// groups: its matcher names a group of them, not each one (Grok's
	// "new" is a fresh session and one after /clear alike)
	groups map[string][]string
}

// startSpecs are the agents whose session start takes a matcher, from
// each one's own source. Cursor has none: its importer of Claude Code's
// hooks says a SessionStart matcher "is not supported in Cursor, hooks
// will fire for all triggers".
var startSpecs = map[string]*startSpec{
	// claude 2.1.316: SessionStart's matcher is tested on its source,
	// startup|resume|clear|compact|fork, as a tool's name is
	"claude": {sources: HookStarts},
	// codex-rs/hooks: SessionStartSource is the same five, and a matcher of
	// names and "|" is matched name by name (engine/matcher.rs)
	"codex": {sources: HookStarts},
	// gemini-cli: SessionStartSource is startup, resume and clear, and the
	// matcher is compared whole
	"gemini": {sources: []string{"startup", "resume", "clear"}, one: true},
	// the Grok CLI (docs/user-guide/10-hooks.md): the start source is "new"
	// for a fresh session, /new and /clear included, or "load" for one
	// resumed; Claude Code's own words never match there
	"grok":     {sources: []string{"startup", "resume", "clear"}, groups: grokStarts},
	"grok-app": {sources: []string{"startup", "resume", "clear"}, groups: grokStarts},
}

var grokStarts = map[string][]string{"new": {"startup", "clear"}, "load": {"resume"}}

// startSet is a session-start matcher's starts, in HookStarts' order, or
// why it names none: a start it doesn't know, or one named twice.
func startSet(m string) ([]string, error) {
	parts := strings.Split(m, "|")
	for i, p := range parts {
		if !slices.Contains(HookStarts, p) || slices.Contains(parts[:i], p) {
			return nil, fmt.Errorf("session-start can't be narrowed to %s", m)
		}
	}
	return slices.DeleteFunc(slices.Clone(HookStarts), func(s string) bool { return !slices.Contains(parts, s) }), nil
}

// startMatcher is agent's own matcher for a session-start hook narrowed to
// m, "" for every start, or why it can't run the hook at just those. A
// hook is never widened to starts it was narrowed away from, nor written
// with a matcher the agent never matches: either would run it where the
// user said not to, or nowhere, and say nothing.
func startMatcher(agent, m string) (string, error) {
	if m == "" {
		return "", nil
	}
	set, err := startSet(m)
	if err != nil {
		return "", err
	}
	sp := startSpecs[agent]
	if sp == nil {
		return "", errors.New("its session start can't be narrowed: it runs a hook at every start")
	}
	in := slices.DeleteFunc(slices.Clone(sp.sources), func(s string) bool { return !slices.Contains(set, s) })
	switch {
	case len(in) == 0:
		return "", fmt.Errorf("it has no session start called %s", strings.Join(set, " or "))
	case len(in) == len(sp.sources):
		// every start it has: the rest are ones it never makes
		return "", nil
	case sp.one:
		if len(in) > 1 {
			return "", fmt.Errorf("its session start can be narrowed to one of %s, not to %s", strings.Join(sp.sources, ", "), strings.Join(in, "|"))
		}
		return in[0], nil
	case sp.groups != nil:
		for _, g := range slices.Sorted(maps.Keys(sp.groups)) {
			if slices.Equal(sp.groups[g], in) {
				return g, nil
			}
		}
		var says []string
		for _, g := range slices.Sorted(maps.Keys(sp.groups)) {
			says = append(says, strings.Join(sp.groups[g], "|"))
		}
		return "", fmt.Errorf("its session start can be narrowed to %s, not to %s", strings.Join(says, " or "), strings.Join(in, "|"))
	}
	return strings.Join(in, "|"), nil
}

// startsTaken are the session-start matchers agent can be given, each in
// HookStarts' order: what the page offers it.
func startsTaken(agent string) []string {
	var out []string
	for bits := 1; bits < 1<<len(HookStarts); bits++ {
		var set []string
		for i, s := range HookStarts {
			if bits&(1<<i) != 0 {
				set = append(set, s)
			}
		}
		m := strings.Join(set, "|")
		if _, err := startMatcher(agent, m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// hookSpec is how one agent keeps its hooks.
type hookSpec struct {
	path   string
	events map[string]string // portable → the agent's own
	native []string          // every event the agent has
	tools  map[string]string // shell, edit → the agent's matcher for them
	agent  string            // whose: the agent's id, for its session start's matcher
	flat   bool              // Cursor: an entry is the command itself, not a group of them
	ms     bool              // Gemini: timeouts in milliseconds
	toml   bool              // Grok: hooks are [[hooks.<Event>]] tables in config.toml, not JSON
}

// hookAgents are the agents magpie can give hooks to.
var hookAgents = []string{"claude", "codex", "cursor", "gemini", "grok", "grok-app"}

// grokHookEvents are the events the Grok CLI fires (docs/user-guide/10-hooks.md),
// shared by Grok Build and the Grok App that runs it. Only the ones magpie
// maps a portable event to, plus an agent's own ("grok:Stop"), are offered;
// the rest are listed so an own-event hook can name any of them.
var grokHookEvents = []string{"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PostToolUseFailure", "PermissionDenied", "Stop", "StopFailure", "StopCancelled", "Notification",
	"SubagentStart", "SubagentStop", "PreCompact", "PostCompact"}

// grokHookTools is Grok's own tool names for a portable tool matcher. Grok also
// reads Claude's names (Bash → run_terminal_command, Edit|Write|MultiEdit →
// search_replace), so the shared Claude tools would do; these are its own.
var grokHookTools = map[string]string{
	"shell": "run_terminal_command",
	"edit":  "search_replace",
}

// grokHookSpec is how the Grok CLI keeps hooks: [[hooks.<Event>]] array tables
// in its config.toml, each with a [[hooks.<Event>.hooks]] child holding the
// command. Grok Build reads ~/.grok/config.toml; Grok App, on a custom route,
// reads agent-home/config.toml. Both are the same shape.
func grokHookSpec(path string) *hookSpec {
	return &hookSpec{
		path: path,
		events: map[string]string{
			"turn-end": "Stop", "after-tool": "PostToolUse", "before-tool": "PreToolUse",
			"prompt": "UserPromptSubmit", "session-start": "SessionStart",
		},
		native: grokHookEvents,
		tools:  grokHookTools,
		toml:   true,
	}
}

// toolEvents are the portable events that take a matcher.
var toolEvents = []string{"after-tool", "before-tool"}

// hookSpecOf is how t keeps its hooks, or why magpie can't give it any.
// claudeHookEvents are every hook event Claude Code has, in its own order:
// the HOOK_EVENTS enum in claude 2.1.296. A settings.json hook under any
// other name is one Claude Code never runs (#2020, emo172).
var claudeHookEvents = []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "PostToolBatch", "Notification",
	"UserPromptSubmit", "UserPromptExpansion", "SessionStart", "SessionEnd", "Stop", "StopFailure", "SubagentStart",
	"SubagentStop", "PreCompact", "PostCompact", "PreModelSwitch", "PostModelSwitch", "PermissionRequest",
	"PermissionDenied", "Setup", "TeammateIdle", "TaskCreated", "TaskCompleted", "Elicitation", "ElicitationResult",
	"ConfigChange", "WorktreeCreate", "WorktreeRemove", "InstructionsLoaded", "CwdChanged", "FileChanged",
	"DirectoryAdded", "MessageDisplay"}

func hookSpecOf(t *Target) (*hookSpec, string) {
	sp, why := hookSpecFor(t)
	if sp != nil {
		sp.agent = t.Agent.ID
	}
	return sp, why
}

func hookSpecFor(t *Target) (*hookSpec, string) {
	if t.Agent.WSL != "" {
		return nil, wslNoHooks(t.Agent)
	}
	switch t.Agent.ID {
	case "claude":
		return &hookSpec{
			path: filepath.Join(claudeDir(), "settings.json"),
			events: map[string]string{"turn-end": "Stop", "after-tool": "PostToolUse", "before-tool": "PreToolUse",
				"prompt": "UserPromptSubmit", "session-start": "SessionStart"},
			native: claudeHookEvents,
			tools:  map[string]string{"shell": "Bash", "edit": "Edit|Write|MultiEdit"},
		}, ""
	case "codex":
		// codex-rs/hooks: hooks.json beside config.toml, whose events are
		// HookEventName's
		return &hookSpec{
			path: filepath.Join(codexDir(), "hooks.json"),
			events: map[string]string{"turn-end": "Stop", "after-tool": "PostToolUse", "before-tool": "PreToolUse",
				"prompt": "UserPromptSubmit", "session-start": "SessionStart"},
			native: []string{"PreToolUse", "PermissionRequest", "PostToolUse", "PreCompact", "PostCompact", "SessionStart", "SessionEnd",
				"UserPromptSubmit", "SubagentStart", "SubagentStop", "Stop", "Interrupt"},
			tools: map[string]string{"shell": "Bash", "edit": "apply_patch"},
		}, ""
	case "gemini":
		// gemini-cli packages/core/src/hooks/types.ts, HookEventName
		return &hookSpec{
			path: filepath.Join(home(), ".gemini", "settings.json"),
			events: map[string]string{"turn-end": "AfterAgent", "after-tool": "AfterTool", "before-tool": "BeforeTool",
				"prompt": "BeforeAgent", "session-start": "SessionStart"},
			native: []string{"BeforeTool", "AfterTool", "BeforeAgent", "AfterAgent", "Notification", "SessionStart", "SessionEnd",
				"PreCompress", "BeforeModel", "AfterModel", "BeforeToolSelection"},
			tools: map[string]string{"shell": "run_shell_command", "edit": "write_file|replace"},
			ms:    true,
		}, ""
	case "cursor":
		return &hookSpec{
			path: filepath.Join(home(), ".cursor", "hooks.json"),
			events: map[string]string{"turn-end": "stop", "after-tool": "postToolUse", "before-tool": "preToolUse",
				"prompt": "beforeSubmitPrompt", "session-start": "sessionStart"},
			native: []string{"beforeShellExecution", "afterShellExecution", "beforeMCPExecution", "afterMCPExecution", "beforeReadFile",
				"afterFileEdit", "beforeSubmitPrompt", "stop", "afterAgentResponse", "afterAgentThought", "sessionStart", "sessionEnd",
				"preToolUse", "postToolUse", "postToolUseFailure", "subagentStart", "subagentStop", "preCompact"},
			tools: map[string]string{"shell": "Shell", "edit": "Write"},
			flat:  true,
		}, ""
	case "grok":
		// Grok Build: [[hooks.<Event>]] tables in ~/.grok/config.toml
		return grokHookSpec(filepath.Join(grokDir(), "config.toml")), ""
	case "grok-app":
		// Grok App runs the same CLI, on a custom route reading
		// agent-home/config.toml, the same [[hooks.<Event>]] shape
		return grokHookSpec(t.MCP.Path), ""
	}
	return nil, fmt.Sprintf("%s has no hooks magpie can write", t.Agent.Name)
}

// event is the agent's own name for h's event, or why it has none.
func (sp *hookSpec) event(agent string, h *Hook) (string, error) {
	if a, ev, ok := strings.Cut(h.Event, ":"); ok {
		if !sameHookFamily(a, agent) {
			return "", fmt.Errorf("%s is an event of another agent's", ev)
		}
		if !slices.Contains(sp.native, ev) {
			return "", fmt.Errorf("it has no event called %s", ev)
		}
		return ev, nil
	}
	if ev := sp.events[h.Event]; ev != "" {
		return ev, nil
	}
	return "", fmt.Errorf("no event called %s", h.Event)
}

// entry is h as the agent's file holds it, under its event ev.
func (sp *hookSpec) entry(h *Hook, ev string) json.RawMessage {
	m := h.Matcher
	switch {
	case h.Event == "session-start":
		m, _ = startMatcher(sp.agent, h.Matcher) // one it can't take is never written (syncHooks)
	case !strings.Contains(h.Event, ":"):
		m = sp.tools[h.Matcher] // "" for none, and for an event that takes no matcher
	}
	timeout := h.Timeout
	if sp.ms {
		timeout *= 1000
	}
	if sp.toml {
		// Grok: a [[hooks.<Event>]] table with a [[hooks.<Event>.hooks]]
		// child. The block text is what goes in the file and what is
		// recorded (as a JSON string) so an entry the user changed is
		// theirs, exactly as with a JSON agent's.
		return json.RawMessage(grokHookBlock(ev, m, h.Command, timeout))
	}
	var v any
	if sp.flat {
		v = struct {
			Command string `json:"command"`
			Timeout int    `json:"timeout,omitempty"`
			Matcher string `json:"matcher,omitempty"`
		}{h.Command, timeout, m}
	} else {
		type cmd struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout,omitempty"`
		}
		v = struct {
			Matcher string `json:"matcher,omitempty"`
			Hooks   []cmd  `json:"hooks"`
		}{m, []cmd{{"command", h.Command, timeout}}}
	}
	b, _ := json.Marshal(v)
	return b
}

// maxHookDescription is how long a hook's description can be, in
// characters: a line that says what it is for, not a document.
const maxHookDescription = 200

// grokBlockText decodes a Grok hook entry (a JSON string holding the TOML
// block) into the block text.
func grokBlockText(entry json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(entry, &s) != nil {
		return "", false
	}
	return s, true
}

// grokHookBlock is one Grok hook as a TOML block: a [[hooks.<Event>]] array
// table, its matcher (when any) and a single [[hooks.<Event>.hooks]] child
// with the command. Returned as a JSON string so it can sit in HookEntry.Entry
// and be compared and moved exactly as a JSON agent's entry is.
func grokHookBlock(ev, matcher, command string, timeout int) string {
	var b strings.Builder
	b.WriteString("[[hooks." + ev + "]]\n")
	if matcher != "" {
		b.WriteString("matcher = " + strconv.Quote(matcher) + "\n")
	}
	b.WriteString("  [[hooks." + ev + ".hooks]]\n")
	b.WriteString("  type = \"command\"\n")
	b.WriteString("  command = " + strconv.Quote(command) + "\n")
	if timeout > 0 {
		b.WriteString("  timeout = " + strconv.Itoa(timeout) + "\n")
	}
	s, _ := json.Marshal(b.String())
	return string(s)
}

// sameHookFamily is whether two agent ids are the same agent for an own
// event: Grok Build and Grok App run the one Grok CLI, so an agent's own
// ("grok:Notification") event goes to both, as a portable one does.
func sameHookFamily(a, b string) bool {
	if a == b {
		return true
	}
	return (a == "grok" || a == "grok-app") && (b == "grok" || b == "grok-app")
}

func (h *Hook) check() error {
	if err := checkName("hook", h.Name); err != nil {
		return err
	}
	if strings.TrimSpace(h.Command) == "" {
		return errors.New("a hook needs a command to run")
	}
	if utf8.RuneCountInString(h.Description) > maxHookDescription {
		return fmt.Errorf("a hook's description is at most %d characters", maxHookDescription)
	}
	if h.Timeout < 0 || h.Timeout > 86400 {
		return errors.New("a hook's timeout is between 0 (the agent's own) and 86400 seconds")
	}
	if a, ev, ok := strings.Cut(h.Event, ":"); ok {
		if !slices.Contains(hookAgents, a) || ev == "" {
			return fmt.Errorf("no event called %s", h.Event)
		}
		for _, x := range h.Agents {
			if !sameHookFamily(x, a) {
				return fmt.Errorf("%s is %s's own event: only it can be given this hook", ev, a)
			}
		}
		return nil
	}
	if !slices.Contains(HookEvents, h.Event) {
		return fmt.Errorf("no event called %s", h.Event)
	}
	if h.Matcher != "" && h.Event == "session-start" {
		_, err := startSet(h.Matcher)
		return err
	}
	if h.Matcher != "" && (!slices.Contains(toolEvents, h.Event) || !slices.Contains(HookTools, h.Matcher)) {
		return fmt.Errorf("%s can't be narrowed to %s", h.Event, h.Matcher)
	}
	return nil
}

func (l *Library) hook(name string) *Hook {
	for _, h := range l.Hooks {
		if h.Name == name {
			return h
		}
	}
	return nil
}

// canonical is a JSON value written one way, its keys sorted, for two
// entries to be told the same however each was spaced.
func canonical(raw []byte) string {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// readHooksFile is an agent's hooks file as JSON, "" for one not there. One
// that can't be read, or isn't JSON, is an error: what it holds is unknown,
// never nothing to keep.
func readHooksFile(path string) ([]byte, error) {
	raw, err := edit.Read(path)
	if err != nil {
		return nil, err
	}
	raw = edit.TrimBOM(raw)
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	j := jsonc.ToJSON(raw)
	if !json.Valid(j) {
		return nil, fmt.Errorf("%s isn't JSON magpie can read: it is left as it is", path)
	}
	if !gjson.ParseBytes(j).IsObject() {
		return nil, fmt.Errorf("%s isn't a JSON object: it is left as it is", path)
	}
	return j, nil
}

// hookList is the list the file has under hooks.<ev>.
func hookList(doc []byte, path, ev string) ([]json.RawMessage, error) {
	r := gjson.GetBytes(doc, "hooks."+ev)
	if !r.Exists() {
		return nil, nil
	}
	if !r.IsArray() {
		return nil, fmt.Errorf("%s: hooks.%s isn't a list", path, ev)
	}
	var list []json.RawMessage
	if err := json.Unmarshal([]byte(r.Raw), &list); err != nil {
		return nil, fmt.Errorf("%s: hooks.%s: %w", path, ev, err)
	}
	return list, nil
}

// grokHookBlocks reads the [[hooks.<ev>]] blocks a Grok config.toml has, each
// as the block text it is, so it lines up with what grokHookBlock records and
// the same find/claim/drop logic a JSON list uses applies. A file that isn't
// valid TOML is an error, never treated as having no hooks.
func grokHookBlocks(path, ev string) ([]json.RawMessage, error) {
	raw, err := edit.Read(path)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	lines := edit.SplitLines(string(raw))
	header := "[[hooks." + ev + "]]"
	var blocks []json.RawMessage
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != header {
			continue
		}
		j := grokBlockEnd(lines, i)
		block := strings.Join(lines[i:j], "\n") + "\n"
		s, _ := json.Marshal(block)
		blocks = append(blocks, json.RawMessage(s))
		i = j - 1
	}
	return blocks, nil
}

// grokBlockEnd is the line index just past the [[hooks.<ev>]] block starting
// at from: its body, including any [[hooks.<ev>.hooks]] child array tables,
// up to the next table header of another event, trailing blank lines excluded.
func grokBlockEnd(lines []string, from int) int {
	ev, _ := grokArrayEvent(lines[from])
	child := "hooks." + ev + "." // a table under this event continues the block
	j := from + 1
	for j < len(lines) {
		s := strings.TrimSpace(lines[j])
		if isGrokHeader(s) {
			if name := grokTableName(s); !strings.HasPrefix(name, child) {
				break
			}
		}
		j++
	}
	for j-1 > from && strings.TrimSpace(lines[j-1]) == "" {
		j--
	}
	return j
}

// grokTableName is the dotted table name a [[x.y]] header declares, "" for a
// line that isn't one.
func grokTableName(line string) string {
	if ev, ok := grokArrayEvent(line); ok {
		return "hooks." + ev
	}
	return ""
}

// isGrokHeader is whether a line starts a TOML table ([x] or [[x]]).
func isGrokHeader(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

// writeGrokHooks applies the drop and add of Grok hook blocks to the config:
// each block whose exact text is dropped is taken out, each added one appended
// after the last [[hooks.*]] block (or at the end). Comments and the user's
// own blocks around them are left as they are; the result is validated as TOML
// before it is written, so a bad state never lands.
func writeGrokHooks(path string, drop, add [][]byte) error {
	raw, err := edit.Read(path)
	if err != nil {
		return err
	}
	lines := edit.SplitLines(string(raw))
	dropped := map[string]bool{}
	for _, d := range drop {
		dropped[string(d)] = true
	}
	var out []string
	lastHooks := -1 // line count up to and including the last [[hooks.*]] block
	i := 0
	for i < len(lines) {
		// start a block only at a [[hooks.<Event>]] header, never at a
		// child [[hooks.<Event>.hooks]] (grokArrayEvent returns that as
		// "Event.hooks"), which belongs to the block above it
		if ev, ok := grokArrayEvent(lines[i]); ok && ev != "" && !strings.Contains(ev, ".") {
			j := grokBlockEnd(lines, i)
			block := strings.Join(lines[i:j], "\n") + "\n"
			if dropped[block] {
				for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
					out = out[:len(out)-1] // take the blank line before it too
				}
			} else {
				out = append(out, lines[i:j]...)
			}
			lastHooks = len(out)
			i = j
			continue
		}
		out = append(out, lines[i])
		i++
	}
	for _, a := range add {
		// the block text ends in "\n"; TrimRight then SplitLines leaves no
		// trailing empty element to drop
		blk := edit.SplitLines(strings.TrimRight(string(a), "\n"))
		if lastHooks > 0 {
			insert := append([]string{""}, blk...)
			tail := append([]string{}, out[lastHooks:]...)
			out = append(append(out[:lastHooks], insert...), tail...)
			lastHooks += len(insert)
		} else {
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
			out = append(out, blk...)
			lastHooks = len(out)
		}
	}
	return edit.WriteTOMLFile(path, out)
}

// grokArrayEvent is the event a [[hooks.<ev>]] or [[hooks.<ev>.hooks]] header
// names, or "" for a header of another table. The bool says the line is an
// array-table header of the hooks namespace at all.
func grokArrayEvent(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "[[") || !strings.HasSuffix(s, "]]") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(s, "[["), "]]")
	if !strings.HasPrefix(name, "hooks.") {
		return "", false
	}
	return strings.TrimPrefix(name, "hooks."), true
}

// syncHooks writes into t the hooks the library gives it and takes out the
// ones it no longer does: only entries exactly as magpie wrote them, the
// user's own hooks, before and after them, left where they are.
func (l *Library) syncHooks(t *Target, b *backups, res *Result) {
	id := t.Agent.ID
	a := l.applied(id)
	var want []*Hook
	for _, h := range l.Hooks {
		if slices.Contains(h.Agents, id) {
			want = append(want, h)
		}
	}
	if len(a.Hooks) == 0 && len(want) == 0 {
		return
	}
	sp, why := hookSpecOf(t)
	if sp == nil {
		for _, h := range want {
			res.fail(id, "hook:"+h.Name, errors.New(why))
		}
		return
	}
	failAll := func(err error) {
		for _, h := range want {
			res.fail(id, "hook:"+h.Name, err)
		}
		if len(want) == 0 {
			res.fail(id, "hooks", err)
		}
	}
	var doc []byte
	if !sp.toml {
		d, err := readHooksFile(sp.path)
		if err != nil {
			failAll(err)
			return
		}
		doc = d
	}
	lists := map[string][]json.RawMessage{}
	listOf := func(ev string) ([]json.RawMessage, error) {
		if x, ok := lists[ev]; ok {
			return x, nil
		}
		var x []json.RawMessage
		var err error
		if sp.toml {
			x, err = grokHookBlocks(sp.path, ev)
		} else {
			x, err = hookList(doc, sp.path, ev)
		}
		if err == nil {
			lists[ev] = x
		}
		return x, err
	}
	type wanted struct {
		h     *Hook
		ev    string
		entry json.RawMessage
		done  bool
	}
	var ws []*wanted
	for _, h := range want {
		ev, err := sp.event(id, h)
		if err == nil && h.Event == "session-start" {
			_, err = startMatcher(id, h.Matcher)
		}
		if err != nil {
			res.fail(id, "hook:"+h.Name, err)
			continue
		}
		if _, err := listOf(ev); err != nil {
			res.fail(id, "hook:"+h.Name, err)
			continue
		}
		ws = append(ws, &wanted{h: h, ev: ev, entry: sp.entry(h, ev)})
	}
	claimed := map[string]map[int]bool{} // event → the entries magpie's records found
	find := func(ev string, entry json.RawMessage) int {
		c := canonical(entry)
		for i, e := range lists[ev] {
			if !claimed[ev][i] && canonical(e) == c {
				return i
			}
		}
		return -1
	}
	claim := func(ev string, i int) {
		if claimed[ev] == nil {
			claimed[ev] = map[int]bool{}
		}
		claimed[ev][i] = true
	}
	var kept []HookEntry
	drop := map[string][]int{} // event → the entries to take out
	var unread []HookEntry     // records of an event whose list can't be read now
	for _, rec := range a.Hooks {
		if _, err := listOf(rec.Event); err != nil {
			res.fail(id, "hook:"+rec.Name, err)
			unread = append(unread, rec)
			continue
		}
		i := find(rec.Event, rec.Entry)
		if i < 0 {
			continue // gone, or changed by the user: theirs now
		}
		claim(rec.Event, i)
		if j := slices.IndexFunc(ws, func(w *wanted) bool {
			return !w.done && w.h.Name == rec.Name && w.ev == rec.Event && canonical(w.entry) == canonical(rec.Entry)
		}); j >= 0 {
			ws[j].done = true
			kept = append(kept, rec)
			continue
		}
		drop[rec.Event] = append(drop[rec.Event], i)
	}
	add := map[string][]json.RawMessage{}
	var added []HookEntry
	for _, w := range ws {
		// an agent gone from here is given nothing new; what it has stays
		// its until unticked
		if w.done || t.Gone {
			continue
		}
		// the same entry of the user's own already runs: it isn't written
		// twice, nor taken as magpie's
		if i := find(w.ev, w.entry); i >= 0 {
			continue
		}
		add[w.ev] = append(add[w.ev], w.entry)
		added = append(added, HookEntry{Name: w.h.Name, Event: w.ev, Entry: w.entry})
	}
	if len(drop) == 0 && len(add) == 0 {
		a.Hooks = append(kept, unread...)
		return
	}
	if sp.toml {
		if err := b.keep(id, sp.path); err != nil {
			failAll(err)
			return
		}
		err := os.MkdirAll(filepath.Dir(sp.path), 0o755)
		if err == nil {
			var drops, adds [][]byte
			// the dropped blocks: the entry text each index in lists holds
			for ev, idxs := range drop {
				for _, i := range idxs {
					if blk, ok := grokBlockText(lists[ev][i]); ok {
						drops = append(drops, []byte(blk))
					}
				}
			}
			for _, es := range add {
				for _, e := range es {
					if blk, ok := grokBlockText(e); ok {
						adds = append(adds, []byte(blk))
					}
				}
			}
			err = writeGrokHooks(sp.path, drops, adds)
		}
		if err != nil {
			failAll(fmt.Errorf("%s: %w", sp.path, err))
			return
		}
		res.changed(id)
		a.Hooks = append(append(kept, added...), unread...)
		return
	}
	// what each list becomes, and where each entry left in it moves to
	var set []edit.KV
	var del []string
	moved := map[string]map[int]int{} // event → old index → new, -1 for one taken out
	for _, ev := range slices.Sorted(func(yield func(string) bool) {
		seen := map[string]bool{}
		for k := range drop {
			if !seen[k] && !yield(k) {
				return
			}
			seen[k] = true
		}
		for k := range add {
			if !seen[k] && !yield(k) {
				return
			}
			seen[k] = true
		}
	}) {
		old := lists[ev]
		m := map[int]int{}
		var next []json.RawMessage
		for i, e := range old {
			if slices.Contains(drop[ev], i) {
				m[i] = -1
				continue
			}
			m[i] = len(next)
			next = append(next, e)
		}
		next = append(next, add[ev]...)
		moved[ev] = m
		if len(next) == 0 {
			del = append(del, "hooks."+ev)
		} else {
			set = append(set, edit.KV{Path: "hooks." + ev, Value: next})
		}
	}
	if err := b.keep(id, sp.path); err != nil {
		failAll(err)
		return
	}
	// Codex trusts a hook by where it is (codex-rs/hooks: hook_key, the
	// file's path, the event, the group's and the handler's index): an
	// entry taken out from before the user's own would leave their trust
	// on the wrong ones, so it is moved with them
	var rekey func() error
	if id == "codex" && len(drop) > 0 {
		cfg := filepath.Join(codexDir(), "config.toml")
		f, err := codexRekey(cfg, sp.path, moved)
		if err != nil {
			failAll(err)
			return
		}
		if f != nil {
			if err := b.keep(id, cfg); err != nil {
				failAll(err)
				return
			}
			rekey = f
		}
	}
	raw := doc
	if raw == nil {
		raw = []byte("{}\n")
	} else if orig, err := edit.Read(sp.path); err == nil {
		raw = orig // its comments and spacing, as the user has them
	}
	if sp.flat && !gjson.GetBytes(jsonc.ToJSON(edit.TrimBOM(raw)), "version").Exists() {
		set = append(set, edit.KV{Path: "version", Value: 1})
	}
	out, err := edit.PatchJSON(raw, set, del)
	if err == nil && len(del) > 0 && len(set) == 0 {
		// a hooks object left empty by magpie goes with what it held
		if h := gjson.GetBytes(jsonc.ToJSON(edit.TrimBOM(out)), "hooks"); h.IsObject() && len(h.Map()) == 0 {
			out, err = edit.PatchJSON(out, nil, []string{"hooks"})
		}
	}
	if err == nil {
		err = os.MkdirAll(filepath.Dir(sp.path), 0o755)
	}
	if err == nil {
		err = edit.WriteAtomic(sp.path, out)
	}
	if err != nil {
		failAll(fmt.Errorf("%s: %w", sp.path, err))
		return
	}
	res.changed(id)
	if rekey != nil {
		if err := rekey(); err != nil {
			res.fail(id, "hooks", fmt.Errorf("the hooks are written, but Codex's trust of the ones after them couldn't be moved: review them in Codex's /hooks (%w)", err))
		}
	}
	a.Hooks = append(append(kept, added...), unread...)
}

// codexState is a [hooks.state."<key>"] header line in Codex's config.toml.
var codexState = regexp.MustCompile(`^(\s*\[\s*hooks\s*\.\s*state\s*\.\s*)("(?:[^"\\]|\\.)*"|'[^']*')(\s*\].*)$`)

// codexStateKey splits a hook's trust key: the file's path, the event's
// label, the group's index and the handler's.
func codexStateKey(k string) (path, label string, group, handler int, ok bool) {
	parts := strings.Split(k, ":")
	if len(parts) < 4 {
		return "", "", 0, 0, false
	}
	n := len(parts)
	g, err1 := strconv.Atoi(parts[n-2])
	h, err2 := strconv.Atoi(parts[n-1])
	if err1 != nil || err2 != nil {
		return "", "", 0, 0, false
	}
	return strings.Join(parts[:n-3], ":"), parts[n-3], g, h, true
}

// snake is an event's label in Codex's trust keys (hook_event_key_label):
// PreToolUse is pre_tool_use.
func snake(ev string) string {
	var b strings.Builder
	for i, r := range ev {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// samePath is whether two paths are the same file.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(filepath.Dir(a))
	rb, err2 := filepath.EvalSymlinks(filepath.Dir(b))
	return err1 == nil && err2 == nil && filepath.Base(a) == filepath.Base(b) && ra == rb
}

// codexRekey is what moves Codex's trust records of hooks.json's groups
// along with them (moved: event → old index → new, -1 for one taken out),
// or nil when none needs moving. A record kept in some other spelling than
// a [hooks.state."…"] table of its own can't be moved: that is an error,
// and nothing is written.
func codexRekey(cfg, hooksPath string, moved map[string]map[int]int) (func() error, error) {
	raw, err := edit.Read(cfg)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var whole struct {
		Hooks struct {
			State map[string]any `toml:"state"`
		} `toml:"hooks"`
	}
	if err := toml.Unmarshal(raw, &whole); err != nil {
		return nil, fmt.Errorf("%s: %w", cfg, err)
	}
	labels := map[string]map[int]int{}
	for ev, m := range moved {
		labels[snake(ev)] = m
	}
	// to is where the record keyed k goes: ok false for one that stays
	to := func(k string) (string, bool) {
		p, label, g, h, ok := codexStateKey(k)
		if !ok || !samePath(p, hooksPath) {
			return "", false
		}
		m := labels[label]
		if m == nil {
			return "", false
		}
		n, in := m[g]
		if !in {
			// a record of a group past the end of the list, whose place
			// an entry magpie adds takes: it goes
			return "", g >= len(m)
		}
		if n == g {
			return "", false
		}
		if n < 0 {
			return "", true
		}
		return fmt.Sprintf("%s:%s:%d:%d", p, label, n, h), true
	}
	need := map[string]bool{}
	for k := range whole.Hooks.State {
		if _, ok := to(k); ok {
			need[k] = true
		}
	}
	if len(need) == 0 {
		return nil, nil
	}
	lines := strings.SplitAfter(string(raw), "\n")
	type span struct {
		from, to int
		key      string
		m        []string
	}
	var spans []span
	for i, ln := range lines {
		m := codexState.FindStringSubmatch(strings.TrimRight(ln, "\r\n"))
		if m == nil {
			continue
		}
		q := m[2]
		var k string
		if q[0] == '\'' {
			k = q[1 : len(q)-1]
		} else if u, err := strconv.Unquote(q); err == nil {
			k = u
		} else {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if s := strings.TrimSpace(lines[j]); strings.HasPrefix(s, "[") {
				end = j
				break
			}
		}
		spans = append(spans, span{i, end, k, m})
	}
	for k := range need {
		if !slices.ContainsFunc(spans, func(s span) bool { return s.key == k }) {
			return nil, fmt.Errorf("%s keeps Codex's trust of %s in a way magpie can't move: take the hook out in Codex's /hooks instead", cfg, k)
		}
	}
	return func() error {
		var out strings.Builder
		at := 0
		for _, s := range spans {
			if !need[s.key] {
				continue
			}
			out.WriteString(strings.Join(lines[at:s.from], ""))
			at = s.to
			nk, _ := to(s.key)
			if nk == "" {
				continue // its group is gone, and its record with it
			}
			// the key's end, :<label>:<group>:<handler>, is digits and a
			// label alone: it is written over as it is spelled
			_, label, g, h, _ := codexStateKey(s.key)
			_, _, ng, _, _ := codexStateKey(nk)
			oldEnd, newEnd := fmt.Sprintf(":%s:%d:%d", label, g, h), fmt.Sprintf(":%s:%d:%d", label, ng, h)
			q := s.m[2]
			nq := q[:len(q)-1-len(oldEnd)] + newEnd + q[len(q)-1:]
			nl := s.m[1] + nq + s.m[3]
			if strings.HasSuffix(lines[s.from], "\r\n") {
				nl += "\r\n"
			} else if strings.HasSuffix(lines[s.from], "\n") {
				nl += "\n"
			}
			out.WriteString(nl)
			out.WriteString(strings.Join(lines[s.from+1:s.to], ""))
		}
		out.WriteString(strings.Join(lines[at:], ""))
		return edit.WriteAtomic(cfg, []byte(out.String()))
	}, nil
}

// hooksOff is whether the agent has every hook turned off in its settings,
// for the page to say why one given it doesn't run.
func hooksOff(t *Target, sp *hookSpec) bool {
	switch t.Agent.ID {
	case "claude":
		v, _ := edit.GetJSON(sp.path, "disableAllHooks")
		return v == "true"
	case "gemini":
		v, _ := edit.GetJSON(sp.path, "hooksConfig.enabled")
		return v == "false"
	case "codex":
		f, _ := edit.GetTOMLTable(filepath.Join(codexDir(), "config.toml"), "features")
		return f["hooks"] == "false"
	}
	return false
}

// ---- changes ---------------------------------------------------------------

// SaveHook adds a hook, or replaces the one called old (renaming it). An
// agent it can't be given is left out of it, said in the result.
func SaveHook(old string, h Hook) (*Result, error) {
	h.Description = strings.Join(strings.Fields(h.Description), " ")
	if err := h.check(); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if h.Name != old && l.hook(h.Name) != nil {
			return fmt.Errorf("the library already has a hook called %s", h.Name)
		}
		if old != "" {
			i := slices.IndexFunc(l.Hooks, func(x *Hook) bool { return x.Name == old })
			if i < 0 {
				return fmt.Errorf("no hook called %s", old)
			}
			l.Hooks = slices.Delete(l.Hooks, i, i+1)
		}
		h.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(h.Agents))))
		l.Hooks = append(l.Hooks, &h)
		return nil
	})
}

// HookAgents sets which agents get a hook.
func HookAgents(name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		h := l.hook(name)
		if h == nil {
			return fmt.Errorf("no hook called %s", name)
		}
		c := *h
		c.Agents = agents
		if err := c.check(); err != nil {
			return err
		}
		h.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(agents))))
		return nil
	})
}

// SomeHooksAgents gives the hooks named to the agents, or takes them from
// them, each to the agents that can take it: an agent's own event stays
// that agent's alone.
func SomeHooksAgents(names, agents []string, on bool) (*Result, error) {
	if len(names) == 0 {
		return nil, errors.New("no hooks to give the agents")
	}
	if len(agents) == 0 {
		return nil, errors.New("no agents to give the hooks to")
	}
	return change(func(l *Library) error {
		for _, name := range names {
			if l.hook(name) == nil {
				return fmt.Errorf("no hook called %s", name)
			}
		}
		for _, h := range l.Hooks {
			if !slices.Contains(names, h.Name) {
				continue
			}
			list := slices.Clone(h.Agents)
			for _, a := range agents {
				if on && !hookTakes(h, a) {
					continue
				}
				list = set(list, a, on)
			}
			h.Agents = orNone(list)
		}
		return nil
	})
}

// hookTakes is whether agent a can be given h.
func hookTakes(h *Hook, a string) bool {
	if x, _, ok := strings.Cut(h.Event, ":"); ok {
		return sameHookFamily(x, a)
	}
	if h.Event == "session-start" {
		if _, err := startMatcher(a, h.Matcher); err != nil {
			return false
		}
	}
	return slices.Contains(hookAgents, a)
}

// RemoveHooks takes the hooks named out of the library, and what magpie
// wrote of them out of every agent. A name the library hasn't is said in
// Unremoved, the others gone all the same.
func RemoveHooks(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, errors.New("no hooks to remove")
	}
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	var failed []Problem
	res, err := change(func(l *Library) error {
		failed = nil
		for _, name := range names {
			if l.hook(name) == nil {
				failed = append(failed, Problem{What: "hook:" + name, Error: "no hook called " + name})
			}
		}
		if len(failed) == len(names) {
			return errors.New(failed[0].Error)
		}
		l.Hooks = slices.DeleteFunc(l.Hooks, func(h *Hook) bool { return slices.Contains(names, h.Name) })
		return nil
	})
	if res != nil {
		res.Unremoved = failed
	}
	return res, err
}

// wslNoHooks is why a, an agent in WSL, is given no hooks, its distro
// running or not.
func wslNoHooks(a *agent.Agent) string {
	return fmt.Sprintf("magpie gives hooks only to agents on this computer, not yet to %s in WSL", a.Name)
}

// ---- the page --------------------------------------------------------------

// HookView is a library hook, and what each agent makes of it.
type HookView struct {
	*Hook
	// Native is what each agent it can go to calls its event
	Native map[string]string `json:"native,omitempty"`
	// Problems are the agents it couldn't be given to, and why
	Problems map[string]string `json:"problems,omitempty"`
}

// HookAgentView is an agent the Hooks tab lists.
type HookAgentView struct {
	ID string `json:"id"`
	// File is where its hooks are; No is why magpie can't give it any
	File string `json:"file,omitempty"`
	No   string `json:"no,omitempty"`
	// WSL: No is that it runs in WSL, which magpie gives no hooks yet
	WSL bool `json:"wsl,omitempty"`
	// Off: it has every hook turned off in its settings
	Off bool `json:"off,omitempty"`
	// Events are its own events, for a hook of its alone
	Events []string `json:"events,omitempty"`
	// Starts are the session-start matchers it can be given (#5604);
	// StartSources the starts it has, to say why it can't be given another
	Starts       []string `json:"starts,omitempty"`
	StartSources []string `json:"startSources,omitempty"`
}

// hooksView is the Hooks page for these agents, and those in stopped WSL
// distros (asleep) that the page lists by what magpie last knew of them
// (asleepView): greyed as while their distro runs, since magpie gives no
// agent in WSL hooks yet, and nothing of theirs opened (#3896, #4237).
func (l *Library) hooksView(targets []*Target, asleep []*agent.Agent, of func(string) map[string]string) ([]HookView, []HookAgentView) {
	agents := []HookAgentView{}
	specs := map[string]*hookSpec{}
	for _, t := range targets {
		sp, why := hookSpecOf(t)
		av := HookAgentView{ID: t.Agent.ID, No: why, WSL: t.Agent.WSL != ""}
		if sp != nil {
			specs[t.Agent.ID] = sp
			av.File, av.Off, av.Events = sp.path, hooksOff(t, sp), sp.native
			av.Starts = startsTaken(t.Agent.ID)
			if ss := startSpecs[t.Agent.ID]; ss != nil {
				av.StartSources = ss.sources
			}
		}
		agents = append(agents, av)
	}
	for _, a := range asleep {
		if shapeOf(a) != nil {
			agents = append(agents, HookAgentView{ID: a.ID, No: wslNoHooks(a), WSL: true})
		}
	}
	hooks := []HookView{}
	for _, h := range l.Hooks {
		c := *h
		c.Agents = orNone(c.Agents)
		hv := HookView{Hook: &c, Native: map[string]string{}, Problems: of("hook:" + h.Name)}
		for id, sp := range specs {
			if ev, err := sp.event(id, h); err == nil {
				hv.Native[id] = ev
			}
		}
		// an agent ticked that can't take it says why, before any change
		for _, av := range agents {
			if av.No != "" && slices.Contains(h.Agents, av.ID) {
				if hv.Problems == nil {
					hv.Problems = map[string]string{}
				}
				if hv.Problems[av.ID] == "" {
					hv.Problems[av.ID] = av.No
				}
			}
		}
		hooks = append(hooks, hv)
	}
	return hooks, agents
}

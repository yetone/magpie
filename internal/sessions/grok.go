package sessions

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
)

// Grok Build keeps a session in a folder of its own under its sessions
// folder (~/.grok/sessions), in a folder per working directory, its path
// URL-encoded: <cwd>/<session id>/. summary.json there names the id, the
// cwd, the time it was made, its model-generated title (generated_title,
// else session_summary), the session it was forked from or restored from
// (parent_session_id) and, for a subagent's, a session_kind that starts
// with "subagent". updates.jsonl is the session's events, one a line, each
// with its time (timestamp, in seconds); a "turn_completed" one carries
// what the turn spent, by model (usage.modelUsage): input with what was
// read from the cache in it (cachedReadTokens), output with the reasoning.
// A subagent's session is a folder of its own beside the others, its turns
// only its own — its parent's are the coordinator's — so it counts in the
// session it ran in, once. A fork starts with a copy of its source's
// updates, counted where they were first written: only those from the time
// it was made on count in it. `grok --resume <id>` picks one up again.
//
// A "tool_call" update is a call of a tool, written once a call (the
// "tool_call_update"s after it are its progress), its name in
// _meta["x.ai/tool"].name. Grok Build has no tool for skills: its prompt
// lists each skill's SKILL.md by its path, and the model calls one up by
// reading that file (read_file, or a command run_terminal_command runs),
// as Codex's does. The user calls one up by typing /<name>: the prompt goes
// in the updates as typed, and the skill's text is put in it only in
// chat_history.jsonl, so a /<name> prompt counts as that skill when a
// skill or command of that name is in one of the folders Grok Build finds
// them in. Its own commands (/compact) are marked hostTurn.

// GrokDir is Grok Build's folder: $GROK_HOME, else ~/.grok.
func GrokDir() string {
	if d := strings.TrimSpace(appdir.Getenv("GROK_HOME")); d != "" {
		return expandHome(d)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".grok")
}

type grokSummary struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	CreatedAt      string `json:"created_at"`
	GeneratedTitle string `json:"generated_title"`
	SessionSummary string `json:"session_summary"`
	CurrentModel   string `json:"current_model_id"`
	Parent         string `json:"parent_session_id"`
	Kind           string `json:"session_kind"`
}

// grokSummaryHook, when a test sets it, is told each time a summary.json
// is read.
var grokSummaryHook func(path string)

func readGrokSummary(path string) (grokSummary, bool) {
	if grokSummaryHook != nil {
		grokSummaryHook(path)
	}
	var s grokSummary
	b, err := os.ReadFile(path)
	return s, err == nil && json.Unmarshal(b, &s) == nil
}

// grokKind is what a listing needs of a session's summary.json: whether it
// is a subagent's, and whose.
type grokKind struct{ Kind, Parent string }

// grokKinds keeps each summary's kind as the listing last read it: a
// listing runs on every poll, and reading and decoding every session's
// summary each time cost more than the rest of the listing together.
var grokKinds listMemo[grokKind]

func grokFiles() []file {
	if !Reads("grok") {
		return nil
	}
	kinds := grokKinds.round()
	defer kinds.done()
	root := filepath.Join(GrokDir(), "sessions")
	paths, _ := filepath.Glob(filepath.Join(root, "*", "*", "updates.jsonl"))
	// a session's subagents are named in its subagents/ folder too
	parentOf := map[string]string{}
	subs, _ := filepath.Glob(filepath.Join(root, "*", "*", "subagents", "*"))
	for _, p := range subs {
		child := strings.TrimSuffix(filepath.Base(p), ".json")
		parentOf[child] = filepath.Base(filepath.Dir(filepath.Dir(p)))
	}
	var out []file
	for _, p := range paths {
		dir := filepath.Dir(p)
		id := filepath.Base(dir)
		f := file{agent: "grok", key: "grok:" + id, path: p, main: true}
		if !stat(&f) {
			continue
		}
		// its title, its cwd and whose subagent it is are in its summary
		m := file{path: filepath.Join(dir, "summary.json")}
		if stat(&m) {
			f.w().manifest = m.path
			f.size += m.size
			if m.mod.After(f.mod) {
				f.mod = m.mod
			}
			s, ok := kinds.get(m.path, m.size, m.mod.Time(), func() (grokKind, bool) {
				s, ok := readGrokSummary(m.path)
				return grokKind{s.Kind, s.Parent}, ok
			})
			if ok && strings.HasPrefix(s.Kind, "subagent") {
				parent := s.Parent
				if parent == "" {
					parent = parentOf[id]
				}
				if parent != "" && parent != id && safeID.MatchString(parent) {
					f.key, f.main = "grok:"+parent, false
				}
			}
		}
		out = append(out, f)
	}
	return out
}

type grokUsage struct {
	Input      int `json:"inputTokens"`
	Output     int `json:"outputTokens"`
	CacheRead  int `json:"cachedReadTokens"`
	CacheWrite int `json:"cacheCreationTokens"`
}

// tokens are Grok's counts as magpie keeps them: input without the cache.
func (u grokUsage) tokens() Tokens {
	return Tokens{Input: max(0, u.Input-u.CacheRead-u.CacheWrite), Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
}

type grokLine struct {
	Params struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Meta struct {
				HostTurn bool `json:"hostTurn"`
			} `json:"_meta"`
			Usage *struct {
				grokUsage
				ModelUsage map[string]grokUsage `json:"modelUsage"`
			} `json:"usage"`
		} `json:"update"`
	} `json:"params"`
}

var (
	grokTurn = []byte(`"turn_completed"`)
	grokUser = []byte(`"user_message_chunk"`)
	grokCall = []byte(`"sessionUpdate":"tool_call"`)
	grokHook = []byte(`"sessionUpdate":"hook_execution"`)
)

// grokCarry is what a read of a Grok Build session's updates carries on
// with: where it read to, what of its summary the updates were counted
// under, and its first prompt as far as it was read.
type grokCarry struct {
	Tail     *tail     `json:"tail,omitempty"`
	Created  time.Time `json:"created,omitzero"`
	Since    time.Time `json:"since,omitzero"`
	Model    string    `json:"model,omitempty"`
	Prompt   string    `json:"prompt,omitempty"`
	InPrompt bool      `json:"in_prompt,omitempty"`
	// the first prompt, before it is told from a tag
	Title string `json:"title,omitempty"`
}

// parseGrok reads a session's updates, with its summary. Grok Build only
// appends to updates.jsonl, so a session read before is read on from where
// that read left off; its summary, rewritten as it goes on, is read again
// each time. When the file was cut short or replaced, or the summary now
// says another time or model the updates are counted under, it is read
// whole again.
func parseGrok(f file, old *state) *state {
	var c grokCarry
	var m grokSummary
	hasM := false
	if f.m().manifest != "" {
		if m, hasM = readGrokSummary(f.m().manifest); hasM {
			c.Model = m.CurrentModel
			c.Created, _ = time.Parse(time.RFC3339Nano, m.CreatedAt)
			if m.Parent != "" {
				// what it copied from its source is older than it
				c.Since = c.Created.Truncate(time.Second)
			}
		} else {
			m = grokSummary{}
		}
	}
	var s *state
	var off int64
	if o := old; o != nil && o.more().Grok != nil && o.more().Grok.Model == c.Model && o.more().Grok.Created.Equal(c.Created) && o.more().Grok.Since.Equal(c.Since) && o.more().Grok.Tail.holds(f.path) {
		s, c, off = o.clone(), *o.more().Grok, o.more().Grok.Tail.Off
	} else {
		s = &state{}
		if hasM {
			s.saw(c.Created, f.main)
		}
	}
	s.Size, s.Mod = f.size, f.mod.UnixNano()
	s.ID, s.Cwd, s.own().Named = m.Info.ID, m.Info.Cwd, title(m.GeneratedTitle)
	if s.more().Named == "" {
		s.own().Named = title(m.SessionSummary)
	}
	end, err := scanAt(f.path, off, nil, func(b []byte, _, _ int64) bool {
		grokUpdate(s, &c, b, f.main)
		return true
	})
	c.Tail = nil
	if err == nil {
		c.Tail = markTail(f.path, end, 0, end)
	}
	t := c.Title
	if c.InPrompt {
		t = title(c.Prompt)
	}
	s.Title, s.own().First = t, ""
	if strings.HasPrefix(t, "<") {
		s.Title, s.own().First = "", untagged(t)
	}
	s.own().Grok = &c
	return s
}

// grokUpdate counts one line of a session's updates.
func grokUpdate(s *state, c *grokCarry, b []byte, main bool) {
	at := numTS(b)
	if !c.Since.IsZero() && at.Before(c.Since) {
		return
	}
	if bytes.Contains(b, grokHook) {
		// its hooks run as it starts and ends too: a session left open
		// overnight isn't at work till it is closed
		return
	}
	s.saw(at, main)
	prompt := main && bytes.Contains(b, grokUser)
	user := prompt && c.Title == ""
	if !user && c.InPrompt {
		// the first prompt's chunks are over
		c.Title, c.Prompt, c.InPrompt = title(c.Prompt), "", false
	}
	if bytes.Contains(b, grokCall) {
		grokTool(s, at, b)
		return
	}
	if !prompt && !bytes.Contains(b, grokTurn) {
		return
	}
	var l grokLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	u := l.Params.Update
	switch u.SessionUpdate {
	case "user_message_chunk":
		if u.Content.Type != "text" {
			return
		}
		if user {
			c.Prompt += u.Content.Text
			c.InPrompt = true
		}
		if !u.Meta.HostTurn {
			s.tool(at, "", grokSlashSkill(s.Cwd, u.Content.Text))
		}
	case "turn_completed":
		if u.Usage == nil {
			return
		}
		date := dateOf(at)
		if len(u.Usage.ModelUsage) == 0 {
			s.use(date, cmp.Or(c.Model, "grok"), u.Usage.tokens())
			return
		}
		for name, mu := range u.Usage.ModelUsage {
			s.use(date, name, mu.tokens())
		}
	}
}

// grokTool counts a "tool_call" update: the tool, and each skill it calls
// up by reading its SKILL.md.
func grokTool(s *state, at time.Time, b []byte) {
	u := gjson.GetBytes(b, "params.update")
	if u.Get("sessionUpdate").String() != "tool_call" {
		return
	}
	name := cmp.Or(u.Get(`_meta.x\.ai/tool.name`).String(), u.Get("title").String())
	if name == "" {
		return
	}
	var text string
	switch name {
	case "read_file":
		text = cmp.Or(u.Get("rawInput.target_file").String(), u.Get("rawInput.path").String())
	case "run_terminal_command":
		text = u.Get("rawInput.command").String()
	}
	s.tool(at, name, "")
	if strings.Contains(text, "SKILL.md") {
		for _, k := range skillReadsIn(text) {
			s.tool(at, "", k)
		}
	}
}

// grokSkillName is a name a /<name> prompt can call a skill up by.
var grokSkillName = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// grokSlashSkill is the skill a prompt typed as /<name> calls up, "" when
// it calls up none: no skill or command of that name is in a folder Grok
// Build finds them in, from the session's folder up and in the user's.
func grokSlashSkill(cwd, text string) string {
	rest, ok := strings.CutPrefix(text, "/")
	if !ok {
		return ""
	}
	name := rest
	if i := strings.IndexFunc(rest, unicode.IsSpace); i >= 0 {
		name = rest[:i]
	}
	if !grokSkillName.MatchString(name) || strings.Contains(name, "..") {
		return ""
	}
	var bases []string
	home, _ := os.UserHomeDir()
	roots := []string{home}
	for d := filepath.Clean(cwd); cwd != "" && filepath.IsAbs(d); {
		roots = append(roots, d)
		up := filepath.Dir(d)
		if up == d {
			break
		}
		d = up
	}
	for _, r := range roots {
		if r == "" {
			continue
		}
		for _, v := range []string{".grok", ".agents", ".claude", ".cursor"} {
			bases = append(bases, filepath.Join(r, v))
		}
	}
	bases = append(bases, GrokDir(), filepath.Join(GrokDir(), "bundled"))
	for _, b := range bases {
		if fileExists(filepath.Join(b, "skills", name, "SKILL.md")) || fileExists(filepath.Join(b, "commands", name+".md")) {
			return name
		}
	}
	return ""
}

var tsKey = []byte(`"timestamp":`)

// numTS reads the time of a line from its first "timestamp" key when it is
// a number: seconds, or milliseconds when it is that large.
func numTS(b []byte) time.Time {
	i := bytes.Index(b, tsKey)
	if i < 0 {
		return time.Time{}
	}
	rest := bytes.TrimLeft(b[i+len(tsKey):], " ")
	j := 0
	for j < len(rest) && j < 20 && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	n, err := strconv.ParseInt(string(rest[:j]), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	if n > 1e11 {
		return ms(n)
	}
	return time.Unix(n, 0)
}

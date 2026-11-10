package sessions

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
)

// Factory's Droid keeps a session under its sessions folder
// (~/.factory/sessions, or $FACTORY_HOME_OVERRIDE/.factory/sessions), in a
// folder per working directory (older ones right in it): <id>.jsonl, the
// transcript, and <id>.settings.json beside it. The transcript's first line
// is a session_start event — the id, droid's title for it, the cwd, the
// session it was forked from (parent, with forkedAtMessageId the last
// message it copied) and, for a subagent's or a /btw side question's, the
// session that called it (callingSessionId). Each line after is an event,
// a "message" one carrying its time (ISO) and the message, role and content
// blocks as Anthropic's. What the session spent is in its settings, one
// running total of its own (tokenUsage: input without the cache, output
// with the thinking in it) for the model it is on (model); a fork starts at
// none, and a subagent's counts in the session that called it. The total
// isn't split by turn, so it counts on the day of the last message.
// `droid --resume <id>` picks one up again.

// FactoryDir is Droid's folder: $FACTORY_HOME_OVERRIDE/.factory, else
// ~/.factory.
func FactoryDir() string {
	home := strings.TrimSpace(appdir.Getenv("FACTORY_HOME_OVERRIDE"))
	if home == "" {
		home, _ = os.UserHomeDir()
	} else {
		home = expandHome(home)
	}
	return filepath.Join(home, ".factory")
}

type droidStart struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Cwd     string `json:"cwd"`
	LastCwd string `json:"lastCwd"`
	Parent  string `json:"parent"`
	Calling string `json:"callingSessionId"`
	Forked  string `json:"forkedAtMessageId"`
}

// readDroidStart is a transcript's session_start line.
func readDroidStart(path string) (droidStart, bool) {
	var s droidStart
	ok := false
	scanAt(path, 0, nil, func(b []byte, _, _ int64) bool {
		ok = json.Unmarshal(b, &s) == nil && s.Type == "session_start"
		return false
	})
	return s, ok
}

// droidCallers is whose subagent each transcript is, as its session_start
// line said when the listing last read it: a listing runs on every poll, and
// opening every session's transcript for its first line each time cost more
// than the rest of the listing together.
var droidCallers listMemo[string]

func droidFiles() []file {
	if !Reads("droid") {
		return nil
	}
	root := filepath.Join(FactoryDir(), "sessions")
	a, _ := filepath.Glob(filepath.Join(root, "*.jsonl"))
	b, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	callers := droidCallers.round()
	defer callers.done()
	var out []file
	for _, p := range append(a, b...) {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if !safeID.MatchString(id) {
			continue
		}
		f := file{agent: "droid", key: "droid:" + id, path: p, main: true}
		if !stat(&f) {
			continue
		}
		calling, _ := callers.get(p, f.size, f.mod.Time(), func() (string, bool) {
			s, ok := readDroidStart(p)
			return s.Calling, ok
		})
		// what it spent and on which model are in its settings
		m := file{path: strings.TrimSuffix(p, ".jsonl") + ".settings.json"}
		if stat(&m) {
			f.w().manifest = m.path
			f.size += m.size
			if m.mod.After(f.mod) {
				f.mod = m.mod
			}
		}
		if calling != "" && calling != id && safeID.MatchString(calling) {
			f.key, f.main = "droid:"+calling, false
		}
		out = append(out, f)
	}
	return out
}

type droidUsage struct {
	Input      int `json:"inputTokens"`
	Output     int `json:"outputTokens"`
	CacheWrite int `json:"cacheCreationTokens"`
	CacheRead  int `json:"cacheReadTokens"`
}

type droidSettings struct {
	Model string      `json:"model"`
	Usage *droidUsage `json:"tokenUsage"`
}

type droidLine struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// droidText is a message's text: its content as a string, or its text
// blocks.
func droidText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// droidTools are a message's tool calls: each tool's name, and the skill
// a call of Droid's Skill tool calls up ({"skill":"<name>"}).
func droidTools(raw json.RawMessage) [][2]string {
	var blocks []struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Input struct {
			Skill    any    `json:"skill"`
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
		} `json:"input"`
	}
	json.Unmarshal(raw, &blocks)
	var out [][2]string
	for _, b := range blocks {
		if b.Type == "tool_use" && b.Name != "" {
			skill, _ := b.Input.Skill.(string)
			if b.Name != "Skill" {
				skill = ""
			}
			if strings.EqualFold(b.Name, "Read") {
				skill = routedSkill(b.Input.FilePath + b.Input.Path)
			}
			out = append(out, [2]string{b.Name, strings.TrimSpace(skill)})
		}
	}
	return out
}

// droidPicked is what droid says, in the <system-notification> it puts
// after a prompt, of each skill the user called up by typing /<name> (or
// a builtin one droid ran for them); <skill filePath="…"> and
// <name>…</name> follow it, the name HTML-escaped.
const droidPicked = "selected the following skill for immediate execution"

// droidSlashSkills are the skills a prompt calls up by /<name>: one for
// each notification droid added. A /<name> no skill has gets none, and
// is sent as typed.
func droidSlashSkills(text string) []string {
	var out []string
	for rest := text; ; {
		i := strings.Index(rest, droidPicked)
		if i < 0 {
			return out
		}
		rest = rest[i+len(droidPicked):]
		j := strings.Index(rest, "<skill filePath=\"")
		if j < 0 {
			return out
		}
		rest = rest[j:]
		k := strings.Index(rest, "\">\n<name>")
		if k < 0 || strings.ContainsRune(rest[:k], '\n') {
			continue
		}
		rest = rest[k+len("\">\n<name>"):]
		name, after, ok := strings.Cut(rest, "</name>")
		if !ok || strings.ContainsRune(name, '\n') {
			continue
		}
		rest = after
		if name = strings.TrimSpace(html.UnescapeString(name)); name != "" {
			out = append(out, name)
		}
	}
}

// droidCarry is what a read of a Droid transcript carries on with: where
// it read to, whether it has passed the session_start line and a fork's
// copied lines, and what its settings' total was counted as, to be taken
// back when the total moves on.
type droidCarry struct {
	Tail    *tail  `json:"tail,omitempty"`
	Started bool   `json:"started,omitempty"`
	Upto    string `json:"upto,omitempty"`
	Copied  bool   `json:"copied,omitempty"`
	// the settings' total, as counted
	Date   string `json:"date,omitempty"`
	Model  string `json:"model,omitempty"`
	Tokens Tokens `json:"tokens,omitzero"`
}

// parseDroid reads a session's transcript, with its settings. Droid only
// appends to a transcript, so one read before is read on from where that
// read left off, and its settings' running total, counted on the day of
// the last message, is taken back and counted again as it is now. A
// transcript cut short or replaced is read whole again.
func parseDroid(f file, old *state) *state {
	var s *state
	var c droidCarry
	var off int64
	if old != nil && old.more().Droid != nil && old.more().Droid.Tail.holds(f.path) {
		s, c, off = old.clone(), *old.more().Droid, old.more().Droid.Tail.Off
		s.dropUse(c.Date, c.Model, c.Tokens)
	} else {
		s = &state{}
	}
	s.Size, s.Mod = f.size, f.mod.UnixNano()
	end, err := scanAt(f.path, off, nil, func(b []byte, _, _ int64) bool {
		droidLineOf(s, &c, b, f.main)
		return true
	})
	c.Tail = nil
	if err == nil {
		c.Tail = markTail(f.path, end, 0, end)
	}
	c.Date, c.Model, c.Tokens = "", "", Tokens{}
	if f.m().manifest != "" {
		var st droidSettings
		if b, err := os.ReadFile(f.m().manifest); err == nil && json.Unmarshal(b, &st) == nil && st.Usage != nil {
			when := s.Last
			if when.IsZero() {
				when = time.Unix(0, s.Mod)
			}
			u := st.Usage
			c.Date, c.Model = dateOf(when), droidModelName(st.Model)
			c.Tokens = Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
			s.use(c.Date, c.Model, c.Tokens)
		}
	}
	s.own().Droid = &c
	return s
}

// droidLineOf counts one line of a transcript.
func droidLineOf(s *state, c *droidCarry, b []byte, main bool) {
	var l droidLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	if !c.Started {
		c.Started = true
		var st droidStart
		if l.Type == "session_start" && json.Unmarshal(b, &st) == nil {
			s.ID, s.Cwd, s.own().Named = st.ID, st.Cwd, title(st.Title)
			if s.more().Named == "New Session" {
				s.own().Named = "" // Droid's until it names one
			}
			if st.LastCwd != "" {
				s.Cwd = st.LastCwd
			}
			// a fork's lines up to Upto are copies of its source's
			c.Upto, c.Copied = st.Forked, st.Forked != ""
		}
		return
	}
	if l.Type != "message" {
		return
	}
	if c.Copied {
		if l.ID == c.Upto {
			c.Copied = false
		}
		return
	}
	at := tsAt(b, false)
	s.saw(at, main)
	switch l.Message.Role {
	case "user":
		text := droidText(l.Message.Content)
		for _, k := range droidSlashSkills(text) {
			s.tool(at, "", k)
		}
		if strings.TrimSpace(text) == "" || !main {
			return
		}
		if strings.HasPrefix(strings.TrimSpace(text), "<") {
			if s.more().First == "" {
				s.own().First = untagged(text)
			}
			return
		}
		s.day(dateOf(at)).Prompts++
		if s.Title == "" {
			s.Title = title(text)
		}
	case "assistant":
		if main {
			s.day(dateOf(at)).Replies++
		}
		for _, c := range droidTools(l.Message.Content) {
			s.tool(at, c[0], c[1])
		}
	}
}

// droidSlot is the tail droid gives a custom model's id made of its name
// ("custom:Kimi-K2-[Groq]-0").
var droidSlot = lazyre.New(`(-\[[^\]]*\])?-\d+$`)

// droidModelName is a session's model as it is priced: a custom one
// ("custom:<id>") is the model its entry in droid's settings asks for —
// magpie's own are "<provider>/<model>" — else its id without droid's
// additions; Factory's own are as they are.
func droidModelName(m string) string {
	if m == "" {
		return "droid"
	}
	if !strings.HasPrefix(m, "custom:") {
		return m
	}
	if raw, err := os.ReadFile(filepath.Join(FactoryDir(), "settings.json")); err == nil {
		of := ""
		gjson.GetBytes(jsonc.ToJSONInPlace(raw), "customModels").ForEach(func(_, v gjson.Result) bool {
			if v.Get("id").String() == m {
				of = v.Get("model").String()
				return false
			}
			return true
		})
		if of != "" {
			return of
		}
	}
	return droidSlot.ReplaceAllString(strings.TrimPrefix(m, "custom:"), "")
}

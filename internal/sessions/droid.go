package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/appdir"
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

func droidFiles() []file {
	root := filepath.Join(FactoryDir(), "sessions")
	a, _ := filepath.Glob(filepath.Join(root, "*.jsonl"))
	b, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
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
		// what it spent and on which model are in its settings
		m := file{path: strings.TrimSuffix(p, ".jsonl") + ".settings.json"}
		if stat(&m) {
			f.manifest = m.path
			f.size += m.size
			if m.mod.After(f.mod) {
				f.mod = m.mod
			}
		}
		if s, ok := readDroidStart(p); ok && s.Calling != "" && s.Calling != id && safeID.MatchString(s.Calling) {
			f.key, f.main = "droid:"+s.Calling, false
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

// droidTools are a message's tool calls, by name.
func droidTools(raw json.RawMessage) []string {
	var blocks []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	json.Unmarshal(raw, &blocks)
	var out []string
	for _, b := range blocks {
		if b.Type == "tool_use" && b.Name != "" {
			out = append(out, b.Name)
		}
	}
	return out
}

// parseDroid reads a session's transcript whole, with its settings.
func parseDroid(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano()}
	// a fork's lines up to upto are copies of its source's
	upto, copied := "", false
	first := true
	scan(f.path, 0, func(b []byte) {
		var l droidLine
		if json.Unmarshal(b, &l) != nil {
			return
		}
		if first {
			first = false
			var st droidStart
			if l.Type == "session_start" && json.Unmarshal(b, &st) == nil {
				s.ID, s.Cwd, s.Named = st.ID, st.Cwd, title(st.Title)
				if st.LastCwd != "" {
					s.Cwd = st.LastCwd
				}
				upto, copied = st.Forked, st.Forked != ""
			}
			return
		}
		if l.Type != "message" {
			return
		}
		if copied {
			if l.ID == upto {
				copied = false
			}
			return
		}
		at := tsAt(b, false)
		s.saw(at, f.main)
		switch l.Message.Role {
		case "user":
			text := droidText(l.Message.Content)
			if strings.TrimSpace(text) == "" || !f.main {
				return
			}
			if strings.HasPrefix(strings.TrimSpace(text), "<") {
				if s.First == "" {
					s.First = untagged(text)
				}
				return
			}
			s.day(dateOf(at)).Prompts++
			if s.Title == "" {
				s.Title = title(text)
			}
		case "assistant":
			if f.main {
				s.day(dateOf(at)).Replies++
			}
			for _, name := range droidTools(l.Message.Content) {
				s.tool(at, name, "")
			}
		}
	})
	if f.manifest != "" {
		var st droidSettings
		if b, err := os.ReadFile(f.manifest); err == nil && json.Unmarshal(b, &st) == nil && st.Usage != nil {
			when := s.Last
			if when.IsZero() {
				when = time.Unix(0, s.Mod)
			}
			u := st.Usage
			s.use(dateOf(when), droidModelName(st.Model), Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
		}
	}
	return s
}

// droidSlot is the tail droid gives a custom model's id made of its name
// ("custom:Kimi-K2-[Groq]-0").
var droidSlot = regexp.MustCompile(`(-\[[^\]]*\])?-\d+$`)

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

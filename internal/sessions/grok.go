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

	"github.com/yetone/magpie/internal/appdir"
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

func readGrokSummary(path string) (grokSummary, bool) {
	var s grokSummary
	b, err := os.ReadFile(path)
	return s, err == nil && json.Unmarshal(b, &s) == nil
}

func grokFiles() []file {
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
			f.manifest = m.path
			f.size += m.size
			if m.mod.After(f.mod) {
				f.mod = m.mod
			}
			if s, ok := readGrokSummary(m.path); ok && strings.HasPrefix(s.Kind, "subagent") {
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
	grokHook = []byte(`"sessionUpdate":"hook_execution"`)
)

// parseGrok reads a session's updates whole, with its summary.
func parseGrok(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano()}
	var since time.Time
	model := ""
	if f.manifest != "" {
		if m, ok := readGrokSummary(f.manifest); ok {
			s.ID, s.Cwd, model = m.Info.ID, m.Info.Cwd, m.CurrentModel
			s.Named = title(m.GeneratedTitle)
			if s.Named == "" {
				s.Named = title(m.SessionSummary)
			}
			created, _ := time.Parse(time.RFC3339Nano, m.CreatedAt)
			if m.Parent != "" {
				// what it copied from its source is older than it
				since = created.Truncate(time.Second)
			}
			s.saw(created, f.main)
		}
	}
	prompt, inPrompt := "", false
	scan(f.path, 0, func(b []byte) {
		at := numTS(b)
		if !since.IsZero() && at.Before(since) {
			return
		}
		if bytes.Contains(b, grokHook) {
			// its hooks run as it starts and ends too: a session left open
			// overnight isn't at work till it is closed
			return
		}
		s.saw(at, f.main)
		user := f.main && s.Title == "" && bytes.Contains(b, grokUser)
		if !user && inPrompt {
			// the first prompt's chunks are over
			s.Title, inPrompt = title(prompt), false
		}
		if !user && !bytes.Contains(b, grokTurn) {
			return
		}
		var l grokLine
		if json.Unmarshal(b, &l) != nil {
			return
		}
		u := l.Params.Update
		switch u.SessionUpdate {
		case "user_message_chunk":
			if user && u.Content.Type == "text" {
				prompt += u.Content.Text
				inPrompt = true
			}
		case "turn_completed":
			if u.Usage == nil {
				return
			}
			date := dateOf(at)
			if len(u.Usage.ModelUsage) == 0 {
				s.use(date, cmp.Or(model, "grok"), u.Usage.tokens())
				return
			}
			for name, mu := range u.Usage.ModelUsage {
				s.use(date, name, mu.tokens())
			}
		}
	})
	if inPrompt {
		s.Title = title(prompt)
	}
	if strings.HasPrefix(s.Title, "<") {
		s.Title, s.First = "", untagged(s.Title)
	}
	return s
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

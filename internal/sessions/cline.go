package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// Cline's CLI (3, on the Cline SDK) keeps a session in a folder of its own
// under its sessions folder (~/.cline/data/sessions): <id>.json, the
// manifest — the cwd, the time started, the first prompt, the title in its
// metadata — and <id>.messages.json, the conversation, written anew whole
// as it goes on. A subagent's is <agent id>.messages.json in the folder of
// the session it ran in. An assistant message carries its model
// (modelInfo.id), its time (ts, milliseconds) and its usage (metrics): the
// AI SDK's counts, input with the cache in it, output with the reasoning.
// A session forked from another (metadata.fork) starts with a copy of that
// one's messages, which count where they were first written: only those
// from its forkedAt on count in it. `cline --id <id>` picks one up again.

// ClineSessionDir is Cline's sessions folder: $CLINE_SESSION_DATA_DIR, else
// sessions in $CLINE_DATA_DIR, else in data in $CLINE_DIR, else in
// ~/.cline/data.
func ClineSessionDir() string {
	if d := strings.TrimSpace(appdir.Getenv("CLINE_SESSION_DATA_DIR")); d != "" {
		return d
	}
	data := strings.TrimSpace(appdir.Getenv("CLINE_DATA_DIR"))
	if data == "" {
		dir := strings.TrimSpace(appdir.Getenv("CLINE_DIR"))
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".cline")
		}
		data = filepath.Join(dir, "data")
	}
	return filepath.Join(data, "sessions")
}

func clineFiles() []file {
	if !Reads("cline") {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(ClineSessionDir(), "*", "*.messages.json"))
	var out []file
	for _, p := range paths {
		id := filepath.Base(filepath.Dir(p))
		f := file{agent: "cline", key: "cline:" + id, path: p, main: filepath.Base(p) == id+".messages.json"}
		if !stat(&f) {
			continue
		}
		if f.main {
			// its title and its fork are in the manifest, beside it
			m := file{path: filepath.Join(filepath.Dir(p), id+".json")}
			if stat(&m) {
				f.w().manifest = m.path
				f.size += m.size
				if m.mod.After(f.mod) {
					f.mod = m.mod
				}
			}
		}
		out = append(out, f)
	}
	return out
}

type clineManifest struct {
	SessionID string `json:"session_id"`
	StartedAt string `json:"started_at"`
	Cwd       string `json:"cwd"`
	Prompt    string `json:"prompt"`
	Metadata  struct {
		Title string `json:"title"`
		Fork  *struct {
			ForkedAt string `json:"forkedAt"`
		} `json:"fork"`
	} `json:"metadata"`
}

type clineMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	TS        int64           `json:"ts"`
	ModelInfo *struct {
		ID string `json:"id"`
	} `json:"modelInfo"`
	Metrics *struct {
		Input      int `json:"inputTokens"`
		Output     int `json:"outputTokens"`
		CacheRead  int `json:"cacheReadTokens"`
		CacheWrite int `json:"cacheWriteTokens"`
	} `json:"metrics"`
}

// clineCarry is what a read of a session's messages.json keeps, to know
// whether it can stand for the next: the file as it was read, and what of
// the manifest its counts were made under. Prompt is the first prompt in
// it that names the session, for when the manifest's doesn't.
type clineCarry struct {
	Size   int64     `json:"size"`
	Mod    int64     `json:"mod"`
	Since  time.Time `json:"since,omitzero"`
	Start  time.Time `json:"start,omitzero"`
	Prompt string    `json:"prompt,omitempty"`
}

// parseCline reads a session's messages whole, as they are written, with
// its manifest. Cline rewrites the manifest on its own (a title given, the
// session touched), so when only that changed and it still says the same
// start and fork, what the messages were counted as stands and only the
// manifest is read again.
func parseCline(f file, old *state) *state {
	var since, start time.Time
	var m clineManifest
	hasM := false
	if f.m().manifest != "" {
		if b, err := os.ReadFile(f.m().manifest); err == nil && json.Unmarshal(b, &m) == nil {
			hasM = true
			if m.Metadata.Fork != nil {
				since, _ = time.Parse(time.RFC3339Nano, m.Metadata.Fork.ForkedAt)
			}
			at, _ := time.Parse(time.RFC3339Nano, m.StartedAt)
			if since.IsZero() || !at.Before(since) {
				start = at
			}
		}
	}
	name := func(s *state, prompt string) *state {
		s.Size, s.Mod = f.size, f.mod.UnixNano()
		s.ID, s.Cwd, s.own().Named, s.Title, s.own().First = "", "", "", "", ""
		if hasM {
			s.ID, s.Cwd = m.SessionID, m.Cwd
			s.own().Named = title(m.Metadata.Title)
			clineTitle(s, m.Prompt)
		}
		if f.main {
			clineTitle(s, prompt)
		}
		return s
	}
	fi, err := os.Stat(f.path)
	if err != nil {
		return name(&state{}, "")
	}
	if o := old; o != nil && o.more().Cline != nil && o.more().Cline.Size == fi.Size() && o.more().Cline.Mod == fi.ModTime().UnixNano() && o.more().Cline.Since.Equal(since) && o.more().Cline.Start.Equal(start) {
		s := o.clone()
		return name(s, o.more().Cline.Prompt) // the title its messages gave, when the manifest gives none

	}
	s := &state{}
	s.saw(start, f.main)
	c := &clineCarry{Size: fi.Size(), Mod: fi.ModTime().UnixNano(), Since: since, Start: start}
	if clineHook != nil {
		clineHook(f.path)
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		return name(s, "")
	}
	var doc struct {
		Messages []clineMessage `json:"messages"`
	}
	if json.Unmarshal(b, &doc) != nil {
		// half written, or not Cline's: read again when it changes
		return name(s, "")
	}
	named := false
	for _, m := range doc.Messages {
		if !named && m.Role == "user" {
			// the first prompt that names the session by itself
			var t state
			text := ccText(m.Content)
			if clineTitle(&t, text); t.Title != "" || t.more().First != "" {
				c.Prompt, named = text, true
			}
		}
		at := ms(m.TS)
		if !since.IsZero() && at.Before(since) {
			continue // the copy of the session it was forked from
		}
		s.saw(at, f.main)
		if m.Role == "assistant" {
			for _, c := range clineTools(m.Content) {
				s.tool(at, c[0], c[1])
			}
		}
		if u := m.Metrics; m.Role == "assistant" && u != nil && m.ModelInfo != nil && m.ModelInfo.ID != "" {
			s.use(dateOf(at), m.ModelInfo.ID, Tokens{Input: max(0, u.Input-u.CacheRead-u.CacheWrite), Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
		}
	}
	s.own().Cline = c
	return name(s, c.Prompt)
}

// clineTools are the tools a reply called, each with the skill it brought
// in: Cline's content blocks are Anthropic's, a call
// {"type":"tool_use","name","input"}, and its skills tool's input is
// {"skill": <name>, "args"?}. A /<skill> typed with the skills tool on is
// sent as typed and the model calls the tool for it; with the tool off
// Cline puts the skill's text in the prompt and nothing names it.
func clineTools(content json.RawMessage) [][2]string {
	var blocks []struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Input struct {
			Skill string `json:"skill"`
		} `json:"input"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out [][2]string
	for _, b := range blocks {
		if b.Type != "tool_use" || b.Name == "" {
			continue
		}
		k := ""
		if b.Name == "skills" {
			k = b.Input.Skill
		}
		out = append(out, [2]string{b.Name, k})
	}
	return out
}

// clineHook, when a test sets it, is told each time a messages file is
// read.
var clineHook func(path string)

var (
	clineNotice = regexp.MustCompile(`(?s)<mode_notice\b[^>]*>.*?</mode_notice>`)
	clineInput  = regexp.MustCompile(`</?(?:user_input|user_command)\b[^>]*>`)
)

// clineTitle takes a session's title from a prompt: the words typed, out of
// the tags Cline wraps them in (<user_input mode="act">) and its notices.
func clineTitle(s *state, prompt string) {
	if s.Title != "" || s.more().First != "" {
		return
	}
	t := strings.TrimSpace(clineInput.ReplaceAllString(clineNotice.ReplaceAllString(prompt, " "), " "))
	if strings.HasPrefix(t, "<") {
		s.own().First = untagged(t)
	} else {
		s.Title = title(t)
	}
}

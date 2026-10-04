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
				f.manifest = m.path
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

// parseCline reads a session's messages whole, as they are written.
func parseCline(f file) *state {
	s := &state{Size: f.size, Mod: f.mod.UnixNano()}
	var since time.Time
	if f.manifest != "" {
		var m clineManifest
		if b, err := os.ReadFile(f.manifest); err == nil && json.Unmarshal(b, &m) == nil {
			s.ID, s.Cwd = m.SessionID, m.Cwd
			s.Named = title(m.Metadata.Title)
			if m.Metadata.Fork != nil {
				since, _ = time.Parse(time.RFC3339Nano, m.Metadata.Fork.ForkedAt)
			}
			start, _ := time.Parse(time.RFC3339Nano, m.StartedAt)
			if since.IsZero() || !start.Before(since) {
				s.saw(start, f.main)
			}
			clineTitle(s, m.Prompt)
		}
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		return s
	}
	var doc struct {
		Messages []clineMessage `json:"messages"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return s
	}
	for _, m := range doc.Messages {
		if f.main && m.Role == "user" && s.Title == "" && s.First == "" {
			clineTitle(s, ccText(m.Content))
		}
		at := ms(m.TS)
		if !since.IsZero() && at.Before(since) {
			continue // the copy of the session it was forked from
		}
		s.saw(at, f.main)
		if u := m.Metrics; m.Role == "assistant" && u != nil && m.ModelInfo != nil && m.ModelInfo.ID != "" {
			s.use(dateOf(at), m.ModelInfo.ID, Tokens{Input: max(0, u.Input-u.CacheRead-u.CacheWrite), Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
		}
	}
	return s
}

var (
	clineNotice = regexp.MustCompile(`(?s)<mode_notice\b[^>]*>.*?</mode_notice>`)
	clineInput  = regexp.MustCompile(`</?(?:user_input|user_command)\b[^>]*>`)
)

// clineTitle takes a session's title from a prompt: the words typed, out of
// the tags Cline wraps them in (<user_input mode="act">) and its notices.
func clineTitle(s *state, prompt string) {
	if s.Title != "" || s.First != "" {
		return
	}
	t := strings.TrimSpace(clineInput.ReplaceAllString(clineNotice.ReplaceAllString(prompt, " "), " "))
	if strings.HasPrefix(t, "<") {
		s.First = untagged(t)
	} else {
		s.Title = title(t)
	}
}

package sessions

import (
	"bytes"
	"encoding/json"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/tidwall/jsonc"
)

// Pi writes a file per session, <time>_<session id>.jsonl, in a folder per
// working directory under its agent folder's sessions/ (or all in one
// folder of the user's choosing): a "session" header naming the id and the
// folder, then an entry per line. An assistant message carries its model
// and its usage, input without the cache and output with the reasoning; a
// tool's result may carry the usage of model work it did, and "usage",
// "compaction" and "branch_summary" entries theirs. A session forked from
// another starts with a copy of that one's entries, times and all, which
// are counted where they were first written. omp's sessions are the same
// kind (omp.go), with a title of the header's or a title_change entry's, and
// "model_usage" entries as Pi's "usage" ones.
//
// Each user message typed is a prompt and each assistant message a reply;
// the tools called are the assistant's toolCall content parts, and those a
// tool called in turn (a codemode script's) its result's nestedCalls. Pi
// has no tool for skills: one is called up by reading its SKILL.md, or by
// /skill:name, which puts the skill in the prompt as <skill name="…">.

// PiDir is Pi's agent folder: $PI_CODING_AGENT_DIR, else ~/.pi/agent.
func PiDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return expandHome(d)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

// piSessionDir is the one folder the user told Pi to keep its sessions in,
// $PI_CODING_AGENT_SESSION_DIR or the sessionDir setting, "" for none.
func piSessionDir() string {
	if d := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); d != "" {
		return expandHome(d)
	}
	b, err := os.ReadFile(filepath.Join(PiDir(), "settings.json"))
	if err != nil {
		return ""
	}
	var s struct {
		SessionDir string `json:"sessionDir"`
	}
	if json.Unmarshal(jsonc.ToJSON(b), &s) != nil || s.SessionDir == "" {
		return ""
	}
	return expandHome(s.SessionDir)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

func piFiles() []file {
	paths, _ := filepath.Glob(filepath.Join(PiDir(), "sessions", "*", "*.jsonl"))
	if d := piSessionDir(); d != "" {
		more, _ := filepath.Glob(filepath.Join(d, "*.jsonl"))
		paths = append(paths, more...)
	}
	var out []file
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		name := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		_, id, ok := strings.Cut(name, "_")
		if !ok || id == "" {
			continue
		}
		f := file{agent: "pi", key: "pi:" + id, path: p, main: true}
		if stat(&f) {
			out = append(out, f)
		}
	}
	return out
}

type piUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

func (u *piUsage) tokens() Tokens {
	if u == nil {
		return Tokens{}
	}
	return Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
}

type piLine struct {
	Type          string   `json:"type"`
	ID            string   `json:"id"`
	Cwd           string   `json:"cwd"`
	ParentSession string   `json:"parentSession"`
	Name          string   `json:"name"`    // session_info
	Title         string   `json:"title"`   // omp's header, title_change
	ModelID       string   `json:"modelId"` // model_change
	Model         string   `json:"model"`   // usage; omp's model_change, as provider/model
	Usage         *piUsage `json:"usage"`   // usage, compaction, branch_summary
	Message       *struct {
		Role        string          `json:"role"`
		Model       string          `json:"model"`
		Content     json.RawMessage `json:"content"`
		Usage       *piUsage        `json:"usage"`
		NestedCalls *struct {
			Calls []struct {
				Name string `json:"name"`
			} `json:"calls"`
		} `json:"nestedCalls"` // toolResult
	} `json:"message"`
}

// piPart is an assistant message's content part: a toolCall's name and
// arguments (as Pi's ai package writes ToolCall).
type piPart struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments struct {
		Path string `json:"path"`
	} `json:"arguments"`
}

var (
	piHeader  = []byte(`"type":"session"`)
	piInfo    = []byte(`"type":"session_info"`)
	piModel   = []byte(`"type":"model_change"`)
	piRetitle = []byte(`"type":"title_change"`)
	piUsed    = []byte(`"usage":{`)
	piUserMsg = []byte(`"role":"user"`)
	piAsst    = []byte(`"role":"assistant"`)
	piNested  = []byte(`"nestedCalls":{`)
)

func piParse(s *state, b []byte, main bool) {
	at := tsAt(b, false)
	header := s.ID == "" && bytes.Contains(b, piHeader)
	// a forked session's copy of the entries it was forked from
	copied := !s.Since.IsZero() && !at.IsZero() && at.Before(s.Since)
	if !copied {
		s.saw(at, main)
	}
	want := header || bytes.Contains(b, piInfo) || bytes.Contains(b, piModel) || bytes.Contains(b, piRetitle) ||
		!copied && (bytes.Contains(b, piUsed) || bytes.Contains(b, piAsst) || bytes.Contains(b, piNested)) ||
		(s.Title == "" || !copied && main) && bytes.Contains(b, piUserMsg)
	if !want {
		return
	}
	var l piLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	switch l.Type {
	case "session":
		if s.ID == "" {
			s.ID, s.Cwd = l.ID, l.Cwd
			if l.ParentSession != "" {
				s.Since = at
			}
			if l.Title != "" {
				s.Named = title(l.Title)
			}
		}
	case "session_info":
		s.Named = title(l.Name)
	case "title_change":
		// omp's, one with each new title
		if l.Title != "" {
			s.Named = title(l.Title)
		}
	case "model_change":
		if l.ModelID != "" {
			s.Model = l.ModelID
		} else if _, id, ok := strings.Cut(l.Model, "/"); ok && id != "" {
			s.Model = id
		}
	case "usage", "model_usage":
		if !copied && l.Model != "" {
			s.use(dateOf(at), l.Model, l.Usage.tokens())
		}
	case "compaction", "branch_summary":
		if !copied && s.Model != "" {
			s.use(dateOf(at), s.Model, l.Usage.tokens())
		}
	case "message":
		m := l.Message
		if m == nil {
			return
		}
		switch m.Role {
		case "user":
			text := ccText(m.Content)
			skill := piSkillBlock(text)
			if main && !copied {
				if piPrompt(text) != "" || skill != "" {
					s.day(dateOf(at)).Prompts++
				}
				s.tool(at, "", skill)
			}
			if main && s.Title == "" {
				if s.Title = piPrompt(text); s.Title == "" && s.First == "" {
					s.First = untagged(text)
				}
			}
		case "assistant":
			if m.Model != "" {
				s.Model = m.Model
			}
			if copied {
				return
			}
			if main {
				s.day(dateOf(at)).Replies++
			}
			var parts []piPart
			if json.Unmarshal(m.Content, &parts) == nil {
				for _, p := range parts {
					if p.Type == "toolCall" {
						s.tool(at, p.Name, piSkillRead(p))
					}
				}
			}
			if m.Model != "" {
				s.use(dateOf(at), m.Model, m.Usage.tokens())
			}
		default:
			if !copied && m.NestedCalls != nil {
				for _, c := range m.NestedCalls.Calls {
					s.tool(at, c.Name, "")
				}
			}
			// a tool that did model work of its own, on the model in use
			if !copied && m.Usage != nil && s.Model != "" {
				s.use(dateOf(at), s.Model, m.Usage.tokens())
			}
		}
	}
}

// piSkillBlock is the skill a prompt calls up with /skill:name, which Pi
// sends as <skill name="…" location="…">, "" for none.
func piSkillBlock(text string) string {
	rest, ok := strings.CutPrefix(text, `<skill name="`)
	if !ok {
		return ""
	}
	name, _, ok := strings.Cut(rest, `"`)
	if !ok {
		return ""
	}
	return name
}

// piSkillRead is the skill a read calls up: Pi's of a SKILL.md, named by
// its folder, or omp's of a skill:// URL; "" for any other call.
func piSkillRead(p piPart) string {
	if p.Name != "read" {
		return ""
	}
	path := strings.ReplaceAll(p.Arguments.Path, `\`, "/")
	if rest, ok := strings.CutPrefix(path, "skill://"); ok {
		// the skill itself, not a file it refers to
		name, file, _ := strings.Cut(strings.TrimSuffix(rest, "/"), "/")
		if file != "" && file != "SKILL.md" {
			return ""
		}
		return name
	}
	dir, file := pathpkg.Split(path)
	if file != "SKILL.md" || strings.Trim(dir, "/.") == "" {
		return ""
	}
	return pathpkg.Base(dir)
}

// piPrompt is the words of a prompt, or "" for what an extension or a
// template put in wrapped in tags.
func piPrompt(text string) string {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "<") {
		return ""
	}
	return title(t)
}

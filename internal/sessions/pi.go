package sessions

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/appdir"
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
	if d := appdir.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return expandHome(d)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

// piSessionDir is the one folder the user told Pi to keep its sessions in,
// $PI_CODING_AGENT_SESSION_DIR or the sessionDir setting, "" for none.
func piSessionDir() string {
	if d := appdir.Getenv("PI_CODING_AGENT_SESSION_DIR"); d != "" {
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

func piFiles() []file { return piFilesIn(PiDir(), piSessionDir()) }

// piFilesIn are the sessions in a Pi folder, and in the folder it was told
// to keep them in ("" for none), with their subagents' (piNamed,
// piSubagentFiles).
func piFilesIn(dir, sessionDir string) []file {
	if !Reads("pi") {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "*.jsonl"))
	if d := sessionDir; d != "" {
		more, _ := filepath.Glob(filepath.Join(d, "*.jsonl"))
		paths = append(paths, more...)
	}
	out := piNamed("pi", paths)
	if d := piSubagentSessionDir(dir); d != "" {
		out = piSubagentFiles(out, d)
	}
	return out
}

// piNamed are the Pi session files among paths, <time>_<id>.jsonl, each
// once, as agent's, with their subagents'. pi-subagents (npm pi-subagents
// 0.88.1) runs a subagent as a Pi of its own, told to keep its session in
// the folder named for its parent's file, beside it:
// <time>_<id>/<run id>/run-<n>/session.jsonl (async-<id>/ and parallel
// levels between for those runs), a header naming the parent
// (parentSession) and then the entries as Pi writes them, its model's
// calls on whichever provider the subagent was given; and a subagent's own
// subagents deeper in the same way. They count in the session they ran
// for, as omp's do (nzvzbmz78932 on X: a Pi subagent's DeepSeek calls on
// Pi's own provider were on no page).
func piNamed(agent string, paths []string) []file {
	var out []file
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		id := piFileID(p)
		if id == "" {
			continue
		}
		key := agent + ":" + id
		f := file{agent: agent, key: key, path: p, main: true}
		if !stat(&f) {
			continue
		}
		out = append(out, f)
		for _, q := range piSessionsUnder(strings.TrimSuffix(p, ".jsonl")) {
			if seen[q] {
				continue
			}
			seen[q] = true
			sub := file{agent: agent, key: key, path: q}
			if stat(&sub) {
				out = append(out, sub)
			}
		}
	}
	return out
}

// piFileID is a Pi session file's id, from its name, <time>_<id>.jsonl; ""
// for another file.
func piFileID(p string) string {
	name, ok := strings.CutSuffix(filepath.Base(p), ".jsonl")
	if !ok {
		return ""
	}
	_, id, _ := strings.Cut(name, "_")
	return id
}

// piSessionsUnder are the session files at any depth under dir: Pi's own,
// <time>_<id>.jsonl, and pi-subagents' session.jsonl. Its other files
// there (status, events) are none.
func piSessionsUnder(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "session.jsonl" || piFileID(p) != "" {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// piSubagentSessionDir is the folder pi-subagents was told to keep every
// subagent's session in instead, its config's defaultSessionDir
// (<agent folder>/extensions/subagent/config.json), "" for none.
func piSubagentSessionDir(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "extensions", "subagent", "config.json"))
	if err != nil {
		return ""
	}
	var c struct {
		DefaultSessionDir string `json:"defaultSessionDir"`
	}
	if json.Unmarshal(jsonc.ToJSON(b), &c) != nil || c.DefaultSessionDir == "" {
		return ""
	}
	if d := expandHome(c.DefaultSessionDir); filepath.IsAbs(d) {
		return filepath.Clean(d)
	}
	return ""
}

// piSubagentFiles adds the sessions under pi-subagents' defaultSessionDir
// not among out yet (<run id>/run-<n>/session.jsonl there): each in the
// session its header's parentSession names (that one's, for a subagent's
// subagent), else a session of its own.
func piSubagentFiles(out []file, dir string) []file {
	seen := map[string]bool{}
	for _, f := range out {
		seen[f.path] = true
	}
	for _, p := range piSessionsUnder(dir) {
		if seen[p] {
			continue
		}
		f := file{agent: "pi", path: p}
		if !stat(&f) {
			continue
		}
		parent, id := piHeadOf(p)
		for i := 0; i < 8 && parent != "" && piFileID(parent) == ""; i++ {
			parent, _ = piHeadOf(parent)
		}
		switch {
		case piFileID(parent) != "":
			f.key = "pi:" + piFileID(parent)
		case id != "":
			f.key, f.main = "pi:"+id, true
		default:
			continue
		}
		out = append(out, f)
	}
	return out
}

var piHeads sync.Map // path → [2]string{parentSession, id}

// piHeadOf is a session file's parent (its header's parentSession) and id,
// read once: Pi writes the header first and never again.
func piHeadOf(p string) (parent, id string) {
	if v, ok := piHeads.Load(p); ok {
		h := v.([2]string)
		return h[0], h[1]
	}
	fh, err := os.Open(p)
	if err != nil {
		return "", ""
	}
	defer fh.Close()
	b := make([]byte, 16<<10)
	n, _ := io.ReadFull(fh, b)
	line, _, done := bytes.Cut(b[:n], []byte("\n"))
	if !done && n == len(b) {
		return "", ""
	}
	var l piLine
	if json.Unmarshal(line, &l) != nil || l.Type != "session" {
		return "", ""
	}
	piHeads.Store(p, [2]string{l.ParentSession, l.ID})
	return l.ParentSession, l.ID
}

// piSubResults is a pi-subagents tool result's details: the session file
// each subagent it ran wrote.
type piSubResults struct {
	Results []struct {
		SessionFile string `json:"sessionFile"`
	} `json:"results"`
}

// piLeadID is the id of the Pi session a file at p is read in: its own
// for Pi's <time>_<id>.jsonl, else the one whose folder holds it (a
// subagent's session.jsonl, nested as deep as pi-subagents nests runs), else
// "" (a subagent's in defaultSessionDir).
func piLeadID(p string) string {
	if id := piFileID(p); id != "" {
		return id
	}
	for i, d := 0, filepath.Dir(p); i < 8 && d != filepath.Dir(d); i, d = i+1, filepath.Dir(d) {
		if id := piFileID(d + ".jsonl"); len(id) == 36 {
			return id
		}
	}
	return ""
}

// piSubagentsRead is whether a tool result's model work is pi-subagents'
// subagents', each with a session file read on its own (piNamed,
// piSubagentFiles) in the session keyed id: their calls are counted there,
// on the model and provider each ran, and not again on the result. Pi
// writes the result with their usage summed, and it was counted on the
// lead's model: a lead on magpie's entry took a DeepSeek subagent's calls
// for the gateway's, which left them out (nzvzbmz78932 on X).
func piSubagentsRead(agent, id string, d *piSubResults) bool {
	if agent == "omp" || d == nil || len(d.Results) == 0 {
		return false
	}
	own, read := "", false
	for _, r := range d.Results {
		p := strings.ReplaceAll(r.SessionFile, `\`, "/")
		if p == "" {
			return false
		}
		// beside the lead's file, in the folder named for it
		if id != "" && strings.Contains(p, "_"+id+"/") {
			continue
		}
		// in pi-subagents' defaultSessionDir, which only Pi's own is read in
		if !read && agent == "pi" {
			if d := piSubagentSessionDir(PiDir()); d != "" {
				own = strings.TrimSuffix(strings.ReplaceAll(d, `\`, "/"), "/") + "/"
			}
		}
		read = true
		if own == "" || !strings.HasPrefix(p, own) {
			return false
		}
	}
	return true
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
		Details *piSubResults `json:"details"` // toolResult
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
	copied := !s.more().Since.IsZero() && !at.IsZero() && at.Before(s.more().Since)
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
				s.own().Since = at
			}
			if l.Title != "" {
				s.own().Named = title(l.Title)
			}
		}
	case "session_info":
		s.own().Named = title(l.Name)
	case "title_change":
		// omp's, one with each new title
		if l.Title != "" {
			s.own().Named = title(l.Title)
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
				if s.Title = piPrompt(text); s.Title == "" && s.more().First == "" {
					s.own().First = untagged(text)
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
			if !copied && m.Usage != nil && s.Model != "" && !piSubagentsRead(s.Agent, piLeadID(s.parsePath), m.Details) {
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

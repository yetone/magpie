package sessions

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/dirent"
)

// Antigravity CLI's (agy's) conversations, on the Sessions page (RickyWei on
// Discord: the ones started from magpie's Agents page were listed nowhere).
// A conversation is the folder brain/<conversation id>/ of agy's data folder
// (AgyDir); its steps are .system_generated/logs/transcript_full.jsonl, the
// file its calls are read from too (calls_agy.go). As agy 1.3.5 and 1.4.7
// write it (tried in a sandbox HOME against a fake Gemini endpoint), a line
// a step:
//
//	{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":…,
//	 "content":"<USER_REQUEST>\nread a.txt\n</USER_REQUEST>\n<ADDITIONAL_METADATA>…"}
//	{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":…,
//	 "input_tokens":1500,"cache_read_tokens":500,"output_tokens":50,
//	 "thinking":"…","tool_calls":[{"name":"read_file","args":{"Path":"…"}}]}
//	{"step_index":2,"source":"MODEL","type":"GENERIC","created_at":…,
//	 "content":"Created At: …\nCompleted At: …\nShowing lines 1-1 of 1\n1: hi"}
//	{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE",…,"content":"The folder has a.txt."}
//
// SYSTEM steps (notices, the errors of a failed request) are agy's own.
// What agy calls the conversation, and the folder it was started in, are
// its row of conversation_summaries.db beside brain/: title (written by a
// second, small model once the first reply is in, "" until then),
// preview (what the app lists the conversation as: the user's own words
// until it has a title, the title after) and workspace_uris, a JSON list
// of file:// URIs. The Antigravity app's language server is agy's, and
// keeps the same database in its own data folder (antigravity_app.go). The steps name no model:
// a conversation's tokens are counted under the model its database,
// conversations/<id>.db, names for each step (agyStepModels), as its calls
// are, and under "agy" when it names none. `agy --conversation <id>` picks one up again, from any
// folder; started on magpie's gateway, it needs the variables and --model
// the agent's row gives (AgyStart).

// AgyStart, when set (package agent sets it), is the command that starts
// agy on magpie's gateway, "" when agy isn't set to use magpie.
var AgyStart func() string

// agySummary is what conversation_summaries.db says of a conversation.
type agySummary struct {
	Title, Preview, Cwd string
	// Last is last_modified_time: the time of a conversation that has
	// no transcript to read it from
	Last time.Time
	// Parent is the conversation that started this one as its subagent
	// (agyFold), "" for one the user started; Kind is its agent_name
	Parent string `json:",omitempty"`
	Kind   string `json:",omitempty"`
}

// agySummaries is each conversation_summaries.db's rows by its path (agy's
// and the Antigravity app's), read again only when the database (or its
// write-ahead log) changed. A read that fails keeps the rows read before.
var agySummaries struct {
	sync.Mutex
	read map[string]agySummaryRead
}

type agySummaryRead struct {
	rev  string
	rows map[string]agySummary
}

// agySummariesOf are the rows of the conversation_summaries.db in dir.
func agySummariesOf(dir string) map[string]agySummary { return agySummariesOn(dir, runtime.GOOS) }

// agySummariesOn is agySummariesOf with the folders the rows name read as
// goos names them: "linux" for a WSL distro's, read from Windows.
func agySummariesOn(dir, goos string) map[string]agySummary {
	path := filepath.Join(dir, "conversation_summaries.db")
	rev := ""
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			rev += strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(fi.Size(), 10) + ";"
		} else if p == path {
			return nil
		}
	}
	agySummaries.Lock()
	defer agySummaries.Unlock()
	was, seen := agySummaries.read[path]
	if seen && was.rev == rev {
		return was.rows
	}
	rows, ok := readAgySummaries(path, goos)
	if !ok {
		return was.rows
	}
	if agySummaries.read == nil {
		agySummaries.read = map[string]agySummaryRead{}
	}
	agySummaries.read[path] = agySummaryRead{rev, rows}
	return rows
}

// readAgySummaries opens the database for the one read and closes it: it
// is read again only when it changed.
func readAgySummaries(path, goos string) (map[string]agySummary, bool) {
	db, done, err := openAppStore(path)
	if err != nil {
		return nil, false
	}
	defer done()
	return agyRowsOn(db, goos)
}

// agyRows reads the rows; a database made before preview, or before
// parent_conversation_id and agent_name, were columns is read without them.
func agyRows(db *sql.DB) (map[string]agySummary, bool) { return agyRowsOn(db, runtime.GOOS) }

// agyRowsOn is agyRows with the folders read as goos names them.
func agyRowsOn(db *sql.DB, goos string) (map[string]agySummary, bool) {
	var rows *sql.Rows
	var err error
	for _, cols := range []string{
		"title, preview, workspace_uris, last_modified_time, parent_conversation_id, agent_name",
		"title, preview, workspace_uris, last_modified_time, '', ''",
		"title, '', workspace_uris, last_modified_time, '', ''",
	} {
		if rows, err = db.Query("SELECT conversation_id, " + cols + " FROM conversation_summaries"); err == nil {
			break
		}
	}
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	out := map[string]agySummary{}
	for rows.Next() {
		var id string
		var t, p, ws, parent, kind sql.NullString
		var last any
		if rows.Scan(&id, &t, &p, &ws, &last, &parent, &kind) != nil || id == "" {
			continue
		}
		var uris []string
		json.Unmarshal([]byte(ws.String), &uris)
		s := agySummary{Title: t.String, Preview: p.String, Last: agyTime(last), Parent: parent.String, Kind: kind.String}
		for _, u := range uris {
			if s.Cwd = agyFolder(u, goos); s.Cwd != "" {
				break
			}
		}
		out[id] = s
	}
	return out, rows.Err() == nil
}

// agyTime is a last_modified_time as the driver gives it: a time, or the
// text agy writes ("2026-10-25 09:48:56.074501+00:00").
func agyTime(v any) time.Time {
	switch v := v.(type) {
	case time.Time:
		return v
	case string:
		for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", time.RFC3339Nano, "2006-01-02 15:04:05.999999999"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t
			}
		}
	case []byte:
		return agyTime(string(v))
	}
	return time.Time{}
}

// agyFolder is the folder a workspace URI names on goos: file:///C:/p (or
// VS Code's file:///c%3A/p) is C:\p on Windows, file://server/share/p
// \\server\share\p. "" for a URI that names no local folder.
func agyFolder(uri, goos string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return ""
	}
	p := u.Path
	if goos != "windows" {
		if u.Host != "" && u.Host != "localhost" {
			return ""
		}
		return p
	}
	if u.Host != "" && u.Host != "localhost" {
		return `\\` + u.Host + strings.ReplaceAll(p, "/", `\`)
	}
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = strings.ToUpper(p[1:2]) + p[2:]
	}
	return strings.ReplaceAll(p, "/", `\`)
}

// agyTranscripts are each conversation's transcript_full.jsonl in an agy
// data folder, keyed agy:<conversation id>.
func agyTranscripts(dir string) []file {
	if !Reads("agy") {
		return nil
	}
	brain := filepath.Join(dir, "brain")
	dirs, _ := os.ReadDir(brain)
	var out []file
	for _, d := range dirs {
		if !dirent.IsFolder(brain, d) {
			continue
		}
		p := filepath.Join(brain, d.Name(), ".system_generated", "logs", "transcript_full.jsonl")
		f := file{agent: "agy", key: "agy:" + d.Name(), path: p, main: true}
		if stat(&f) {
			out = append(out, f)
		}
	}
	return out
}

// agyFiles are agy's conversations on this computer, each with its row of
// conversation_summaries.db: rev is that row, so a title agy writes after
// the transcript reads the conversation again, and only that one.
func agyFiles() []file { return agyFold(agyFilesIn(AgyDir(), runtime.GOOS)) }

// agyFold files each subagent's conversation under the conversation at
// the top of it (JohnRee97129809 on X: every subagent agy ran was listed
// as a conversation of its own, and the user's own were lost among
// them). agy keeps a subagent's conversation as it keeps any other, its
// own brain/<id>/ and conversations/<id>.db, and its row of
// conversation_summaries.db names the conversation that started it,
// parent_conversation_id (its nesting_depth how deep); agy's own list
// leaves it out. It goes under the key of the one at the top (a
// subagent's subagent too), as a subagent's file (main false), its own
// id in sid and the one that started it in parent: the session's
// subagents (subagentsOf). One whose parent isn't listed stays a
// conversation of its own. Its rev is marked, so a conversation read
// as one of its own is read again as a subagent's, which counts no
// prompts (parseAgy). Each WSL distro's are its own. The call sources
// keep each conversation's own id (agyCallFiles).
func agyFold(fs []file) []file {
	parents := map[string]map[string]string{} // distro → listed id → its parent
	for _, f := range fs {
		if f.agent != "agy" {
			continue
		}
		in := parents[f.m().wsl]
		if in == nil {
			in = map[string]string{}
			parents[f.m().wsl] = in
		}
		p := ""
		if a := f.m().agy; a != nil && safeID.MatchString(a.Parent) {
			p = a.Parent
		}
		in[strings.TrimPrefix(f.key, "agy:")] = p
	}
	for i := range fs {
		f := &fs[i]
		if f.agent != "agy" {
			continue
		}
		in := parents[f.m().wsl]
		id := strings.TrimPrefix(f.key, "agy:")
		// up to the first whose parent isn't listed; a chain that comes
		// round (or runs past any agy nests) folds nothing
		root, n := id, 0
		for ; n < 64; n++ {
			up := in[root]
			if _, listed := in[up]; up == "" || !listed {
				break
			}
			if up == id {
				n = 64
				break
			}
			root = up
		}
		if root == id || n == 64 {
			continue
		}
		f.key, f.main = "agy:"+root, false
		m := f.w()
		m.sid, m.parent, m.rev = id, in[id], m.rev+"\x00sub"
	}
	return fs
}

// agyFilesIn is agyFiles of the agy data folder dir, whose rows name
// folders as goos does. A conversation with a
// row and no transcript (#1718: agy keeps each one's
// steps in conversations/<id>.db too, protobuf this doesn't read) is
// listed from its row alone: its title, folder and time, and no tokens.
func agyFilesIn(dir, goos string) []file {
	if !Reads("agy") {
		return nil
	}
	out := agyTranscripts(dir)
	rows := agySummariesOn(dir, goos)
	if len(out) == 0 && len(rows) == 0 {
		return nil
	}
	has := make(map[string]bool, len(out))
	for i := range out {
		id := strings.TrimPrefix(out[i].key, "agy:")
		has[id] = true
		rev := ""
		if r, ok := rows[id]; ok {
			out[i].w().agy = &r
			rev = agyRev(r)
		}
		// the conversation's database names its steps' models, and may
		// be written after the transcript
		for _, p := range agyDBFiles(out[i].path) {
			if fi, err := os.Stat(p); err == nil {
				rev += "\x00" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(fi.Size(), 10)
			}
		}
		if rev != "" {
			out[i].w().rev = rev
		}
	}
	for id, r := range rows {
		if has[id] || !safeID.MatchString(id) {
			continue
		}
		f := file{agent: "agy", key: "agy:" + id, path: filepath.Join(dir, "conversations", id+".db"), main: true}
		if stat(&f) {
			m := f.w()
			m.agy, m.rev = &r, agyRev(r)
			out = append(out, f)
		}
	}
	return out
}

func agyRev(r agySummary) string {
	return r.Title + "\x00" + r.Preview + "\x00" + r.Cwd + "\x00" + r.Last.UTC().Format(time.RFC3339Nano)
}

// agyRowOnly says the file is a conversation's own database, listed from
// its row: not a transcript to read.
func agyRowOnly(path string) bool { return strings.HasSuffix(path, ".db") }

// agyLine is a step of a transcript.
type agyLine struct {
	Step      *int   `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Content   string `json:"content"`
	Thinking  string `json:"thinking"`
	Input     *int   `json:"input_tokens"`
	CacheRead *int   `json:"cache_read_tokens"`
	Output    *int   `json:"output_tokens"`
	ToolCalls []struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"tool_calls"`
}

// agyCarry is what a read of a transcript carries on with: where it read
// to, the highest step counted, and the model steps' tokens by their index,
// so a step written again (as the call reader allows) counts once. Sub is
// a read of a subagent's conversation (agyFold): its prompts are its
// parent's asking, and its time runs beside its parent's.
type agyCarry struct {
	Tail  *tail              `json:"tail,omitempty"`
	Last  int                `json:"last"`
	Steps map[int]agyCounted `json:"steps,omitempty"`
	Sub   bool               `json:"sub,omitempty"`
}

type agyCounted struct {
	Date   string `json:"d"`
	Tokens Tokens `json:"t"`
	// Model is the model the step's tokens are counted under, "" for
	// agyModel
	Model string `json:"m,omitempty"`
}

// agyModel is the name a step's tokens are counted under when its
// conversation's database names no model for it.
const agyModel = "agy"

// agyModelOr is the name a step of model m is counted under.
func agyModelOr(m string) string {
	if m == "" {
		return agyModel
	}
	return m
}

// parseAgy reads a transcript, on from where the read before left off when
// the file has only grown (agy appends a step at a time).
func parseAgy(f file, old *state) *state {
	if agyRowOnly(f.path) {
		s := &state{Size: f.size, Mod: f.mod.UnixNano(), StateMore: moreOf(StateMore{DBRevision: f.m().rev})}
		if f.m().agy != nil {
			s.own().Named, s.Cwd = title(f.m().agy.Title), f.m().agy.Cwd
			// no transcript to read the prompt from: what the row lists
			// it as until it has a title
			s.Title = title(f.m().agy.Preview)
			if !f.m().agy.Last.IsZero() {
				s.saw(f.m().agy.Last, f.main)
			}
		}
		return s
	}
	var s *state
	c := agyCarry{Last: -1, Sub: !f.main}
	var off int64
	if old != nil && old.more().Agy != nil && old.more().Agy.Tail.holds(f.path) && old.more().Agy.Sub == !f.main {
		s, c, off = old.clone(), *old.more().Agy, old.more().Agy.Tail.Off
		steps := make(map[int]agyCounted, len(c.Steps))
		for k, v := range c.Steps {
			steps[k] = v
		}
		c.Steps = steps
	} else {
		s = &state{}
	}
	// the database is read once a read has a model's step to count
	var models map[int]string
	read := false
	model := func(step int) string {
		if !read {
			models, read = agyStepModels(agyConversationDB(f.path)), true
		}
		return models[step]
	}
	end, err := scanAt(f.path, off, nil, func(b []byte, _, _ int64) bool {
		agyCount(s, &c, b, model)
		return true
	})
	// a model the database names after its step was counted (or a read
	// before this one counted under agyModel) moves the step's tokens
	for i, was := range c.Steps {
		if m := model(i); m != "" && m != was.Model {
			s.dropUse(was.Date, agyModelOr(was.Model), was.Tokens)
			s.use(was.Date, m, was.Tokens)
			was.Model = m
			c.Steps[i] = was
		}
	}
	c.Tail = nil
	if err == nil {
		c.Tail = markTail(f.path, end, 0, end)
	}
	s.Size, s.Mod, s.own().DBRevision = f.size, f.mod.UnixNano(), f.m().rev
	s.own().Named, s.Cwd = "", ""
	if f.m().agy != nil {
		s.own().Named, s.Cwd = title(f.m().agy.Title), f.m().agy.Cwd
	}
	s.own().Agy = &c
	return s
}

// agyCount counts one step of a transcript, under the model model names
// for its index.
func agyCount(s *state, c *agyCarry, b []byte, model func(int) string) {
	var l agyLine
	if json.Unmarshal(b, &l) != nil || l.Step == nil {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, l.CreatedAt)
	if err != nil {
		return
	}
	i := *l.Step
	if l.Source == "MODEL" && l.Type == "PLANNER_RESPONSE" && l.Input != nil && l.Output != nil {
		t := Tokens{Input: *l.Input, Output: *l.Output}
		if l.CacheRead != nil {
			t.CacheRead = *l.CacheRead
		}
		if was, ok := c.Steps[i]; ok {
			s.dropUse(was.Date, agyModelOr(was.Model), was.Tokens)
		}
		if !t.zero() {
			if c.Steps == nil {
				c.Steps = map[int]agyCounted{}
			}
			m := model(i)
			c.Steps[i] = agyCounted{dateOf(at), t, m}
			s.use(dateOf(at), agyModelOr(m), t)
		} else {
			delete(c.Steps, i)
		}
	}
	if i <= c.Last {
		return // written again: counted when it was first
	}
	c.Last = i
	s.saw(at, !c.Sub)
	switch {
	case l.Source == "USER_EXPLICIT" && l.Type == "USER_INPUT":
		if !c.Sub {
			s.day(dateOf(at)).Prompts++
		}
		if s.Title == "" {
			s.Title = title(agyRequest(l.Content))
		}
		for _, k := range agyAskedSkills(l.Content) {
			s.tool(at, "", k)
		}
	case l.Source == "MODEL" && l.Type == "PLANNER_RESPONSE":
		agyTools(s, at, l)
		if len(l.ToolCalls) == 0 && l.Content != "" && !c.Sub {
			s.day(dateOf(at)).Replies++
		}
	}
}

// agyTools counts a model step's tool calls: each tool, and each skill a
// call calls up by reading its SKILL.md. agy (1.5.13, and the app's
// language server) has no tool for skills: its system prompt lists each
// skill's SKILL.md by its path and tells the model to read it, which it
// does with read_file ({"Path":…}) or run_command ({"CommandLine":…}). A
// skill is named by its SKILL.md's folder, as Codex's and Grok Build's
// are (skillReadsIn); a write_file or edit_file of a SKILL.md calls none
// up.
func agyTools(s *state, at time.Time, l agyLine) {
	for _, tc := range l.ToolCalls {
		s.tool(at, tc.Name, "")
		var text string
		switch tc.Name {
		case "read_file":
			text = gjson.GetBytes(tc.Args, "Path").String()
		case "run_command":
			text = gjson.GetBytes(tc.Args, "CommandLine").String()
		}
		if strings.Contains(text, "SKILL.md") {
			for _, k := range skillReadsIn(text) {
				s.tool(at, "", k)
			}
		}
	}
}

// agySkillCalls are the notes agy adds after a prompt for a skill it puts
// in the prompt whole, each "<SKILL>" then the skill's name in brackets:
// typed as /<name> at the start of the prompt (in print mode and the
// TUI alike), or mentioned. A third note, "The user requested you read
// and use the "<name>" skill", gives the path only; the model's read of
// it is what counts then, so it isn't counted twice.
var agySkillCalls = []string{
	"<SKILL>The user has explicitly invoked the (",
	"<SKILL>The user mentioned the (",
}

// agyAskedSkills are the skills a prompt calls up by itself: those agy
// added a note for after what the user typed (agySkillCalls), once each.
// The user's own words, in <USER_REQUEST>, can't call one up.
func agyAskedSkills(content string) []string {
	if _, after, ok := strings.Cut(content, "</USER_REQUEST>"); ok {
		content = after
	}
	if !strings.Contains(content, "<SKILL>") {
		return nil
	}
	var out []string
	for _, note := range agySkillCalls {
		for rest := content; ; {
			_, after, ok := strings.Cut(rest, note)
			if !ok {
				break
			}
			name, tail, ok := strings.Cut(after, ") skill.")
			rest = tail
			if name = strings.TrimSpace(name); ok && name != "" && !strings.Contains(name, "\n") && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// agyRequest is what the user typed, out of the <USER_REQUEST> agy wraps
// it in with its own metadata; the whole content when it has none.
func agyRequest(content string) string {
	_, rest, ok := strings.Cut(content, "<USER_REQUEST>")
	if !ok {
		return strings.TrimSpace(content)
	}
	req, _, _ := strings.Cut(rest, "</USER_REQUEST>")
	return strings.TrimSpace(req)
}

// agySaid are the steps a transcript shows.
var agySaid = [][]byte{[]byte(`"USER_INPUT"`), []byte(`"MODEL"`)}

// agyTranscript reads a conversation as it was said: what the user typed,
// the model's thinking, text and tool calls, and the tools' results. A step
// written again is said as it was written last, where it was first.
func agyTranscript(path string, keep func([]byte) bool, add func(bool, Part) bool) error {
	if agyRowOnly(path) {
		return nil
	}
	var order []int
	steps := map[int]agyLine{}
	_, err := scanAt(path, 0, nil, func(b []byte, _, _ int64) bool {
		said := false
		for _, k := range agySaid {
			if bytes.Contains(b, k) {
				said = true
				break
			}
		}
		if !said || (keep != nil && !keep(b)) {
			return true
		}
		var l agyLine
		if json.Unmarshal(b, &l) != nil || l.Step == nil {
			return true
		}
		if _, ok := steps[*l.Step]; !ok {
			order = append(order, *l.Step)
		}
		steps[*l.Step] = l
		return true
	})
	if err != nil {
		return err
	}
	for _, i := range order {
		for _, p := range agyParts(steps[i]) {
			if !add(p.Role != "assistant", p) {
				return nil
			}
		}
	}
	return nil
}

// agyParts are what one step said.
func agyParts(l agyLine) []Part {
	var out []Part
	switch {
	case l.Source == "USER_EXPLICIT" && l.Type == "USER_INPUT":
		if t := agyRequest(l.Content); t != "" {
			out = append(out, Part{Role: "user", Kind: "text", Text: t})
		}
	case l.Source == "MODEL" && l.Type == "PLANNER_RESPONSE":
		if l.Thinking != "" {
			out = append(out, Part{Role: "assistant", Kind: "thinking", Text: l.Thinking})
		}
		if l.Content != "" {
			out = append(out, Part{Role: "assistant", Kind: "text", Text: l.Content})
		}
		for _, tc := range l.ToolCalls {
			out = append(out, Part{Role: "assistant", Kind: "tool_use", Name: tc.Name, Text: string(tc.Args)})
		}
	case l.Source == "MODEL" && l.Content != "":
		// a tool's result, after the times agy heads it with
		text := l.Content
		for _, h := range []string{"Created At:", "Completed At:"} {
			if strings.HasPrefix(text, h) {
				_, text, _ = strings.Cut(text, "\n")
			}
		}
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, Part{Role: "tool", Kind: "tool_result", Text: text})
		}
	}
	return out
}

// forgetAgySummaries forgets the rows read of conversation_summaries.db.
func forgetAgySummaries() {
	agySummaries.Lock()
	agySummaries.read = nil
	agySummaries.Unlock()
}

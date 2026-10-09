package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

// ReasonixDir is the native session state home, which may be separate from
// REASONIX_HOME (the configuration home). Reading it never starts Reasonix.
func ReasonixDir() string {
	for _, name := range []string{"REASONIX_STATE_HOME", "REASONIX_HOME"} {
		if d := appdir.Getenv(name); d != "" {
			return d
		}
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		if d := appdir.Getenv("APPDATA"); d != "" {
			return filepath.Join(d, "reasonix")
		}
		return filepath.Join(home, "AppData", "Roaming", "reasonix")
	}
	return filepath.Join(home, ".reasonix")
}

type reasonixMeta struct {
	ID      string    `json:"id"`
	Title   string    `json:"topic_title"`
	Custom  string    `json:"custom_title"`
	Preview string    `json:"preview"`
	Cwd     string    `json:"workspace_root"`
	Created time.Time `json:"created_at"`
	Updated time.Time `json:"updated_at"`
}

// Directory discovery does not open metadata or transcript files.
func reasonixSessionDirs() []string {
	root := ReasonixDir()
	dirs := []string{filepath.Join(root, "sessions")}
	projects, _ := SessionGlob(filepath.Join(root, "projects", "*", "sessions"))
	dirs = append(dirs, projects...)
	out := []string{}
	for _, dir := range dirs {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

const reasonixRevision = "reasonix-native-v3:"

// Legacy transcripts are JSONL messages; their .jsonl.meta sidecar supplies
// identity, title and workspace. Usage lives in a separate .turns.jsonl ledger.
// Neither daily global stats (no session id) nor event/DAG logs are transcripts.
// v4/v5 framed stores require their own codec and are not read as legacy files.
func reasonixFiles() []file {
	dirs := reasonixSessionDirs()
	out := []file{}
	chosen := map[string][]file{}
	for _, dir := range dirs {
		for _, e := range readDirectory(dir) {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".jsonl") || strings.HasPrefix(n, ".") {
				continue
			}
			stem := strings.TrimSuffix(n, ".jsonl")
			if strings.Contains(stem, ".") && reasonixSidecar(stem) {
				continue
			}
			p := filepath.Join(dir, n)
			b, err := os.ReadFile(p + ".meta")
			var m reasonixMeta
			manifest := p + ".meta"
			if os.IsNotExist(err) {
				// Older saved conversations have no metadata sidecar. Validate
				// the message prefix, keep their title, and leave usage unknown.
				head := []byte(headOf(p))
				role := strAt(head, []byte(`"role":"`))
				if role == "" {
					role = strAt(bytes.ReplaceAll(head, []byte(`": "`), []byte(`":"`)), []byte(`"role":"`))
				}
				if role != "system" && role != "user" && role != "assistant" && role != "tool" {
					continue
				}
				legacyID := sha256.Sum256([]byte(filepath.Clean(p)))
				m.ID, manifest = "legacy-"+hex.EncodeToString(legacyID[:16]), ""
			} else if err != nil || json.Unmarshal(b, &m) != nil || m.ID == "" {
				continue // unreadable or malformed metadata is not an empty one
			}
			key := "reasonix:" + m.ID
			rev := sha256.Sum256(b)
			f := file{agent: "reasonix", path: p, key: key, main: true, sid: m.ID, manifest: manifest, rev: reasonixRevision + hex.EncodeToString(rev[:])}
			if !stat(&f) {
				continue
			}
			ledger := file{agent: "reasonix", path: filepath.Join(dir, stem+".turns.jsonl"), key: key, sid: m.ID}
			pair := []file{}
			if stat(&ledger) {
				f.rev += ":ledger"
				pair = []file{f, ledger}
			} else {
				wire := file{agent: "reasonix", path: filepath.Join(dir, stem+".wire.jsonl"), key: key, sid: m.ID, manifest: p}
				if stat(&wire) {
					f.rev += ":wire"
					// Wire sequences restart on resume. Rebuild this bounded log when it
					// or its transcript/totals changes; never persist a global seq watermark.
					wire.rev = f.rev + ":" + strconv.FormatInt(f.size, 10) + ":" + strconv.FormatInt(f.mod.UnixNano(), 10) + ":" + prefixHash(p, f.size)
					for _, sidecar := range []string{p + ".telemetry.json", filepath.Join(dir, stem+".wire.meta.json")} {
						bytes, err := os.ReadFile(sidecar)
						if err != nil {
							wire.rev += ":" + err.Error()
							continue
						}
						digest := sha256.Sum256(bytes)
						wire.rev += ":" + hex.EncodeToString(digest[:])
					}
					pair = []file{f, wire}
				} else {
					f.rev += ":no-ledger"
					pair = []file{f}
				}
			}
			// A copied session retains its id. Read one copy, not two totals.
			old := chosen[m.ID]
			if len(old) == 0 || f.mod.After(old[0].mod) || f.mod.Equal(old[0].mod) && f.path < old[0].path {
				chosen[m.ID] = pair
			}
		}
	}
	reasonixNativeFiles(chosen)
	ids := make([]string, 0, len(chosen))
	for id := range chosen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, chosen[id]...)
	}
	return out
}

func reasonixSidecar(stem string) bool {
	for _, suffix := range []string{".events", ".turns", ".conflicts", ".guardian", ".wire", ".adjudication", ".execution"} {
		if strings.HasSuffix(stem, suffix) {
			return true
		}
	}
	return false
}

type reasonixMessage struct {
	NativeSeq    uint64  `json:"-"`
	DisplaySize  int     `json:"-"`
	ID           string  `json:"id"`
	Origin       string  `json:"origin"`
	Role         string  `json:"role"`
	ModelRef     string  `json:"modelRef"`
	HostAuthored bool    `json:"host_authored"`
	Content      string  `json:"content"`
	RawContent   *string `json:"raw_content"`
	Thinking     string  `json:"reasoning_content"`
	Name         string  `json:"name"`
	At           int64   `json:"createdAt"`
	Local        bool    `json:"local_only"`
	Tools        []struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"tool_calls"`
}

var reasonixUsageKind = regexp.MustCompile(`"(?:kind|recordType)"\s*:\s*"(?:usage|checkpoint)"`)

// Most ledger bytes are streamed text and tool output. Reject them from the
// first scan buffer, before gathering a potentially multi-megabyte line.
func reasonixUsageHead(b []byte) bool { return reasonixUsageKind.Match(b) }

func reasonixLine(s *state, b []byte, main bool) {
	if !main {
		var e struct {
			Record    string `json:"recordType"`
			Session   string `json:"sessionId"`
			Seq       uint64 `json:"seq"`
			Kind      string `json:"kind"`
			At        int64  `json:"createdAt"`
			Compacted uint64 `json:"compactedThroughSeq"`
			Event     struct {
				Usage *struct {
					Prompt    int    `json:"promptTokens"`
					Output    int    `json:"completionTokens"`
					Hit       int    `json:"cacheHitTokens"`
					Model     string `json:"model"`
					Estimated bool   `json:"estimated"`
				} `json:"usage"`
			} `json:"event"`
		}
		if json.Unmarshal(b, &e) != nil {
			s.UsageIncomplete = true
			return
		}
		if e.Session != s.ID {
			s.UsageIncomplete = true
			return
		}
		if e.Record == "checkpoint" {
			if e.Compacted > 0 {
				s.UsageIncomplete = true
			}
			return
		}
		if e.Record != "event" || e.Kind != "usage" || e.Event.Usage == nil || e.Seq == 0 || e.Seq <= s.ReasonixSeq {
			return
		}
		// Sequence numbers increase across turns in one native ledger. Persist
		// the usage watermark with the aggregate, including after a cold start.
		s.ReasonixSeq = e.Seq
		u := e.Event.Usage
		if u.Estimated || u.Model == "" || u.Prompt < 0 || u.Output < 0 || u.Hit < 0 || u.Hit > u.Prompt || e.At <= 0 {
			s.UsageIncomplete = true
			return
		}
		at := time.UnixMilli(e.At)
		s.saw(at, false)
		// Reasoning is part of completion; sessionCache* and context* fields
		// are gauges, not additional billable usage.
		s.use(dateOf(at), strings.TrimPrefix(u.Model, "magpie/"), Tokens{Input: u.Prompt - u.Hit, Output: u.Output, CacheRead: u.Hit})
		return
	}
	var m reasonixMessage
	if json.Unmarshal(b, &m) != nil || m.Local || m.HostAuthored || m.Origin == "host" {
		return
	}
	if m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
		return
	}
	if m.Role == "user" && s.Title == "" {
		s.Title = title(reasonixTitleText(m))
	}
	// A model identity is useful even when this release stores no session
	// token counts. Keep presence separate from usage; never infer tokens.
	model := strings.TrimPrefix(m.ModelRef, "magpie/")
	if m.Role == "assistant" && model != "" {
		if s.Models == nil {
			s.Models = map[string]Tokens{}
		}
		if _, ok := s.Models[model]; !ok {
			s.Models[model] = Tokens{}
		}
	}
	if m.Role == "user" {
		s.ReasonixAt = m.At
		if cwd := reasonixWorkspace(m.Content); m.RawContent != nil && cwd != "" {
			s.ReasonixCwd = cwd
		}
	}
	if m.At <= 0 && m.Role == "assistant" {
		m.At = s.ReasonixAt
	}
	if m.At <= 0 {
		return
	}
	at := time.UnixMilli(m.At)
	s.saw(at, true)
	d := s.day(dateOf(at))
	if m.Role == "user" {
		d.Prompts++
	}
	if m.Role == "assistant" {
		d.Replies++
		if model != "" {
			if d.Models == nil {
				d.Models = map[string]Tokens{}
			}
			if _, ok := d.Models[model]; !ok {
				d.Models[model] = Tokens{}
			}
		}
		for _, tool := range m.Tools {
			s.tool(at, tool.Name, "")
		}
	}
}

func reasonixMetadata(s *state, f file) {
	s.ID = f.sid
	if !f.main {
		return
	}
	s.DBRevision = f.rev
	s.UsageIncomplete = f.manifest == "" || strings.HasSuffix(f.rev, ":no-ledger")
	if f.manifest == "" {
		s.Cwd, s.Named, s.Custom = s.ReasonixCwd, "", ""
		return
	}
	var m reasonixMeta
	b, err := os.ReadFile(f.manifest)
	if err != nil || json.Unmarshal(b, &m) != nil {
		return
	}
	s.Cwd, s.Named, s.Custom, s.DBRevision = m.Cwd, m.Title, m.Custom, f.rev
	if s.Cwd == "" {
		s.Cwd = s.ReasonixCwd
	}
	if s.Title == "" {
		s.Title = title(m.Preview)
	}
	if !m.Created.IsZero() && (s.Start.IsZero() || m.Created.Before(s.Start)) {
		s.Start = m.Created
	}
	if m.Updated.After(s.Last) {
		s.Last = m.Updated
	}
}

func reasonixTranscript(path string, add func(bool, Part) bool) error {
	if filepath.Base(path) == "events.frames" || filepath.Base(path) == "events.jsonl" {
		return reasonixStoreTranscript(path, add)
	}
	_, err := scanAt(path, 0, nil, func(b []byte, _, _ int64) bool {
		var m reasonixMessage
		if json.Unmarshal(b, &m) != nil {
			return true
		}
		return reasonixMessageParts(m, add)
	})
	return err
}

func reasonixMessageParts(m reasonixMessage, add func(bool, Part) bool) bool {
	if m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
		return true
	}
	if m.Thinking != "" && !add(false, Part{Role: m.Role, Kind: "thinking", Text: m.Thinking}) {
		return false
	}
	kind := "text"
	if m.Role == "tool" {
		kind = "tool_result"
	}
	if !add(m.Role == "user", Part{Role: m.Role, Kind: kind, Name: m.Name, Text: m.userText()}) {
		return false
	}
	for _, tool := range m.Tools {
		if !add(false, Part{Role: m.Role, Kind: "tool_use", Name: tool.Name, Text: tool.Arguments}) {
			return false
		}
	}
	return true
}

// raw_content is the human input before the host wraps workspace, skills and
// execution policy around it. Presence (including empty) wins over content.
func (m reasonixMessage) userText() string {
	if m.Role == "user" && m.RawContent != nil {
		return *m.RawContent
	}
	return m.Content
}

// Preview only: preserve original transcript bytes and explicit raw input.
// These leading blocks are the producer's TransientUserBlockTags contract.
var reasonixTitleBlocks = regexp.MustCompile(`(?s)^\s*<(response-language|reasoning-language|memory-update|background-jobs|active-goal|autoresearch-runtime|hook-context|capability-route|interrupted-turn-recovery|execution-policy)(?:\s+[^>]*)?>.*?</(?:response-language|reasoning-language|memory-update|background-jobs|active-goal|autoresearch-runtime|hook-context|capability-route|interrupted-turn-recovery|execution-policy)>\s*`)

func reasonixTitleText(m reasonixMessage) string {
	if m.RawContent != nil {
		return *m.RawContent
	}
	text := m.Content
	for {
		stripped := reasonixTitleBlocks.ReplaceAllString(text, "")
		if stripped == text {
			break
		}
		text = stripped
	}
	text = strings.TrimSpace(text)
	// Old compaction summaries are model context, not a user title.
	if m.Origin != "user" && strings.HasPrefix(text, "<compaction-summary>") {
		return ""
	}
	return text
}

var reasonixWorkspaceLine = regexp.MustCompile(`(?m)^Current workspace: ("(?:[^"\\]|\\.)*")\.`)

func reasonixWorkspace(content string) string {
	if !strings.HasPrefix(content, "<workspace>\n") {
		return ""
	}
	match := reasonixWorkspaceLine.FindStringSubmatch(content)
	var cwd string
	if len(match) == 2 {
		_ = json.Unmarshal([]byte(match[1]), &cwd)
	}
	return cwd
}

// parseReasonixWire reads request frames, not messages. The log is capped at
// 8 MB by Reasonix; unchanged files reuse their cached summary. Models come
// from the usage quote, or the executor turn that owns the request. Dates come
// from that turn's actual user-message index, not the file mtime.
func parseReasonixWire(f file) *state {
	s := &state{ID: f.sid, Size: f.size, Mod: f.mod.UnixNano(), DBRevision: f.rev}
	// Find the last referenced user index before reading timestamp anchors.
	// Large transcript tails (tool output/streaming text) are irrelevant to
	// these frames and are not reread on each wire update.
	maxIndex := -1
	_, err := scanAt(f.path, 0, func(b []byte) bool { return bytes.Contains(b, []byte(`"turn_started"`)) }, func(b []byte, _, _ int64) bool {
		var frame struct {
			Kind  string `json:"kind"`
			Index int    `json:"msgIndex"`
		}
		if json.Unmarshal(b, &frame) != nil {
			s.UsageIncomplete = true
			return true
		}
		if frame.Kind == "turn_started" && frame.Index > maxIndex {
			maxIndex = frame.Index
		}
		return true
	})
	if err != nil || maxIndex < 0 {
		s.UsageIncomplete = true
	}
	var times []int64
	if maxIndex >= 0 {
		_, err = scanAt(f.manifest, 0, nil, func(b []byte, _, _ int64) bool {
			var m struct {
				Role string `json:"role"`
				At   int64  `json:"createdAt"`
			}
			if json.Unmarshal(b, &m) != nil {
				s.UsageIncomplete = true
			}
			if m.Role != "user" {
				m.At = 0
			}
			times = append(times, m.At)
			return len(times) <= maxIndex
		})
		if err != nil {
			s.UsageIncomplete = true
		}
	}
	model := ""
	var at int64
	count := 0
	var total, epoch Tokens
	epochCount := 0
	var seq uint64
	firstTurn := true
	off, scanErr := scanAt(f.path, 0, nil, func(b []byte, _, _ int64) bool {
		var frame struct {
			Kind         string `json:"kind"`
			Seq          uint64 `json:"seq"`
			AuthoredTurn int    `json:"authoredTurn"`
			Model        string `json:"modelRef"`
			Index        int    `json:"msgIndex"`
			Usage        *struct {
				Prompt    int    `json:"promptTokens"`
				Output    int    `json:"completionTokens"`
				Hit       int    `json:"cacheHitTokens"`
				Source    string `json:"source"`
				Estimated bool   `json:"estimated"`
				Quote     struct {
					Model string `json:"modelRef"`
				} `json:"costQuote"`
			} `json:"usage"`
		}
		if json.Unmarshal(b, &frame) != nil {
			s.UsageIncomplete = true
			return true
		}
		if frame.Seq > 0 {
			if seq > 0 && frame.Seq <= seq {
				epoch, epochCount = Tokens{}, 0
			}
			seq = frame.Seq
		}
		if frame.Kind == "turn_started" {
			if firstTurn && frame.AuthoredTurn > 1 {
				s.UsageIncomplete = true
			}
			firstTurn = false
			model, at = frame.Model, 0
			if frame.Index >= 0 && frame.Index < len(times) {
				at = times[frame.Index]
			}
			return true
		}
		if frame.Kind != "usage" {
			return true
		}
		u := frame.Usage
		if u == nil {
			s.UsageIncomplete = true
			return true
		}
		ref := u.Quote.Model
		if ref == "" && (u.Source == "" || u.Source == "executor") {
			ref = model
		}
		if ref == "" || at <= 0 || u.Estimated || u.Prompt < 0 || u.Output < 0 || u.Hit < 0 || u.Hit > u.Prompt {
			s.UsageIncomplete = true
			return true
		}
		tokens := Tokens{Input: u.Prompt - u.Hit, Output: u.Output, CacheRead: u.Hit}
		count++
		epochCount++
		epoch.add(tokens)
		total.add(tokens)
		stamp := time.UnixMilli(at)
		s.saw(stamp, false)
		s.use(dateOf(stamp), strings.TrimPrefix(ref, "magpie/"), tokens)
		return true
	})
	if scanErr != nil || off < f.size {
		s.UsageIncomplete = true
	}
	meta := strings.TrimSuffix(f.path, ".jsonl") + ".meta.json"
	if b, err := os.ReadFile(meta); err == nil {
		var m struct {
			Truncated bool `json:"truncated"`
		}
		if json.Unmarshal(b, &m) != nil || m.Truncated {
			s.UsageIncomplete = true
		}
	} else if !os.IsNotExist(err) {
		s.UsageIncomplete = true
	}
	if count == 0 {
		s.UsageIncomplete = true
	}
	// Telemetry is a check on retained-frame coverage, not another token source.
	// It has no model/date breakdown, so adding it would count requests twice.
	if b, err := os.ReadFile(f.manifest + ".telemetry.json"); err == nil {
		var report struct {
			Version int `json:"version"`
			Usage   struct {
				Prompt    int  `json:"promptTokens"`
				Output    int  `json:"completionTokens"`
				Hit       int  `json:"cacheHitTokens"`
				Count     int  `json:"requestCount"`
				Estimated bool `json:"estimated"`
			} `json:"usage"`
		}
		valid := json.Unmarshal(b, &report) == nil && report.Version == 1 && !report.Usage.Estimated
		matches := func(t Tokens, n int) bool {
			return report.Usage.Prompt == t.Input+t.CacheRead && report.Usage.Output == t.Output && report.Usage.Hit == t.CacheRead && report.Usage.Count == n
		}
		// 2.29.0 resets its telemetry accumulator on a fresh serve --resume;
		// the wire log retains prior processes. Compare against either shape.
		if !valid || !matches(total, count) && !matches(epoch, epochCount) {
			s.UsageIncomplete = true
		}
	} else if !os.IsNotExist(err) {
		s.UsageIncomplete = true
	}
	return s
}

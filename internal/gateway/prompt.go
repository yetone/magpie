package gateway

// What a request's prompt is made of: the agent's system prompt, its tool
// definitions, the skills it lists, the instruction files it read in
// (CLAUDE.md, AGENTS.md), the files its tools read, its tools' other output, and the
// conversation, each as the tokens it takes of the model's context window.
// Read from the body the agent sent, as it sent it: the parsed Request has
// the system blocks run together, and Codex's additional_tools lifted out.
//
// The sizes are estimated as the body is read, then scaled to the prompt
// the vendor counted once it answers (calibrate). The Prompt keeps only
// names — the instruction files' and read files' paths, the tools' names,
// "turn 3" — never the prompt's text, and it is what goes in the routing
// history on disk. An item's text is read again from the body only when
// the context window card asks for it (PromptText), from the last few
// requests' bodies the trace holds in memory (keepBody).

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// The kinds of a prompt's parts, in the order they are shown.
const (
	PartSystem  = "system"  // the agent's own prompt: system, developer and harness messages
	PartTools   = "tools"   // tool definitions
	PartSkills  = "skills"  // the skills the agent lists for the model to load
	PartMemory  = "memory"  // instruction files, reminders
	PartFiles   = "files"   // files and images the tools read in
	PartResults = "results" // the tools' other output
	PartChat    = "chat"    // what the user and the model said, the model's calls
)

var partOrder = []string{PartSystem, PartTools, PartSkills, PartMemory, PartFiles, PartResults, PartChat}

// Prompt is a request's prompt, part by part.
type Prompt struct {
	Window int `json:"window,omitempty"` // the context window of the model that answered, when known
	// Tokens: the prompt, as the vendor counted it when Counted, the parts
	// scaled to it; estimated from the body before that
	Tokens  int  `json:"tokens"`
	Counted bool `json:"counted,omitempty"`
	// Held: the request named a previous response, whose turns the vendor
	// holds; they are in Tokens once counted, as the conversation
	Held  bool         `json:"held,omitempty"`
	Turns int          `json:"turns,omitempty"` // the user's turns in it
	Parts []PromptPart `json:"parts"`
	// sent is what the body's bytes were made of (sentSizes), for the
	// ledger row's Record.UpParts; never written to the routing history
	sent sentSizes
}

// sentSizes is a body's bytes by what they were, as the builder read the
// pieces it already had in hand: the agent's own prompt and its
// instructions (system, skills, memory), the tools' definitions, the
// images and documents sent inline, and all it read (the rest is the
// conversation, and the JSON around it). Counting them is a length added
// per piece, no second read of the body (#upload).
type sentSizes struct{ system, tools, images, all int }

// size is how many bytes a piece took in the body: its raw JSON, its
// text, an image's source, a sealed piece's size.
func (pc *PromptPiece) size() int { return len(pc.raw) + len(pc.img) + len(pc.Text) + pc.Size }

// PromptPart is one kind of what a prompt holds.
type PromptPart struct {
	Kind   string       `json:"kind"`
	Tokens int          `json:"tokens"`
	Items  []PromptItem `json:"items,omitempty"` // largest first, promptItems at most
}

// PromptItem is one thing in a part: a tool, an MCP server's tools, an
// instruction file, a file read, a turn.
type PromptItem struct {
	Name   string `json:"name"`
	Tag    string `json:"tag,omitempty"` // what it is: mcp, user, project, skills, reminder, image, read, shell, turn…
	N      int    `json:"n,omitempty"`   // how many it gathers: an MCP server's tools, a tool's results
	Tokens int    `json:"tokens"`
}

// promptItems is how many items a part keeps; the rest are summed in one.
const promptItems = 24

// tokensOf estimates the tokens of a text: about four characters a token
// for ASCII, a token a character for the rest (CJK, mostly).
func tokensOf(s string) int {
	ascii, other := 0, 0
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			ascii++
			i++
			continue
		}
		_, n := utf8.DecodeRuneInString(s[i:])
		other++
		i += n
	}
	return (ascii+3)/4 + other
}

func rawTokens(b json.RawMessage) int { return (len(b) + 3) / 4 }

// imageTokens is what an image is taken to cost: about what Anthropic
// counts for a 1092×1092 one.
const imageTokens = 1600

// promptBuilder reads a body through decodeJSONView: what it keeps of
// the body, a piece's raw JSON, lives only as long as the builder, which
// the body outlives (promptOf's request; promptPieces' kept body, its
// pieces read out before PromptText returns). Each item's content and
// output copied out was 0.8 of the 14.9 MB a 680 KB Codex request
// allocated (#3383).
type promptBuilder struct {
	parts map[string]map[string]*PromptItem
	turn  int
	held  bool
	calls map[string]toolCall // call id → what was called, for its result
	// keep: each item's text is kept too, in pieces, by part and item
	// (itemKey), for PromptText; off when only the sizes are read
	keep   bool
	pieces map[string][]PromptPiece
	// sent: the pieces' bytes by what they were (sentSizes); again off
	// while one tool result is shared among the files it read (result),
	// so its bytes count once
	sent  sentSizes
	again bool
}

// PromptPiece is a stretch of an item's text as the request sent it: a
// message, a tool's definition, a call and its input, what a tool
// answered. Sealed reasoning is only named, with its size; an image is
// its picture when the request carried it inline (Src).
type PromptPiece struct {
	// Role: system, user, assistant, thinking, call, result, tool, image,
	// sealed; "" for text that is the item itself (an instruction file)
	Role string `json:"role,omitempty"`
	Name string `json:"name,omitempty"` // the tool called or defined
	Text string `json:"text,omitempty"`
	Size int    `json:"size,omitempty"` // a sealed piece's bytes
	// Src: an image's picture as a data: URL, when the request sent it
	// inline as a PNG, JPEG, GIF or WebP; a linked image stays a name
	Src string `json:"src,omitempty"`
	raw []byte // JSON (or a custom tool's text), Text once read out
	img []byte // an image's source as the request sent it, Src once read out
}

func txt(role, s string) PromptPiece { return PromptPiece{Role: role, Text: s} }

func rawPiece(role, name string, raw []byte) PromptPiece {
	return PromptPiece{Role: role, Name: name, raw: raw}
}

// imgPiece is an image, its source (an Anthropic source, an image_url,
// Gemini's inlineData) kept to be read out as its picture.
func imgPiece(src []byte) PromptPiece { return PromptPiece{Role: "image", img: src} }

func sealedPiece(n int) PromptPiece { return PromptPiece{Role: "sealed", Size: n} }

// itemKey is an item's key in its part: its tag and name.
func itemKey(tag, name string) string { return tag + "\x00" + name }

type toolCall struct {
	name  string
	files []string // the files it reads, when it reads files
}

func newPromptBuilder() *promptBuilder {
	return &promptBuilder{parts: map[string]map[string]*PromptItem{}, calls: map[string]toolCall{}}
}

func (b *promptBuilder) add(kind, name, tag string, tokens int, pc PromptPiece) {
	if tokens <= 0 {
		return
	}
	m := b.parts[kind]
	if m == nil {
		m = map[string]*PromptItem{}
		b.parts[kind] = m
	}
	key := itemKey(tag, name)
	it := m[key]
	if it == nil {
		it = &PromptItem{Name: name, Tag: tag}
		m[key] = it
	}
	it.N++
	it.Tokens += tokens
	if !b.again {
		n := pc.size()
		b.sent.all += n
		switch {
		case tag == "image":
			b.sent.images += n
		case kind == PartSystem || kind == PartSkills || kind == PartMemory:
			b.sent.system += n
		case kind == PartTools:
			b.sent.tools += n
		}
	}
	if b.keep {
		b.pieces[kind+"\x00"+key] = append(b.pieces[kind+"\x00"+key], pc)
	}
}

// chat adds to the conversation's current turn.
func (b *promptBuilder) chat(tokens int, pc PromptPiece) {
	if b.turn == 0 {
		b.turn = 1
	}
	b.add(PartChat, strconv.Itoa(b.turn), "turn", tokens, pc)
}

func (b *promptBuilder) newTurn() { b.turn++ }

func (b *promptBuilder) prompt() *Prompt {
	p := &Prompt{Held: b.held, Turns: b.turn, Parts: []PromptPart{}, sent: b.sent}
	for _, kind := range partOrder {
		m := b.parts[kind]
		if len(m) == 0 {
			continue
		}
		part := PromptPart{Kind: kind}
		items := make([]PromptItem, 0, len(m))
		for _, it := range m {
			part.Tokens += it.Tokens
			c := *it
			if c.Tag != "mcp" && c.Tag != "namespace" && c.Tag != "result" {
				c.N = 0 // a count only where it says something
			}
			if c.N == 1 {
				c.N = 0
			}
			items = append(items, c)
		}
		if kind == PartChat {
			slices.SortFunc(items, func(a, b PromptItem) int {
				x, _ := strconv.Atoi(a.Name)
				y, _ := strconv.Atoi(b.Name)
				return x - y
			})
			// the turns keep the latest ones, oldest first
			if len(items) > promptItems {
				rest := PromptItem{Name: "earlier", Tag: "turns", N: len(items) - promptItems + 1}
				for _, it := range items[:len(items)-promptItems+1] {
					rest.Tokens += it.Tokens
				}
				items = append([]PromptItem{rest}, items[len(items)-promptItems+1:]...)
			}
		} else {
			slices.SortFunc(items, func(a, b PromptItem) int {
				if a.Tokens != b.Tokens {
					return b.Tokens - a.Tokens
				}
				return strings.Compare(a.Name, b.Name)
			})
			if len(items) > promptItems {
				rest := PromptItem{Name: "other", Tag: "more", N: len(items) - promptItems + 1}
				for _, it := range items[promptItems-1:] {
					rest.Tokens += it.Tokens
				}
				items = append(items[:promptItems-1:promptItems-1], rest)
			}
		}
		part.Items = items
		p.Tokens += part.Tokens
		p.Parts = append(p.Parts, part)
	}
	return p
}

// skillsApart reads a prompt kept when the skills were memory: they are
// their own part, before it.
func (p *Prompt) skillsApart() {
	if p == nil {
		return
	}
	for i, x := range p.Parts {
		if x.Kind != PartMemory {
			continue
		}
		skills := PromptPart{Kind: PartSkills}
		kept := x.Items[:0:0]
		for _, it := range x.Items {
			if it.Tag == "skills" {
				skills.Tokens += it.Tokens
				skills.Items = append(skills.Items, it)
			} else {
				kept = append(kept, it)
			}
		}
		if skills.Tokens == 0 {
			return
		}
		x.Tokens -= skills.Tokens
		x.Items = kept
		parts := append(append(p.Parts[:i:i], skills), x)
		if x.Tokens <= 0 {
			parts = parts[:i+1]
		}
		p.Parts = append(parts, p.Parts[i+1:]...)
		return
	}
}

// promptOf reads what a request's prompt holds; nil when the body isn't
// one it can read.
func promptOf(from provider.Protocol, body []byte) (p *Prompt) {
	defer func() {
		if recover() != nil {
			p = nil
		}
	}()
	b := newPromptBuilder()
	var ok bool
	switch from {
	case provider.Anthropic:
		ok = b.anthropic(body)
	case provider.Responses:
		ok = b.responses(body)
	case provider.Chat:
		ok = b.chatCompletions(body)
	case provider.Gemini:
		ok = b.gemini(body)
	}
	if !ok {
		return nil
	}
	return b.prompt()
}

// calibrate scales the parts to the prompt the vendor counted, and notes
// the window of the model that answered.
func (p *Prompt) calibrate(counted, window int) {
	if p == nil {
		return
	}
	if window > 0 {
		p.Window = window
	}
	if counted <= 0 || p.Counted {
		return
	}
	est := p.Tokens
	if est <= 0 {
		return
	}
	scale := float64(counted) / float64(est)
	sum := 0
	for i := range p.Parts {
		part := &p.Parts[i]
		part.Tokens = int(float64(part.Tokens)*scale + 0.5)
		for j := range part.Items {
			part.Items[j].Tokens = int(float64(part.Items[j].Tokens)*scale + 0.5)
		}
		sum += part.Tokens
	}
	// what rounding left goes to the largest part, so they add up
	if n := len(p.Parts); n > 0 && sum != counted {
		big := 0
		for i := range p.Parts {
			if p.Parts[i].Tokens > p.Parts[big].Tokens {
				big = i
			}
		}
		p.Parts[big].Tokens += counted - sum
	}
	p.Tokens, p.Counted = counted, true
}

// --- text: instruction files, reminders, harness messages ---

var (
	// Claude Code: "Contents of /path/CLAUDE.md (project instructions, …):"
	contentsOf = lazyre.New(`(?m)^Contents of (\S.*?) \(([^()\n]*(?:\([^()\n]*\)[^()\n]*)*)\):[ \t]*$`)
	// OpenCode: "Instructions from: /path/AGENTS.md"
	instructionsFrom = lazyre.New(`(?m)^Instructions from: (\S.*?)[ \t]*$`)
	// Codex: "# AGENTS.md instructions for /path"
	agentsFor = lazyre.New(`^# (\S+\.md) instructions for (\S.*?)[ \t]*\n`)
	leadTag   = lazyre.New(`^\s*<([a-zA-Z_][\w -]{0,40})>`)
)

// memoryTag says whose an instruction file is from how the agent
// describes it, or its path.
func memoryTag(path, desc string) string {
	d := strings.ToLower(desc)
	switch {
	case strings.Contains(d, "user's") || strings.Contains(d, "global"):
		return "user"
	case strings.Contains(d, "local"):
		return "local"
	case strings.Contains(d, "auto-memory") || strings.Contains(d, "memory"):
		return "memory"
	case d != "":
		return "project"
	}
	if strings.Contains(path, "/.claude/") || strings.Contains(path, "/.codex/") || strings.Contains(path, "/.config/") {
		return "user"
	}
	return "project"
}

// splitMemory takes the instruction files out of a text: each is added as
// memory, and what is left is returned.
func (b *promptBuilder) splitMemory(text string) string {
	type section struct {
		at, body    int
		path, desc  string
		openCodeish bool
	}
	var ss []section
	for _, m := range contentsOf.FindAllStringSubmatchIndex(text, -1) {
		ss = append(ss, section{at: m[0], body: m[1], path: text[m[2]:m[3]], desc: text[m[4]:m[5]]})
	}
	for _, m := range instructionsFrom.FindAllStringSubmatchIndex(text, -1) {
		ss = append(ss, section{at: m[0], body: m[1], path: text[m[2]:m[3]], openCodeish: true})
	}
	if len(ss) == 0 {
		return text
	}
	slices.SortFunc(ss, func(a, b section) int { return a.at - b.at })
	var rest strings.Builder
	rest.WriteString(text[:ss[0].at])
	for i, s := range ss {
		end := len(text)
		if i+1 < len(ss) {
			end = ss[i+1].at
		}
		chunk := text[s.at:end]
		// a reminder's closing tag and what follows it aren't the file's
		if j := strings.Index(chunk, "</system-reminder>"); j >= 0 {
			rest.WriteString(chunk[j:])
			chunk = chunk[:j]
		}
		b.add(PartMemory, s.path, memoryTag(s.path, s.desc), tokensOf(chunk), txt("", chunk))
	}
	return rest.String()
}

// skillsList heads the skills Claude Code lists, in its own prompt or a
// reminder; the list runs to the next blank line.
const skillsList = "The following skills are available for use with the Skill tool:"

// splitSkills takes the list of skills out of a text, as skills, and
// returns what is left.
func (b *promptBuilder) splitSkills(text, role string) string {
	at := strings.Index(text, skillsList)
	if at < 0 {
		return text
	}
	body := text[at+len(skillsList):]
	items := strings.TrimLeft(body, "\n")
	end := len(text)
	if j := strings.Index(items, "\n\n"); j >= 0 {
		end -= len(items) - j
	}
	list := text[at:end]
	b.add(PartSkills, "skills", "skills", tokensOf(list), txt(role, list))
	return text[:at] + strings.TrimLeft(text[end:], "\n")
}

// harnessName names a system or developer text by its leading tag, or
// what it is.
func harnessName(text, fallback string) string {
	if m := leadTag.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	if strings.HasPrefix(text, "# Environment") {
		return "environment"
	}
	return fallback
}

// systemText adds the agent's own prompt, with the instruction files in
// it taken out as memory.
func (b *promptBuilder) systemText(text, name string) {
	rest := b.splitSkills(b.splitMemory(text), "system")
	if strings.HasPrefix(strings.TrimSpace(rest), "<skills_instructions>") {
		b.add(PartSkills, "skills", "skills", tokensOf(rest), txt("system", rest))
		return
	}
	if strings.TrimSpace(rest) == "" {
		return
	}
	b.add(PartSystem, harnessName(rest, name), "", tokensOf(rest), txt("system", rest))
}

// userText adds a user's text: an instruction file or reminder the agent
// put in as the user, or what the user said. It says which.
func (b *promptBuilder) userText(text string) (said bool) {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "<system-reminder>"):
		rest := b.splitSkills(b.splitMemory(t), "user")
		rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(rest), "<system-reminder>"), "</system-reminder>"))
		if rest != "" {
			b.add(PartMemory, "reminders", "reminder", tokensOf(rest), txt("user", rest))
		}
		return false
	case agentsFor.MatchString(t):
		m := agentsFor.FindStringSubmatch(t)
		path := strings.TrimRight(m[2], "/") + "/" + m[1]
		b.add(PartMemory, path, "project", tokensOf(t), txt("user", t))
		return false
	case strings.HasPrefix(t, "<environment_context>") || strings.HasPrefix(t, "<user_instructions>") ||
		strings.HasPrefix(t, "<turn_aborted>") || strings.HasPrefix(t, "<user_shell_command>"):
		b.add(PartSystem, harnessName(t, "context"), "", tokensOf(t), txt("user", t))
		return false
	}
	b.chat(tokensOf(text), txt("user", text))
	return true
}

// --- tool calls and what they read ---

var (
	// the tools that read a file, and where they take its path
	readTools = map[string]bool{"Read": true, "read": true, "read_file": true, "ReadFile": true, "view": true,
		"View": true, "NotebookRead": true, "readFile": true, "open_file": true, "cat": true}
	pathKeys  = []string{"file_path", "path", "filePath", "target_file", "notebook_path", "file", "filename", "uri"}
	cmdOfExec = lazyre.New(`\bcmd\s*:\s*"((?:[^"\\]|\\.)*)"`)
	toolOfJS  = lazyre.New(`\btools\.(\w+)\s*\(`)
	readCmds  = map[string]bool{"cat": true, "head": true, "tail": true, "nl": true, "bat": true, "less": true, "more": true}
)

// filesOfCommand is the files a shell command reads, when all it does is
// read files: cat, head, tail, nl, bat, sed -n.
func filesOfCommand(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	// bash -lc "…", as Codex sends its shell tool's
	for _, pre := range []string{"bash -lc ", "bash -c ", "sh -c ", "zsh -lc "} {
		if strings.HasPrefix(cmd, pre) {
			cmd = strings.Trim(strings.TrimSpace(cmd[len(pre):]), `"'`)
		}
	}
	if strings.ContainsAny(cmd, "|;&><`$") {
		// a pipeline or more than one command: not a plain read; but
		// "cd dir && cat x" still is
		if i := strings.Index(cmd, "&&"); i >= 0 && strings.HasPrefix(cmd, "cd ") && !strings.ContainsAny(cmd[i+2:], "|;&><`$") {
			cmd = strings.TrimSpace(cmd[i+2:])
		} else {
			return nil
		}
	}
	f := strings.Fields(cmd)
	if len(f) < 2 {
		return nil
	}
	sed := f[0] == "sed" && slices.Contains(f, "-n")
	if !readCmds[f[0]] && !sed {
		return nil
	}
	var files []string
	for i := 1; i < len(f); i++ {
		a := strings.Trim(f[i], `"'`)
		if strings.HasPrefix(a, "-") {
			if (a == "-n" || a == "-c") && !sed && i+1 < len(f) {
				i++ // head -n 40
			}
			continue
		}
		if sed && strings.HasSuffix(a, "p") && strings.ContainsAny(a, "0123456789,") && !strings.Contains(a, "/") && !strings.Contains(a, ".") {
			continue // sed -n 1,80p
		}
		if _, err := strconv.Atoi(a); err == nil {
			continue
		}
		files = append(files, a)
	}
	return files
}

// callOf reads what a tool call does from its name and input: the files
// it reads, and the name its results go by.
func callOf(name string, input []byte) toolCall {
	c := toolCall{name: name}
	var args map[string]json.RawMessage
	_ = decodeJSONView(input, &args)
	str := func(k string) string {
		var s string
		if decodeJSONView(args[k], &s) == nil {
			return s
		}
		return ""
	}
	if readTools[name] {
		for _, k := range pathKeys {
			if s := str(k); s != "" {
				c.files = []string{s}
				return c
			}
		}
	}
	cmd := str("command")
	if cmd == "" {
		cmd = str("cmd")
	}
	if cmd == "" {
		var argv []string
		if decodeJSONView(args["command"], &argv) == nil {
			cmd = strings.Join(argv, " ")
		}
	}
	if cmd == "" && args == nil {
		// Codex's exec takes JavaScript: text(await tools.exec_command({cmd:"cat x"}))
		s := string(input)
		if m := toolOfJS.FindStringSubmatch(s); m != nil {
			c.name = m[1]
		}
		if m := cmdOfExec.FindStringSubmatch(s); m != nil {
			cmd, _ = strconv.Unquote(`"` + m[1] + `"`)
		}
	}
	if cmd != "" {
		c.files = filesOfCommand(cmd)
	}
	return c
}

// result adds what a tool answered: the files it read, or its output.
func (b *promptBuilder) result(callID string, tokens int, pc PromptPiece) {
	c, ok := b.calls[callID]
	if !ok {
		c.name = "tool"
	}
	if pc.Name == "" {
		pc.Name = c.name
	}
	if len(c.files) > 0 {
		each := tokens / len(c.files)
		for i, f := range c.files {
			n := each
			if i == 0 {
				n += tokens - each*len(c.files)
			}
			tag := "read"
			if !readTools[c.name] {
				tag = "shell"
			}
			b.again = i > 0
			b.add(PartFiles, f, tag, n, pc)
		}
		b.again = false
		return
	}
	b.add(PartResults, c.name, "result", tokens, pc)
}

// tool adds a tool definition: an MCP server's tools as one.
func (b *promptBuilder) tool(name string, tokens int, def []byte) {
	pc := rawPiece("tool", name, def)
	if strings.HasPrefix(name, "mcp__") {
		rest := strings.TrimPrefix(name, "mcp__")
		server := rest
		if i := strings.Index(rest, "__"); i > 0 {
			server = rest[:i]
		}
		b.add(PartTools, server, "mcp", tokens, pc)
		return
	}
	b.add(PartTools, name, "", tokens, pc)
}

// --- Anthropic Messages ---

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Data      string          `json:"data"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Source    json.RawMessage `json:"source"`
}

// blocksOf reads content that is a string or a list of blocks.
func blocksOf(c json.RawMessage) []rawBlock {
	var s string
	if decodeJSONView(c, &s) == nil {
		return []rawBlock{{Type: "text", Text: s}}
	}
	var bs []rawBlock
	_ = decodeJSONView(c, &bs)
	return bs
}

func (b *promptBuilder) anthropic(body []byte) bool {
	var req struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	if decodeJSONView(body, &req) != nil {
		return false
	}
	for _, s := range blocksOf(req.System) {
		if strings.HasPrefix(s.Text, "x-anthropic-billing-header") {
			b.add(PartSystem, "billing", "", tokensOf(s.Text), txt("system", s.Text))
			continue
		}
		b.systemText(s.Text, "prompt")
	}
	for _, t := range req.Tools {
		var d struct {
			Name string `json:"name"`
		}
		_ = decodeJSONView(t, &d)
		b.tool(d.Name, rawTokens(t), t)
	}
	for _, m := range req.Messages {
		bs := blocksOf(m.Content)
		switch m.Role {
		case "system", "developer":
			for _, x := range bs {
				b.systemText(x.Text, "harness")
			}
			continue
		case "user":
			// a message that answers tool calls goes on with the turn;
			// one where the user says something begins one
			results := slices.ContainsFunc(bs, func(x rawBlock) bool { return x.Type == "tool_result" })
			if !results && slices.ContainsFunc(bs, func(x rawBlock) bool { return x.Type == "text" && userSays(x.Text) }) {
				b.newTurn()
			}
		}
		for _, x := range bs {
			b.anthropicBlock(m.Role, x)
		}
	}
	return true
}

// userSays tells what the user said from what the agent put in for them.
func userSays(text string) bool { return userWords(text) != "" }

var (
	// reminders are what agents add to a user's message for the model
	// alone: Claude Code's <system-reminder>, ZCode's with a source=.
	reminders = lazyre.New(`(?s)<system-reminder(?:\s[^>]*)?>.*?</system-reminder>`)
	// myRequest heads what the user typed in the Codex app, after the
	// context it puts first (# Context from my IDE setup:, <in-app-browser-context>…).
	myRequest = lazyre.New(`(?m)^## My request(?: for Codex)?:[ \t]*\n?`)
	// injected are how a text the agent put in as the user begins, beside
	// agentsFor and the reminders: Codex's and the Codex app's context,
	// Reasonix's snapshot of the workspace, Claude Code's and ZCode's
	// notices, plugin metadata, agent lists and todo nudges.
	injected = []string{
		"<system-reminder", "<environment_context>", "<user_instructions>", "<turn_aborted>",
		"<user_shell_command>", "<subagent_notification>", "<in-app-browser-context",
		"<external_codex_apps_open_page>", "<session-context",
		"<task-notification>", "<subagent-notification>", "<plugin_reference>",
		"Available agent types for the Agent tool:", "Available agent types and the tools they have access to:",
		"New agent types are now available for the Agent tool:",
		"The TodoWrite tool hasn't been used recently.", "The task tools haven't been used recently.",
		"Conversation rewind applied.", "Workspace rewind applied.",
		// the tool list changed: Claude Code's deferred tools and MCP
		// servers' instructions, told after a result
		"The following deferred tools are", "# MCP Server Instructions", "The following MCP servers",
	}
)

// userWords is what the user said in a text of theirs: "" for a text the
// agent put in (an instruction file, a reminder, a notice), else the text
// without the reminders in it, and after the Codex app's context. Routing
// (turnIn, userText, firstWords) and the prompt's turns go by it alike.
func userWords(text string) string {
	// each pattern is looked for only where its words are: run over every
	// user text of a long conversation on each request, the line-anchored
	// myRequest alone took a seventh of a Codex request's CPU (#3394)
	t := text
	if strings.Contains(t, "<system-reminder") {
		t = reminders.ReplaceAllString(t, "")
	}
	t = strings.TrimSpace(t)
	if strings.Contains(t, "## My request") {
		if at := myRequest.FindAllStringIndex(t, -1); len(at) > 0 {
			t = strings.TrimSpace(t[at[len(at)-1][1]:])
		}
	}
	if t == "" || agentsFor.MatchString(t) {
		return ""
	}
	for _, lead := range injected {
		if strings.HasPrefix(t, lead) {
			return ""
		}
	}
	return t
}

// saysAnything is whether userSaid has anything, without joining the
// words of a long turn only to see they are there
func saysAnything(m Message) bool {
	return slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Kind == Text && userSays(p.Text) })
}

// userSaid is what the user said in a message: the words of its texts.
func userSaid(m Message) string {
	var said []string
	for _, p := range m.Parts {
		if p.Kind == Text {
			if w := userWords(p.Text); w != "" {
				said = append(said, w)
			}
		}
	}
	return strings.Join(said, "\n")
}

func (b *promptBuilder) anthropicBlock(role string, x rawBlock) {
	switch x.Type {
	case "text":
		if role == "user" {
			b.userText(x.Text)
		} else {
			b.chat(tokensOf(x.Text), txt(role, x.Text))
		}
	case "thinking":
		b.chat(tokensOf(x.Thinking), txt("thinking", x.Thinking))
	case "redacted_thinking":
		b.chat(tokensOf(x.Data)/4, sealedPiece(len(x.Data)))
	case "tool_use", "server_tool_use":
		b.calls[x.ID] = callOf(x.Name, x.Input)
		b.chat(tokensOf(x.Name)+rawTokens(x.Input), rawPiece("call", x.Name, x.Input))
	case "tool_result", "web_search_tool_result", "web_fetch_tool_result":
		n := 0
		var texts []string
		for _, c := range blocksOf(x.Content) {
			switch c.Type {
			case "image", "document":
				b.add(PartFiles, c.Type, "image", imageTokens, imgPiece(c.Source))
			default:
				n += tokensOf(c.Text)
				texts = append(texts, c.Text)
			}
		}
		pc := txt("result", strings.Join(texts, "\n"))
		if len(x.Content) > 0 && x.Content[0] != '"' && x.Content[0] != '[' {
			n = rawTokens(x.Content)
			pc = rawPiece("result", "", x.Content)
		}
		b.result(x.ToolUseID, n, pc)
	case "image", "document":
		if x.Type == "document" && len(x.Source) > 0 && !strings.Contains(string(x.Source), `"base64"`) {
			b.add(PartFiles, "document", "image", rawTokens(x.Source), rawPiece("", "", x.Source))
			return
		}
		b.add(PartFiles, x.Type, "image", imageTokens, imgPiece(x.Source))
	default:
		b.chat(tokensOf(x.Text), txt(role, x.Text))
	}
}

// --- OpenAI Responses ---

func (b *promptBuilder) responses(body []byte) bool {
	var req struct {
		Instructions json.RawMessage   `json:"instructions"`
		Input        json.RawMessage   `json:"input"`
		Tools        []json.RawMessage `json:"tools"`
		Previous     string            `json:"previous_response_id"`
	}
	if decodeJSONView(body, &req) != nil {
		return false
	}
	b.held = req.Previous != ""
	var ins string
	if decodeJSONView(req.Instructions, &ins) == nil && ins != "" {
		b.systemText(ins, "prompt")
	}
	for _, t := range req.Tools {
		b.responsesTool(t)
	}
	var s string
	if decodeJSONView(req.Input, &s) == nil {
		b.newTurn()
		b.userText(s)
		return true
	}
	items, _ := inputList(req.Input)
	for _, it := range items {
		b.responsesItem(it)
	}
	return true
}

func (b *promptBuilder) responsesTool(t json.RawMessage) {
	var d struct {
		Type     string            `json:"type"`
		Name     string            `json:"name"`
		Tools    []json.RawMessage `json:"tools"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	_ = decodeJSONView(t, &d)
	name := d.Name
	if name == "" && d.Function != nil {
		name = d.Function.Name
	}
	if name == "" {
		name = d.Type // web_search, image_generation…
	}
	if d.Type == "namespace" || len(d.Tools) > 0 {
		// a namespace of tools: Codex's own functions one by one, any other
		// as one, as an MCP server's
		if name == "functions" {
			for _, x := range d.Tools {
				b.responsesTool(x)
			}
			return
		}
		// an MCP server's namespace is named as its tools are, mcp__<server>…;
		// Codex's own (clock, collaboration…) aren't MCP
		tag := "namespace"
		if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
			tag, name = "mcp", strings.TrimRight(strings.SplitN(rest, "__", 2)[0], "_")
		}
		b.add(PartTools, name, tag, rawTokens(t), rawPiece("tool", name, t))
		if p := b.parts[PartTools][tag+"\x00"+name]; p != nil {
			p.N += len(d.Tools) - 1
		}
		return
	}
	b.tool(name, rawTokens(t), t)
}

func (b *promptBuilder) responsesItem(raw json.RawMessage) {
	var it struct {
		Type      string            `json:"type"`
		Role      string            `json:"role"`
		Content   json.RawMessage   `json:"content"`
		CallID    string            `json:"call_id"`
		Name      string            `json:"name"`
		Arguments string            `json:"arguments"`
		Input     string            `json:"input"`
		Output    json.RawMessage   `json:"output"`
		Tools     []json.RawMessage `json:"tools"`
		Summary   []struct {
			Text string `json:"text"`
		} `json:"summary"`
		Encrypted string          `json:"encrypted_content"`
		Action    json.RawMessage `json:"action"`
	}
	if decodeJSONView(raw, &it) != nil {
		return
	}
	if it.Type == "" && it.Role != "" {
		it.Type = "message"
	}
	switch it.Type {
	case "additional_tools":
		for _, t := range it.Tools {
			b.responsesTool(t)
		}
	case "message":
		parts := responsesContent(it.Content)
		switch it.Role {
		case "system", "developer":
			for _, p := range parts {
				b.systemText(p.text, "prompt")
			}
		case "user":
			if slices.ContainsFunc(parts, func(p rPart) bool { return p.image || userSays(p.text) }) {
				b.newTurn()
			}
			for _, p := range parts {
				if p.image {
					b.add(PartFiles, "image", "image", imageTokens, imgPiece(p.src))
					continue
				}
				b.userText(p.text)
			}
		default:
			for _, p := range parts {
				b.chat(tokensOf(p.text), txt("assistant", p.text))
			}
		}
	case "reasoning":
		n := 0
		var said []string
		for _, s := range it.Summary {
			n += tokensOf(s.Text)
			said = append(said, s.Text)
		}
		// the reasoning itself, sealed: about three quarters of its
		// base64 is what was sealed
		n += len(it.Encrypted) * 3 / 4 / 4
		pc := txt("thinking", strings.Join(said, "\n\n"))
		if len(it.Encrypted) > 0 {
			pc.Size = len(it.Encrypted)
		}
		b.chat(n, pc)
	case "function_call", "custom_tool_call", "local_shell_call", "tool_search_call":
		input := []byte(it.Arguments)
		if it.Type == "custom_tool_call" {
			input = []byte(it.Input)
		}
		if it.Type == "local_shell_call" {
			input = it.Action
		}
		b.calls[it.CallID] = callOf(it.Name, input)
		b.chat(tokensOf(it.Name)+(len(input)+3)/4, rawPiece("call", it.Name, input))
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output", "tool_search_output":
		n := 0
		var s string
		if decodeJSONView(it.Output, &s) == nil {
			n = tokensOf(s)
		} else {
			var texts []string
			for _, p := range responsesContent(it.Output) {
				if p.image {
					b.add(PartFiles, "image", "image", imageTokens, imgPiece(p.src))
					continue
				}
				n += tokensOf(p.text)
				texts = append(texts, p.text)
			}
			s = strings.Join(texts, "\n")
		}
		if it.Type == "tool_search_output" {
			for _, t := range it.Tools {
				b.responsesTool(t)
			}
		}
		b.result(it.CallID, n, txt("result", s))
	case "compaction", "compaction_summary":
		b.add(PartChat, "compacted", "compacted", rawTokens(raw), sealedPiece(len(raw)))
	default:
		b.chat(rawTokens(raw), rawPiece("", it.Type, raw))
	}
}

type rPart struct {
	text  string
	image bool
	src   json.RawMessage // an image's image_url
}

func responsesContent(c json.RawMessage) []rPart {
	var s string
	if decodeJSONView(c, &s) == nil {
		return []rPart{{text: s}}
	}
	var ps []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	_ = decodeJSONView(c, &ps)
	out := make([]rPart, 0, len(ps))
	for _, p := range ps {
		out = append(out, rPart{text: p.Text, image: p.Type == "input_image" || p.Type == "input_file" || p.Type == "image_url", src: p.ImageURL})
	}
	return out
}

// --- OpenAI Chat Completions ---

func (b *promptBuilder) chatCompletions(body []byte) bool {
	var req struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			Reasoning  string          `json:"reasoning_content"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	if decodeJSONView(body, &req) != nil {
		return false
	}
	for _, t := range req.Tools {
		b.responsesTool(t)
	}
	for _, m := range req.Messages {
		parts := responsesContent(m.Content)
		switch m.Role {
		case "system", "developer":
			for _, p := range parts {
				b.systemText(p.text, "prompt")
			}
		case "user":
			if slices.ContainsFunc(parts, func(p rPart) bool { return p.image || userSays(p.text) }) {
				b.newTurn()
			}
			for _, p := range parts {
				if p.image {
					b.add(PartFiles, "image", "image", imageTokens, imgPiece(p.src))
					continue
				}
				b.userText(p.text)
			}
		case "tool", "function":
			n := 0
			var texts []string
			for _, p := range parts {
				if p.image {
					b.add(PartFiles, "image", "image", imageTokens, imgPiece(p.src))
					continue
				}
				n += tokensOf(p.text)
				texts = append(texts, p.text)
			}
			b.result(m.ToolCallID, n, txt("result", strings.Join(texts, "\n")))
		default:
			// its reasoning, what it said and its calls, a piece each
			b.chat(tokensOf(m.Reasoning), txt("thinking", m.Reasoning))
			n := 0
			var texts []string
			for _, p := range parts {
				n += tokensOf(p.text)
				texts = append(texts, p.text)
			}
			b.chat(n, txt("assistant", strings.Join(texts, "\n")))
			for _, c := range m.ToolCalls {
				b.calls[c.ID] = callOf(c.Function.Name, []byte(c.Function.Arguments))
				b.chat(tokensOf(c.Function.Name)+tokensOf(c.Function.Arguments), rawPiece("call", c.Function.Name, []byte(c.Function.Arguments)))
			}
		}
	}
	return true
}

// --- Gemini ---

func (b *promptBuilder) gemini(body []byte) bool {
	type gPart struct {
		Text         string          `json:"text"`
		Thought      bool            `json:"thought"`
		InlineData   json.RawMessage `json:"inlineData"`
		FileData     json.RawMessage `json:"fileData"`
		FunctionCall *struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"functionCall"`
		FunctionResponse *struct {
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Response json.RawMessage `json:"response"`
		} `json:"functionResponse"`
	}
	var req struct {
		System *struct {
			Parts []gPart `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Role  string  `json:"role"`
			Parts []gPart `json:"parts"`
		} `json:"contents"`
		Tools []struct {
			Decls []json.RawMessage `json:"functionDeclarations"`
		} `json:"tools"`
	}
	if decodeJSONView(body, &req) != nil {
		return false
	}
	if req.System != nil {
		for _, p := range req.System.Parts {
			b.systemText(p.Text, "prompt")
		}
	}
	for _, t := range req.Tools {
		for _, d := range t.Decls {
			var n struct {
				Name string `json:"name"`
			}
			_ = decodeJSONView(d, &n)
			b.tool(n.Name, rawTokens(d), d)
		}
	}
	for _, c := range req.Contents {
		if c.Role != "model" && slices.ContainsFunc(c.Parts, func(p gPart) bool { return userSays(p.Text) && p.FunctionResponse == nil }) {
			b.newTurn()
		}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				// Gemini matches a response to its call by name, or id
				id := p.FunctionCall.ID
				if id == "" {
					id = "name:" + p.FunctionCall.Name
				}
				b.calls[id] = callOf(p.FunctionCall.Name, p.FunctionCall.Args)
				b.chat(tokensOf(p.FunctionCall.Name)+rawTokens(p.FunctionCall.Args), rawPiece("call", p.FunctionCall.Name, p.FunctionCall.Args))
			case p.FunctionResponse != nil:
				id := p.FunctionResponse.ID
				if id == "" {
					id = "name:" + p.FunctionResponse.Name
				}
				if _, ok := b.calls[id]; !ok {
					b.calls[id] = toolCall{name: p.FunctionResponse.Name}
				}
				b.result(id, rawTokens(p.FunctionResponse.Response), rawPiece("result", "", p.FunctionResponse.Response))
			case len(p.InlineData) > 0 || len(p.FileData) > 0:
				b.add(PartFiles, "image", "image", imageTokens, imgPiece(p.InlineData))
			case c.Role == "model":
				role := "assistant"
				if p.Thought {
					role = "thinking"
				}
				b.chat(tokensOf(p.Text), txt(role, p.Text))
			default:
				b.userText(p.Text)
			}
		}
	}
	return true
}

// calibrated is the prompt scaled to what the vendor counted, as a copy:
// the trace hands out the one it holds to readers outside its lock.
func (p *Prompt) calibrated(counted, window int) *Prompt {
	if p == nil {
		return nil
	}
	c := *p
	c.Parts = make([]PromptPart, len(p.Parts))
	for i, part := range p.Parts {
		part.Items = append([]PromptItem(nil), part.Items...)
		c.Parts[i] = part
	}
	c.calibrate(counted, window)
	return &c
}

// promptCounted is the prompt the vendor counted for the try that
// answered: the last billable one's input, read from its cache or not.
func promptCounted(us []RouteUsage) int {
	if len(us) == 0 {
		return 0
	}
	u := us[len(us)-1]
	return u.Input + u.CacheRead + u.CacheWrite
}

// windowFor is the context window of a provider's model; 0 when unknown.
func windowFor(id, model string) int {
	if id == "" || model == "" {
		return 0
	}
	p, err := provider.Find(id)
	if err != nil || p == nil {
		return 0
	}
	return windowOf(*p, model)
}

// livePrompt is p as a route still answering shows it: in the window of
// the model its try went to, read as the try went out (packinggoods on
// Discord: a request in flight on a remote magpie's claude-opus-5-5 said
// the model's window isn't known, its window set only once it was done).
// p itself is left as it is, for the route's end to scale and give the
// window of the model that answered.
func livePrompt(p *Prompt, window int) *Prompt {
	if p == nil || window <= 0 || p.Window == window {
		return p
	}
	c := *p
	c.Window = window
	return &c
}

// inspectPrompt reads the request's prompt beside it, putting the estimate
// in the trace as soon as it is read; wait returns it, for the route's
// end to scale to what was counted. masked is the body as the vendor would
// get it with masking on, kept a while for the card's text (PromptText);
// plain when it went unmasked, so is masked only when the card asks.
func (s *Server) inspectPrompt(tr *Route, from provider.Protocol, body, masked []byte, plain bool) (wait func() *Prompt) {
	ch := make(chan *Prompt, 1)
	go func() {
		p := promptOf(from, body)
		if p != nil {
			s.trace.keepBody(tr.ID, from, masked, plain)
			s.trace.update(tr, func(t *Route) {
				if t.Prompt == nil {
					t.Prompt = livePrompt(p, t.window)
				}
			})
		}
		ch <- p
	}()
	var p *Prompt
	got := false
	return func() *Prompt {
		if !got {
			p, got = <-ch, true
		}
		return p
	}
}

// convOf is the conversation of a request that names no session: a digest
// of its first user turn, the same from one request of it to the next.
func convOf(h http.Header, body []byte) string {
	if sessionOf(h) != "" {
		return ""
	}
	return conversationID(h, body)
}

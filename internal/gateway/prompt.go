package gateway

// What a request's prompt is made of: the agent's system prompt, its tool
// definitions, the instruction files it read in (CLAUDE.md, AGENTS.md,
// skills), the files its tools read, its tools' other output, and the
// conversation, each as the tokens it takes of the model's context window.
// Read from the body the agent sent, as it sent it: the parsed Request has
// the system blocks run together, and Codex's additional_tools lifted out.
//
// The sizes are estimated as the body is read, then scaled to the prompt
// the vendor counted once it answers (calibrate). Only names are kept — the
// instruction files' and read files' paths, the tools' names, "turn 3" —
// never the prompt's text.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/provider"
)

// The kinds of a prompt's parts, in the order they are shown.
const (
	PartSystem  = "system"  // the agent's own prompt: system, developer and harness messages
	PartTools   = "tools"   // tool definitions
	PartMemory  = "memory"  // instruction files, skills, reminders
	PartFiles   = "files"   // files and images the tools read in
	PartResults = "results" // the tools' other output
	PartChat    = "chat"    // what the user and the model said, the model's calls
)

var partOrder = []string{PartSystem, PartTools, PartMemory, PartFiles, PartResults, PartChat}

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
}

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

type promptBuilder struct {
	parts map[string]map[string]*PromptItem
	turn  int
	held  bool
	calls map[string]toolCall // call id → what was called, for its result
}

type toolCall struct {
	name  string
	files []string // the files it reads, when it reads files
}

func newPromptBuilder() *promptBuilder {
	return &promptBuilder{parts: map[string]map[string]*PromptItem{}, calls: map[string]toolCall{}}
}

func (b *promptBuilder) add(kind, name, tag string, tokens int) {
	if tokens <= 0 {
		return
	}
	m := b.parts[kind]
	if m == nil {
		m = map[string]*PromptItem{}
		b.parts[kind] = m
	}
	key := tag + "\x00" + name
	it := m[key]
	if it == nil {
		it = &PromptItem{Name: name, Tag: tag}
		m[key] = it
	}
	it.N++
	it.Tokens += tokens
}

// chat adds to the conversation's current turn.
func (b *promptBuilder) chat(tokens int) {
	if b.turn == 0 {
		b.turn = 1
	}
	b.add(PartChat, strconv.Itoa(b.turn), "turn", tokens)
}

func (b *promptBuilder) newTurn() { b.turn++ }

func (b *promptBuilder) prompt() *Prompt {
	p := &Prompt{Held: b.held, Turns: b.turn, Parts: []PromptPart{}}
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
	contentsOf = regexp.MustCompile(`(?m)^Contents of (\S.*?) \(([^()\n]*(?:\([^()\n]*\)[^()\n]*)*)\):[ \t]*$`)
	// OpenCode: "Instructions from: /path/AGENTS.md"
	instructionsFrom = regexp.MustCompile(`(?m)^Instructions from: (\S.*?)[ \t]*$`)
	// Codex: "# AGENTS.md instructions for /path"
	agentsFor = regexp.MustCompile(`^# (\S+\.md) instructions for (\S.*?)[ \t]*\n`)
	leadTag   = regexp.MustCompile(`^\s*<([a-zA-Z_][\w -]{0,40})>`)
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
		b.add(PartMemory, s.path, memoryTag(s.path, s.desc), tokensOf(chunk))
	}
	return rest.String()
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
	rest := b.splitMemory(text)
	if strings.HasPrefix(strings.TrimSpace(rest), "<skills_instructions>") {
		b.add(PartMemory, "skills", "skills", tokensOf(rest))
		return
	}
	b.add(PartSystem, harnessName(rest, name), "", tokensOf(rest))
}

// userText adds a user's text: an instruction file or reminder the agent
// put in as the user, or what the user said. It says which.
func (b *promptBuilder) userText(text string) (said bool) {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "<system-reminder>"):
		rest := b.splitMemory(t)
		rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(rest), "<system-reminder>"), "</system-reminder>"))
		if rest != "" {
			b.add(PartMemory, "reminders", "reminder", tokensOf(rest))
		}
		return false
	case agentsFor.MatchString(t):
		m := agentsFor.FindStringSubmatch(t)
		path := strings.TrimRight(m[2], "/") + "/" + m[1]
		b.add(PartMemory, path, "project", tokensOf(t))
		return false
	case strings.HasPrefix(t, "<environment_context>") || strings.HasPrefix(t, "<user_instructions>") ||
		strings.HasPrefix(t, "<turn_aborted>") || strings.HasPrefix(t, "<user_shell_command>"):
		b.add(PartSystem, harnessName(t, "context"), "", tokensOf(t))
		return false
	}
	b.chat(tokensOf(text))
	return true
}

// --- tool calls and what they read ---

var (
	// the tools that read a file, and where they take its path
	readTools = map[string]bool{"Read": true, "read": true, "read_file": true, "ReadFile": true, "view": true,
		"View": true, "NotebookRead": true, "readFile": true, "open_file": true, "cat": true}
	pathKeys  = []string{"file_path", "path", "filePath", "target_file", "notebook_path", "file", "filename", "uri"}
	cmdOfExec = regexp.MustCompile(`\bcmd\s*:\s*"((?:[^"\\]|\\.)*)"`)
	toolOfJS  = regexp.MustCompile(`\btools\.(\w+)\s*\(`)
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
	_ = json.Unmarshal(input, &args)
	str := func(k string) string {
		var s string
		if json.Unmarshal(args[k], &s) == nil {
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
		if json.Unmarshal(args["command"], &argv) == nil {
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
func (b *promptBuilder) result(callID string, tokens int) {
	c, ok := b.calls[callID]
	if !ok {
		c.name = "tool"
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
			b.add(PartFiles, f, tag, n)
		}
		return
	}
	b.add(PartResults, c.name, "result", tokens)
}

// tool adds a tool definition: an MCP server's tools as one.
func (b *promptBuilder) tool(name string, tokens int) {
	if strings.HasPrefix(name, "mcp__") {
		rest := strings.TrimPrefix(name, "mcp__")
		server := rest
		if i := strings.Index(rest, "__"); i > 0 {
			server = rest[:i]
		}
		b.add(PartTools, server, "mcp", tokens)
		return
	}
	b.add(PartTools, name, "", tokens)
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
	if json.Unmarshal(c, &s) == nil {
		return []rawBlock{{Type: "text", Text: s}}
	}
	var bs []rawBlock
	_ = json.Unmarshal(c, &bs)
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
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	for _, s := range blocksOf(req.System) {
		if strings.HasPrefix(s.Text, "x-anthropic-billing-header") {
			b.add(PartSystem, "billing", "", tokensOf(s.Text))
			continue
		}
		b.systemText(s.Text, "prompt")
	}
	for _, t := range req.Tools {
		var d struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(t, &d)
		b.tool(d.Name, rawTokens(t))
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
func userSays(text string) bool {
	t := strings.TrimSpace(text)
	return t != "" && !strings.HasPrefix(t, "<system-reminder>") && !agentsFor.MatchString(t) &&
		!strings.HasPrefix(t, "<environment_context>") && !strings.HasPrefix(t, "<user_instructions>")
}

func (b *promptBuilder) anthropicBlock(role string, x rawBlock) {
	switch x.Type {
	case "text":
		if role == "user" {
			b.userText(x.Text)
		} else {
			b.chat(tokensOf(x.Text))
		}
	case "thinking":
		b.chat(tokensOf(x.Thinking))
	case "redacted_thinking":
		b.chat(tokensOf(x.Data) / 4)
	case "tool_use", "server_tool_use":
		b.calls[x.ID] = callOf(x.Name, x.Input)
		b.chat(tokensOf(x.Name) + rawTokens(x.Input))
	case "tool_result", "web_search_tool_result", "web_fetch_tool_result":
		n := 0
		for _, c := range blocksOf(x.Content) {
			switch c.Type {
			case "image", "document":
				b.add(PartFiles, c.Type, "image", imageTokens)
			default:
				n += tokensOf(c.Text)
			}
		}
		if len(x.Content) > 0 && x.Content[0] != '"' && x.Content[0] != '[' {
			n = rawTokens(x.Content)
		}
		b.result(x.ToolUseID, n)
	case "image", "document":
		if x.Type == "document" && len(x.Source) > 0 && !strings.Contains(string(x.Source), `"base64"`) {
			b.add(PartFiles, "document", "image", rawTokens(x.Source))
			return
		}
		b.add(PartFiles, x.Type, "image", imageTokens)
	default:
		b.chat(tokensOf(x.Text))
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
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	b.held = req.Previous != ""
	var ins string
	if json.Unmarshal(req.Instructions, &ins) == nil && ins != "" {
		b.systemText(ins, "prompt")
	}
	for _, t := range req.Tools {
		b.responsesTool(t)
	}
	var s string
	if json.Unmarshal(req.Input, &s) == nil {
		b.newTurn()
		b.userText(s)
		return true
	}
	var items []json.RawMessage
	_ = json.Unmarshal(req.Input, &items)
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
	_ = json.Unmarshal(t, &d)
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
		b.add(PartTools, name, tag, rawTokens(t))
		if p := b.parts[PartTools][tag+"\x00"+name]; p != nil {
			p.N += len(d.Tools) - 1
		}
		return
	}
	b.tool(name, rawTokens(t))
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
	if json.Unmarshal(raw, &it) != nil {
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
					b.add(PartFiles, "image", "image", imageTokens)
					continue
				}
				b.userText(p.text)
			}
		default:
			for _, p := range parts {
				b.chat(tokensOf(p.text))
			}
		}
	case "reasoning":
		n := 0
		for _, s := range it.Summary {
			n += tokensOf(s.Text)
		}
		// the reasoning itself, sealed: about three quarters of its
		// base64 is what was sealed
		n += len(it.Encrypted) * 3 / 4 / 4
		b.chat(n)
	case "function_call", "custom_tool_call", "local_shell_call", "tool_search_call":
		input := []byte(it.Arguments)
		if it.Type == "custom_tool_call" {
			input = []byte(it.Input)
		}
		if it.Type == "local_shell_call" {
			input = it.Action
		}
		b.calls[it.CallID] = callOf(it.Name, input)
		b.chat(tokensOf(it.Name) + (len(input)+3)/4)
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output", "tool_search_output":
		n := 0
		var s string
		if json.Unmarshal(it.Output, &s) == nil {
			n = tokensOf(s)
		} else {
			for _, p := range responsesContent(it.Output) {
				if p.image {
					b.add(PartFiles, "image", "image", imageTokens)
					continue
				}
				n += tokensOf(p.text)
			}
		}
		if it.Type == "tool_search_output" {
			for _, t := range it.Tools {
				b.responsesTool(t)
			}
		}
		b.result(it.CallID, n)
	case "compaction", "compaction_summary":
		b.add(PartChat, "compacted", "compacted", rawTokens(raw))
	default:
		b.chat(rawTokens(raw))
	}
}

type rPart struct {
	text  string
	image bool
}

func responsesContent(c json.RawMessage) []rPart {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return []rPart{{text: s}}
	}
	var ps []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(c, &ps)
	out := make([]rPart, 0, len(ps))
	for _, p := range ps {
		out = append(out, rPart{text: p.Text, image: p.Type == "input_image" || p.Type == "input_file" || p.Type == "image_url"})
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
	if json.Unmarshal(body, &req) != nil {
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
					b.add(PartFiles, "image", "image", imageTokens)
					continue
				}
				b.userText(p.text)
			}
		case "tool", "function":
			n := 0
			for _, p := range parts {
				if p.image {
					b.add(PartFiles, "image", "image", imageTokens)
					continue
				}
				n += tokensOf(p.text)
			}
			b.result(m.ToolCallID, n)
		default:
			n := tokensOf(m.Reasoning)
			for _, p := range parts {
				n += tokensOf(p.text)
			}
			for _, c := range m.ToolCalls {
				b.calls[c.ID] = callOf(c.Function.Name, []byte(c.Function.Arguments))
				n += tokensOf(c.Function.Name) + tokensOf(c.Function.Arguments)
			}
			b.chat(n)
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
	if json.Unmarshal(body, &req) != nil {
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
			_ = json.Unmarshal(d, &n)
			b.tool(n.Name, rawTokens(d))
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
				b.chat(tokensOf(p.FunctionCall.Name) + rawTokens(p.FunctionCall.Args))
			case p.FunctionResponse != nil:
				id := p.FunctionResponse.ID
				if id == "" {
					id = "name:" + p.FunctionResponse.Name
				}
				if _, ok := b.calls[id]; !ok {
					b.calls[id] = toolCall{name: p.FunctionResponse.Name}
				}
				b.result(id, rawTokens(p.FunctionResponse.Response))
			case len(p.InlineData) > 0 || len(p.FileData) > 0:
				b.add(PartFiles, "image", "image", imageTokens)
			case c.Role == "model":
				b.chat(tokensOf(p.Text))
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

// inspectPrompt reads the request's prompt beside it, putting the estimate
// in the trace as soon as it is read; wait returns it, for the route's
// end to scale to what was counted.
func (s *Server) inspectPrompt(tr *Route, from provider.Protocol, body []byte) (wait func() *Prompt) {
	ch := make(chan *Prompt, 1)
	go func() {
		p := promptOf(from, body)
		if p != nil {
			s.trace.update(tr, func(t *Route) {
				if t.Prompt == nil {
					t.Prompt = p
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

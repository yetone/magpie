package gateway

// A Cursor subscription runs through the genuine cursor-agent CLI, the way a
// Claude one runs through Claude Code (claude_subscription.go): the caller's
// tools are an MCP server whose calls park until the caller sends results.
//
// cursor-agent's stream says a tool call started but not with the id the
// helper hands magpie, so the tool_use goes out when the helper calls back:
// once every started call has come in, the answer so far ends with them.
//
// It runs in a home of magpie's own. The user's ~/.cursor has their hooks,
// rules and MCP servers, which are theirs and not the caller's — and hooks
// that answer slowly leave cursor-agent reconnecting forever instead of
// ending the turn. Only the sign-in is shared: the keychain on a Mac, the
// auth file elsewhere.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cursorSettle is how long after a tool call magpie waits for more of a
// batch before handing them to the caller. cursor-agent makes a server's
// calls one after another, so a batch is mostly one: the next comes once
// the caller has answered the first.
var cursorSettle = 300 * time.Millisecond

// cursorPatience stays under the minute cursor-agent's MCP client waits.
var cursorPatience = 45 * time.Second

func (b *subscriptionBridge) startCursor(ctx context.Context, req *Request, model string) (*subscriptionRun, <-chan Event, error) {
	binary := provider.CursorExecutable()
	if binary == "" {
		return nil, nil, errors.New("Cursor's CLI is not installed; install it with `curl https://cursor.com/install -fsS | bash` and run `cursor-agent login`")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	home, err := cursorHome()
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", "magpie-cursor-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	tools := bridgeTools(req)
	if len(tools) > 0 {
		tools = append(tools, bridgeTool{Name: waitTool,
			Description: "Wait for a tool call that is still running in the user's environment, and get its result. Only for a call whose result said it is still running.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"call":{"type":"string","description":"the id the still-running result gave"}},"required":["call"]}`)})
	}
	toolsPath := filepath.Join(tmp, "tools.json")
	toolBytes, _ := json.Marshal(tools)
	if err := os.WriteFile(toolsPath, toolBytes, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	token := randomToken()
	callback := callbackBaseURL() + "/_magpie/claude-mcp/" + token
	ws := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(filepath.Join(ws, ".cursor"), 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"magpie": map[string]any{"command": exe, "args": []string{"claude-mcp-helper", callback, toolsPath}},
	}})
	if err := os.WriteFile(filepath.Join(ws, ".cursor", "mcp.json"), mcp, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}

	args := []string{"-p", "--output-format", "stream-json", "--stream-partial-output",
		"--trust", "--workspace", ws, "--model", model}
	if len(tools) > 0 {
		args = append(args, "--approve-mcps")
	}
	cmd := exec.CommandContext(context.Background(), binary, args...)
	cmd.Dir = ws
	cmd.Env = cursorEnv(os.Environ(), home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	run := &subscriptionRun{bridge: b, token: token, model: model, cmd: cmd, tmp: tmp, pending: map[string]chan mcpToolResult{},
		patience: cursorPatience, late: map[string]chan mcpToolResult{}}
	run.setSegmentInput(req)
	c := &cursorTurn{run: run}
	run.onCall = c.called
	run.resume = c.resumed
	run.begin = func() Event {
		run.mu.Lock()
		input := run.segmentInput
		run.mu.Unlock()
		return Event{Kind: KStart, MsgID: "msg_" + randomToken()[:24], Model: model,
			Usage: Usage{Input: input, State: "provisional"}}
	}
	run.timer = time.AfterFunc(30*time.Minute, run.abort)
	segment := run.attach()
	run.emit(run.begin())
	b.mu.Lock()
	b.runs[token] = run
	b.mu.Unlock()

	if err := cmd.Start(); err != nil {
		b.removeRun(run)
		return nil, nil, err
	}
	go func() {
		_, _ = io.Copy(&lockedWriter{run: run}, io.LimitReader(stderr, 1<<20))
	}()
	go c.read(stdout)
	go func() {
		_ = cmd.Wait()
		run.finish()
	}()

	prompt := renderCursorPrompt(req, len(tools) > 0)
	if _, err := io.WriteString(stdin, prompt); err != nil {
		run.abort()
		return nil, nil, err
	}
	_ = stdin.Close()
	return run, segment, nil
}

// bridgeTools are the caller's tools as the MCP helper offers them, less
// those tool_choice rules out.
func bridgeTools(req *Request) []bridgeTool {
	tools := make([]bridgeTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		if req.ToolChoice == "none" || (strings.HasPrefix(req.ToolChoice, "name:") && t.Name != strings.TrimPrefix(req.ToolChoice, "name:")) {
			continue
		}
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, bridgeTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return tools
}

// cursorHome is the home cursor-agent runs in: magpie's, kept between runs
// so its caches are, with the user's sign-in linked into it.
func cursorHome() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	home := filepath.Join(base, "magpie", "cursor-home")
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
		return "", err
	}
	real, _ := os.UserHomeDir()
	link := func(rel string) {
		src, dst := filepath.Join(real, rel), filepath.Join(home, rel)
		if _, err := os.Stat(src); err != nil {
			return
		}
		if cur, err := os.Readlink(dst); err == nil && cur == src {
			return
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		_ = os.Remove(dst)
		_ = os.Symlink(src, dst)
	}
	switch runtime.GOOS {
	case "darwin":
		link(filepath.Join("Library", "Keychains")) // where the CLI keeps its tokens
		link(filepath.Join(".cursor", "auth.json"))
	case "linux":
		if os.Getenv("XDG_CONFIG_HOME") == "" {
			link(filepath.Join(".config", "cursor", "auth.json"))
		}
	}
	// Only the caller's tools, never a shell or an edit of Cursor's own.
	cfg, _ := json.Marshal(map[string]any{"version": 1, "permissions": map[string]any{
		"allow": []string{"Mcp(magpie:*)"},
		"deny":  []string{"Shell(*)", "Write(**)"},
	}})
	if err := os.WriteFile(filepath.Join(home, ".cursor", "cli-config.json"), cfg, 0o600); err != nil {
		return "", err
	}
	return home, nil
}

func cursorEnv(env []string, home string) []string {
	blocked := map[string]bool{"HOME": true, "CURSOR_CONFIG_DIR": true, "CURSOR_API_KEY": true, "NO_OPEN_BROWSER": true}
	if runtime.GOOS == "windows" {
		blocked["USERPROFILE"] = true
	}
	out := make([]string, 0, len(env)+2)
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if !blocked[strings.ToUpper(k)] {
			out = append(out, e)
		}
	}
	out = append(out, "HOME="+home)
	if runtime.GOOS == "windows" {
		out = append(out, "USERPROFILE="+home)
	}
	return out
}

// renderCursorPrompt is the conversation as one text: cursor-agent takes a
// prompt, not messages, and no images.
func renderCursorPrompt(req *Request, tools bool) string {
	blocks, _ := renderClaudePrompt(req)
	var b strings.Builder
	if tools {
		b.WriteString("<external_system_instructions>\nThe only tools in this session are those of the magpie MCP server; Cursor's own tools (shell, reading, editing and searching files) are turned off, and this workspace is empty. Call a magpie tool whenever the conversation needs one.\n</external_system_instructions>\n\n")
	}
	for _, bl := range blocks {
		if t, ok := bl["text"].(string); ok {
			b.WriteString(t)
		}
	}
	return b.String()
}

// cursorTurn follows one cursor-agent run.
type cursorTurn struct {
	run *subscriptionRun

	mu     sync.Mutex
	handed int         // tool calls in the answer under way
	queued []Event     // tool calls made while the caller had the last ones
	settle *time.Timer //
	done   bool
}

// called puts a tool call into the answer, or, while the caller is still
// running the last ones, into the next.
func (c *cursorTurn) called(id, name string, args json.RawMessage) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	evs := []Event{{Kind: KToolStart, ID: id, Name: name}, {Kind: KToolArgs, Text: string(args)}}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.run.attached() {
		c.queued = append(c.queued, evs...)
		return
	}
	for _, ev := range evs {
		c.run.emit(ev)
	}
	c.handed++
	c.handOverLocked()
}

// resumed gives the new answer the calls made in between.
func (c *cursorTurn) resumed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queued) == 0 {
		return
	}
	for _, ev := range c.queued {
		if ev.Kind == KToolStart {
			c.handed++
		}
		c.run.emit(ev)
	}
	c.queued = nil
	c.handOverLocked()
}

// handOverLocked ends the answer at its tool calls once no more are coming.
func (c *cursorTurn) handOverLocked() {
	if c.settle != nil {
		c.settle.Stop()
	}
	c.settle = time.AfterFunc(cursorSettle, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.handed == 0 {
			return
		}
		c.handed = 0
		c.run.mu.Lock()
		input, output := c.run.segmentInput, c.run.segmentOutput
		c.run.mu.Unlock()
		c.run.emit(Event{Kind: KUsage, Usage: Usage{Input: input, Output: output, State: "provisional"}})
		c.run.emit(Event{Kind: KStop, Stop: "tool"})
		c.run.endSegment()
	})
}

func (c *cursorTurn) read(rd io.Reader) {
	s := bufio.NewScanner(rd)
	s.Buffer(make([]byte, 64<<10), 64<<20)
	for s.Scan() {
		var e struct {
			Type        string          `json:"type"`
			Subtype     string          `json:"subtype"`
			Text        string          `json:"text"`
			TimestampMS json.RawMessage `json:"timestamp_ms"`
			ModelCallID string          `json:"model_call_id"`
			IsError     bool            `json:"is_error"`
			Result      string          `json:"result"`
			Message     struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
			Usage struct {
				Input      int `json:"inputTokens"`
				Output     int `json:"outputTokens"`
				CacheRead  int `json:"cacheReadTokens"`
				CacheWrite int `json:"cacheWriteTokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(s.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "thinking":
			if e.Subtype == "delta" && e.Text != "" {
				c.run.emit(Event{Kind: KThink, Text: e.Text})
			}
		case "assistant":
			// the pieces as they come carry a timestamp; the whole message
			// again at the end of a step, with a model call id or nothing
			if len(e.TimestampMS) == 0 || e.ModelCallID != "" {
				continue
			}
			for _, p := range e.Message.Content {
				if p.Type == "text" && p.Text != "" {
					c.run.emit(Event{Kind: KText, Text: p.Text})
				}
			}
		case "result":
			c.mu.Lock()
			c.done = true
			c.mu.Unlock()
			if e.IsError {
				c.run.emit(Event{Kind: KError, Text: e.Result})
			} else {
				c.run.emit(Event{Kind: KUsage, Usage: Usage{Input: e.Usage.Input, Output: e.Usage.Output, CacheRead: e.Usage.CacheRead, CacheWrite: e.Usage.CacheWrite, State: "final"}})
				c.run.emit(Event{Kind: KStop, Stop: "stop"})
			}
			c.run.endSegment()
			// the CLI lingers a minute after it has answered
			time.AfterFunc(2*time.Second, c.run.abort)
		}
	}
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if !done {
		c.run.emit(Event{Kind: KError, Text: c.run.lastError("cursor-agent ended without an answer")})
	}
}

// lastError is the last thing the agent said on stderr, or else def.
func (r *subscriptionRun) lastError(def string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(r.stderr.String()), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		return l
	}
	return def
}

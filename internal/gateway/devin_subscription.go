package gateway

// A Devin subscription runs through the genuine devin CLI in ACP mode, the
// way a Cursor one runs through cursor-agent (cursor_subscription.go): the
// caller's tools are an MCP server named in session/new, whose calls park in
// the stdio helper until the caller sends results.
//
// devin acp speaks JSON-RPC over stdio: initialize, session/new (which takes
// the magpie MCP server), session/set_mode, then session/prompt. The answer
// streams back as session/update notifications; the prompt call itself
// returns the stop reason and usage.
//
// It runs in a home of magpie's own. The user's ~/.config/devin has their
// rules, skills and MCP servers, which are theirs and not the caller's; the
// home's own config denies every built-in tool and allows only the magpie
// server's. Only the sign-in is shared: credentials.toml is linked into it.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// devinPatience stays under the minute devin's MCP client waits on a call.
var devinPatience = 45 * time.Second

// devinHandshake bounds initialize/session/new: a devin that does not answer
// is a dead devin.
var devinHandshake = 60 * time.Second

func (b *subscriptionBridge) startDevin(ctx context.Context, req *Request, model string) (*subscriptionRun, <-chan Event, error) {
	binary := provider.DevinExecutable()
	if binary == "" {
		return nil, nil, errors.New("Devin's CLI is not installed; install it (https://docs.devin.ai/cli) and run `devin auth login`")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	home, err := devinHome()
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", "magpie-devin-")
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
	if err := os.MkdirAll(ws, 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}

	cmd := exec.CommandContext(context.Background(), binary, "acp", "--model", model)
	cmd.Dir = ws
	cmd.Env = devinEnv(os.Environ(), home)
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
		patience: devinPatience, late: map[string]chan mcpToolResult{}}
	c := &cursorTurn{run: run}
	run.onCall = c.called
	run.resume = c.resumed
	run.begin = func() Event { return Event{Kind: KStart, MsgID: "msg_" + randomToken()[:24], Model: model} }
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
	conn := &devinConn{stdin: stdin, pending: map[int64]chan devinReply{}, run: run}
	go conn.read(stdout)
	go func() {
		_ = cmd.Wait()
		run.finish()
	}()

	shake, cancel := context.WithTimeout(ctx, devinHandshake)
	defer cancel()
	fail := func(err error) (*subscriptionRun, <-chan Event, error) {
		run.abort()
		return nil, nil, err
	}
	if _, err := conn.call(shake, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]any{"name": "magpie", "version": "0"},
		"clientCapabilities": map[string]any{},
	}); err != nil {
		return fail(fmt.Errorf("devin acp initialize: %w", err))
	}
	mcpServers := []any{}
	if len(tools) > 0 {
		mcpServers = append(mcpServers, map[string]any{
			"name":    "magpie",
			"command": exe,
			"args":    []string{"claude-mcp-helper", callback, toolsPath},
			"env":     []any{},
		})
	}
	res, err := conn.call(shake, "session/new", map[string]any{"cwd": ws, "mcpServers": mcpServers})
	if err != nil {
		return fail(fmt.Errorf("devin acp session/new: %w", err))
	}
	var sess struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(res, &sess) != nil || sess.SessionID == "" {
		return fail(errors.New("devin acp session/new: no session id"))
	}
	// bypass approves the magpie tools without a prompt; the home's deny
	// rules still hold, so nothing of devin's own runs. A session that
	// knows no bypass keeps its default mode and the allow rule instead.
	_, _ = conn.call(shake, "session/set_mode", map[string]any{"sessionId": sess.SessionID, "modeId": "bypass"})

	prompt, err := renderDevinPrompt(req, len(tools) > 0)
	if err != nil {
		return fail(err)
	}
	id, err := conn.send("session/prompt", map[string]any{"sessionId": sess.SessionID, "prompt": prompt})
	if err != nil {
		return fail(err)
	}
	go func() {
		res, err := conn.await(context.Background(), id)
		if err != nil {
			run.emit(Event{Kind: KError, Text: "devin: " + err.Error()})
		} else {
			var done struct {
				StopReason string `json:"stopReason"`
				Usage      struct {
					Input     int `json:"inputTokens"`
					Output    int `json:"outputTokens"`
					CacheRead int `json:"cachedReadTokens"`
				} `json:"usage"`
			}
			_ = json.Unmarshal(res, &done)
			run.emit(Event{Kind: KUsage, Usage: Usage{Input: done.Usage.Input, Output: done.Usage.Output, CacheRead: done.Usage.CacheRead}})
			run.emit(Event{Kind: KStop, Stop: stopFromACP(done.StopReason)})
		}
		run.endSegment()
		time.AfterFunc(2*time.Second, run.abort)
	}()
	return run, segment, nil
}

func stopFromACP(s string) string {
	switch s {
	case "max_tokens", "max_turn_requests":
		return "length"
	case "refusal":
		return "filter"
	}
	return "stop"
}

// devinConn is the JSON-RPC side of a devin acp process: calls out over
// stdin, and from stdout — responses, session/update notifications, and the
// odd request devin asks of its client (permission prompts, which are
// declined, and anything else, which is unsupported).
type devinConn struct {
	stdin   io.Writer
	run     *subscriptionRun
	wmu     sync.Mutex
	seq     atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan devinReply
}

type devinReply struct {
	result json.RawMessage
	err    error
}

type devinRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *devinRPCError) Error() string { return e.Message }

func (c *devinConn) send(method string, params any) (int64, error) {
	id := c.seq.Add(1)
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return 0, err
	}
	ch := make(chan devinReply, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	c.wmu.Lock()
	_, err = c.stdin.Write(append(b, '\n'))
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return 0, err
	}
	return id, nil
}

func (c *devinConn) await(ctx context.Context, id int64) (json.RawMessage, error) {
	c.mu.Lock()
	ch := c.pending[id]
	c.mu.Unlock()
	if ch == nil {
		return nil, errors.New("no such call")
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *devinConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, err := c.send(method, params)
	if err != nil {
		return nil, err
	}
	return c.await(ctx, id)
}

func (c *devinConn) answer(id json.RawMessage, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	c.wmu.Lock()
	_, _ = c.stdin.Write(append(b, '\n'))
	c.wmu.Unlock()
}

func (c *devinConn) refuse(id json.RawMessage, code int, msg string) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id),
		"error": map[string]any{"code": code, "message": msg}})
	c.wmu.Lock()
	_, _ = c.stdin.Write(append(b, '\n'))
	c.wmu.Unlock()
}

func (c *devinConn) read(rd io.Reader) {
	s := bufio.NewScanner(rd)
	s.Buffer(make([]byte, 64<<10), 64<<20)
	for s.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *devinRPCError  `json:"error"`
		}
		if json.Unmarshal(s.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			// a request devin asks of its client: permission prompts are
			// declined, the rest (fs, terminal, …) unimplemented
			if msg.Method == "session/request_permission" {
				c.answer(msg.ID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
			} else {
				c.refuse(msg.ID, -32601, "magpie does not implement "+msg.Method)
			}
		case msg.Method == "session/update":
			var p struct {
				Update struct {
					Kind    string `json:"sessionUpdate"`
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
					Usage struct {
						Input     int `json:"inputTokens"`
						Output    int `json:"outputTokens"`
						CacheRead int `json:"cachedReadTokens"`
					} `json:"usage"`
				} `json:"update"`
			}
			if json.Unmarshal(msg.Params, &p) != nil {
				continue
			}
			switch p.Update.Kind {
			case "agent_message_chunk":
				if p.Update.Content.Text != "" {
					c.run.emit(Event{Kind: KText, Text: p.Update.Content.Text})
				}
			case "agent_thought_chunk":
				if p.Update.Content.Text != "" {
					c.run.emit(Event{Kind: KThink, Text: p.Update.Content.Text})
				}
			case "usage":
				u := p.Update.Usage
				if u.Input+u.Output+u.CacheRead > 0 {
					c.run.emit(Event{Kind: KUsage, Usage: Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead}})
				}
			}
		case len(msg.ID) > 0:
			var rid int64
			if json.Unmarshal(msg.ID, &rid) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[rid]
			delete(c.pending, rid)
			c.mu.Unlock()
			if ch != nil {
				if msg.Error != nil {
					ch <- devinReply{err: msg.Error}
				} else {
					ch <- devinReply{result: msg.Result}
				}
			}
		}
	}
	// the process ended or its stream broke: free whatever waits
	c.mu.Lock()
	pending := c.pending
	c.pending = map[int64]chan devinReply{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- devinReply{err: errors.New("devin ended without an answer")}
	}
}

// devinHome is the home devin runs in: magpie's, kept between runs so its
// caches are, with the user's sign-in linked into it and a config that
// leaves only the magpie server's tools.
func devinHome() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	home := filepath.Join(base, "magpie", "devin-home")
	var cfgDir, link, src string
	if runtime.GOOS == "windows" {
		app := filepath.Join(home, "AppData", "Roaming")
		cfgDir = filepath.Join(app, "devin")
		link = filepath.Join(app, "devin", "credentials.toml")
		src = provider.DevinCredentialsPath()
	} else {
		cfgDir = filepath.Join(home, ".config", "devin")
		link = filepath.Join(home, ".local", "share", "devin", "credentials.toml")
		src = provider.DevinCredentialsPath()
	}
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(src); err == nil {
		if cur, err := os.Readlink(link); err != nil || cur != src {
			_ = os.Remove(link)
			_ = os.Symlink(src, link)
		}
	} else {
		return "", errors.New("Devin is not signed in; run `devin auth login`")
	}
	// Only the caller's tools: nothing of devin's own (files, shell, web,
	// other MCP servers or the settings it borrows from other agents). The
	// deny names are the built-in tools' own; the scoped rules cover what
	// they reach. mcp__* is not denied — it would beat the magpie allow.
	cfg, _ := json.Marshal(map[string]any{
		"version": 1,
		"permissions": map[string]any{
			"deny": []string{"read", "edit", "write", "exec", "grep", "glob", "fetch",
				"Read(**)", "Write(**)", "Fetch(*)", "Fetch(**)"},
			"allow": []string{"mcp__magpie__*"},
		},
		"read_config_from":  map[string]bool{"agents_standard": false, "claude": false, "codex": false, "copilot": false, "cursor": false, "opencode": false, "windsurf": false, "zed": false},
		"subagents_enabled": false,
		"auto_update":       false,
		"notify":            "never",
	})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), cfg, 0o600); err != nil {
		return "", err
	}
	return home, nil
}

func devinEnv(env []string, home string) []string {
	blocked := map[string]bool{"HOME": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true, "NO_OPEN_BROWSER": true}
	out := make([]string, 0, len(env)+6)
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		up := strings.ToUpper(k)
		if blocked[up] || strings.HasPrefix(up, "DEVIN_") || strings.HasPrefix(up, "WINDSURF_") ||
			strings.HasPrefix(up, "CODEIUM_") || strings.HasPrefix(up, "MAGPIE_MCP_") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, "HOME="+home)
	if runtime.GOOS == "windows" {
		out = append(out, "USERPROFILE="+home,
			"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
			"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"))
	} else {
		out = append(out,
			"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
			"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
			"XDG_CACHE_HOME="+filepath.Join(home, ".cache"))
	}
	return out
}

// renderDevinPrompt is the conversation as ACP content blocks: the caller's
// tools are named as they come over MCP.
func renderDevinPrompt(req *Request, tools bool) ([]map[string]any, error) {
	blocks, err := renderClaudePrompt(req)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(blocks)+1)
	if tools {
		out = append(out, map[string]any{"type": "text", "text": "<external_system_instructions>\nThe only tools in this session are those of the magpie MCP server; Devin's own tools (shell, reading, editing and searching files, the web) are turned off, and this workspace is empty. Call a magpie tool whenever the conversation needs one.\n</external_system_instructions>"})
	}
	for _, b := range blocks {
		switch b["type"] {
		case "image":
			if src, ok := b["source"].(map[string]any); ok {
				if src["type"] == "base64" {
					out = append(out, map[string]any{"type": "image", "data": src["data"], "mimeType": src["media_type"]})
				} else if u, ok := src["url"].(string); ok {
					out = append(out, map[string]any{"type": "resource_link", "uri": u, "name": "image"})
				}
			}
		default:
			out = append(out, b)
		}
	}
	return out, nil
}

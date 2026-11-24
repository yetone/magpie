// Package fasttools is a stdio MCP server that does in one call what an
// agent otherwise takes many turns for: several searches at once
// (batch_search), several files' outlines and line ranges (read_slices),
// every edit of a change, checked before any is written and verified after
// (apply_edits), and independent checks run side by side (run_checks).
//
// It is given to Antigravity (agy, and the Antigravity app, which read the
// same ~/.gemini/config/plugins) as a plugin of its own (plugin.go), with a
// SessionStart hook that tells a new conversation where it is
// (session.go), so its first turns aren't spent finding out. In agy a turn
// costs seconds before the model says anything, and much of a session is
// turns of one search or one edit each: one call doing several saves those
// turns.
package fasttools

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// outCap is the most text a tool gives back: Antigravity keeps a longer
// MCP result in a file of its own and gives the model a path to read
// instead, another turn.
const outCap = 8000

// Run is `magpie fast-tools …`: mcp serves the tools on stdin and stdout;
// session-start is the SessionStart hook.
func Run(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "mcp":
		return serve(os.Stdin, os.Stdout)
	case "session-start":
		SessionStart(os.Stdin, os.Stdout)
		return nil
	case "-h", "--help", "help":
		fmt.Println(usage)
		return nil
	}
	return errors.New(usage)
}

const usage = `usage: magpie fast-tools mcp | session-start

What Antigravity's fast-tools plugin starts, once it is turned on (the
Antigravity CLI's "fast tools" on the Agents page, or
magpie agy fasttools on):
  mcp            the batch_search, read_slices, apply_edits and run_checks MCP server (stdio)
  session-start  the SessionStart hook: a snapshot of the workspace for a new conversation`

type rpc struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

type server struct {
	mu  sync.Mutex
	enc *json.Encoder
	wg  sync.WaitGroup
}

func (s *server) send(v any) {
	s.mu.Lock()
	_ = s.enc.Encode(v)
	s.mu.Unlock()
}

// serve answers requests from r on w until r ends. Calls run side by side:
// a long run_checks doesn't hold a search behind it.
func serve(r io.Reader, w io.Writer) error {
	s := &server{enc: json.NewEncoder(w)}
	in := bufio.NewScanner(r)
	in.Buffer(make([]byte, 64<<10), 32<<20)
	for in.Scan() {
		var m rpc
		if json.Unmarshal(in.Bytes(), &m) != nil || m.Method == "" || len(m.ID) == 0 {
			continue // a notification, or a reply to nothing asked
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(m)
		}()
	}
	s.wg.Wait()
	return in.Err()
}

func (s *server) handle(req rpc) {
	var result any
	var rpcErr any
	switch req.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ServerName, "version": "1"},
			"instructions":    instructions,
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": toolList()}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		text, err := call(p.Name, p.Arguments)
		if err != nil {
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
		} else {
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": capText(text)}}}
		}
	default:
		rpcErr = map[string]any{"code": -32601, "message": "no method " + req.Method}
	}
	out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcErr != nil {
		out["error"] = rpcErr
	} else {
		out["result"] = result
	}
	s.send(out)
}

// call runs one tool.
func call(name string, args json.RawMessage) (string, error) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	switch name {
	case "batch_search":
		return batchSearch(args)
	case "read_slices":
		return readSlices(args)
	case "apply_edits":
		return applyEdits(args)
	case "run_checks":
		return runChecks(args)
	}
	return "", fmt.Errorf("no tool %q", name)
}

// capText cuts text to outCap, saying how much was left out.
func capText(s string) string {
	if len(s) <= outCap {
		return s
	}
	cut := outCap - 120
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	if i := strings.LastIndexByte(s[:cut], '\n'); i > cut/2 {
		cut = i + 1
	}
	return s[:cut] + fmt.Sprintf("\n… %d more bytes left out: narrow the query (a path, a smaller range, fewer queries)\n", len(s)-cut)
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// root is the folder a call works in: its cwd when given, else the
// server's own, unless that is the plugin's folder, where Antigravity may
// start a plugin's server.
func root(cwd string) (string, error) {
	if !filepath.IsAbs(cwd) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		if filepath.Base(wd) == PluginName && filepath.Base(filepath.Dir(wd)) == "plugins" {
			return "", errors.New("give cwd, the workspace's absolute path")
		}
		cwd = filepath.Join(wd, cwd)
	}
	fi, err := os.Stat(cwd)
	if err != nil {
		return "", fmt.Errorf("cwd %s: %w", cwd, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("cwd %s isn't a folder", cwd)
	}
	return filepath.Clean(cwd), nil
}

// at is p in dir: p itself when absolute.
func at(dir, p string) string {
	if p == "" {
		return dir
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, filepath.FromSlash(p))
}

// shown is p as a result names it: relative to dir when inside it.
func shown(dir, p string) string {
	if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

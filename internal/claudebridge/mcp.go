// Package claudebridge contains the small stdio MCP helper used by magpie's
// Claude Subscription bridge. The helper is launched by the genuine Claude
// Code binary; tool execution is handed back to the parent magpie process.
package claudebridge

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sync"
	"time"
)

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type callbackRequest struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
}

type callbackResponse struct {
	Content any  `json:"content"`
	IsError bool `json:"is_error,omitempty"`
	// Tools, when set, replace the tools offered: the client loaded one
	// mid-turn (Claude Code's ToolSearch)
	Tools []tool `json:"tools,omitempty"`
}

// listWait is how long a call's answer waits for Claude Code to list the
// tools again, once told they changed.
var listWait = 5 * time.Second

// RunMCP runs the hidden stdio MCP subprocess. args are callback URL and tools
// JSON path, or, for an agent whose MCP servers are set once for every run
// (Grok), MAGPIE_MCP_CALLBACK and MAGPIE_MCP_TOOLS in the environment it
// hands down. It intentionally implements only the MCP methods Claude Code needs.
func RunMCP(args []string) error {
	if len(args) == 0 && os.Getenv("MAGPIE_MCP_CALLBACK") != "" {
		args = []string{os.Getenv("MAGPIE_MCP_CALLBACK"), os.Getenv("MAGPIE_MCP_TOOLS")}
	}
	if len(args) != 2 {
		return errors.New("claude MCP helper expects callback URL and tools file")
	}
	callbackURL, toolsPath := args[0], args[1]
	b, err := os.ReadFile(toolsPath)
	if err != nil {
		return err
	}
	var tools []tool
	if err := json.Unmarshal(b, &tools); err != nil {
		return fmt.Errorf("read MCP tools: %w", err)
	}

	client := &http.Client{} // a tools/call intentionally blocks until the agent returns its result
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64<<10), 16<<20)
	enc := json.NewEncoder(os.Stdout)
	var outMu sync.Mutex
	respond := func(v any) {
		outMu.Lock()
		_ = enc.Encode(v)
		outMu.Unlock()
	}
	// listed is closed by the next tools/list, once the tools changed
	var toolsMu sync.Mutex
	var listed chan struct{}
	retool := func(next []tool) {
		toolsMu.Lock()
		if reflect.DeepEqual(next, tools) {
			toolsMu.Unlock()
			return
		}
		tools = next
		done := make(chan struct{})
		listed = done
		toolsMu.Unlock()
		respond(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
		select {
		case <-done:
		case <-time.After(listWait):
		}
	}

	for in.Scan() {
		var req rpcRequest
		if json.Unmarshal(in.Bytes(), &req) != nil {
			continue
		}
		// Notifications have no id and must not receive a response.
		if len(req.ID) == 0 {
			continue
		}
		go func(req rpcRequest) {
			result := any(nil)
			var rpcErr any
			switch req.Method {
			case "initialize":
				result = map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
					"serverInfo":      map[string]any{"name": "magpie", "version": "1"},
				}
			case "tools/list":
				toolsMu.Lock()
				result = map[string]any{"tools": tools}
				if listed != nil {
					// answered before the calls waiting on it go on
					defer close(listed)
					listed = nil
				}
				toolsMu.Unlock()
			case "tools/call":
				var p struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
					Meta      map[string]any  `json:"_meta"`
				}
				if err := json.Unmarshal(req.Params, &p); err != nil {
					rpcErr = map[string]any{"code": -32602, "message": err.Error()}
					break
				}
				// Claude Code names the call it is making; an agent that does
				// not (Cursor) gets an id of the helper's own.
				id, _ := p.Meta["claudecode/toolUseId"].(string)
				if id == "" {
					id = newCallID()
				}
				payload, _ := json.Marshal(callbackRequest{ToolCallID: id, Name: p.Name, Arguments: p.Arguments})
				httpReq, _ := http.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(payload))
				httpReq.Header.Set("Content-Type", "application/json")
				res, err := client.Do(httpReq)
				if err != nil {
					rpcErr = map[string]any{"code": -32000, "message": err.Error()}
					break
				}
				body, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
				res.Body.Close()
				if res.StatusCode != http.StatusOK {
					rpcErr = map[string]any{"code": -32000, "message": string(body)}
					break
				}
				var cb callbackResponse
				if err := json.Unmarshal(body, &cb); err != nil {
					rpcErr = map[string]any{"code": -32000, "message": err.Error()}
					break
				}
				if len(cb.Tools) > 0 {
					// Claude Code takes up the tools before the result: its
					// next request offers them
					retool(cb.Tools)
				}
				result = map[string]any{"content": cb.Content, "isError": cb.IsError}
			default:
				rpcErr = map[string]any{"code": -32601, "message": "method not found"}
			}
			response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID)}
			if rpcErr != nil {
				response["error"] = rpcErr
			} else {
				response["result"] = result
			}
			respond(response)
		}(req)
	}
	return in.Err()
}

func newCallID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "call_" + hex.EncodeToString(b[:])
}

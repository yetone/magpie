package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// An agent's call to a tool of one of magpie's own MCP servers is a row of
// the request log (usage.SourceMCP), apart from the model requests (ferock
// on Discord): which tool, when, the provider and model it used, how long
// it took and whether it worked. serveMCP writes one for every tools/call
// the gateway's servers answer (image, vision, web search, plugin tools,
// the hub), and the Magpie Image server an agent runs reports its own
// (MCPCalledMethod), as its calls never pass here. A call's arguments are
// never kept: the row has the tool's name and, when it failed, what it
// said, clipped as a vendor's error is (keepMsg).

// MCPCalledMethod is the JSON-RPC method the Magpie Image server an agent
// runs tells the gateway's image MCP server of a tool call it answered:
// MCPCalled, as params. It is answered with an empty result.
const MCPCalledMethod = "magpie/toolCalled"

// MCPCalled is one tool call the Magpie Image server answered.
type MCPCalled struct {
	Tool string `json:"tool"`
	// Millis is how long the call took, to its answer
	Millis int64 `json:"ms"`
	// Model is what the gateway's answer named the model by: provider/model
	Model string `json:"model,omitempty"`
	// Error is what a failed call said
	Error string `json:"error,omitempty"`
}

// imageServerName is what the request log names both image servers by.
const imageServerName = "magpie-image"

// mcpUseKey holds a tool call's *mcpUse in its request's context.
type mcpUseKey struct{}

// mcpUse is what one tool call was: its server, and the provider and
// model of the last gateway request it made (appendUsage), which may run
// after the call has answered (an image still drawing).
type mcpUse struct {
	mu                             sync.Mutex
	agent, server, provider, model string
	// tool is the tool that ran, when the call named another: the hub's
	// call_tool runs the tool it names
	tool string
}

func mcpUseOf(ctx context.Context) *mcpUse {
	u, _ := ctx.Value(mcpUseKey{}).(*mcpUse)
	return u
}

// used is that the call's request went to provider's model.
func (u *mcpUse) used(provider, model string) {
	if u == nil || provider == "" {
		return
	}
	u.mu.Lock()
	u.provider, u.model = provider, model
	u.mu.Unlock()
}

// usedRef is used with the provider/model an answer names the model by.
func (u *mcpUse) usedRef(ref string) {
	if p, m, ok := strings.Cut(ref, "/"); ok {
		u.used(p, m)
	}
}

// from is that the call was of what o, an earlier call's, used: a
// generation_result's, of the generate call that started the job.
func (u *mcpUse) from(o *mcpUse) {
	if u == nil || o == nil || u == o {
		return
	}
	o.mu.Lock()
	p, m := o.provider, o.model
	o.mu.Unlock()
	u.used(p, m)
}

// on names the server the call went to, for the hub's, whose tools are
// of the servers in the Library.
func (u *mcpUse) on(server string) {
	if u == nil || server == "" {
		return
	}
	u.mu.Lock()
	u.server = server
	u.mu.Unlock()
}

// as names the agent the call was for, for the hub's, mounted per agent.
func (u *mcpUse) as(agent string) {
	if u == nil || agent == "" {
		return
	}
	u.mu.Lock()
	u.agent = agent
	u.mu.Unlock()
}

// named names the tool that ran, for the hub's call_tool.
func (u *mcpUse) named(tool string) {
	if u == nil || tool == "" {
		return
	}
	u.mu.Lock()
	u.tool = tool
	u.mu.Unlock()
}

func (u *mcpUse) get() (agent, server, provider, model string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.agent, u.server, u.provider, u.model
}

// callMCPTool answers a tools/call to server with call, and writes its row.
func callMCPTool(r *http.Request, server string, m mcpMessage, call func(*http.Request, mcpMessage) (any, map[string]any)) (any, map[string]any) {
	use := &mcpUse{server: server}
	start := time.Now()
	result, rpcErr := call(r.WithContext(context.WithValue(r.Context(), mcpUseKey{}, use)), m)
	var p struct {
		Name string `json:"name"`
	}
	json.Unmarshal(m.Params, &p)
	agent, server, prov, model := use.get()
	use.mu.Lock()
	if use.tool != "" {
		p.Name = use.tool
	}
	use.mu.Unlock()
	logMCPCall(r, agent, server, r.URL.Path, p.Name, prov, model, start, time.Since(start), mcpFailure(result, rpcErr))
	return result, rpcErr
}

// logMCPCall writes a tool call's row; agent "" is the request's caller.
// endpoint is the server's path, "stdio" for one an agent runs.
func logMCPCall(r *http.Request, agent, server, endpoint, tool, prov, model string, start time.Time, took time.Duration, failure string) {
	who := callerOf(r)
	rec := usage.Record{Operation: "execute_tool", Time: start, Agent: who.agent, Via: who.via,
		Provider: prov, Model: model, Requested: keepMsg(tool), Host: server, Millis: took.Milliseconds(),
		Error: keepMsg(failure), Endpoint: endpoint, Kind: usage.KindMCP, Source: usage.SourceMCP}
	if agent != "" {
		rec.Agent = agent
	}
	appendUsage(r, rec)
}

// mcpFailure is what a failed call said: its JSON-RPC error's message, or
// the text of a result that is an error; "" for one that worked.
func mcpFailure(result any, rpcErr map[string]any) string {
	if rpcErr != nil {
		if s, _ := rpcErr["message"].(string); s != "" {
			return s
		}
		return "failed"
	}
	var res struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	switch v := result.(type) {
	case map[string]any:
		if e, _ := v["isError"].(bool); !e {
			return ""
		}
	case nil:
		return ""
	}
	b, err := json.Marshal(result)
	if err != nil || json.Unmarshal(b, &res) != nil || !res.IsError {
		return ""
	}
	for _, c := range res.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			return c.Text
		}
	}
	return "failed"
}

// imageMCPCalled writes the row of a call the Magpie Image server an
// agent runs answered (MCPCalledMethod).
func imageMCPCalled(r *http.Request, raw json.RawMessage) (any, map[string]any) {
	var c MCPCalled
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
	}
	switch c.Tool {
	case GenerateImageTool, GenerateVideoTool, GenerationResultTool:
	default:
		return nil, map[string]any{"code": -32602, "message": "no tool " + c.Tool + " is Magpie Image's"}
	}
	took := time.Duration(max(c.Millis, 0)) * time.Millisecond
	prov, model, _ := strings.Cut(c.Model, "/")
	if model == "" {
		prov = ""
	}
	logMCPCall(r, "", imageServerName, "stdio", c.Tool, prov, model, time.Now().Add(-took), took, c.Error)
	return map[string]any{}, nil
}

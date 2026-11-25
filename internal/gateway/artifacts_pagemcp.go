package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/lazyre"
)

// A page's mcp (docs/subsystems/artifacts.md, "MCP") is the tools of the
// MCP servers in magpie's Library, as Claude's mcp capability is a
// viewer's connectors: the page declares a manifest of servers and their
// tools, the viewer's shell asks the viewer once for each server, and
// posts each call here with its own token. The page never reaches this
// route, and nothing outside the manifest is listed or run.
const (
	artifactMCPMost  = 300     // calls a minute, for each artifact
	artifactMCPInput = 1 << 20 // bytes of one call's input, as the contract has it
)

var artifactMCPCalls = newMinuteLimit(artifactMCPMost)

// hostNameChars are what a host:<name> server's name keeps of a local
// server's: the rest is _, as the contract has it.
var hostNameChars = lazyre.New(`[^A-Za-z0-9_-]`)

// artifactMCPError refuses a call with the contract's McpError members
// besides code and message.
func artifactMCPError(w http.ResponseWriter, status int, code, server, msg string, more map[string]any) {
	e := map[string]any{"code": code, "message": msg}
	if server != "" {
		e["server"] = server
	}
	for k, v := range more {
		e[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": e})
}

// libraryServerOf is the Library server a manifest's name is: the one of
// that name, else, for host:<name>, the one whose name reads <name> with
// anything outside [A-Za-z0-9_-] as _. "" when there is none.
func libraryServerOf(name string, ss []HubServer) string {
	for _, s := range ss {
		if s.Name == name {
			return s.Name
		}
	}
	host, ok := strings.CutPrefix(name, "host:")
	if !ok || host == "" {
		return ""
	}
	for _, s := range ss {
		if s.Name == host {
			return s.Name
		}
	}
	for _, s := range ss {
		if hostNameChars.ReplaceAllString(s.Name, "_") == host {
			return s.Name
		}
	}
	return ""
}

// pageToolInfo is a tool as listTools gives it.
type pageToolInfo struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// toolAnnotations are the hints a server declared of a tool, only those
// it gave as true or false: absent means it didn't say.
func toolAnnotations(raw json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	out := map[string]bool{}
	for _, k := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
		if b, ok := m[k].(bool); ok {
			out[k] = b
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, _ := json.Marshal(out)
	return b
}

func pageToolOf(t HubTool, schemas bool) pageToolInfo {
	p := pageToolInfo{Name: t.Tool, Annotations: toolAnnotations(t.Spec["annotations"])}
	json.Unmarshal(t.Spec["description"], &p.Description)
	if schemas {
		p.InputSchema = t.Spec["inputSchema"]
		if len(p.InputSchema) == 0 {
			p.InputSchema = json.RawMessage(`{"type":"object"}`)
		}
		p.OutputSchema = t.Spec["outputSchema"]
	}
	return p
}

// readOnly is whether t's server declared it doesn't change anything:
// yes, no, or not said (ok false).
func readOnly(t pageToolInfo) (ro, ok bool) {
	var a struct {
		ReadOnly *bool `json:"readOnlyHint"`
	}
	if json.Unmarshal(t.Annotations, &a) != nil || a.ReadOnly == nil {
		return false, false
	}
	return *a.ReadOnly, true
}

// artifactMCP is POST <page>/_api/mcp from the shell: {t, version, op,
// server, tool, input, watch}. op list answers {servers}, the manifest's
// servers that are in the Library with the manifest's tools they list;
// describe, one tool with its schemas; call, {result, annotations}, or
// the McpError the page gets.
func (s *Server) artifactMCP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	var c struct {
		Token   string          `json:"t"`
		Version int             `json:"version"`
		Op      string          `json:"op"`
		Server  string          `json:"server"`
		Tool    string          `json:"tool"`
		Input   json.RawMessage `json:"input"`
		Watch   bool            `json:"watch"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, artifactMCPInput+64<<10))
	if err != nil || len(body) > artifactMCPInput+32<<10 || json.Unmarshal(body, &c) != nil {
		artifactMCPError(w, http.StatusBadRequest, "bad_request", "", fmt.Sprintf("a call is a JSON object, its input at most %d MiB", artifactMCPInput>>20), nil)
		return
	}
	if !local(r) || !loopbackHost(r.Host) {
		artifactMCPError(w, http.StatusForbidden, "not_granted", "", "an artifact calls MCP tools in a browser on this computer, at "+URL(), nil)
		return
	}
	id, n, ok := ArtifactRef(r.PathValue("ref"))
	if !ok || strings.Contains(r.PathValue("ref"), "/") {
		artifactMCPError(w, http.StatusNotFound, "not_granted", "", "no such artifact here", nil)
		return
	}
	a, err := artifact.Get(id)
	if err != nil {
		artifactMCPError(w, http.StatusNotFound, "not_granted", "", "the artifact is gone", nil)
		return
	}
	if !artifact.ShellTokenOK(id, c.Token) {
		artifactMCPError(w, http.StatusForbidden, "not_granted", "", "only the viewer's shell calls MCP tools", nil)
		return
	}
	if c.Version <= 0 {
		c.Version = a.Latest().N
		if n > 0 {
			c.Version = n
		}
	}
	var man *artifact.MCPConfig
	if v := a.Version(c.Version); v != nil {
		man = v.Capabilities.MCP()
	}
	if man == nil {
		artifactMCPError(w, http.StatusForbidden, "not_granted", "", "this version of the page doesn't declare mcp", nil)
		return
	}
	on := MCPHub != nil && MCPHub.On()
	var ss []HubServer
	if on {
		ss = hubServersOf(r.Context(), "")
	}
	switch c.Op {
	case "list":
		out := []map[string]any{}
		if on {
			lib := s.pageTools(r.Context(), man, ss)
			for _, d := range man.Servers {
				name := libraryServerOf(d.Server, ss)
				if name == "" {
					continue
				}
				tools := []pageToolInfo{}
				for _, t := range lib[name] {
					if man.Allows(d.Server, t.Tool) {
						tools = append(tools, pageToolOf(t, false))
					}
				}
				out = append(out, map[string]any{"server": d.Server, "kind": "connector", "authStatus": "connected", "tools": tools})
			}
		}
		// the manifest too: the shell asks the viewer about its servers,
		// and lists one not asked about yet from it alone
		res := map[string]any{"servers": out, "manifest": man.Servers}
		if !on {
			res["off"] = errHubOff
		}
		writeJSON(w, http.StatusOK, res)
		return
	case "describe", "call":
	default:
		artifactMCPError(w, http.StatusBadRequest, "bad_request", "", "op is list, describe or call", nil)
		return
	}
	if !man.Allows(c.Server, c.Tool) {
		artifactMCPError(w, http.StatusForbidden, "not_in_manifest", c.Server, fmt.Sprintf("%s's %s isn't in the page's manifest", c.Server, c.Tool), nil)
		return
	}
	if !on {
		artifactMCPError(w, http.StatusNotFound, "server_not_connected", c.Server, errHubOff, nil)
		return
	}
	name := libraryServerOf(c.Server, ss)
	if name == "" {
		artifactMCPError(w, http.StatusNotFound, "server_not_connected", c.Server, "no MCP server named "+c.Server+" in magpie's Library: add it in Library → MCP", nil)
		return
	}
	who := access.Caller(r.Context())
	if !who.MCPAllows(name, c.Tool) {
		artifactMCPError(w, http.StatusForbidden, "blocked_by_policy", c.Server, keyMCPToolRefused(who, name, c.Tool), nil)
		return
	}
	var info *pageToolInfo
	for _, t := range s.pageTools(r.Context(), man, ss)[name] {
		if t.Tool == c.Tool {
			p := pageToolOf(t, c.Op == "describe")
			info = &p
		}
	}
	if c.Op == "describe" {
		if info == nil {
			artifactMCPError(w, http.StatusBadRequest, "bad_request", c.Server, c.Server+" doesn't list "+c.Tool+" now", nil)
			return
		}
		writeJSON(w, http.StatusOK, info)
		return
	}
	if info != nil && c.Watch {
		if ro, said := readOnly(*info); said && !ro {
			artifactMCPError(w, http.StatusBadRequest, "bad_request", c.Server, c.Tool+" changes things (readOnlyHint false): call it with callTool, never watch it", nil)
			return
		}
	}
	input := c.Input
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	if len(input) > artifactMCPInput {
		artifactMCPError(w, http.StatusBadRequest, "bad_request", c.Server, fmt.Sprintf("a call's input is at most %d MiB", artifactMCPInput>>20), nil)
		return
	}
	if input[0] != '{' {
		artifactMCPError(w, http.StatusBadRequest, "bad_request", c.Server, "a tool's input is a JSON object", nil)
		return
	}
	if !artifactMCPCalls.allow(a.ID, time.Now()) {
		artifactMCPError(w, http.StatusTooManyRequests, "rate_limited", c.Server, fmt.Sprintf("a page makes at most %d MCP calls a minute", artifactMCPMost), map[string]any{"retryable": true, "retryAfterMs": 10000})
		return
	}
	use := mcpUseOf(r.Context())
	use.on(name)
	use.as("artifact")
	use.named(c.Tool)
	ctx := WithHubCaller(r.Context(), HubCaller{Session: "artifact-" + a.ID})
	ctx, cancel := context.WithTimeout(ctx, hubCallWait)
	defer cancel()
	res, err := MCPHub.Call(ctx, "", name, c.Tool, input)
	if err != nil {
		why := err.Error()
		if r.Context().Err() != nil {
			artifactMCPError(w, 499, "cancelled", c.Server, "the call was cancelled", nil)
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			why = "it didn't answer in " + hubCallWait.String()
		}
		artifactMCPError(w, http.StatusBadGateway, "server_unavailable", c.Server, "the MCP server "+c.Server+" couldn't run "+c.Tool+": "+why, map[string]any{"retryable": true})
		return
	}
	var got struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(res, &got) != nil {
		artifactMCPError(w, http.StatusBadGateway, "upstream_error", c.Server, c.Server+" answered something that isn't a tool's result", nil)
		return
	}
	if got.IsError {
		msg := c.Tool + " failed"
		for _, b := range got.Content {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				msg = hubCut(strings.TrimSpace(b.Text), 2000)
				break
			}
		}
		artifactMCPError(w, http.StatusOK, "tool_error", c.Server, msg, map[string]any{"result": res})
		return
	}
	out := map[string]any{"result": res}
	if info != nil && len(info.Annotations) > 0 {
		out["annotations"] = info.Annotations
	}
	writeJSON(w, http.StatusOK, out)
}

// pageTools are the tools the Library servers a manifest names list
// now, by Library server.
func (s *Server) pageTools(ctx context.Context, man *artifact.MCPConfig, ss []HubServer) map[string][]HubTool {
	want := map[string]bool{}
	for _, d := range man.Servers {
		if name := libraryServerOf(d.Server, ss); name != "" {
			want[name] = true
		}
	}
	out := map[string][]HubTool{}
	if len(want) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, hubListWait)
	defer cancel()
	who := access.Caller(ctx)
	for _, t := range MCPHub.Tools(ctx, "") {
		if want[t.Server] && who.MCPAllows(t.Server, t.Tool) {
			out[t.Server] = append(out[t.Server], t)
		}
	}
	return out
}

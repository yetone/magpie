package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/access"
)

// A gateway key held to some MCP servers and tools (access.Key.MCP,
// wakaka on Discord) is held to them on every route that reaches one:
//
//   - magpie's own servers (web search, image recognition, image
//     generation, Magpie Browser, the plugin tools) and the hub all answer
//     through serveMCP, which refuses a server the key may not use, leaves
//     the tools it may not use out of tools/list and refuses a tools/call
//     of one (mcpKeyServe);
//   - the hub's tools are of the Library's servers, so it holds each to
//     the key by the server behind it: hubServersOf and hubTools leave out
//     what the key may not use, and hubMCPRun, which every hub call goes
//     through, refuses it;
//   - the relays, /mcp/<name> (a remote server magpie signed in to) and
//     /mcp/run/<name> (a command magpie runs), pass the server's own
//     messages on, so mcpKeyGuard reads the request for a tools/call and
//     the answer for a tools/list (mcpToolsFilter);
//   - OwnMCPPath lists only the servers the key may use.

// hubServerName is what serveMCP names the hub by; its tools are the
// Library's servers', which the hub holds to the key itself.
const hubServerName = "magpie"

// keyMCPServerRefused is what a key is told of a server it may not use.
func keyMCPServerRefused(who access.Identity, server string) string {
	return "the gateway key " + quoteName(who.KeyName) + " may not use the MCP server " + server + ": its owner picks the MCP servers it may use in Gateway → Gateway keys"
}

// keyMCPToolRefused is what a key is told of a tool it may not use.
func keyMCPToolRefused(who access.Identity, server, tool string) string {
	return "the gateway key " + quoteName(who.KeyName) + " may not use the tool " + tool + " of the MCP server " + server + ": its owner picks the MCP tools it may use in Gateway → Gateway keys"
}

func quoteName(s string) string { return "\"" + s + "\"" }

// mcpCallName is the tool a tools/call names.
func mcpCallName(params json.RawMessage) string {
	var p struct {
		Name string `json:"name"`
	}
	json.Unmarshal(params, &p)
	return p.Name
}

// mcpKeyServe is whether serveMCP may answer r for server at all: a key
// held to MCP servers that may not use it is refused, and told so.
func mcpKeyServe(w http.ResponseWriter, r *http.Request, server string) bool {
	if server == hubServerName {
		return true
	}
	who := access.Caller(r.Context())
	if ok, _ := who.MCPServer(server); ok {
		return true
	}
	log.Printf("refused %s %s: gateway key %q may not use the MCP server %s", r.Method, r.URL.Path, who.KeyName, server)
	http.Error(w, keyMCPServerRefused(who, server), http.StatusForbidden)
	return false
}

// mcpKeyCall is the JSON-RPC error a tools/call of server is refused
// with for r's key; nil when it may run. The hub's calls are held by the
// server behind each (hubMCPRun).
func mcpKeyCall(r *http.Request, server string, m mcpMessage) map[string]any {
	if server == hubServerName {
		return nil
	}
	who := access.Caller(r.Context())
	if tool := mcpCallName(m.Params); !who.MCPAllows(server, tool) {
		return map[string]any{"code": -32602, "message": keyMCPToolRefused(who, server, tool)}
	}
	return nil
}

// mcpKeyList is result, a tools/list's of server, without the tools r's
// key may not use.
func mcpKeyList(r *http.Request, server string, result any) any {
	if server == hubServerName {
		return result
	}
	who := access.Caller(r.Context())
	if _, every := who.MCPServer(server); every {
		return result
	}
	b, err := json.Marshal(map[string]any{"result": result})
	if err != nil {
		return map[string]any{"tools": []any{}}
	}
	out, changed := keepTools(b, func(tool string) bool { return who.MCPAllows(server, tool) })
	if !changed {
		return result
	}
	var m struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(out, &m) != nil {
		return map[string]any{"tools": []any{}}
	}
	return m.Result
}

// keepTools is b, a JSON-RPC message or a batch of them, with each
// result's tools (a tools/list's answer) cut to those keep names, and
// whether anything was cut. A tools list that can't be read is none.
func keepTools(b []byte, keep func(string) bool) ([]byte, bool) {
	t := bytes.TrimSpace(b)
	if len(t) > 0 && t[0] == '[' {
		var msgs []json.RawMessage
		if json.Unmarshal(t, &msgs) != nil {
			return b, false
		}
		changed := false
		for i, m := range msgs {
			if out, ok := keepTools(m, keep); ok {
				msgs[i], changed = out, true
			}
		}
		if !changed {
			return b, false
		}
		out, err := json.Marshal(msgs)
		if err != nil {
			return []byte("[]"), true
		}
		return out, true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(t, &m) != nil || len(m["result"]) == 0 {
		return b, false
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(m["result"], &res) != nil {
		return b, false
	}
	raw, ok := res["tools"]
	if !ok {
		return b, false
	}
	var tools []json.RawMessage
	json.Unmarshal(raw, &tools)
	kept := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		var named struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(tool, &named) == nil && named.Name != "" && keep(named.Name) {
			kept = append(kept, tool)
		}
	}
	if len(kept) == len(tools) && len(tools) > 0 || string(bytes.TrimSpace(raw)) == "[]" {
		return b, false
	}
	res["tools"], _ = json.Marshal(kept)
	m["result"], _ = json.Marshal(res)
	out, err := json.Marshal(m)
	if err != nil {
		return b, false
	}
	return out, true
}

// mcpKeyGuard holds a request to a relayed server (/mcp/<name>,
// /mcp/run/<name>) to its key's MCP list: refused when the key may not
// use the server; when it may use only some of its tools, a tools/call of
// another is answered with a JSON-RPC error and never sent, and the
// answers are passed through mcpToolsFilter. It is the writer to answer
// through, the request (its body read again), and done, to call once the
// answer is written; ok false when it answered r itself.
func mcpKeyGuard(w http.ResponseWriter, r *http.Request, server string) (_ http.ResponseWriter, _ *http.Request, done func(), ok bool) {
	who := access.Caller(r.Context())
	if !who.MCPHeld() {
		return w, r, func() {}, true
	}
	allowed, every := who.MCPServer(server)
	if !allowed {
		log.Printf("refused %s %s: gateway key %q may not use the MCP server %s", r.Method, r.URL.Path, who.KeyName, server)
		http.Error(w, keyMCPServerRefused(who, server), http.StatusForbidden)
		return nil, nil, nil, false
	}
	if every {
		return w, r, func() {}, true
	}
	if r.Method == http.MethodPost {
		body, ok := readMCPBody(w, r, 32<<20)
		if !ok {
			return nil, nil, nil, false
		}
		t := bytes.TrimSpace(body)
		var msgs []mcpMessage
		var err error
		if len(t) > 0 && t[0] == '[' {
			err = json.Unmarshal(t, &msgs)
		} else {
			var m mcpMessage
			err = json.Unmarshal(t, &m)
			msgs = []mcpMessage{m}
		}
		// a message that can't be read can't be told a tool it may use
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error: the body isn't a JSON-RPC message"}})
			return nil, nil, nil, false
		}
		for _, m := range msgs {
			if m.Method != "tools/call" {
				continue
			}
			if tool := mcpCallName(m.Params); !who.MCPAllows(server, tool) {
				log.Printf("refused %s %s: gateway key %q may not use the tool %s of the MCP server %s", r.Method, r.URL.Path, who.KeyName, tool, server)
				if len(msgs) > 1 {
					http.Error(w, keyMCPToolRefused(who, server, tool), http.StatusForbidden)
					return nil, nil, nil, false
				}
				writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32602, "message": keyMCPToolRefused(who, server, tool)}})
				return nil, nil, nil, false
			}
		}
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	}
	f := &mcpToolsFilter{w: w, keep: func(tool string) bool { return who.MCPAllows(server, tool) }}
	return f, r, f.finish, true
}

// mcpToolsFilter writes a relayed server's answers with the tools its
// caller's key may not use left out of each tools/list's: a JSON answer
// whole once written (done), an event stream event by event.
type mcpToolsFilter struct {
	w    http.ResponseWriter
	keep func(string) bool
	// mode is how the body is written, decided by its Content-Type
	mode   int
	status int
	buf    bytes.Buffer
	event  [][]byte // the event stream's lines read of the event not yet ended
}

const (
	mcpFilterUndecided = iota
	mcpFilterJSON
	mcpFilterEvents
	mcpFilterPass
	mcpFilterRefused
)

func (f *mcpToolsFilter) Header() http.Header { return f.w.Header() }

func (f *mcpToolsFilter) WriteHeader(code int) {
	if f.mode != mcpFilterUndecided {
		return
	}
	ct := strings.ToLower(f.w.Header().Get("Content-Type"))
	enc := strings.ToLower(strings.TrimSpace(f.w.Header().Get("Content-Encoding")))
	readable := enc == "" || enc == "identity"
	switch {
	case !readable && (strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/event-stream")):
		// an answer that can't be read can't be told to list only the
		// tools the key may use
		f.mode = mcpFilterRefused
		f.w.Header().Del("Content-Encoding")
		f.w.Header().Del("Content-Length")
		f.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		f.w.WriteHeader(http.StatusBadGateway)
		io.WriteString(f.w, "the MCP server answered in an encoding ("+enc+") magpie can't read, so it can't hold the answer to the tools this gateway key may use\n")
		return
	case strings.HasPrefix(ct, "application/json"):
		f.mode, f.status = mcpFilterJSON, code
		f.w.Header().Del("Content-Length")
		return
	case strings.HasPrefix(ct, "text/event-stream"):
		f.mode = mcpFilterEvents
		f.w.Header().Del("Content-Length")
	default:
		f.mode = mcpFilterPass
	}
	f.w.WriteHeader(code)
}

func (f *mcpToolsFilter) Write(b []byte) (int, error) {
	if f.mode == mcpFilterUndecided {
		f.WriteHeader(http.StatusOK)
	}
	switch f.mode {
	case mcpFilterJSON:
		return f.buf.Write(b)
	case mcpFilterEvents:
		f.buf.Write(b)
		if err := f.lines(); err != nil {
			return 0, err
		}
		return len(b), nil
	case mcpFilterRefused:
		return len(b), nil
	}
	return f.w.Write(b)
}

// lines writes each event of the stream that has ended.
func (f *mcpToolsFilter) lines() error {
	for {
		i := bytes.IndexByte(f.buf.Bytes(), '\n')
		if i < 0 {
			return nil
		}
		line := bytes.Clone(f.buf.Next(i + 1))
		if len(bytes.TrimRight(line, "\r\n")) > 0 {
			f.event = append(f.event, line)
			continue
		}
		if err := f.endEvent(line); err != nil {
			return err
		}
	}
}

// endEvent writes the event read, its data's tools cut, then end.
func (f *mcpToolsFilter) endEvent(end []byte) error {
	var data [][]byte
	for _, l := range f.event {
		if v, ok := bytes.CutPrefix(bytes.TrimRight(l, "\r\n"), []byte("data:")); ok {
			data = append(data, bytes.TrimPrefix(v, []byte(" ")))
		}
	}
	var out bytes.Buffer
	cut := false
	if len(data) > 0 {
		if kept, changed := keepTools(bytes.Join(data, []byte("\n")), f.keep); changed {
			cut = true
			for _, l := range f.event {
				if !bytes.HasPrefix(l, []byte("data:")) {
					out.Write(l)
				}
			}
			out.WriteString("data: ")
			out.Write(kept)
			out.WriteString("\n")
		}
	}
	if !cut {
		for _, l := range f.event {
			out.Write(l)
		}
	}
	out.Write(end)
	f.event = nil
	_, err := f.w.Write(out.Bytes())
	return err
}

func (f *mcpToolsFilter) Flush() {
	if f.mode == mcpFilterJSON || f.mode == mcpFilterRefused {
		return
	}
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
}

// finish writes what is held: a JSON answer, cut, or the end of a stream
// that stopped mid-event.
func (f *mcpToolsFilter) finish() {
	switch f.mode {
	case mcpFilterJSON:
		out, _ := keepTools(f.buf.Bytes(), f.keep)
		f.w.WriteHeader(f.status)
		f.w.Write(out)
	case mcpFilterEvents:
		for _, l := range f.event {
			f.w.Write(l)
		}
		f.w.Write(f.buf.Bytes())
	}
}

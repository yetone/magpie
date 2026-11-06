package access

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

// A key's MCP list (Key.MCP, wakaka on Discord) holds it to some of the
// MCP servers the gateway serves, and some of their tools: the Library's
// servers (by the hub, /mcp/<name>, /mcp/run/<name>) and magpie's own
// (web search, image recognition, image generation, Magpie Browser, the
// plugin tools). An entry is a server's name, every tool of it, or
// "<server>/<tool>", that one tool; NoMCP alone holds the key to none.
// An empty list is every server, as keys always were.

// NoMCP is the list of a key held to no MCP server at all.
const NoMCP = "-"

// mcpServerRe is a server's name as the Library takes one (library's
// nameRe), and as magpie names its own servers.
var mcpServerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// CleanMCP is a key's MCP list as it is kept: each entry trimmed and
// checked, told apart in no other way than as written, a tool dropped
// whose server is listed whole, and NoMCP dropped beside any server.
func CleanMCP(in []string) ([]string, error) {
	var out []string
	none := false
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if e == NoMCP {
			none = true
			continue
		}
		server, tool, hasTool := strings.Cut(e, "/")
		if !mcpServerRe.MatchString(server) || hasTool && (tool == "" || len(tool) > 128 || strings.TrimSpace(tool) != tool) {
			return nil, errors.New("A key's MCP entry is <server> or <server>/<tool>, like github or github/search_code: " + e)
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	// a server listed whole has every tool already
	out = slices.DeleteFunc(out, func(e string) bool {
		server, _, hasTool := strings.Cut(e, "/")
		return hasTool && slices.Contains(out, server)
	})
	if len(out) > 500 {
		return nil, errors.New("A key can list at most 500 MCP servers and tools")
	}
	if len(out) == 0 && none {
		return []string{NoMCP}, nil
	}
	return out, nil
}

// mcpServer is whether the list l lets in server, and whether it lets in
// every tool of it; an empty list lets in everything.
func mcpServer(l []string, server string) (ok, every bool) {
	if len(l) == 0 {
		return true, true
	}
	for _, e := range l {
		s, _, hasTool := strings.Cut(e, "/")
		if s != server {
			continue
		}
		if !hasTool {
			return true, true
		}
		ok = true
	}
	return ok, false
}

// MCPHeld is whether the key, or the key it was issued under, is held to
// some MCP servers or tools.
func (who Identity) MCPHeld() bool {
	return len(who.MCP) > 0 || who.Issuer != nil && len(who.Issuer.MCP) > 0
}

// MCPServer is whether the key may reach the MCP server called server,
// and whether it may use every tool of it (else MCPAllows says which).
func (who Identity) MCPServer(server string) (ok, every bool) {
	ok, every = mcpServer(who.MCP, server)
	if who.Issuer != nil {
		pOK, pEvery := mcpServer(who.Issuer.MCP, server)
		ok, every = ok && pOK, every && pEvery
	}
	return ok, ok && every
}

// MCPAllows is whether the key may use the tool of the MCP server called
// server.
func (who Identity) MCPAllows(server, tool string) bool {
	allows := func(l []string) bool {
		ok, every := mcpServer(l, server)
		return every || ok && slices.Contains(l, server+"/"+tool)
	}
	return allows(who.MCP) && (who.Issuer == nil || allows(who.Issuer.MCP))
}

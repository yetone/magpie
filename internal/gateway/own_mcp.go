package gateway

import (
	"context"
	"net/http"
	"slices"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gwtool"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// OwnMCPPath lists the MCP servers magpie serves itself (web search,
// image recognition, image generation, memory, the plugins' tools) that are on
// now, for an app that gives a model the Library's servers and wants
// magpie's own beside them: the community chat page (ferock on Discord),
// which read only library.json. A server is listed while an agent that
// mounts it would get its tools to work: the search while plugins/search
// serves and something searches (canSearch), image recognition while a
// model sees (seer: not Off in Settings), image generation while a model
// draws (drawer: not Off), the plugin tools while plugins/tools serves
// and a plugin that is on gives a tool. Each is named as its own
// initialize names it, with its path on this gateway; nothing in it is a
// secret.
const OwnMCPPath = "/v1/magpie/mcp"

// OwnMCP is one of magpie's own MCP servers, as OwnMCPPath lists it.
type OwnMCP struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Path  string `json:"path"` // on this gateway, Streamable HTTP
}

// toolsOn is how many plugins that are on give a tool, from the note the
// host keeps (never starting it); a variable for tests.
var toolsOn = plugin.ToolsOn

// ownMCP are magpie's own MCP servers that are on now.
func ownMCP() []OwnMCP {
	out := []OwnMCP{}
	if canSearch() {
		out = append(out, OwnMCP{"magpie-web-search", "Magpie Web Search", SearchMCPPath})
	}
	if _, ok := seer(); ok {
		out = append(out, OwnMCP{"magpie-vision", "Magpie Image Recognition", VisionMCPPath})
	}
	if _, ok := drawer(); ok {
		out = append(out, OwnMCP{imageServerName, "Magpie Image", ImageMCPPath})
	}
	// memory is always there: it needs no model
	out = append(out, OwnMCP{memoryServerName, "Magpie Memory", MemoryMCPPath})
	if gwtool.Served(gwtool.PluginTools) {
		// a note that can't be read says nothing: the server is listed,
		// and its tools/list tells
		if n, err := toolsOn(); err != nil || n > 0 {
			out = append(out, OwnMCP{"magpie-tools", "Magpie Plugin Tools", ToolsMCPPath})
		}
	}
	return out
}

func (s *Server) ownMCPList(w http.ResponseWriter, r *http.Request) {
	// a gateway key held to some MCP servers is told of those alone
	who := access.Caller(r.Context())
	ss := slices.DeleteFunc(ownMCP(), func(o OwnMCP) bool { ok, _ := who.MCPServer(o.Name); return !ok })
	writeJSON(w, 200, map[string]any{"servers": ss})
}

// OwnMCPTools are magpie's own MCP servers that are on now, Magpie
// Browser beside them, each with the names of its tools, for the picker
// of the servers and tools a gateway key may use (wakaka on Discord). The
// plugin tools' server is listed with no tools when they can't be read:
// it can still be given whole.
func OwnMCPTools(ctx context.Context) []OwnMCPServer {
	out := []OwnMCPServer{}
	for _, o := range ownMCP() {
		s := OwnMCPServer{Name: o.Name, Title: o.Title}
		switch o.Name {
		case "magpie-web-search":
			s.Tools = []string{searchMCPTool, fetchMCPTool}
		case "magpie-vision":
			s.Tools = []string{visionMCPTool}
		case imageServerName:
			s.Tools = []string{GenerateImageTool, GenerateVideoTool, GenerationResultTool}
		case memoryServerName:
			s.Tools = []string{memorySearchTool, memorySaveTool, memoryForgetTool}
		case "magpie-tools":
			ts, _ := servedTools(ctx)
			for _, t := range ts {
				s.Tools = append(s.Tools, t.name)
			}
		}
		out = append(out, s)
	}
	return append(out, OwnMCPServer{Name: "magpie-browser", Title: "Magpie Browser", Tools: []string{"browser"}})
}

// OwnMCPServer is one of magpie's own MCP servers with its tools' names.
type OwnMCPServer struct {
	Name  string   `json:"name"`
	Title string   `json:"title"`
	Tools []string `json:"tools"`
}

// OwnUnready is why magpie's own server name (Magpie Web Search, Magpie
// Image Recognition, Magpie Image) can't answer yet, in English for the
// Library's Discover to say on its row: "" while ownMCP lists it, and for
// any other name. Its tools answer all the same, with how to set it up
// (matrix0487 on Discord: all three in Discover, never a dead tool).
func OwnUnready(name string) string {
	switch name {
	case "magpie-web-search":
		switch {
		case canSearch():
			return ""
		case !searchServed():
			return "Web search is off in Plugins › Built-in: its tools say so until it is turned on"
		}
		return "Nothing searches yet: add a search API in Settings → Models → Web search, or a provider that can search. Until then its tools say so"
	case "magpie-vision":
		if _, ok := seer(); ok {
			return ""
		}
		if provider.HeldSettings().Vision == "off" {
			return "Image recognition is off in Settings → Models → Images: its tool says so until a model is picked there"
		}
		return "No model that sees images is set up yet: it reads images once a provider with one is added, and says so until then"
	case imageServerName:
		if _, ok := drawer(); ok {
			return ""
		}
		if settings.Load().ImageGen == "off" {
			return "Image generation is off in Settings → Models → Images: its tools say so until a model is picked there"
		}
		return "No model that draws images is set up yet: it draws once a provider with an image model is added, and says so until then"
	}
	return ""
}

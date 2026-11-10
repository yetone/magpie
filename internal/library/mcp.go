package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/claudelock"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/imagemcp"
	"github.com/yetone/magpie/internal/lazyre"
)

// Server is one MCP server: a command magpie's agents start, or a URL they
// reach.
type Server struct {
	Name   string `json:"name"`
	Bundle string `json:"bundle,omitempty"` // the Claude Code plugin it came with (plugin.go)
	// Transport is stdio for a command, http (streamable) or sse for a URL.
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Agents    []string          `json:"agents"`
	// DisabledTools are the server's tools its agents leave out, by the
	// names the server gives them, written in each agent's own way where
	// it has one (mcp_tools_off.go)
	DisabledTools []string `json:"disabledTools,omitempty"`
	// OAuth is an OAuth app of the user's own that magpie signs in to the
	// server as, for one whose sign-in doesn't let magpie register itself
	// (GitHub's, 新v学员 on Discord). It is magpie's alone: the agents are
	// given the server as before.
	OAuth *OAuthApp `json:"oauth,omitempty"`
	// StartupTimeout is the seconds an agent waits for the server to start
	// and list its tools: Codex's startup_timeout_sec (#3168), the only
	// agent given it. 0 leaves it to the agent (Codex's own is 30s).
	StartupTimeout int `json:"startupTimeout,omitempty"`
	// Local keeps the server to this computer (huhuy72219405 on X: a
	// server of a program or folder only this computer has). A backup or
	// a sync never carries it, and one coming in neither takes it away nor
	// puts another computer's of that name in its place (carry.go).
	Local bool `json:"local,omitempty"`
	// Plugin is the plugin (plugin.Name) whose tools the server is: the
	// gateway serves them at its URL, and the hub calls the plugin itself
	// (plugin_tools.go)
	Plugin string `json:"plugin,omitempty"`
	// Hidden is a server turned off in the Library (ServerOn), or a
	// plugin's whose tools were hidden from every agent on the Plugins
	// page (PluginServerOn, chengming0v0 on Discord): no agent is given
	// it, nor a project, nor an app at the MCP router's address. Paused are the agents it had, given back when it is shown
	// again. Both go once the server has an agent again (save).
	Hidden bool     `json:"hidden,omitempty"`
	Paused []string `json:"paused,omitempty"`
	// Bridge is the Remote magpie (its provider id) whose MCP router the
	// server is, given a server of its own (bridge_mcp.go)
	Bridge string `json:"bridge,omitempty"`
	// Hub is how the MCP hub (Library.MCPHub) gives the server: "" shares
	// it through the hub's one entry, as every server was before;
	// HubDirect keeps it the agents' own entry, written as with the hub
	// off, for a server that reads its project from the folder it is
	// started in, or keeps a session's state (#4382, cuipengcx90).
	Hub string `json:"hub,omitempty"`
	// Note is the user's own word on what the server is for (youyo.p on
	// Discord: with many of them, which is which). Only the library keeps
	// it, and a sync carries it; no agent is given it (TestNoteNeverGiven).
	Note string `json:"note,omitempty"`

	// about is the hub entry's description, for an agent that shows a
	// server by one before it has connected (Pi, hubServerFor)
	about string

	// startupWrote is the startup_timeout_sec magpie last wrote in the
	// entry being written, 0 for none: one the entry has otherwise is the
	// user's, and stays (startupTheirs)
	startupWrote float64
}

// OAuthApp is an OAuth app made with a server's vendor, by the names
// Claude Code gives one (--client-id, --client-secret, --callback-port;
// "oauth": {"clientId", "callbackPort"}).
type OAuthApp struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// CallbackPort is the port of the callback URL the app was made with;
	// 0 for one made with http://localhost/callback, which takes any
	CallbackPort int `json:"callbackPort,omitempty"`
}

// Remote reports whether the server is reached by URL.
func (s *Server) Remote() bool { return s.Transport == "http" || s.Transport == "sse" }

// ServerOf is a server as it is typed: a URL, or a command and its
// arguments.
func ServerOf(name string, cmd []string) (Server, error) {
	s := Server{Name: name, Agents: []string{}}
	switch {
	case len(cmd) == 0:
		return s, fmt.Errorf("a URL or a command is needed")
	case strings.HasPrefix(cmd[0], "http://") || strings.HasPrefix(cmd[0], "https://"):
		if len(cmd) > 1 {
			return s, fmt.Errorf("a server by URL takes nothing after it")
		}
		s.Transport, s.URL = "http", cmd[0]
	default:
		s.Transport, s.Command, s.Args = "stdio", cmd[0], cmd[1:]
	}
	return s, nil
}

func (s *Server) check() error {
	if err := checkName("server", s.Name); err != nil {
		return err
	}
	s.Command, s.URL = strings.TrimSpace(s.Command), strings.TrimSpace(s.URL)
	switch s.Transport {
	case "stdio":
		if s.Command == "" {
			return fmt.Errorf("%s: a command is needed", s.Name)
		}
		s.Command, s.Args = spacedProgram(s.Command, s.Args)
		s.URL, s.Headers = "", nil
	case "http", "sse":
		if !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
			return fmt.Errorf("%s: the URL has to start with http:// or https://", s.Name)
		}
		s.Command, s.Args, s.Env = "", nil, nil
	default:
		return fmt.Errorf("%s: unknown transport %q", s.Name, s.Transport)
	}
	switch s.Hub {
	case "", "shared":
		s.Hub = ""
	case HubDirect:
	default:
		return fmt.Errorf("%s: unknown hub policy %q: shared or direct", s.Name, s.Hub)
	}
	// magpie signs in only to a streamable HTTP server
	if s.Transport != "http" {
		s.OAuth = nil
	} else if s.OAuth != nil {
		a := *s.OAuth
		a.ClientID, a.ClientSecret = strings.TrimSpace(a.ClientID), strings.TrimSpace(a.ClientSecret)
		if a.CallbackPort < 0 || a.CallbackPort > 65535 {
			return fmt.Errorf("%s: the callback port has to be from 1 to 65535", s.Name)
		}
		s.OAuth = &a
		if a.ClientID == "" {
			if a.ClientSecret != "" || a.CallbackPort != 0 {
				return fmt.Errorf("%s: the OAuth app needs its client ID", s.Name)
			}
			s.OAuth = nil
		}
	}
	if err := checkNote(&s.Note); err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	if s.StartupTimeout < 0 || s.StartupTimeout > maxStartupTimeout {
		return fmt.Errorf("%s: the startup timeout is between 0 (the agent's own) and %d seconds", s.Name, maxStartupTimeout)
	}
	for k := range s.Env {
		if strings.TrimSpace(k) == "" {
			delete(s.Env, k)
		}
	}
	for k := range s.Headers {
		if strings.TrimSpace(k) == "" {
			delete(s.Headers, k)
		}
	}
	return nil
}

// spacedProgram is a program by full path that has spaces in it, typed
// as it is: "/Applications/Agent HUD.app/Contents/MacOS/Agent HUD --mcp"
// (zachzhao1984 on Discord), or C:\Program Files\… on Windows, which a
// command line split at each space makes /Applications/Agent and three
// arguments, a program that isn't there. When cmd is a full path to no
// file and cmd with the words after it, joined back by a space, is a
// program on this computer, that is the program and the rest are its
// arguments: the fewest words that make one, as Windows reads an unquoted
// path. A command that is there, a bare name, or a path no join makes a
// program are left as typed.
func spacedProgram(cmd string, args []string) (string, []string) {
	if len(args) == 0 || !filepath.IsAbs(cmd) || program(cmd) {
		return cmd, args
	}
	p := cmd
	for i, a := range args {
		p += " " + a
		if program(p) {
			return p, slices.Clone(args[i+1:])
		}
	}
	return cmd, args
}

// program is whether path is a file this computer can run.
func program(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || fi.Mode().Perm()&0o111 != 0
}

// same reports whether two definitions start the same server; which agents
// have it doesn't matter.
func (s *Server) same(o *Server) bool {
	return s.Transport == o.Transport && s.Command == o.Command && slices.Equal(s.Args, o.Args) &&
		maps.Equal(s.Env, o.Env) && s.URL == o.URL && maps.Equal(s.Headers, o.Headers) && s.about == o.about
}

// ---- each agent's own format ----------------------------------------------

type mcpFormat int

const (
	fmtClaude mcpFormat = iota
	fmtCodex
	fmtGemini
	fmtOpenCode
	fmtCursor
	fmtCopilot
	fmtCrush
	fmtGoose
	fmtPi
	fmtDesktop
	fmtZCode
	fmtDsh
	// fmtPiNative is the mcp.json Pi 0.99 reads itself (pimcp.go)
	fmtPiNative
	// fmtAntigravity is Antigravity's mcp_config.json: a remote server is
	// its serverUrl, whatever it speaks
	fmtAntigravity
	// fmtHermes is Hermes Agent's mcp_servers in its config.yaml: a url is
	// streamable HTTP unless transport says sse, a command stdio
	fmtHermes
	// fmtOmp is omp's mcp.json, and Qoder's settings.json: "type" says
	// http or sse beside a url, a command needs none
	fmtOmp
	// fmtKimi is Kimi Code's mcp.json, the Python kimi-cli's and the new
	// one's alike: "transport" http or sse beside a url
	fmtKimi
	// fmtDevin is Devin's mcp_config.json, a transport on every entry
	// (stdio, http, sse) as `devin mcp add` writes it
	fmtDevin
	// fmtGrok is Grok Build's [mcp_servers.<name>] tables in config.toml
	fmtGrok
	// fmtCline is Cline's CLI's cline_mcp_settings.json, where an entry's
	// transport is an object of its own (type stdio, streamableHttp, sse)
	fmtCline
	// fmtCommandCode is Command Code's mcp.json: transport stdio or http,
	// and enabled
	fmtCommandCode
	// fmtAlma is Alma's ~/.config/alma/mcp.json: a url is streamable HTTP
	// (SSE when that fails) unless transport says sse, a command stdio
	fmtAlma
	// fmtZed is Zed's settings.json context_servers: command/args/env, or
	// url/headers, which Zed speaks streamable HTTP to
	fmtZed
	// fmtKiro is Kiro CLI's ~/.kiro/settings/mcp.json: mcpServers of
	// command/args/env or url/headers — no type on an entry, its shape
	// saying which, and no way to name SSE (kiro.dev/docs/mcp/configuration)
	fmtKiro
	// fmtLiveAgent is LiveAgent's MCP settings, K-brain's
	// config.json.live-mcp.json: a list of servers, each by its id, stdio
	// or streamable HTTP (liveagent.go)
	fmtLiveAgent
)

// mcpFile is the file an agent keeps its user-wide MCP servers in.
type mcpFile struct {
	Path   string
	Format mcpFormat
	// Also are files given the same servers, read from Path: dsh's other
	// profiles, pi-mcp-extension's mcp.json beside pi-mcp-adapter's file,
	// Claude Desktop's Claude-3p file (each with the user's own fields in
	// its entry kept).
	Also []string
	// Extra are files servers are found in too, for bringing into the
	// library, but not written: Pi's mcp.json that pi-mcp-adapter no longer
	// reads, with what couldn't be moved from it.
	Extra []string
	// WSL: the agent runs in a WSL distro, where a Windows program isn't
	// one it can start, but for magpie's own (side); Distro is its name and
	// Home its $HOME as the distro spells it
	WSL          bool
	Distro, Home string
	// Literal: what reads the file reads no reference to a variable
	// (pi-mcp-extension), whatever its format's syntax (refsOf)
	Literal bool
	// NoCmdRefs: the agent reads no reference in a command or its args,
	// whatever its format's other agents do (Droid's, cmdRefs)
	NoCmdRefs bool
}

// files are every file the servers are written into.
func (f *mcpFile) files() []string { return append([]string{f.Path}, f.Also...) }

// key is the object the servers are kept under.
func (f *mcpFile) key() string {
	switch f.Format {
	case fmtCodex, fmtHermes, fmtGrok:
		return "mcp_servers"
	case fmtOpenCode, fmtCrush:
		return "mcp"
	case fmtGoose:
		return "extensions"
	case fmtZCode:
		return "mcp.servers"
	case fmtZed:
		return "context_servers"
	}
	return "mcpServers"
}

// supports says why the agent can't reach a server, or nil when it can.
func (f *mcpFile) supports(s *Server) error {
	if f.Format == fmtDesktop && s.Remote() {
		return errNoRemote
	}
	if s.Transport == "sse" && (f.Format == fmtCodex || f.Format == fmtGoose || f.Format == fmtDsh || f.Format == fmtPiNative || f.Format == fmtGrok || f.Format == fmtCommandCode || f.Format == fmtZed || f.Format == fmtKiro || f.Format == fmtLiveAgent) {
		return errNoSSE
	}
	if f.Format == fmtDsh && s.Name != "" && !dshServerName.MatchString(s.Name) {
		return errDshName
	}
	if err := f.refsProblem(s); err != nil {
		return err
	}
	_, err := f.side(s)
	return err
}

// windowsPath is a program named as Windows names one: C:/…, a path with a
// backslash in it, or …/npx.cmd.
var windowsPath = lazyre.New(`^[A-Za-z]:[\\/]|\\|(?i)\.(?:exe|cmd|bat|ps1)$`)

// foreign says why an entry by the server's name that magpie can't read as
// a server is one it mustn't write over, or nil: Zed's server from an
// extension is its "settings" alone, which a command written beside it
// would take the place of, and taking the server off Zed then would take
// the extension's settings with it.
func (f *mcpFile) foreign(old map[string]any) error {
	if f.Format != fmtZed || old == nil {
		return nil
	}
	if _, ok := f.decode("", old); ok {
		return nil
	}
	return errZedExtension
}

var errZedExtension = errors.New("Zed has a server by this name from one of its extensions, which magpie leaves as it is: give the server another name to give it to Zed")

// errNoRemote is what the page says of an app that reaches only a server
// it runs itself (Claude Desktop, whose remote ones are its Connectors).
var errNoRemote = errors.New("no-remote")

// errNoSSE is what the page says of an agent that can't reach a server
// over SSE (Codex, Goose, DeepSeek Harness, Pi's own MCP, Grok Build — its
// type = "sse" is taken, but it speaks streamable HTTP to it all the same,
// Command Code, whose own add refuses sse, Zed, whose only remote transport
// POSTs to the url: crates/context_server/src/transport/http.rs, Kiro CLI,
// whose docs name no SSE at all).
var errNoSSE = errors.New("no-sse")

// ordered is a JSON object that keeps its keys in the order given, so an
// entry reads the way the agent's own would.
type ordered []kv

type kv struct {
	k string
	v any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(e.k)
		v, err := json.Marshal(e.v)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func strs(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func list(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

// MarshalYAML writes the object as a YAML mapping in the same order.
func (o ordered) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, e := range o {
		var v yaml.Node
		if err := v.Encode(e.v); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.k}, &v)
	}
	return n, nil
}

// selfTimeout is the seconds an agent waits for a tool of s: what
// magpie-image's video tool needs, at least two minutes for
// magpie-browser's, magpie-web-search's and magpie-vision's, else def.
func selfTimeout(s *Server, def int) int {
	if s.Name == selfServerName && s.Command != "" {
		return int(imagemcp.ToolTimeout().Seconds())
	}
	if (s.Name == searchServerName || s.Name == visionServerName) && s.Command != "" {
		// a search a searcher model answers, a slow page, or a look at a
		// big image takes a while
		return max(def, 120)
	}
	if s.Name == browserServerName && s.Command != "" {
		// a browser_wait waits up to a minute, and the page settles after
		return max(def, 120)
	}
	return def
}

// maxStartupTimeout is the longest startup timeout a server is given: a
// day, as a hook's timeout.
const maxStartupTimeout = 86400

// startupTheirs says whether old, a Codex entry, has a startup timeout
// magpie didn't write: a startup_timeout_ms (magpie writes only _sec, and
// Codex reads _ms only without it), or a startup_timeout_sec other than
// the one magpie wrote last. That one is the user's, and magpie's value
// never goes over it.
func startupTheirs(s *Server, old map[string]any) bool {
	if _, ok := old["startup_timeout_ms"]; ok {
		return true
	}
	v, ok := old["startup_timeout_sec"]
	return ok && (s.startupWrote == 0 || !isNumber(v, s.startupWrote))
}

// startupWritten is the startup_timeout_sec of magpie's in the entry it
// writes for s over old, for the next write to know it by: 0 for none.
func (f *mcpFile) startupWritten(s *Server, old map[string]any) float64 {
	if f.Format != fmtCodex || startupTheirs(s, old) {
		return 0
	}
	return float64(s.StartupTimeout)
}

// startupBehind says whether a Codex entry lacks the startup timeout
// magpie gives s, or has one magpie wrote that s no longer has.
func startupBehind(s *Server, old map[string]any) bool {
	if startupTheirs(s, old) {
		return false
	}
	v, ok := old["startup_timeout_sec"]
	if s.StartupTimeout == 0 {
		return ok
	}
	return !isNumber(v, float64(s.StartupTimeout))
}

// wroteStartup is s with the startup timeout magpie last wrote in its
// entry.
func wroteStartup(s *Server, v float64) *Server {
	if s.startupWrote == v {
		return s
	}
	c := *s
	c.startupWrote = v
	return &c
}

// gooseOldTimeout is the timeout magpie wrote for every Goose server before
// magpie-image was given a longer one.
const gooseOldTimeout = 300

// behind says whether an entry magpie wrote before lacks the timeout it
// gives s now: Codex's tool_timeout_sec is missing, or Goose's timeout is
// still the gooseOldTimeout every server had, or Codex's startup timeout
// isn't the one s has (startupBehind). A value the user chose is none of
// these, so it is left as it is.
func (f *mcpFile) behind(s *Server, old map[string]any) bool {
	if f.Format == fmtCodex && old != nil && startupBehind(s, old) {
		return true
	}
	// a key Codex refuses for the entry's transport stops Codex from
	// starting at all, so such an entry is written again without it
	if f.Format == fmtCodex && slices.ContainsFunc(codexRefused(s), func(k string) bool { _, ok := old[k]; return ok }) {
		return true
	}
	if selfTimeout(s, 0) == 0 {
		return false
	}
	switch f.Format {
	case fmtCodex:
		_, ok := old["tool_timeout_sec"]
		return !ok
	case fmtGoose:
		return isNumber(old["timeout"], gooseOldTimeout)
	}
	return false
}

func isNumber(v any, n float64) bool {
	switch x := v.(type) {
	case int:
		return float64(x) == n
	case int64:
		return float64(x) == n
	case uint64:
		return float64(x) == n
	case float64:
		return x == n
	}
	return false
}

// encode is the server as this agent writes it.
func (f *mcpFile) encode(s *Server) ordered {
	var o ordered
	// references to variables in the agent's own syntax (envref.go)
	r := f.refsOf()
	c := *s
	c.Headers, c.Env = mapTo(r.headers, s.Headers), mapTo(r.env, s.Env)
	s = &c
	add := func(k string, v any) { o = append(o, kv{k, v}) }
	optional := func(k string, m map[string]string) {
		if len(m) > 0 {
			add(k, m)
		}
	}
	switch f.Format {
	case fmtClaude, fmtCrush, fmtZCode:
		add("type", s.Transport)
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			add("env", strs(s.Env))
		}
	case fmtGemini:
		switch s.Transport {
		case "http":
			add("httpUrl", s.URL)
			optional("headers", s.Headers)
		case "sse":
			add("url", s.URL)
			optional("headers", s.Headers)
		default:
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtOpenCode:
		if s.Remote() {
			add("type", "remote")
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("type", "local")
			add("command", append([]string{s.Command}, s.Args...))
			optional("environment", s.Env)
		}
		add("enabled", true)
	case fmtCursor, fmtDesktop, fmtZed:
		// Zed's own Add Local Server and Add Remote Server write these keys
		// (docs/src/ai/mcp.md); its settings read a command as the Stdio
		// form and a url as the Http one, with no "source" or "type"
		// (crates/settings_content/src/project.rs
		// ContextServerSettingsContent, zed-industries/zed 2c99f547)
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtCopilot:
		if s.Remote() {
			add("type", s.Transport)
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("type", "local")
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
		add("tools", []string{"*"})
	case fmtGoose:
		add("enabled", true)
		add("name", s.Name)
		switch s.Transport {
		case "http":
			add("type", "streamable_http")
			add("uri", s.URL)
			optional("headers", s.Headers)
			// Goose fills a header's ${NAME} only from a name listed here
			if keys := gooseKeys(s.Headers, nil); len(keys) > 0 {
				add("env_keys", keys)
			}
		case "sse":
			add("type", "sse")
			add("uri", s.URL)
		default:
			// a command's variable comes through env_keys, as envs are
			// given as written
			plain, pass := codexEnv(s.Env)
			add("type", "stdio")
			add("cmd", s.Command)
			add("args", list(s.Args))
			optional("envs", plain)
			if len(pass) > 0 {
				add("env_keys", pass)
			}
		}
		add("timeout", selfTimeout(s, 300))
	case fmtPi:
		// pi-mcp-extension reads the transport from "transport",
		// pi-mcp-adapter from "httpTransport"; each ignores the other's
		if s.Remote() {
			t := "streamable-http"
			if s.Transport == "sse" {
				t = "sse"
			}
			add("transport", t)
			add("httpTransport", t)
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtPiNative:
		// "type" is optional: a url is streamable HTTP, a command stdio
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
		if s.about != "" {
			add("description", s.about)
		}
	case fmtHermes, fmtAlma:
		// Hermes' own `hermes mcp add` writes url/headers or
		// command/args/env; transport: sse is its only other transport
		// (tools/mcp_tool.py). Alma's own add writes the same keys, and
		// it reads transport "sse" alone, else tries streamable HTTP
		// first (0.4.164's out/main/index.js connectRemoteServer)
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
			if s.Transport == "sse" {
				add("transport", "sse")
			}
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtOmp:
		if s.Remote() {
			add("type", s.Transport)
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtKimi, fmtDevin:
		// as `kimi mcp add` and `devin mcp add` write them; Kimi's
		// transport is left out for a command, as its own add leaves it
		if s.Remote() {
			add("url", s.URL)
			add("transport", s.Transport)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
			if f.Format == fmtDevin {
				add("transport", "stdio")
			}
		}
	case fmtKiro:
		// as the docs show them: command/args/env, or url/headers — no
		// type, the entry's shape saying which (kiro.dev/docs/mcp/
		// configuration); an SSE server never comes this far
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtLiveAgent:
		// as LiveAgent's own MCP settings save one (K-brain's LiveServer,
		// internal/mcp/live.go): on, stdio or http, args, env and headers
		// left out when there are none
		add("enabled", true)
		if s.Remote() {
			add("transport", "http")
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("transport", "stdio")
			add("command", s.Command)
			if len(s.Args) > 0 {
				add("args", list(s.Args))
			}
			optional("env", s.Env)
		}
	case fmtCline:
		// as `cline mcp add` writes it; a url with no type would be SSE
		if s.Remote() {
			t := ordered{{"type", "streamableHttp"}, {"url", s.URL}}
			if s.Transport == "sse" {
				t[0].v = "sse"
			}
			if len(s.Headers) > 0 {
				t = append(t, kv{"headers", s.Headers})
			}
			add("transport", t)
		} else {
			t := ordered{{"type", "stdio"}, {"command", s.Command}, {"args", list(s.Args)}}
			if len(s.Env) > 0 {
				t = append(t, kv{"env", s.Env})
			}
			add("transport", t)
		}
	case fmtCommandCode:
		if s.Remote() {
			add("transport", "http")
			add("enabled", true)
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("transport", "stdio")
			add("enabled", true)
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtGrok:
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtAntigravity:
		// Antigravity tells SSE from streamable HTTP itself; "type" only
		// lets magpie read an SSE server back as one (agy keeps the key)
		if s.Remote() {
			if s.Transport == "sse" {
				add("type", "sse")
			}
			add("serverUrl", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case fmtCodex:
		// a reference is a variable Codex reads by name: Authorization's
		// bearer token, a header whole, a variable passed on to a command
		if s.Remote() {
			plain, bearer, fromEnv := codexHeaders(s.Headers)
			add("url", s.URL)
			optional("http_headers", plain)
			if bearer != "" {
				add("bearer_token_env_var", bearer)
			}
			optional("env_http_headers", fromEnv)
		} else {
			plain, pass := codexEnv(s.Env)
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", plain)
			if len(pass) > 0 {
				add("env_vars", pass)
			}
		}
		// Codex gives a tool a minute unless its entry says more
		if t := selfTimeout(s, 0); t > 0 {
			add("tool_timeout_sec", t)
		}
		// and 30s to start and list its tools unless its entry says more
		if s.StartupTimeout > 0 {
			add("startup_timeout_sec", s.StartupTimeout)
		}
	case fmtDsh:
		// a reference is a !!js expression dsh evaluates (dshRef)
		add("serverName", s.Name)
		if s.Remote() {
			add("transport", "streamable-http")
			add("url", s.URL)
			if len(s.Headers) > 0 {
				add("headers", dshRefs(s.Headers))
			}
		} else {
			add("transport", "stdio")
			add("command", s.Command)
			add("args", list(s.Args))
			if len(s.Env) > 0 {
				add("env", dshRefs(s.Env))
			}
		}
	}
	return o
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func strList(v any) []string {
	a, _ := v.([]any)
	var out []string
	for _, x := range a {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func strMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, x := range m {
		out[k] = fmt.Sprint(x)
	}
	return out
}

// decode reads one of the agent's entries; false for one magpie can't read
// as a server (a Goose builtin, say).
func (f *mcpFile) decode(name string, m map[string]any) (*Server, bool) {
	s := &Server{Name: name}
	remote := func(transport, url string, headers any) {
		s.Transport, s.URL, s.Headers = transport, url, strMap(headers)
	}
	local := func(cmd string, args any, env any) {
		s.Transport, s.Command, s.Args, s.Env = "stdio", cmd, strList(args), strMap(env)
	}
	switch f.Format {
	case fmtOpenCode:
		if str(m, "type") == "remote" {
			remote("http", str(m, "url"), m["headers"])
		} else if c := strList(m["command"]); len(c) > 0 {
			local(c[0], nil, m["environment"])
			s.Args = c[1:]
		}
	case fmtGoose:
		switch str(m, "type") {
		case "stdio":
			local(str(m, "cmd"), m["args"], m["envs"])
			// a variable Goose reads by name comes back as a reference
			for _, n := range codexEnvVars(m["env_keys"]) {
				putRef(&s.Env, n, "${"+n+"}")
			}
		case "streamable_http":
			remote("http", str(m, "uri"), m["headers"])
		case "sse":
			remote("sse", str(m, "uri"), m["headers"])
		}
	case fmtAntigravity:
		// agy reads a url where there's no serverUrl too
		u := str(m, "serverUrl")
		if u == "" {
			u = str(m, "url")
		}
		if u != "" {
			t := "http"
			if str(m, "type") == "sse" {
				t = "sse"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	case fmtCodex:
		// the variables Codex reads by name come back as references
		ref := putRef
		if u := str(m, "url"); u != "" {
			remote("http", u, m["http_headers"])
			if b := str(m, "bearer_token_env_var"); b != "" {
				ref(&s.Headers, "Authorization", "Bearer ${"+b+"}")
			}
			for k, v := range strMap(m["env_http_headers"]) {
				ref(&s.Headers, k, "${"+v+"}")
			}
		} else {
			local(str(m, "command"), m["args"], m["env"])
			for _, n := range codexEnvVars(m["env_vars"]) {
				ref(&s.Env, n, "${"+n+"}")
			}
		}
	case fmtGemini:
		if u := str(m, "httpUrl"); u != "" {
			remote("http", u, m["headers"])
		} else if u := str(m, "url"); u != "" {
			t := "sse"
			if str(m, "type") == "http" {
				t = "http"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	case fmtDsh:
		// a variable read as dshRef writes it is a reference; any other
		// value dsh works out itself (!!js) isn't one magpie can hold
		headers, env := dshUnrefs(m["headers"]), dshUnrefs(m["env"])
		for _, k := range owned[fmtDsh] {
			v := m[k]
			switch k {
			case "headers":
				v = headers
			case "env":
				v = env
			}
			if hasJS(v) {
				return nil, false
			}
		}
		switch str(m, "transport") {
		case "stdio":
			local(str(m, "command"), m["args"], env)
		case "streamable-http":
			remote("http", str(m, "url"), headers)
		}
	case fmtHermes, fmtKimi, fmtDevin, fmtAlma:
		// each takes a url over a command when an entry has both
		if u := str(m, "url"); u != "" {
			t := "http"
			if str(m, "transport") == "sse" {
				t = "sse"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	case fmtCline:
		// the flat shape Cline's extension wrote is read too, a url with
		// no type being SSE there
		t, nested := m["transport"].(map[string]any)
		if !nested {
			t = m
		}
		ty := str(t, "type")
		if ty == "" && !nested {
			ty = str(t, "transportType")
		}
		if u := str(t, "url"); u != "" {
			tr := "sse"
			if ty == "streamableHttp" || ty == "http" {
				tr = "http"
			}
			remote(tr, u, t["headers"])
		} else {
			local(str(t, "command"), t["args"], t["env"])
		}
	case fmtCommandCode:
		// Command Code reads type for transport too, and speaks
		// streamable HTTP to any url
		t := str(m, "transport")
		if t == "" {
			t = str(m, "type")
		}
		if u := str(m, "url"); u != "" && (t != "stdio" || str(m, "command") == "") {
			tr := "http"
			if t == "sse" {
				tr = "sse"
			}
			remote(tr, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	case fmtPi, fmtPiNative:
		// an entry moved from mcp-adapter.json keeps its httpTransport, so
		// an SSE server still reads as one, which Pi's own can't reach
		if u := str(m, "url"); u != "" {
			t := "http"
			if str(m, "type") == "sse" || str(m, "transport") == "sse" || str(m, "httpTransport") == "sse" {
				t = "sse"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
		// the hub entry's description is magpie's; any other is the user's
		if f.Format == fmtPiNative && name == HubEntry {
			s.about = str(m, "description")
		}
	case fmtKiro:
		// a url is streamable HTTP: the docs give no way to name SSE
		// (kiro.dev/docs/mcp/configuration), so no transport in the entry
		// is read as one
		if u := str(m, "url"); u != "" {
			remote("http", u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	case fmtLiveAgent:
		// K-brain's transport, or a url's or a command's when there is
		// none (normalizeLiveSettings); its http is streamable HTTP alone
		switch t := strings.ToLower(strings.TrimSpace(str(m, "transport"))); {
		case t == "http" || t == "" && str(m, "url") != "":
			remote("http", str(m, "url"), m["headers"])
		case t == "stdio" || t == "":
			local(str(m, "command"), m["args"], m["env"])
		}
	default: // Claude Code, Cursor, Copilot, Crush, ZCode, omp, Grok, Zed
		t := str(m, "type")
		if u := str(m, "url"); u != "" {
			if t != "sse" {
				t = "http"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	}
	if len(s.Args) == 0 {
		s.Args = nil
	}
	r := f.refsOf()
	s.Headers, s.Env = mapFrom(r.headers, s.Headers), mapFrom(r.env, s.Env)
	if s.Transport == "" || (s.Transport == "stdio" && s.Command == "") || (s.Remote() && s.URL == "") {
		return nil, false
	}
	return s, true
}

// entries is every entry under the servers' key, as the file has it: a
// copy of what was parsed when the file last changed. Codex's config.toml
// holds a [projects."<dir>"] table for every folder Codex was ever opened
// in, ten thousand of them for some, and go-toml checks each table's name
// against every one before it, so a parse of such a file was ~0.2s of CPU,
// paid by every visit to the Library (#1665).
func (f *mcpFile) entries() (map[string]map[string]any, error) {
	if f.Format == fmtDsh {
		return dshEntries(f.Path)
	}
	if f.Format == fmtLiveAgent {
		return liveAgentEntries(f.Path)
	}
	kind := fmt.Sprintf("mcp entries %d %s", f.Format, f.key())
	es, err := filememo.Read(kind, f.Path, f.parseEntries)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]any, len(es))
	for name, m := range es {
		out[name] = cloneAny(m).(map[string]any)
	}
	return out, nil
}

// entriesParsed counts parseEntries, for a test to see a file parsed once.
var entriesParsed atomic.Int64

// parseEntries is entries of the file's bytes.
func (f *mcpFile) parseEntries(raw []byte) (map[string]map[string]any, error) {
	entriesParsed.Add(1)
	out := map[string]map[string]any{}
	raw = edit.TrimBOM(raw)
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	var doc map[string]any
	var err error
	switch f.Format {
	case fmtCodex, fmtGrok:
		// only the servers' tables are read: not Codex's thousands of projects
		err = toml.Unmarshal(edit.TOMLWithout(raw, "projects"), &doc)
	case fmtGoose, fmtHermes:
		err = yaml.Unmarshal(raw, &doc)
	default:
		err = json.Unmarshal(jsonc.ToJSON(raw), &doc)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Path, err)
	}
	// the key can be a path: ZCode keeps them in mcp.servers
	var all map[string]any = doc
	for _, k := range strings.Split(f.key(), ".") {
		all, _ = all[k].(map[string]any)
	}
	v2 := openCodeServers(f.Format, all)
	for name, v := range all {
		if m, ok := v.(map[string]any); ok && (v2 == nil || name != "servers") {
			out[name] = m
		}
	}
	// OpenCode 2's own entries win a name over a V1 one, as OpenCode
	// reads them
	for name, v := range v2 {
		if m, ok := v.(map[string]any); ok {
			out[name] = m
		}
	}
	return out, nil
}

// openCodeServers is OpenCode 2's mcp.servers, where its native config and
// its own `opencode mcp add` keep the servers ("Configure MCP servers under
// `mcp.servers`", opencode 2.0.25), or nil. OpenCode 2 reads the V1 shape,
// mcp.<name>, beside it, so one file can hold both, and "servers" there is
// that object, not a server called servers: one with a type, command or
// url of its own is a V1 server by that name.
func openCodeServers(format mcpFormat, mcp map[string]any) map[string]any {
	if format != fmtOpenCode {
		return nil
	}
	m, ok := mcp["servers"].(map[string]any)
	if !ok {
		return nil
	}
	for _, k := range []string{"type", "command", "url"} {
		if _, own := m[k]; own {
			return nil
		}
	}
	return m
}

// openCodeLayout is what the OpenCode file at path holds under mcp: the
// names of its V1 entries (mcp.<name>), those of OpenCode 2's own
// (mcp.servers.<name>), and whether it has mcp.servers at all.
func openCodeLayout(path string) (v1, v2 map[string]bool, native bool, err error) {
	raw, err := edit.Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	raw = edit.TrimBOM(raw)
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil, false, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(jsonc.ToJSON(raw), &doc); err != nil {
		return nil, nil, false, fmt.Errorf("%s: %w", path, err)
	}
	all, _ := doc["mcp"].(map[string]any)
	servers := openCodeServers(fmtOpenCode, all)
	v1, v2 = map[string]bool{}, map[string]bool{}
	for name, v := range all {
		if _, ok := v.(map[string]any); ok && (servers == nil || name != "servers") {
			v1[name] = true
		}
	}
	for name, v := range servers {
		if _, ok := v.(map[string]any); ok {
			v2[name] = true
		}
	}
	return v1, v2, servers != nil, nil
}

// openCodePath is the key path of the server's entry in the OpenCode file:
// OpenCode 2's mcp.servers.<name> once the file keeps its servers there
// (the entry there already, or a new one), else mcp.<name>.
func (f *mcpFile) openCodePath(name string) (string, bool, error) {
	v1, v2, native, err := openCodeLayout(f.Path)
	if err != nil {
		return "", false, err
	}
	if native && (v2[name] || !v1[name]) {
		return "mcp.servers." + name, true, nil
	}
	return "mcp." + name, false, nil
}

// cloneAny is a deep copy of a parsed value, so a caller can change what
// entries gives without changing what the next one gets.
func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = cloneAny(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneAny(x)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(t))
		for i, x := range t {
			out[i] = cloneAny(x).(map[string]any)
		}
		return out
	}
	return v
}

// read is every server in the file, magpie's or not, by name.
func (f *mcpFile) read() (map[string]*Server, error) {
	es, err := f.entries()
	out := map[string]*Server{}
	for name, m := range es {
		if s, ok := f.decode(name, m); ok {
			out[name] = s
		}
	}
	if (f.Format == fmtPi || f.Format == fmtPiNative || f.Format == fmtDesktop) && err == nil {
		// Pi's (or Desktop's) other files, the one magpie writes first
		// winning a name
		for _, p := range append(slices.Clone(f.Also), f.Extra...) {
			more, _ := (&mcpFile{Path: p, Format: f.Format}).read()
			for name, s := range more {
				if out[name] == nil {
					out[name] = s
				}
			}
		}
	}
	return out, err
}

// also is the file of each of Also, for formats written file by file.
func (f *mcpFile) also() []*mcpFile {
	if f.Format != fmtPi && f.Format != fmtDesktop {
		return nil
	}
	var out []*mcpFile
	for _, p := range f.Also {
		out = append(out, &mcpFile{Path: p, Format: f.Format})
	}
	return out
}

// has says whether every file of Also has the server as it is.
func (f *mcpFile) has(s *Server) bool {
	for _, a := range f.also() {
		es, err := a.entries()
		if err != nil {
			continue
		}
		cur, ok := a.current(s.Name, es[s.Name], s)
		if es[s.Name] == nil || !ok || !cur.same(s) {
			return false
		}
	}
	return true
}

// holds says whether any file of Also has an entry by that name.
func (f *mcpFile) holds(name string) bool {
	for _, a := range f.also() {
		if es, err := a.entries(); err == nil && es[name] != nil {
			return true
		}
	}
	return false
}

// owned are the keys of an entry that say what the server is: magpie
// writes them. An entry's other keys — a timeout, the tools it may call,
// a note — are the user's, and kept when magpie writes the server again.
var owned = map[mcpFormat][]string{
	fmtClaude:   {"type", "url", "headers", "command", "args", "env"},
	fmtCrush:    {"type", "url", "headers", "command", "args", "env"},
	fmtGemini:   {"type", "httpUrl", "url", "headers", "command", "args", "env"},
	fmtOpenCode: {"type", "url", "headers", "command", "environment", "enabled"},
	fmtCursor:   {"type", "url", "headers", "command", "args", "env"},
	fmtCopilot:  {"type", "url", "headers", "command", "args", "env"},
	fmtGoose:    {"enabled", "name", "type", "uri", "headers", "cmd", "args", "envs"},
	// and bearer_token_env_var, env_http_headers or env_vars while the
	// server references a variable there (mine)
	fmtCodex:   {"url", "http_headers", "command", "args", "env"},
	fmtDesktop: {"url", "headers", "command", "args", "env"},
	fmtPi:      {"transport", "httpTransport", "url", "headers", "command", "args", "env"},
	// the adapter's transport keys too, dropped when magpie writes the
	// entry again
	fmtPiNative: {"type", "transport", "httpTransport", "url", "headers", "command", "args", "env"},
	fmtZCode:    {"type", "url", "headers", "command", "args", "env"},
	fmtDsh:      {"serverName", "transport", "url", "headers", "command", "args", "env"},
	// timeout, enabled, tools, sampling, auth… are the user's
	fmtHermes: {"transport", "url", "headers", "command", "args", "env"},
	fmtAlma:   {"transport", "url", "headers", "command", "args", "env"},
	fmtOmp:    {"type", "url", "headers", "command", "args", "env"},
	fmtKimi:   {"transport", "type", "url", "headers", "command", "args", "env"},
	fmtDevin:  {"transport", "type", "url", "headers", "command", "args", "env"},
	// enabled and the timeouts are the user's
	fmtGrok: {"type", "url", "headers", "command", "args", "env"},
	// the flat shape's keys go when magpie writes the entry again
	fmtCline:       {"transport", "type", "transportType", "url", "headers", "command", "args", "cwd", "env"},
	fmtCommandCode: {"transport", "type", "url", "headers", "command", "args", "env"},
	// enabled, remote, timeout and oauth are the user's
	fmtZed: {"url", "headers", "command", "args", "env"},
	// disabled, autoApprove, disabledTools and oauth are the user's; a
	// transport names nothing Kiro reads
	fmtKiro: {"transport", "url", "headers", "command", "args", "env"},
	// enabled, and the id's description, docsUrl, cwd, timeoutMs,
	// workspaceRoots, policy and auth, are the user's
	fmtLiveAgent: {"transport", "url", "headers", "command", "args", "env"},

	// a url agy read in place of serverUrl goes when magpie writes one
	fmtAntigravity: {"type", "serverUrl", "url", "headers", "command", "args", "env"},
}

// mine are the keys of the entry magpie writes for s. The keys of Codex's
// and Goose's that name a variable are magpie's only while s references
// one there (refKeys): an env_vars or env_keys the user gave a server
// whose library entry has none stays theirs.
func (f *mcpFile) mine(s *Server) []string {
	own := owned[f.Format]
	if f.Format == fmtPiNative && s.Name == HubEntry {
		own = append(slices.Clone(own), "description")
	}
	if extra := f.refKeys(s); len(extra) > 0 {
		return append(slices.Clone(own), extra...)
	}
	return own
}

// current is the agent's entry read as the server, for comparing with s:
// what decode reads, but for the variables a Codex or Goose entry names in
// keys that are the user's rather than magpie's (mine).
func (f *mcpFile) current(name string, m map[string]any, s *Server) (*Server, bool) {
	cur, ok := f.decode(name, m)
	if !ok || cur == nil || len(f.refKeys(s)) > 0 {
		return cur, ok
	}
	keep := func(got map[string]string, key string) map[string]string {
		plain, _ := m[key].(map[string]any)
		for k := range got {
			if _, set := plain[k]; !set {
				delete(got, k)
			}
		}
		if len(got) == 0 {
			return nil
		}
		return got
	}
	switch f.Format {
	case fmtCodex:
		if cur.Remote() {
			cur.Headers = keep(cur.Headers, "http_headers")
		} else {
			cur.Env = keep(cur.Env, "env")
		}
	case fmtGoose:
		if !cur.Remote() {
			cur.Env = keep(cur.Env, "envs")
		}
	}
	return cur, true
}

// merged is the entry magpie writes, with what the user added to the old
// one kept; a default magpie gives (Copilot's tools, Goose's timeout)
// yields to the user's.
func (f *mcpFile) merged(s *Server, old map[string]any) ordered {
	o := f.encode(s)
	mine := f.mine(s)
	behind := f.behind(s, old)
	// Codex's startup timeout is magpie's unless the user wrote one
	// (startupTheirs): theirs is kept as it is, below, with no _sec of
	// magpie's beside a _ms of theirs
	if f.Format == fmtCodex {
		if startupTheirs(s, old) {
			o = slices.DeleteFunc(o, func(e kv) bool { return e.k == "startup_timeout_sec" })
		} else {
			mine = append(slices.Clone(mine), "startup_timeout_sec")
		}
	}
	for i, e := range o {
		if v, ok := old[e.k]; ok && !slices.Contains(mine, e.k) && !(behind && f.Format == fmtGoose && e.k == "timeout") {
			o[i].v = v
		}
		// Codex's env_vars as the user wrote them ({ name, source }), or
		// Goose's env_keys in their order, while they name the same
		// variables
		if v, ok := old[e.k]; ok && (f.Format == fmtCodex && e.k == "env_vars" || f.Format == fmtGoose && e.k == "env_keys") {
			if want, _ := e.v.([]string); slices.Equal(slices.Sorted(slices.Values(codexEnvVars(v))), want) {
				o[i].v = v
			}
		}
	}
	keys := slices.Sorted(maps.Keys(old))
	var refused []string
	if f.Format == fmtCodex {
		refused = codexRefused(s)
	}
	for _, k := range keys {
		if slices.Contains(mine, k) || slices.ContainsFunc(o, func(e kv) bool { return e.k == k }) || slices.Contains(refused, k) {
			continue
		}
		o = append(o, kv{k, old[k]})
	}
	return o
}

// codexRefused are the keys of the other transport, which Codex refuses
// in an entry of s's: a command's beside a URL, a URL's beside a command.
// Codex then won't load its config at all ("env_vars is not supported for
// streamable_http"), so such a key, left from when the server was the
// other kind, goes when magpie writes the entry; the user's keys of s's
// own transport stay (codex-rs/config/src/mcp_types.rs, TryFrom
// RawMcpServerConfig). bearer_token, which Codex refuses for both, is the
// user's to take out.
func codexRefused(s *Server) []string {
	if s.Remote() {
		return []string{"args", "env", "env_vars", "cwd"}
	}
	return []string{"url", "bearer_token_env_var", "http_headers", "env_http_headers", "http_headers_helper", "oauth", "oauth_resource", "auth"}
}

// put writes the server into the file, in place of one by its name.
func (f *mcpFile) put(s *Server, old map[string]any) error {
	return f.putEntry(s, f.merged(s, old))
}

// putEntry writes the entry o for the server into the file.
func (f *mcpFile) putEntry(s *Server, o ordered) error {
	switch f.Format {
	case fmtCodex, fmtGrok:
		return putCodex(f.Path, s.Name, o)
	case fmtGoose:
		m := map[string]any{}
		for _, e := range o {
			m[e.k] = e.v
		}
		return edit.SetYAML(f.Path, edit.KV{Path: "extensions." + s.Name, Value: m})
	case fmtHermes:
		// in its order, beside the user's other settings and comments
		return edit.SetYAML(f.Path, edit.KV{Path: f.key() + "." + s.Name, Value: o})
	case fmtDsh:
		for _, p := range f.files() {
			if err := dshPut(p, s.Name, o); err != nil {
				return err
			}
		}
		return nil
	case fmtLiveAgent:
		return liveAgentPut(f.Path, s.Name, o)
	}
	// Claude Code saves the file under its own lock: read and written
	// back under it, or a save of its own between the two is lost (#4747)
	release, err := f.claudeLock()
	if err != nil {
		return err
	}
	defer release()
	// Pi's files, or Desktop's, all of them or none
	return edit.Atomically(func() error {
		for _, a := range f.also() {
			es, err := a.entries()
			if err != nil {
				return err
			}
			if err := a.put(s, es[s.Name]); err != nil {
				return err
			}
		}
		key := f.key() + "." + s.Name
		if f.Format == fmtOpenCode {
			p, native, err := f.openCodePath(s.Name)
			if err != nil {
				return err
			}
			// OpenCode 2's servers have no enabled: on unless disabled,
			// which is the user's to set
			if key = p; native {
				o = slices.DeleteFunc(slices.Clone(o), func(e kv) bool { return e.k == "enabled" })
			}
		}
		return edit.SetJSON(f.Path, edit.KV{Path: key, Value: o})
	}, f.files()...)
}

// del takes the server by that name out of the file.
func (f *mcpFile) del(name string) error {
	switch f.Format {
	case fmtDsh:
		for _, p := range f.files() {
			if err := dshDel(p, name); err != nil {
				return err
			}
		}
		return nil
	case fmtCodex, fmtGrok:
		return delCodex(f.Path, name, true)
	case fmtLiveAgent:
		return liveAgentDel(f.Path, name)
	case fmtGoose:
		return edit.DelYAML(f.Path, "extensions."+name)
	case fmtHermes:
		return edit.DelYAML(f.Path, f.key()+"."+name)
	}
	// Claude Code saves the file under its own lock: read and written
	// back under it, or a save of its own between the two is lost (#4747)
	release, err := f.claudeLock()
	if err != nil {
		return err
	}
	defer release()
	return edit.Atomically(func() error {
		for _, a := range f.also() {
			if err := a.del(name); err != nil {
				return err
			}
		}
		if f.Format == fmtOpenCode {
			// from wherever OpenCode reads it: mcp.<name>, OpenCode 2's
			// mcp.servers.<name>, or both, and never mcp.servers itself
			v1, v2, _, err := openCodeLayout(f.Path)
			if err != nil {
				return err
			}
			var paths []string
			if v1[name] {
				paths = append(paths, "mcp."+name)
			}
			if v2[name] {
				paths = append(paths, "mcp.servers."+name)
			}
			return edit.DelJSON(f.Path, paths...)
		}
		return edit.DelJSON(f.Path, f.key()+"."+name)
	}, f.files()...)
}

// claudeLock takes the lock Claude Code saves its .claude.json under,
// when the file f writes is that one (claudeJSON): held while it is read
// and written back, or a save of its own between the two is lost
// (#4747). No other file of the library's has a lock of Claude Code's to
// take, and a distro's copy isn't this machine's file: the Claude Code
// there saves under a lock in the distro's own files.
func (f *mcpFile) claudeLock() (func(), error) {
	if f.Format != fmtClaude || filepath.Clean(f.Path) != filepath.Clean(claudeJSON()) {
		return func() {}, nil
	}
	return claudelock.Config(f.Path)
}

// ---- Codex's TOML ---------------------------------------------------------

// delCodex takes out the server's table and the tables under it ([…env]),
// or only those under it, which putCodex writes inline instead.
// Include child array tables (such as env_vars), which would otherwise
// implicitly recreate the removed server.
// Each is one edit of the file: a step that fails puts it back as it was.
func delCodex(path, name string, self bool) error {
	return edit.Atomically(func() error {
		table := "mcp_servers." + name
		if err := edit.SetTOMLTables(path, []string{table + "."}, nil); err != nil {
			return err
		}
		if !self {
			return nil
		}
		return edit.DelTOMLTable(path, table)
	}, path)
}

func putCodex(path, name string, o ordered) error {
	return edit.Atomically(func() error {
		if err := delCodex(path, name, false); err != nil {
			return err
		}
		var kvs []edit.KV
		for _, e := range o {
			kvs = append(kvs, edit.KV{Path: tomlKey(e.k), Value: edit.Raw(tomlValue(e.v))})
		}
		return edit.SetTOMLTable(path, "mcp_servers."+name, kvs...)
	}, path)
}

func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlKey(k string) string {
	if k == "" {
		return `""`
	}
	for _, r := range k {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return tomlString(k)
		}
	}
	return k
}

func tomlValue(v any) string {
	switch x := v.(type) {
	case string:
		return tomlString(x)
	case bool, int, int64:
		return fmt.Sprint(x)
	case float64:
		return tomlFloat(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return fmt.Sprint(x)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = tomlString(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = tomlValue(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]string:
		m := map[string]any{}
		for k, s := range x {
			m[k] = s
		}
		return tomlValue(m)
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = tomlKey(k) + " = " + tomlValue(x[k])
		}
		if len(parts) == 0 {
			return "{}"
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	return tomlString(fmt.Sprint(v))
}

// tomlFloat writes a float as one, so a user's tool_timeout_sec = 120.0
// stays a float and doesn't come back as an integer.
func tomlFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// ---- found in agents ------------------------------------------------------

// Found is a server an agent has that the library doesn't: the agents
// that have it as it is, and those that have another by that name.
type Found struct {
	Server *Server  `json:"server"`
	Others []string `json:"others,omitempty"`
	Icon   string   `json:"icon,omitempty"`
	// Own: the agent's app puts it there itself, each time it starts
	// (Codex's node_repl and cua_repl): it isn't one to bring in, and
	// taking it out doesn't last.
	Own bool `json:"own,omitempty"`
}

// appOwned is whether a server runs from inside the agent's own app
// install — its Mac app bundle, its Microsoft Store app, the Codex app's
// runtimes — as the servers an agent's app writes into its config itself
// do. Another app's server in the agent's config (Netcatty's, #2635) is
// one the user added there: it is brought in like any other.
func appOwned(s *Server, a *agent.Agent) bool {
	if s.Remote() || a == nil {
		return false
	}
	cmd := strings.ToLower(strings.ReplaceAll(s.Command, `\`, "/"))
	if a.ID == "codex" && (strings.Contains(cmd, "/cua_node/") || strings.Contains(cmd, "/openai/codex/runtimes/")) {
		return true
	}
	names := []string{strings.ToLower(a.ID), strings.ToLower(a.Name)}
	if id, _, ok := strings.Cut(names[0], "-"); ok {
		names = append(names, id)
	}
	if n, _, ok := strings.Cut(names[1], " "); ok {
		names = append(names, n)
	}
	segs := strings.Split(cmd, "/")
	for i, seg := range segs {
		// an app is a bundle (Codex.app) or a Store package
		// (WindowsApps/OpenAI.Codex_1.0_x64) the command is inside
		app := strings.HasSuffix(seg, ".app") && i+1 < len(segs) && segs[i+1] == "contents"
		app = app || (i > 0 && segs[i-1] == "windowsapps" && i+1 < len(segs))
		if !app {
			continue
		}
		for _, n := range names {
			if n != "" && strings.Contains(seg, n) {
				return true
			}
		}
	}
	return false
}

func foundServers(l *Library) []Found { return foundServersOf(l, Targets()) }

// foundServersOf is foundServers among these agents.
func foundServersOf(l *Library, targets []*Target) []Found {
	byName := map[string]*Found{}
	var names []string
	for _, t := range targets {
		if t.MCP == nil {
			continue
		}
		have, err := t.MCP.read()
		if err != nil {
			continue
		}
		mine := l.applied(t.Agent.ID).MCP
		for name, s := range have {
			if slices.Contains(mine, name) || l.server(name) != nil || !nameRe.MatchString(name) {
				continue
			}
			f := byName[name]
			switch {
			case f == nil:
				s.Agents = []string{t.Agent.ID}
				byName[name] = &Found{Server: s, Own: appOwned(s, t.Agent)}
				names = append(names, name)
			case f.Server.same(s):
				f.Server.Agents = append(f.Server.Agents, t.Agent.ID)
			default:
				f.Others = append(f.Others, t.Agent.ID)
			}
		}
	}
	sort.Strings(names)
	out := []Found{}
	for _, n := range names {
		out = append(out, *byName[n])
	}
	return out
}

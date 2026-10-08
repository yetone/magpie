package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/imagemcp"
)

// Server is one MCP server: a command magpie's agents start, or a URL they
// reach.
type Server struct {
	Name string `json:"name"`
	// Transport is stdio for a command, http (streamable) or sse for a URL.
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Agents    []string          `json:"agents"`
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
		s.URL, s.Headers = "", nil
	case "http", "sse":
		if !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
			return fmt.Errorf("%s: the URL has to start with http:// or https://", s.Name)
		}
		s.Command, s.Args, s.Env = "", nil, nil
	default:
		return fmt.Errorf("%s: unknown transport %q", s.Name, s.Transport)
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

// same reports whether two definitions start the same server; which agents
// have it doesn't matter.
func (s *Server) same(o *Server) bool {
	return s.Transport == o.Transport && s.Command == o.Command && slices.Equal(s.Args, o.Args) &&
		maps.Equal(s.Env, o.Env) && s.URL == o.URL && maps.Equal(s.Headers, o.Headers)
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
	}
	return "mcpServers"
}

// supports says why the agent can't reach a server, or nil when it can.
func (f *mcpFile) supports(s *Server) error {
	if f.Format == fmtDesktop && s.Remote() {
		return errNoRemote
	}
	if s.Transport == "sse" && (f.Format == fmtCodex || f.Format == fmtGoose || f.Format == fmtDsh || f.Format == fmtPiNative || f.Format == fmtGrok || f.Format == fmtCommandCode) {
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
var windowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]|\\|(?i)\.(?:exe|cmd|bat|ps1)$`)

// errNoRemote is what the page says of an app that reaches only a server
// it runs itself (Claude Desktop, whose remote ones are its Connectors).
var errNoRemote = errors.New("no-remote")

// errNoSSE is what the page says of an agent that can't reach a server
// over SSE (Codex, Goose, DeepSeek Harness, Pi's own MCP, Grok Build — its
// type = "sse" is taken, but it speaks streamable HTTP to it all the same,
// Command Code, whose own add refuses sse).
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
// magpie-image's video tool needs, else def.
func selfTimeout(s *Server, def int) int {
	if s.Name == selfServerName && s.Command != "" {
		return int(imagemcp.ToolTimeout().Seconds())
	}
	return def
}

// gooseOldTimeout is the timeout magpie wrote for every Goose server before
// magpie-image was given a longer one.
const gooseOldTimeout = 300

// behind says whether an entry magpie wrote before lacks the timeout it
// gives s now: Codex's tool_timeout_sec is missing, or Goose's timeout is
// still the gooseOldTimeout every server had. A value the user chose is
// neither, so it is left as it is.
func (f *mcpFile) behind(s *Server, old map[string]any) bool {
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
	case fmtCursor, fmtDesktop:
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
	case fmtHermes:
		// Hermes' own `hermes mcp add` writes url/headers or
		// command/args/env; transport: sse is its only other transport
		// (tools/mcp_tool.py)
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
	case fmtDsh:
		add("serverName", s.Name)
		if s.Remote() {
			add("transport", "streamable-http")
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("transport", "stdio")
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
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
		// a value dsh works out itself (!!js) isn't one magpie can hold
		for _, k := range owned[fmtDsh] {
			if hasJS(m[k]) {
				return nil, false
			}
		}
		switch str(m, "transport") {
		case "stdio":
			local(str(m, "command"), m["args"], m["env"])
		case "streamable-http":
			remote("http", str(m, "url"), m["headers"])
		}
	case fmtHermes, fmtKimi, fmtDevin:
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
	default: // Claude Code, Cursor, Copilot, Crush, ZCode, omp, Grok
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

// entries is every entry under the servers' key, as the file has it.
func (f *mcpFile) entries() (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	if f.Format == fmtDsh {
		return dshEntries(f.Path)
	}
	raw, err := edit.Read(f.Path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return out, err
	}
	var doc map[string]any
	switch f.Format {
	case fmtCodex, fmtGrok:
		err = toml.Unmarshal(raw, &doc)
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
	for name, v := range all {
		if m, ok := v.(map[string]any); ok {
			out[name] = m
		}
	}
	return out, nil
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
	fmtOmp:    {"type", "url", "headers", "command", "args", "env"},
	fmtKimi:   {"transport", "type", "url", "headers", "command", "args", "env"},
	fmtDevin:  {"transport", "type", "url", "headers", "command", "args", "env"},
	// enabled and the timeouts are the user's
	fmtGrok: {"type", "url", "headers", "command", "args", "env"},
	// the flat shape's keys go when magpie writes the entry again
	fmtCline:       {"transport", "type", "transportType", "url", "headers", "command", "args", "cwd", "env"},
	fmtCommandCode: {"transport", "type", "url", "headers", "command", "args", "env"},

	// a url agy read in place of serverUrl goes when magpie writes one
	fmtAntigravity: {"type", "serverUrl", "url", "headers", "command", "args", "env"},
}

// mine are the keys of the entry magpie writes for s. The keys of Codex's
// and Goose's that name a variable are magpie's only while s references
// one there (refKeys): an env_vars or env_keys the user gave a server
// whose library entry has none stays theirs.
func (f *mcpFile) mine(s *Server) []string {
	if extra := f.refKeys(s); len(extra) > 0 {
		return append(slices.Clone(owned[f.Format]), extra...)
	}
	return owned[f.Format]
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
	for _, k := range keys {
		if slices.Contains(mine, k) || slices.ContainsFunc(o, func(e kv) bool { return e.k == k }) {
			continue
		}
		o = append(o, kv{k, old[k]})
	}
	return o
}

// put writes the server into the file, in place of one by its name.
func (f *mcpFile) put(s *Server, old map[string]any) error {
	o := f.merged(s, old)
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
	}
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
		return edit.SetJSON(f.Path, edit.KV{Path: f.key() + "." + s.Name, Value: o})
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
	case fmtGoose:
		return edit.DelYAML(f.Path, "extensions."+name)
	case fmtHermes:
		return edit.DelYAML(f.Path, f.key()+"."+name)
	}
	return edit.Atomically(func() error {
		for _, a := range f.also() {
			if err := a.del(name); err != nil {
				return err
			}
		}
		return edit.DelJSON(f.Path, f.key()+"."+name)
	}, f.files()...)
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

// appOwned is whether a server runs from inside an app's own install — a
// Mac app bundle, a Microsoft Store app, the Codex app's runtimes — as the
// servers an agent's app writes into its config itself do.
func appOwned(s *Server) bool {
	if s.Remote() {
		return false
	}
	cmd := strings.ToLower(strings.ReplaceAll(s.Command, `\`, "/"))
	for _, in := range []string{".app/contents/", "/windowsapps/", "/cua_node/", "/openai/codex/runtimes/"} {
		if strings.Contains(cmd, in) {
			return true
		}
	}
	return false
}

func foundServers(l *Library) []Found {
	byName := map[string]*Found{}
	var names []string
	for _, t := range Targets() {
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
				byName[name] = &Found{Server: s, Own: appOwned(s)}
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

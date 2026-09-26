package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
	"gopkg.in/yaml.v3"

	"github.com/yetone/magpie/internal/edit"
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
)

// mcpFile is the file an agent keeps its user-wide MCP servers in.
type mcpFile struct {
	Path   string
	Format mcpFormat
}

// key is the object the servers are kept under.
func (f *mcpFile) key() string {
	switch f.Format {
	case fmtCodex:
		return "mcp_servers"
	case fmtOpenCode, fmtCrush:
		return "mcp"
	case fmtGoose:
		return "extensions"
	}
	return "mcpServers"
}

// supports says why the agent can't reach a server, or nil when it can.
func (f *mcpFile) supports(s *Server) error {
	if f.Format == fmtDesktop && s.Remote() {
		return errNoRemote
	}
	if s.Transport == "sse" && (f.Format == fmtCodex || f.Format == fmtGoose) {
		return errNoSSE
	}
	return nil
}

// errNoRemote is what the page says of an app that reaches only a server
// it runs itself (Claude Desktop, whose remote ones are its Connectors).
var errNoRemote = errors.New("no-remote")

// errNoSSE is what the page says of an agent that can't reach a server
// over SSE (Codex, Goose).
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

// encode is the server as this agent writes it.
func (f *mcpFile) encode(s *Server) ordered {
	var o ordered
	add := func(k string, v any) { o = append(o, kv{k, v}) }
	optional := func(k string, m map[string]string) {
		if len(m) > 0 {
			add(k, m)
		}
	}
	switch f.Format {
	case fmtClaude, fmtCrush:
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
		case "sse":
			add("type", "sse")
			add("uri", s.URL)
		default:
			add("type", "stdio")
			add("cmd", s.Command)
			add("args", list(s.Args))
			optional("envs", s.Env)
		}
		add("timeout", 300)
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
	case fmtCodex:
		if s.Remote() {
			add("url", s.URL)
			optional("http_headers", s.Headers)
		} else {
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
		case "streamable_http":
			remote("http", str(m, "uri"), m["headers"])
		case "sse":
			remote("sse", str(m, "uri"), m["headers"])
		}
	case fmtCodex:
		if u := str(m, "url"); u != "" {
			remote("http", u, m["http_headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
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
	case fmtPi:
		if u := str(m, "url"); u != "" {
			t := "http"
			if str(m, "transport") == "sse" || str(m, "httpTransport") == "sse" {
				t = "sse"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	default: // Claude Code, Cursor, Copilot, Crush
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
	if s.Transport == "" || (s.Transport == "stdio" && s.Command == "") || (s.Remote() && s.URL == "") {
		return nil, false
	}
	return s, true
}

// entries is every entry under the servers' key, as the file has it.
func (f *mcpFile) entries() (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	raw, err := edit.Read(f.Path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return out, err
	}
	var doc map[string]any
	switch f.Format {
	case fmtCodex:
		err = toml.Unmarshal(raw, &doc)
	case fmtGoose:
		err = yaml.Unmarshal(raw, &doc)
	default:
		err = json.Unmarshal(jsonc.ToJSON(raw), &doc)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Path, err)
	}
	all, _ := doc[f.key()].(map[string]any)
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
	return out, err
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
	fmtCodex:    {"url", "http_headers", "command", "args", "env"},
	fmtDesktop:  {"url", "headers", "command", "args", "env"},
	fmtPi:       {"transport", "httpTransport", "url", "headers", "command", "args", "env"},
}

// merged is the entry magpie writes, with what the user added to the old
// one kept; a default magpie gives (Copilot's tools, Goose's timeout)
// yields to the user's.
func (f *mcpFile) merged(s *Server, old map[string]any) ordered {
	o := f.encode(s)
	mine := owned[f.Format]
	for i, e := range o {
		if v, ok := old[e.k]; ok && !slices.Contains(mine, e.k) {
			o[i].v = v
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
	case fmtCodex:
		return putCodex(f.Path, s.Name, o)
	case fmtGoose:
		m := map[string]any{}
		for _, e := range o {
			m[e.k] = e.v
		}
		return edit.SetYAML(f.Path, edit.KV{Path: "extensions." + s.Name, Value: m})
	}
	return edit.SetJSON(f.Path, edit.KV{Path: f.key() + "." + s.Name, Value: o})
}

// del takes the server by that name out of the file.
func (f *mcpFile) del(name string) error {
	switch f.Format {
	case fmtCodex:
		return delCodex(f.Path, name, true)
	case fmtGoose:
		return edit.DelYAML(f.Path, "extensions."+name)
	}
	return edit.DelJSON(f.Path, f.key()+"."+name)
}

// ---- Codex's TOML ---------------------------------------------------------

// delCodex takes out the server's table and the tables under it ([…env]),
// or only those under it, which putCodex writes inline instead.
// Include child array tables (such as env_vars), which would otherwise
// implicitly recreate the removed server.
func delCodex(path, name string, self bool) error {
	table := "mcp_servers." + name
	if err := edit.SetTOMLTables(path, []string{table + "."}, nil); err != nil {
		return err
	}
	if !self {
		return nil
	}
	return edit.DelTOMLTable(path, table)
}

func putCodex(path, name string, o ordered) error {
	if err := delCodex(path, name, false); err != nil {
		return err
	}
	var kvs []edit.KV
	for _, e := range o {
		kvs = append(kvs, edit.KV{Path: tomlKey(e.k), Value: edit.Raw(tomlValue(e.v))})
	}
	return edit.SetTOMLTable(path, "mcp_servers."+name, kvs...)
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
	case bool, int, int64, float64:
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

// ---- found in agents ------------------------------------------------------

// Found is a server an agent has that the library doesn't: the agents
// that have it as it is, and those that have another by that name.
type Found struct {
	Server *Server  `json:"server"`
	Others []string `json:"others,omitempty"`
	Icon   string   `json:"icon,omitempty"`
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
				byName[name] = &Found{Server: s}
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

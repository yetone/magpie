package cli

// magpie tools and magpie tool: the tools magpie's own MCP servers give
// agents (image generation, image recognition, web search, plugins'
// tools), listed and called from the terminal (iwinux on Discord). They
// are asked of the running gateway's MCP servers themselves
// (gateway.OwnMCPServers), so the tools, their parameters and what they
// do are the ones an agent gets, and nothing else is started (#3318).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/imagemcp"
	"github.com/yetone/magpie/internal/lazyre"
)

const toolUsage = `usage:
  magpie tools                  every tool magpie's MCP servers give agents, with its parameters
  magpie tools <name>           one tool: what it does and each parameter
  magpie tool <name> [--<param> <value>…] [<words>…] [-o <file|folder>] [--json]
                                call one through the running magpie, as an agent's MCP call is

  A parameter is --<param> <value> or --<param>=<value> (- and _ alike); a list
  parameter is given once per item. The words left over are the tool's one required
  text (generate_image's prompt, web_search's query, analyze_image's image), else its
  prompt. A file named in a parameter is read by magpie on this computer.
  An image or video the tool makes is saved: -o names a file or a folder (with --n 2 or
  more a number is added to the name), else it goes in this folder, named from the
  prompt; a file that is there is never written over. Text is printed. --json prints
  the tool's answer as it came and saves nothing.
  An unknown parameter, or a missing required one, is refused before anything is
  called. The models are the ones magpie's Settings pick (Images › Image generation,
  Image recognition, Web search); generate_image and generate_video take --model
  provider/model for another one set up (magpie tools generate_image lists them). It asks the gateway at the port in Settings' Gateway
  address (or MAGPIE_ADDR; MAGPIE_GATEWAY=<url> for another).

  e.g. magpie tool generate_image "a magpie on a branch, watercolour" --size 1536x1024 -o hero.png
       magpie tool generate_image --n 2 --quality high --background transparent "an app icon"
       magpie tool analyze_image ./shot.png --prompt "what does the error say?"
       magpie tool web_search "go 1.27 release notes"`

// toolAgent is the User-Agent the calls go with: magpie's own, as the
// image tool's (gateway.DrawAgent), so a vendor is sent magpie's.
const toolAgent = "magpie-tool/1"

// toolCallMost bounds one ask of the gateway: the image server answers
// within about 20s, a plugin's tool within 55s, and a look at a large
// image or a slow page takes longer.
var toolCallMost = 10 * time.Minute

// toolWaitMost is how long a drawing or video still being made is waited
// on, and toolPoll the pause between asks after it.
var (
	toolWaitMost = 30 * time.Minute
	toolPoll     = time.Second
)

// toolGateway is the running gateway, asked as an agent's MCP client asks.
type toolGateway struct {
	base   string
	client *http.Client
	next   int
}

func newToolGateway() *toolGateway {
	base := strings.TrimRight(os.Getenv("MAGPIE_GATEWAY"), "/")
	if base == "" {
		base = gateway.ClientURL()
	}
	return &toolGateway{base: base, client: &http.Client{Timeout: toolCallMost}}
}

// errNoMagpie is a gateway that doesn't answer at all.
type errNoMagpie struct {
	base string
	err  error
}

func (e errNoMagpie) Error() string {
	return fmt.Sprintf("magpie isn't running at %s: open the app or run magpie serve, then try again (%v)", e.base, e.err)
}

func (g *toolGateway) do(method, path string, body []byte) (*http.Response, error) {
	r, err := http.NewRequest(method, g.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
	}
	r.Header.Set("Authorization", "Bearer "+gateway.Token)
	r.Header.Set("User-Agent", toolAgent)
	res, err := g.client.Do(r)
	if err != nil {
		return nil, errNoMagpie{g.base, err}
	}
	return res, nil
}

// rpc posts one JSON-RPC request to the MCP server at path: its result,
// or what went wrong.
func (g *toolGateway) rpc(path, method string, params any) (json.RawMessage, error) {
	g.next++
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": g.next, "method": method, "params": params})
	res, err := g.do(http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 512<<20))
	if err != nil {
		return nil, fmt.Errorf("the answer from %s was cut off: %w", g.base, err)
	}
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("the magpie at %s has no %s: it is older than this one, update it", g.base, path)
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("the magpie at %s answered %s: %s", g.base, res.Status, strings.TrimSpace(string(data)))
	}
	var m struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("what %s answered isn't JSON-RPC: is it magpie? (%v)", g.base, err)
	}
	if m.Error != nil {
		return nil, errors.New(m.Error.Message)
	}
	return m.Result, nil
}

// fetch is a file the gateway made, at u's path on the gateway asked: a
// URL may name the address agents are given (MAGPIE_PUBLIC_URL), and the
// key goes nowhere else.
func (g *toolGateway) fetch(u string) ([]byte, error) {
	pu, err := url.Parse(u)
	if err != nil || !strings.HasPrefix(pu.Path, "/") {
		return nil, fmt.Errorf("%q isn't a file of magpie's", u)
	}
	res, err := g.do(http.MethodGet, pu.RequestURI(), nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<30))
	if err != nil {
		return nil, fmt.Errorf("downloading %s was cut off: %w", pu.Path, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s %s", pu.Path, res.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// toolParam is a parameter's JSON Schema, as much of it as the terminal
// needs.
type toolParam struct {
	Type        any        `json:"type"`
	Description string     `json:"description"`
	Items       *toolParam `json:"items"`
	Enum        []any      `json:"enum"`
	Minimum     *float64   `json:"minimum"`
	Maximum     *float64   `json:"maximum"`
}

// kind is the parameter's type: the first of a list that isn't null.
func (p toolParam) kind() string {
	switch t := p.Type.(type) {
	case string:
		return t
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

// listedTool is one tool of one of the gateway's MCP servers.
type listedTool struct {
	Server      string `json:"server"`
	path        string
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Properties map[string]toolParam `json:"properties"`
		Required   []string             `json:"required"`
	} `json:"inputSchema"`
}

// params are the tool's parameter names, the required first, each part in
// name order.
func (t listedTool) params() []string {
	var names []string
	for k := range t.InputSchema.Properties {
		names = append(names, k)
	}
	slices.SortFunc(names, func(a, b string) int {
		ra, rb := t.required(a), t.required(b)
		if ra != rb {
			if ra {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return names
}

func (t listedTool) required(name string) bool { return slices.Contains(t.InputSchema.Required, name) }

// wordsParam is the parameter the words left over are: the one required
// text, else prompt; "" when the tool has neither.
func (t listedTool) wordsParam() string {
	var texts []string
	for _, r := range t.InputSchema.Required {
		if p, ok := t.InputSchema.Properties[r]; ok && p.kind() == "string" {
			texts = append(texts, r)
		}
	}
	if len(texts) == 1 {
		return texts[0]
	}
	if p, ok := t.InputSchema.Properties["prompt"]; ok && p.kind() == "string" {
		return "prompt"
	}
	return ""
}

// listTools asks each of the gateway's MCP servers for its tools; a server
// that can't say is noted, a gateway that doesn't answer is the error.
func listTools(g *toolGateway) ([]listedTool, []string, error) {
	var out []listedTool
	var notes []string
	for _, srv := range gateway.OwnMCPServers {
		res, err := g.rpc(srv.Path, "tools/list", map[string]any{})
		if err != nil {
			var no errNoMagpie
			if errors.As(err, &no) {
				return nil, nil, err
			}
			notes = append(notes, srv.Name+": "+err.Error())
			continue
		}
		var l struct {
			Tools []listedTool `json:"tools"`
		}
		if err := json.Unmarshal(res, &l); err != nil {
			notes = append(notes, srv.Name+": its tool list isn't one: "+err.Error())
			continue
		}
		for _, t := range l.Tools {
			t.Server, t.path = srv.Name, srv.Path
			out = append(out, t)
		}
	}
	return out, notes, nil
}

// findTool is the tool called name: the first server's of those that have
// one (gateway.OwnMCPServers' order).
func findTool(tools []listedTool, name string) (listedTool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return listedTool{}, false
}

// firstSentence is a description's first sentence, on one line.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if r := []rune(s); len(r) > 140 {
		s = string(r[:139]) + "…"
	}
	return s
}

// paramShape is how a parameter's value is written: <text>, <1-4>, …
func paramShape(p toolParam) string {
	var shape string
	switch p.kind() {
	case "integer", "number":
		shape = "number"
		if p.kind() == "integer" {
			shape = "whole number"
		}
		if p.Minimum != nil && p.Maximum != nil {
			shape = strconv.FormatFloat(*p.Minimum, 'f', -1, 64) + "-" + strconv.FormatFloat(*p.Maximum, 'f', -1, 64)
		}
	case "boolean":
		shape = "true|false"
	case "array":
		item := toolParam{Type: "string"}
		if p.Items != nil {
			item = *p.Items
		}
		return paramShape(item) + "…"
	case "object":
		shape = "json"
	default:
		shape = "text"
	}
	if len(p.Enum) > 0 {
		var vs []string
		for _, v := range p.Enum {
			vs = append(vs, fmt.Sprint(v))
		}
		shape = strings.Join(vs, "|")
	}
	return "<" + shape + ">"
}

// toolsCmd: magpie tools [<name>]
func toolsCmd(args []string) error {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("unknown flag %s (magpie tools [<name>])", a)
		}
	}
	if len(args) > 1 {
		return errors.New("magpie tools takes one tool's name at most (magpie tools [<name>])")
	}
	g := newToolGateway()
	tools, notes, err := listTools(g)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		t, ok := findTool(tools, args[0])
		if !ok {
			return unknownTool(args[0], tools, notes)
		}
		fmt.Print(toolHelp(t))
		return nil
	}
	server := ""
	for _, t := range tools {
		if t.Server != server {
			if server != "" {
				fmt.Println()
			}
			server = t.Server
			fmt.Println(bold.Render(server))
		}
		fmt.Printf("  %-20s %s\n", t.Name, firstSentence(t.Description))
		var ps []string
		for _, n := range t.params() {
			p := "--" + n + " " + paramShape(t.InputSchema.Properties[n])
			if t.required(n) {
				p += " (required)"
			}
			ps = append(ps, p)
		}
		if len(ps) > 0 {
			fmt.Printf("  %-20s %s\n", "", muted.Render(strings.Join(ps, "  ")))
		}
	}
	if len(tools) == 0 {
		fmt.Println(muted.Render("  no tools: the magpie running has none on"))
	}
	for _, n := range notes {
		fmt.Println(muted.Render("  " + n))
	}
	fmt.Println(muted.Render("\nmagpie tools <name> for one in full · magpie tool <name> --<param> <value> … to call it"))
	return nil
}

// toolHelp is one tool in full.
func toolHelp(t listedTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", bold.Render(t.Name), muted.Render("("+t.Server+", "+t.path+")"))
	fmt.Fprintf(&b, "  %s\n\n", strings.Join(strings.Fields(t.Description), " "))
	for _, n := range t.params() {
		p := t.InputSchema.Properties[n]
		head := "--" + n + " " + paramShape(p)
		if t.required(n) {
			head += "  required"
		}
		fmt.Fprintf(&b, "  %s\n", head)
		if d := strings.Join(strings.Fields(p.Description), " "); d != "" {
			fmt.Fprintf(&b, "      %s\n", muted.Render(d))
		}
	}
	if w := t.wordsParam(); w != "" {
		fmt.Fprintf(&b, "\n  the words after the name are its --%s: magpie tool %s <%s>\n", w, t.Name, w)
	}
	return b.String()
}

func unknownTool(name string, tools []listedTool, notes []string) error {
	var names []string
	for _, t := range tools {
		names = append(names, t.Name)
	}
	msg := fmt.Sprintf("no tool called %q: nothing was called; there is %s (magpie tools)", name, strings.Join(names, ", "))
	if len(names) == 0 {
		msg = fmt.Sprintf("no tool called %q: the magpie running has no tools on", name)
	}
	if len(notes) > 0 {
		msg += " (" + strings.Join(notes, "; ") + ")"
	}
	return errors.New(msg)
}

// toolCall is a call as the command line asks it.
type toolCall struct {
	args   map[string]any
	output string
	json   bool
}

// parseToolArgs reads in, what follows magpie tool <name>, against t's
// parameters: nothing is called on an error.
func parseToolArgs(t listedTool, in []string) (toolCall, error) {
	c := toolCall{args: map[string]any{}}
	props := t.InputSchema.Properties
	var words []string
	for i := 0; i < len(in); i++ {
		a := in[i]
		if a == "--" {
			words = append(words, in[i+1:]...)
			break
		}
		switch a {
		case "--json":
			c.json = true
			continue
		case "-o", "--output":
			if i+1 >= len(in) || in[i+1] == "" {
				return c, fmt.Errorf("%s needs a file or a folder: nothing was called", a)
			}
			c.output = in[i+1]
			i++
			continue
		}
		if v, ok := strings.CutPrefix(a, "--output="); ok {
			if v == "" {
				return c, errors.New("--output needs a file or a folder: nothing was called")
			}
			c.output = v
			continue
		}
		if !strings.HasPrefix(a, "--") {
			if len(a) > 1 && a[0] == '-' {
				return c, fmt.Errorf("unknown flag %s: nothing was called (words that begin with - go after --)", a)
			}
			words = append(words, a)
			continue
		}
		name, val, hasVal := strings.Cut(a[2:], "=")
		key := paramKey(props, name)
		if key == "" {
			return c, fmt.Errorf("%s takes no --%s: nothing was called; it takes %s (magpie tools %s)", t.Name, name, paramList(t), t.Name)
		}
		p := props[key]
		if !hasVal {
			if p.kind() == "boolean" {
				val = "true"
				if i+1 < len(in) && (in[i+1] == "true" || in[i+1] == "false") {
					val = in[i+1]
					i++
				}
			} else {
				if i+1 >= len(in) {
					return c, fmt.Errorf("--%s needs a value: nothing was called", name)
				}
				val = in[i+1]
				i++
			}
		}
		if p.kind() == "array" {
			item := toolParam{Type: "string", Description: p.Description}
			if p.Items != nil {
				item = *p.Items
				if item.Description == "" {
					item.Description = p.Description
				}
			}
			v, err := paramValue(key, item, val)
			if err != nil {
				return c, err
			}
			list, _ := c.args[key].([]any)
			c.args[key] = append(list, v)
			continue
		}
		if _, dup := c.args[key]; dup {
			return c, fmt.Errorf("--%s is given twice: nothing was called", key)
		}
		v, err := paramValue(key, p, val)
		if err != nil {
			return c, err
		}
		c.args[key] = v
	}
	if len(words) > 0 {
		w := t.wordsParam()
		if w == "" {
			return c, fmt.Errorf("%s takes no words, only --<param> <value>: nothing was called; it takes %s", t.Name, paramList(t))
		}
		if _, dup := c.args[w]; dup {
			return c, fmt.Errorf("--%s is given, and the words %q too: nothing was called", w, strings.Join(words, " "))
		}
		v, err := paramValue(w, props[w], strings.Join(words, " "))
		if err != nil {
			return c, err
		}
		c.args[w] = v
	}
	var missing []string
	for _, r := range t.InputSchema.Required {
		if _, ok := c.args[r]; !ok {
			missing = append(missing, "--"+r)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("%s needs %s: nothing was called (magpie tools %s)", t.Name, strings.Join(missing, " and "), t.Name)
	}
	return c, nil
}

// paramKey is the parameter name names, - and _ alike; "" for none.
func paramKey(props map[string]toolParam, name string) string {
	if _, ok := props[name]; ok {
		return name
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "-", "_") }
	for k := range props {
		if norm(k) == norm(name) {
			return k
		}
	}
	return ""
}

func paramList(t listedTool) string {
	var ps []string
	for _, n := range t.params() {
		ps = append(ps, "--"+n)
	}
	if len(ps) == 0 {
		return "nothing"
	}
	return strings.Join(ps, ", ")
}

// paramValue is val as p's type takes it.
func paramValue(name string, p toolParam, val string) (any, error) {
	var v any
	switch p.kind() {
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s takes a whole number, not %q: nothing was called", name, val)
		}
		v = n
	case "number":
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			return nil, fmt.Errorf("--%s takes a number, not %q: nothing was called", name, val)
		}
		v = f
	case "boolean":
		b, err := strconv.ParseBool(strings.TrimSpace(val))
		if err != nil {
			return nil, fmt.Errorf("--%s takes true or false, not %q: nothing was called", name, val)
		}
		v = b
	case "object", "array":
		if err := json.Unmarshal([]byte(val), &v); err != nil {
			return nil, fmt.Errorf("--%s takes JSON, not %q: nothing was called", name, val)
		}
	default:
		v = localFile(p, val)
	}
	if len(p.Enum) > 0 && !slices.ContainsFunc(p.Enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
		return nil, fmt.Errorf("--%s takes %s, not %q: nothing was called", name, paramShape(toolParam{Enum: p.Enum}), val)
	}
	return v, nil
}

// localFile is val made absolute when p takes a file and val names one
// here, relative to this folder: the gateway doesn't know it.
func localFile(p toolParam, val string) string {
	d := strings.ToLower(p.Description)
	if !strings.Contains(d, "file") || !strings.Contains(d, "path") || val == "" || filepath.IsAbs(val) || strings.Contains(val, "://") || strings.HasPrefix(strings.ToLower(val), "data:") {
		return val
	}
	if st, err := os.Stat(val); err != nil || st.IsDir() {
		return val
	}
	if abs, err := filepath.Abs(val); err == nil {
		return abs
	}
	return val
}

// toolAnswer is a tools/call result.
type toolAnswer struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	} `json:"content"`
	IsError bool                       `json:"isError"`
	Meta    map[string]json.RawMessage `json:"_meta"`
}

func (a toolAnswer) text() string {
	var b strings.Builder
	for _, c := range a.Content {
		if c.Type == "text" && c.Text != "" {
			b.WriteString(strings.TrimRight(c.Text, "\n") + "\n")
		}
	}
	return b.String()
}

// stillMaking is the id an answer says to wait on with generation_result;
// a gateway older than PendingMeta says it in its text alone.
var stillMakingText = lazyre.New(gateway.GenerationResultTool + ` with id "([^"]+)"`)

func (a toolAnswer) pending() string {
	var id string
	if raw, ok := a.Meta[gateway.PendingMeta]; ok && json.Unmarshal(raw, &id) == nil {
		return id
	}
	if a.IsError {
		return ""
	}
	if m := stillMakingText.FindStringSubmatch(a.text()); m != nil {
		return m[1]
	}
	return ""
}

// callTool calls t with args and waits for what it makes: the answer.
func callTool(g *toolGateway, t listedTool, args map[string]any) (json.RawMessage, error) {
	res, err := g.rpc(t.path, "tools/call", map[string]any{"name": t.Name, "arguments": args})
	deadline := time.Now().Add(toolWaitMost)
	for err == nil {
		var a toolAnswer
		json.Unmarshal(res, &a)
		id := a.pending()
		if id == "" || time.Now().After(deadline) {
			break
		}
		fmt.Fprintln(os.Stderr, muted.Render("still being made ("+id+"), waiting…"))
		time.Sleep(toolPoll)
		res, err = g.rpc(t.path, "tools/call", map[string]any{"name": gateway.GenerationResultTool, "arguments": map[string]any{"id": id}})
	}
	return res, err
}

// toolCmd: magpie tool <name> [--<param> <value>…] [<words>…] [-o <path>] [--json]
func toolCmd(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("magpie tool needs a tool's name first: magpie tool <name> --<param> <value> … (magpie tools lists them)")
	}
	g := newToolGateway()
	tools, notes, err := listTools(g)
	if err != nil {
		return err
	}
	t, ok := findTool(tools, args[0])
	if !ok {
		return unknownTool(args[0], tools, notes)
	}
	c, err := parseToolArgs(t, args[1:])
	if err != nil {
		return err
	}
	res, err := callTool(g, t, c.args)
	if err != nil {
		return err
	}
	if c.json {
		var out bytes.Buffer
		if json.Indent(&out, res, "", "  ") != nil {
			out.Reset()
			out.Write(res)
		}
		fmt.Println(out.String())
		var a toolAnswer
		json.Unmarshal(res, &a)
		if a.IsError {
			return errors.New(t.Name + " answered with an error")
		}
		return nil
	}
	var a toolAnswer
	if err := json.Unmarshal(res, &a); err != nil {
		return fmt.Errorf("%s's answer isn't one: %v", t.Name, err)
	}
	if a.IsError {
		return errors.New(strings.TrimSpace(a.text()))
	}
	fmt.Print(a.text())
	prompt, _ := c.args["prompt"].(string)
	if prompt == "" {
		prompt = t.Name
	}
	saved, err := saveMade(g, a, c.output, prompt)
	for _, p := range saved {
		fmt.Printf("%s %s\n", green.Render("saved"), p)
	}
	return err
}

// made is one file an answer brings: its bytes, or where to get them.
type made struct {
	data []byte
	url  string
}

// saveMade saves the images and videos answer a brings, at output (a file
// or a folder) or in this folder: the paths saved.
func saveMade(g *toolGateway, a toolAnswer, output, prompt string) ([]string, error) {
	var inline [][]byte
	for _, c := range a.Content {
		if c.Type != "image" && c.Type != "video" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(c.Data)
		if err != nil {
			return nil, fmt.Errorf("an image came back broken: %v", err)
		}
		inline = append(inline, b)
	}
	var files []gateway.MadeFile
	if raw, ok := a.Meta[gateway.FilesMeta]; ok {
		json.Unmarshal(raw, &files)
	}
	var all []made
	if len(files) == 0 {
		for _, b := range inline {
			all = append(all, made{data: b})
		}
	}
	for _, f := range files {
		if f.Inline && len(inline) > 0 {
			all = append(all, made{data: inline[0]})
			inline = inline[1:]
			continue
		}
		all = append(all, made{url: f.URL})
	}
	if len(all) == 0 {
		return nil, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	at := output
	if at == "" {
		at = "."
	}
	var saved []string
	for i, m := range all {
		if m.data == nil {
			if m.data, err = g.fetch(m.url); err != nil {
				return saved, err
			}
		}
		path, err := imagemcp.Target(dir, at, prompt, madeExt(m.data), i, len(all))
		if err != nil {
			return saved, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return saved, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return saved, err
		}
		_, werr := f.Write(m.data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(path)
			return saved, werr
		}
		saved = append(saved, path)
	}
	return saved, nil
}

// madeExt is the extension of what data is.
func madeExt(data []byte) string {
	switch ct := http.DetectContentType(data); {
	case ct == "image/jpeg":
		return ".jpg"
	case ct == "image/webp":
		return ".webp"
	case ct == "image/gif":
		return ".gif"
	case strings.HasPrefix(ct, "video/webm"):
		return ".webm"
	case strings.HasPrefix(ct, "video/"):
		return ".mp4"
	}
	return ".png"
}

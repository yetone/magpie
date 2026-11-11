package provider

// PLUGIN-SERVED (see AGENTS.md): Grok ("grok") is a retired built-in
// subscription, served by its plugin alone,
// @magpie-community/opencode-grok-auth (provider.BuiltinRetired). Its
// sign-in, models, requests and usage are the plugin's; what is left here
// reads the accounts signed in to the built-in for the move onto the plugin
// (migrate_side.go), installs and finds the Grok CLI the plugin signs in
// with, and shapes the plugin's request bodies (grokBody). A fix belongs in
// the plugin (github.com/magpie-community/plugins, packages/grok), with the
// mover's min raised in internal/provider/migrate_side.go.

// A Grok subscription (SuperGrok, X Premium+) is served straight from the
// backend xAI's Grok Build CLI talks to, OpenAI's Responses API at
// cli-chat-proxy.grok.com, with the CLI's sign-in. Here is who the CLI is
// signed in to, the models it offers, the sign-in, which is the CLI's own
// `login`, and the requests' signing.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
)

// GrokExecutable finds the Grok Build CLI; a var so tests can fake it.
var GrokExecutable = func() string {
	home, _ := os.UserHomeDir()
	path := append(filepath.SplitList(os.Getenv("PATH")), registryPath()...)
	for _, c := range grokCandidates(runtime.GOOS, home, GrokHome(), appdir.Getenv("GROK_BIN_DIR"), path) {
		if isFile(c.path) && (c.own || isGrokBuild(c.path)) {
			return c.path
		}
	}
	return ""
}

// grokCandidate is a place the CLI may be; own when only its installer
// puts a grok there.
type grokCandidate struct {
	path string
	own  bool
}

// grokCandidates are the places to look for the CLI, first to last: the
// installer's bin (GROK_BIN_DIR, else ~/.grok/bin), each folder on PATH,
// then ~/.local/bin. On Windows it is grok.exe — looking for a bare grok
// there missed ~/.grok/bin, and a grok.cmd npm put on PATH first hid it
// (#180). path is the process' PATH and, on Windows, the registry's, which
// has what was installed since magpie started.
func grokCandidates(goos, home, grokHome, binDir string, path []string) []grokCandidate {
	name := "grok"
	if goos == "windows" {
		name = "grok.exe"
	}
	var out []grokCandidate
	seen := map[string]bool{}
	add := func(dir string, own bool) {
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		p := filepath.Join(dir, name)
		k := p
		if goos == "windows" {
			k = strings.ToLower(p)
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, grokCandidate{p, own})
		}
	}
	add(binDir, true)
	add(filepath.Join(grokHome, "bin"), true)
	if home != "" {
		// the installer puts it under ~/.grok whatever GROK_HOME says
		add(filepath.Join(home, ".grok", "bin"), true)
	}
	for _, d := range path {
		add(strings.TrimSpace(d), false)
	}
	if home != "" {
		add(filepath.Join(home, ".local", "bin"), false)
	}
	return out
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// isGrokBuild tells xAI's grok from any other program by that name: its
// installer keeps it under the grok home.
func isGrokBuild(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Windows can't resolve some real paths (a subst drive, a folder
		// OneDrive keeps); the path itself still says where it is
		real = path
	}
	return strings.Contains(strings.ToLower(filepath.ToSlash(real)), "/.grok/")
}

// GrokHome is where the CLI keeps its sign-in and settings.
func GrokHome() string {
	if h := appdir.Getenv("GROK_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".grok")
}

// grokCredential is the CLI's sign-in, as its auth.json keeps it.
type grokCredential struct {
	Key       string    `json:"key"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
	Issuer    string    `json:"oidc_issuer"`
}

// readGrokCredential reads the sign-in the CLI keeps in its home. magpie
// only reads it: the CLI refreshes it with a token that changes each time.
func readGrokCredential(home string) (grokCredential, bool) {
	var all map[string]grokCredential
	if !readJSON(filepath.Join(home, "auth.json"), &all) {
		return grokCredential{}, false
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if c := all[k]; c.Key != "" {
			return c, true
		}
	}
	return grokCredential{}, false
}

// grokTools are the tool types Grok's backend takes; it turns the whole
// request away over another, as over Codex's freeform apply_patch (custom)
// or a namespace, which groups its sub-agent tools. A namespace's functions
// go as functions of their own (grokFlat).
var grokTools = map[string]bool{"function": true, "web_search": true, "x_search": true, "image_generation": true,
	"collections_search": true, "file_search": true, "code_execution": true, "code_interpreter": true,
	"mcp": true, "shell": true, "tool_search": true}

// grokBody leaves out the tools Grok's backend doesn't take, as a backend
// magpie translates for goes without them: Codex, without apply_patch,
// edits files through its shell. Codex's web_search says whether it may
// reach the live web, which Grok's doesn't take either; it searches live.
// And Codex hands reasoning back with "content": null, which the backend
// can't read the encrypted reasoning beside ("Could not decode the
// compaction blob"), so a null content goes. A namespace's functions, as
// collaboration's spawn_agent, go flat (collaboration__spawn_agent), and so
// do the calls to them handed back and a tool_choice naming one (#404).
// A tool_choice with no tools
// left goes too: the backend turns the request away over it ("A
// tool_choice was set on the request but no tools were specified"), as it
// would Codex's compaction summary, sent without tools (#378). A Codex
// agent_message (a subagent's task, or its reply in the lead's history),
// which the backend turns away with 422 "unknown item type", goes as the
// user's message, its sender and recipient said first (grokAgentMessage).
func grokBody(body []byte) []byte { return grokShaped(body, false) }

// grokPluginBody is grokBody for the Grok plugin, but the tool_choice the
// plugin reads stays: a lone "none" (a request that turned tools off says
// so to the plugin, which reads it before adding a tool of its own
// (x_search) and leaves a lone tool_choice out before the request goes to
// Grok; @magpie-community/opencode-grok-auth 0.1.18, #3110), and an object
// of a type Grok's backend doesn't take, an allowed_tools naming the
// client's own tools among them, which the plugin reads the same way and
// leaves out before Grok (#3153).
func grokPluginBody(body []byte) []byte { return grokShaped(body, true) }

func grokShaped(body []byte, forPlugin bool) []byte {
	if !bytes.Contains(body, []byte(`"tools"`)) && !bytes.Contains(body, []byte(`"reasoning"`)) && !bytes.Contains(body, []byte(`"tool_choice"`)) && !bytes.Contains(body, []byte(`"namespace"`)) && !bytes.Contains(body, []byte(`"agent_message"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	dirty := false
	if tools, ok := m["tools"].([]any); ok {
		kept := tools[:0:0]
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				ty, _ := tm["type"].(string)
				if ty == "namespace" {
					dirty = true
					kept = append(kept, grokFlat(tm)...)
					continue
				}
				if !grokTools[ty] {
					dirty = true
					continue
				}
				if _, ok := tm["external_web_access"]; ok {
					delete(tm, "external_web_access")
					dirty = true
				}
			}
			kept = append(kept, t)
		}
		for _, t := range kept {
			if tm, ok := t.(map[string]any); ok && tm["type"] == "function" && objectRoot(tm) {
				dirty = true
			}
		}
		m["tools"] = kept
		if tc, ok := m["tool_choice"].(map[string]any); ok {
			if flatCall(tc) {
				dirty = true
			}
			// a type the backend turns the request away over goes for the
			// plugin to read, as the lone "none" does: it leaves an
			// unsupported one out before Grok instead of adding x_search
			// over it (#3153). The built-in is Grok's last stop.
			if ty, _ := tc["type"].(string); !grokTools[ty] && !forPlugin {
				delete(m, "tool_choice")
				dirty = true
			}
		}
	}
	if tools, _ := m["tools"].([]any); len(tools) == 0 && !(forPlugin && m["tool_choice"] == "none") {
		if _, ok := m["tool_choice"]; ok {
			delete(m, "tool_choice")
			dirty = true
		}
	}
	input, _ := m["input"].([]any)
	for i, it := range input {
		if msg, ok := grokAgentMessage(it); ok {
			input[i], it = msg, msg
			dirty = true
		}
		if im, ok := it.(map[string]any); ok && flatCall(im) {
			dirty = true
		}
		if im, ok := it.(map[string]any); ok && im["type"] == "reasoning" {
			if c, ok := im["content"]; ok && c == nil {
				delete(im, "content")
				dirty = true
			}
		}
	}
	if !dirty {
		return body
	}
	b, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return b
}

// grokAgentMessage is a plain Codex agent_message as the user's message
// Grok's backend takes: "From <author> to <recipient>" first, then its text
// and images as they came, in its place among the input. One sealed
// (encrypted_content on it or on a part), empty, or with a part of another
// kind is left as it is: magpie reads no sealed task, and the gateway's
// guard for one (hasSealedAgentMessage) still sees it. The plugin does the
// same (@magpie-community/opencode-grok-auth 0.1.11, plugins#61).
func grokAgentMessage(it any) (map[string]any, bool) {
	im, ok := it.(map[string]any)
	if !ok || im["type"] != "agent_message" {
		return nil, false
	}
	if _, sealed := im["encrypted_content"]; sealed {
		return nil, false
	}
	parts, _ := im["content"].([]any)
	if len(parts) == 0 {
		return nil, false
	}
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, sealed := pm["encrypted_content"]; sealed {
			return nil, false
		}
		_, isText := pm["text"].(string)
		_, isURL := pm["image_url"].(string)
		if !(pm["type"] == "input_text" && isText || pm["type"] == "input_image" && isURL) {
			return nil, false
		}
	}
	from, _ := im["author"].(string)
	to, _ := im["recipient"].(string)
	header := ""
	switch {
	case from != "" && to != "":
		header = "From " + from + " to " + to
	case from != "":
		header = "From " + from
	case to != "":
		header = "To " + to
	}
	content := parts
	if header != "" {
		content = append([]any{map[string]any{"type": "input_text", "text": header + "\n\n"}}, parts...)
	}
	return map[string]any{"type": "message", "role": "user", "content": content}, true
}

// liteNamespace is the namespace Codex's Responses Lite groups its own
// functions in, which Codex reads as none at all.
const liteNamespace = "functions"

// FlatName is the name a namespaced tool is offered to a model under, which
// takes one flat name: namespace__name, as Codex names an MCP server's tools.
// A name longer than the 64 characters APIs allow is cut and made unique by
// a hash of the whole.
func FlatName(namespace, name string) string {
	flat := namespace + "__" + name
	if len(flat) <= 64 {
		return flat
	}
	sum := sha256.Sum256([]byte(namespace + "\x00" + name))
	return flat[:55] + "_" + hex.EncodeToString(sum[:4])
}

// grokFlat is a namespace's functions, each under its flat name; what else
// it holds, a freeform tool, Grok's backend wouldn't take either.
func grokFlat(ns map[string]any) []any {
	space, _ := ns["name"].(string)
	nested, _ := ns["tools"].([]any)
	var out []any
	for _, n := range nested {
		nm, _ := n.(map[string]any)
		name, _ := nm["name"].(string)
		if nm == nil || nm["type"] != "function" || name == "" {
			continue
		}
		if space != "" && space != liteNamespace {
			nm["name"] = FlatName(space, name)
		}
		out = append(out, nm)
	}
	return out
}

// objectRoot makes a function's parameters an object at the root, which
// Grok's backend wants ("tool parameter root must be an object type"):
// Codex's codex_app automation_update takes an anyOf of objects (Fate on
// Discord). It reports whether it changed anything.
func objectRoot(fn map[string]any) bool {
	ps, _ := fn["parameters"].(map[string]any)
	return ps != nil && ObjectRoot(ps)
}

// ObjectRoot makes a tool's input schema a plain object at the root, with
// no anyOf, oneOf or allOf there: Grok's backend wants one, and so do
// Anthropic's models behind Factory ("input_schema does not support
// oneOf, allOf, or anyOf at the top level", #646). allOf's branches are
// all merged, properties and required alike. Of anyOf's and oneOf's object
// branches the properties are merged, a field each of them requires stays
// required, and the other branches go. A branch that is a union itself, as
// zod writes a discriminated union inside another (Codex desktop's
// automation_update, #1271), is folded the same way first, so its fields
// are kept; a field the branches give different schemas takes any of them.
// It reports whether it changed anything.
func ObjectRoot(ps map[string]any) bool {
	return objectRootIn(ps, ps, 0)
}

// objectRootIn is ObjectRoot on ps, a schema whose local $refs name root's
// $defs; depth bounds how far unions inside unions are followed.
func objectRootIn(root, ps map[string]any, depth int) bool {
	_, any1 := ps["anyOf"]
	_, one := ps["oneOf"]
	_, all := ps["allOf"]
	if ps["type"] == "object" && !any1 && !one && !all {
		return false
	}
	props, _ := ps["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	var required []any
	if r, ok := ps["required"].([]any); ok {
		required = r
	}
	list, _ := ps["allOf"].([]any)
	for _, b := range list {
		bm, _ := b.(map[string]any)
		if bm = grokRef(root, bm); bm == nil {
			continue
		}
		bp, _ := bm["properties"].(map[string]any)
		for k, v := range bp {
			if _, ok := props[k]; !ok {
				props[k] = v
			}
		}
		br, _ := bm["required"].([]any)
		required = append(required, br...)
	}
	delete(ps, "allOf")
	var branches []map[string]any
	for _, k := range []string{"anyOf", "oneOf"} {
		list, _ := ps[k].([]any)
		for _, b := range list {
			bm, _ := b.(map[string]any)
			if bm = grokRef(root, bm); bm == nil {
				continue
			}
			if nestedUnion(bm) && depth < 8 {
				// folded on a copy: the branch may be a $def others name
				bm = maps.Clone(bm)
				objectRootIn(root, bm, depth+1)
			}
			if _, has := bm["properties"]; bm["type"] != "object" && !has {
				continue
			}
			branches = append(branches, bm)
		}
		delete(ps, k)
	}
	// a field every branch requires stays required, beside the root's and
	// allOf's own, which no branch can drop
	var common []any
	// the schemas the branches give a field the root doesn't define
	from := map[string][]any{}
	var order []string
	for i, b := range branches {
		bp, _ := b["properties"].(map[string]any)
		for _, k := range slices.Sorted(maps.Keys(bp)) {
			if _, own := props[k]; own {
				continue
			}
			if _, seen := from[k]; !seen {
				order = append(order, k)
			}
			from[k] = appendDistinct(from[k], bp[k])
		}
		br, _ := b["required"].([]any)
		if i == 0 {
			common = append(common, br...)
			continue
		}
		in := map[any]bool{}
		for _, r := range br {
			in[r] = true
		}
		kept := common[:0]
		for _, r := range common {
			if in[r] {
				kept = append(kept, r)
			}
		}
		common = kept
	}
	for _, k := range order {
		if vs := from[k]; len(vs) == 1 {
			props[k] = vs[0]
		} else {
			props[k] = map[string]any{"anyOf": vs}
		}
	}
	seen := map[string]bool{}
	var merged []any
	for _, r := range append(required, common...) {
		if name, ok := r.(string); ok {
			if seen[name] {
				continue
			}
			seen[name] = true
		}
		merged = append(merged, r)
	}
	required = merged
	ps["type"] = "object"
	ps["properties"] = props
	if len(required) > 0 {
		ps["required"] = required
	} else {
		delete(ps, "required")
	}
	return true
}

// nestedUnion reports whether a branch of a union is a union itself.
func nestedUnion(b map[string]any) bool {
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if _, ok := b[k]; ok {
			return true
		}
	}
	return false
}

// appendDistinct adds v to vs unless one there is the same schema.
func appendDistinct(vs []any, v any) []any {
	vb, _ := json.Marshal(v)
	for _, w := range vs {
		if wb, _ := json.Marshal(w); bytes.Equal(vb, wb) {
			return vs
		}
	}
	return append(vs, v)
}

// grokRef is a branch of a schema, or what its local $ref names in the
// schema's $defs or definitions.
func grokRef(root, b map[string]any) map[string]any {
	ref, _ := b["$ref"].(string)
	if ref == "" {
		return b
	}
	for _, k := range []string{"$defs", "definitions"} {
		if name, ok := strings.CutPrefix(ref, "#/"+k+"/"); ok {
			defs, _ := root[k].(map[string]any)
			d, _ := defs[name].(map[string]any)
			return d
		}
	}
	return nil
}

// flatCall names a call to a namespaced tool, or a tool_choice of one, by
// the flat name it was offered under, and reports whether it was one.
func flatCall(it map[string]any) bool {
	space, ok := it["namespace"].(string)
	if !ok {
		return false
	}
	delete(it, "namespace")
	if name, _ := it["name"].(string); name != "" && space != "" && space != liteNamespace {
		it["name"] = FlatName(space, name)
	}
	return true
}

// agentProbe is agentCommand for asking the CLI something (proc.ProbeContext).
func agentProbe(ctx context.Context, path string, args ...string) *exec.Cmd {
	return withProxy(proc.ProbeContext(ctx, path, args...))
}

func withProxy(cmd *exec.Cmd) *exec.Cmd {
	cmd.Env = netproxy.Env(nil)
	return cmd
}

// runCLISignIn runs an agent's own login command, hands the first link it
// prints to the window, and finishes when the command does and identity
// says who is signed in. using says whether the agent now uses that
// account; failed, when there is one, undoes what a sign-in that did not
// finish left behind. whole, when there is one, says a link has all it
// needs: one that hasn't is the start of a link the CLI wrapped, and the
// lines after it that are nothing but more of it are joined on.
func runCLISignIn(s *signInFlow, what string, env []string, using bool, failed func(), identity func() (user, plan string, ok bool), whole func(link string) bool, path string, args ...string) error {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := proc.CommandContext(ctx, path, args...)
	cmd.Dir, _ = os.UserHomeDir()
	cmd.Env = netproxy.EnvWith(s.proxy, env)
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	s.mu.Lock()
	s.stop = cancel
	s.mu.Unlock()
	got := make(chan string, 1)
	var partial struct {
		sync.Mutex
		link string
		last string // the last line it printed, which says why there's no link
	}
	noLink := func() error {
		partial.Lock()
		defer partial.Unlock()
		if partial.last != "" {
			return fmt.Errorf("%s gave no link to open: %s", what, partial.last)
		}
		return fmt.Errorf("%s gave no link to open", what)
	}
	go func() {
		rd := bufio.NewReader(out)
		sent := false
		link := ""
		send := func() {
			if !sent && link != "" {
				sent = true
				got <- link
			}
		}
		var tail []string
		for {
			raw, rerr := rd.ReadString('\n')
			line := ansi.ReplaceAllString(strings.TrimRight(raw, "\r\n"), "")
			switch {
			case sent:
			case link == "":
				if !failedLine.MatchString(line) {
					link = cursorLoginURL.FindString(line)
				}
			case linkRest.MatchString(strings.TrimSpace(line)) && (!whole(link) || queryRest.MatchString(strings.TrimSpace(line))):
				// a whole link takes only more of its query, not "Waiting..." printed after it
				link += strings.TrimSpace(line)
			default:
				send() // what came after it isn't more of it
			}
			// a whole link is handed on once what came with it is read,
			// so the rest of one wrapped after its last param joins too
			if whole == nil || whole(link) && rd.Buffered() == 0 {
				send()
			}
			if !sent {
				partial.Lock()
				partial.link = link
				partial.Unlock()
			}
			if strings.TrimSpace(line) != "" {
				tail = append(tail, strings.TrimSpace(line))
				partial.Lock()
				partial.last = strings.TrimSpace(line)
				partial.Unlock()
			}
			if rerr != nil {
				break
			}
		}
		send()
		if !sent {
			close(got)
		}
		err := cmd.Wait()
		cancel()
		forgetAccountCaches()
		if err == nil {
			// a second account's identity is its keeping (addGrokLogin):
			// not once the sign-in is canceled
			var user, plan string
			if s.keep(func() error {
				var ok bool
				if user, plan, ok = identity(); !ok {
					return fmt.Errorf("%s finished without an account", what)
				}
				return nil
			}) == nil {
				s.finish(SignInState{State: "done", User: user, Plan: plan, Using: using})
				return
			}
		}
		if failed != nil {
			failed()
		}
		msg := what + " didn't finish"
		if n := len(tail); n > 0 {
			msg = tail[n-1]
		}
		s.finish(SignInState{State: "failed", Error: msg})
	}()
	var u string
	select {
	case l, ok := <-got:
		if !ok {
			return noLink()
		}
		u = l
	case <-time.After(linkWait):
		// a link that never came whole is still the one there is
		partial.Lock()
		u = partial.link
		partial.Unlock()
		if u == "" {
			cancel()
			return noLink()
		}
	}
	s.mu.Lock()
	s.st.URL = u
	s.mu.Unlock()
	return nil
}

// failedLine is a line saying the login failed, whose URL is the endpoint
// it couldn't reach, not a page to open: grok 1.0.46 behind a proxy it
// can't get through prints "Error: error sending request for url
// (https://auth.x.ai/oauth2/device/code): …" (𝕏 on Discord).
var failedLine = lazyre.New(`(?i)\berror\b`)

// linkWait is how long a login command has to print its link.
var linkWait = 30 * time.Second

// linkRest is a line that is nothing but more of a link: no spaces, only
// what a URL holds.
var linkRest = lazyre.New(`^[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+$`)

// queryRest is what may still follow a link that already has its challenge
// and uuid: the rest of a value, or more params.
var queryRest = lazyre.New(`^[A-Za-z0-9\-_%&=]+$`)

// AgentUser is who an agent is signed in to itself, as its own files say,
// for an agent whose vendor a plugin serves too; "" when not known.
func AgentUser(agent string) string {
	if agent == "grok" {
		u, _ := GrokUser(GrokHome())
		return u
	}
	return ""
}

// GrokUser is who the grok with this home is signed in to.
func GrokUser(home string) (string, bool) {
	c, ok := readGrokCredential(home)
	return c.Email, ok
}

// GrokFlatBody is a Responses body with Codex's namespaced functions flat
// (namespace__name) and every function's parameters an object at the root,
// the two things Grok's backend turns a request away over wherever it is
// served (Copilot's grok-4.7, #4374), and nothing else: tool types, reasoning
// and the rest go as they came. The model's call to a flat name is given its
// namespace back by the gateway (namespacedIn).
func GrokFlatBody(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"namespace"`)) && !bytes.Contains(body, []byte(`"anyOf"`)) && !bytes.Contains(body, []byte(`"oneOf"`)) && !bytes.Contains(body, []byte(`"allOf"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	dirty := false
	if tools, ok := m["tools"].([]any); ok {
		var out []any
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			switch {
			case tm != nil && tm["type"] == "namespace":
				dirty = true
				for _, f := range grokFlat(tm) {
					if fm, ok := f.(map[string]any); ok {
						objectRoot(fm)
					}
					out = append(out, f)
				}
			case tm != nil && tm["type"] == "function":
				if objectRoot(tm) {
					dirty = true
				}
				out = append(out, t)
			default:
				out = append(out, t)
			}
		}
		m["tools"] = out
	}
	if tc, ok := m["tool_choice"].(map[string]any); ok && flatCall(tc) {
		dirty = true
	}
	input, _ := m["input"].([]any)
	for _, it := range input {
		if im, ok := it.(map[string]any); ok && flatCall(im) {
			dirty = true
		}
	}
	if !dirty {
		return body
	}
	b, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return b
}

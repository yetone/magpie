package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/proc"
)

// A ChatGPT account's requests are made the way Codex CLI makes them, as
// Alma's Codex plugin does: the backend serves Codex's clients, and gates
// its newest models on a User-Agent that names one — without it
// /codex/responses answers 404 "Model not found" for a model its own
// /codex/models lists.

// codexSign authenticates a request to the ChatGPT backend with the
// tokens token hands out, and says it comes from Codex CLI.
func codexSign(token func(context.Context) (tok, accountID string, err error)) func(context.Context, *http.Request, []byte) error {
	return func(ctx context.Context, req *http.Request, body []byte) error {
		tok, accountID, err := token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if accountID != "" {
			req.Header.Set("chatgpt-account-id", accountID)
		}
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		// the version and the User-Agent name one Codex, never older than
		// the client whose request this is (its version header, relayed)
		ver := newerVersion(codexVersion(), req.Header.Get("version"))
		req.Header.Set("version", ver)
		codexClientHeaders(req.Header, ver)
		if body == nil {
			return nil
		}
		req.Header.Set("Accept", "text/event-stream")
		sessionHeaders(req.Header, body)
		return nil
	}
}

// codexClientHeaders are what every request Codex CLI makes carries,
// whatever it is for: its originator and its User-Agent, naming Codex of
// version ver (codex-rs login/src/auth/default_client.rs default_headers,
// which its token refresh, its /wham reads and its model requests are all
// sent with). The ChatGPT account's own calls that aren't a model's
// (/wham/usage, the reset credits, the token refresh) went with Go's
// User-Agent and no originator, unlike any request Codex makes.
func codexClientHeaders(h http.Header, ver string) {
	h.Set("originator", "codex_cli_rs")
	h.Set("User-Agent", codexUserAgent(ver))
}

// codexAccountHeaders are the headers of a call Codex makes about the
// ChatGPT account (its /wham reads): the account it is about and
// codexClientHeaders. Codex sends no version header with them.
func codexAccountHeaders(accountID string) map[string]string {
	h := http.Header{}
	codexClientHeaders(h, codexVersion())
	m := map[string]string{"chatgpt-account-id": accountID}
	for k := range h {
		m[k] = h.Get(k)
	}
	return m
}

// sessionHeaders names the conversation body belongs to (its
// prompt_cache_key) in the headers Codex CLI 0.162 sends it in, session-id
// and thread-id. The ChatGPT backend finds a conversation's cached prompt
// by them, not by the body's key: on a real account a request with tools
// and the key alone was cached 0 tokens six times in a row, and 7552 of
// 7659 from the second on with the headers (#1473). The underscored names
// older Codex sent go no more, since a proxy may drop a header named with
// an underscore. A body without a key leaves the session the client named.
func sessionHeaders(h http.Header, body []byte) {
	h.Del("session_id")
	h.Del("conversation_id")
	var v struct {
		Key string `json:"prompt_cache_key"`
	}
	json.Unmarshal(body, &v)
	if v.Key != "" {
		h.Set("session-id", v.Key)
		h.Set("thread-id", v.Key)
	}
}

var codexUA struct {
	sync.Once
	os string
}

// codexUserAgent is the User-Agent Codex CLI of version v sends:
// "codex_cli_rs/0.155.1 (Mac OS 26.6.0; arm64) Apple_Terminal/455".
func codexUserAgent(v string) string {
	codexUA.Do(func() { codexUA.os = codexOS() })
	arch := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
	if arch == "" {
		arch = runtime.GOARCH
	}
	term := map[string]string{"darwin": "Apple_Terminal/455", "windows": "WindowsTerminal"}[runtime.GOOS]
	if term == "" {
		term = "xterm-256color"
	}
	return "codex_cli_rs/" + v + " (" + codexUA.os + "; " + arch + ") " + term
}

var versionRE = lazyre.New(`\d+(\.\d+)+`)

// codexOS names the system as Codex CLI does: "Mac OS 26.6.0",
// "Windows 10.0.26100", "Linux 6.8.0".
func codexOS() string {
	run := func(name string, args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, _ := proc.CommandContext(ctx, name, args...).Output()
		return versionRE.FindString(string(out))
	}
	three := func(v string) string {
		for strings.Count(v, ".") < 2 {
			v += ".0"
		}
		return v
	}
	switch runtime.GOOS {
	case "darwin":
		if v := run("sw_vers", "-productVersion"); v != "" {
			return "Mac OS " + three(v)
		}
		return "Mac OS"
	case "windows":
		if v := run("cmd", "/c", "ver"); v != "" { // "Microsoft Windows [Version 10.0.26100.4652]"
			if parts := strings.Split(v, "."); len(parts) > 3 {
				v = strings.Join(parts[:3], ".")
			}
			return "Windows " + v
		}
		return "Windows"
	}
	if v := run("uname", "-r"); v != "" {
		return "Linux " + three(v)
	}
	return "Linux"
}

// codexPromptsPath is where the instructions of the account's models are
// kept, next to its model list, for every magpie process to find.
func codexPromptsPath() string {
	return filepath.Join(filepath.Dir(catalog.LivePath("codex")), "codex-prompts.json")
}

// codexPrompts reads the instructions each model is sent with out of a
// /codex/models answer, or Codex CLI's models_cache.json, which is one.
func codexPrompts(b []byte) map[string]string {
	var list struct {
		Models []struct {
			Slug     string `json:"slug"`
			Base     string `json:"base_instructions"`
			Messages struct {
				Template string `json:"instructions_template"`
			} `json:"model_messages"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	out := map[string]string{}
	for _, m := range list.Models {
		if s := m.Messages.Template; s != "" {
			out[m.Slug] = s
		} else if m.Base != "" {
			out[m.Slug] = m.Base
		}
	}
	return out
}

// saveCodexPrompts keeps the instructions a /codex/models answer carries,
// adding to those of models it doesn't list (another account's).
func saveCodexPrompts(b []byte) {
	fresh := codexPrompts(b)
	if len(fresh) == 0 {
		return
	}
	path := codexPromptsPath()
	all := map[string]string{}
	if old, err := os.ReadFile(path); err == nil {
		json.Unmarshal(old, &all)
	}
	for k, v := range fresh {
		all[k] = v
	}
	out, _ := json.Marshal(all)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return
	}
	// Two writes in one tick share an mtime, and promptFile.get would
	// keep the first account's list. Drop it under the cache's lock.
	savedPrompts.drop()
}

// promptFile is a file of instructions by model, read again when it changes.
type promptFile struct {
	sync.Mutex
	path string
	mod  time.Time
	m    map[string]string
}

// drop forgets the cached copy so the next get reads the file again even
// when its mtime has not moved.
func (f *promptFile) drop() {
	f.Lock()
	f.path, f.mod, f.m = "", time.Time{}, nil
	f.Unlock()
}

func (f *promptFile) get(path, model string, parse func([]byte) map[string]string) (string, bool) {
	f.Lock()
	defer f.Unlock()
	st, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if path != f.path || !st.ModTime().Equal(f.mod) {
		f.path, f.mod, f.m = path, st.ModTime(), nil
		if b, err := os.ReadFile(path); err == nil {
			f.m = parse(b)
		}
	}
	s, ok := f.m[model]
	return s, ok
}

var savedPrompts, codexCLIPrompts promptFile

// codexInstructions is what Codex CLI would send as a model's
// instructions: the account's model list's, Codex CLI's own copy of it
// when magpie hasn't asked yet, else its bundled catalog's for the model,
// or Codex's prompt for a model it doesn't know (codexcat.Official).
func codexInstructions(model string) string {
	if s, ok := savedPrompts.get(codexPromptsPath(), model, func(b []byte) map[string]string {
		var m map[string]string
		json.Unmarshal(b, &m)
		return m
	}); ok {
		return s
	}
	if s, ok := codexCLIPrompts.get(catalog.CodexModelsCache(), model, codexPrompts); ok {
		return s
	}
	return codexcat.Official(model)
}

// codexBody makes a Responses request the one Codex CLI would send: it
// streams only, keeps nothing (so its items carry no ids), carries Codex's
// instructions — the client's own riding first in the input — and rejects
// the sampling knobs, explicit cache options and any service tier but
// Fast's and Ultrafast's.
func codexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	// prompt_cache_options, which pi-ai sends as {"mode":"explicit"}: the
	// ChatGPT backend turns it away on every model it serves ("is not
	// supported on this model", 400, tried 2026-10-14 on gpt-5.5,
	// gpt-5.6-sol, gpt-6-sol, gpt-6-astra and gpt-6-luna, #2154); its
	// cache follows the session-id header instead (#1473)
	for _, k := range []string{"max_output_tokens", "max_completion_tokens", "temperature", "top_p", "previous_response_id", "user", "safety_identifier", "prompt_cache_options"} {
		delete(m, k)
	}
	// Codex's Fast mode sends "priority", its Ultrafast "ultrafast"; any
	// other tier stays out
	if t := m["service_tier"]; t != "priority" && t != "ultrafast" {
		delete(m, "service_tier")
	}
	model, _ := m["model"].(string)
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}}}}
	}
	input, _ := m["input"].([]any)
	input = codexInput(input)
	own, _ := m["instructions"].(string)
	if k, _ := m["prompt_cache_key"].(string); k == "" {
		if k = conversationKey(own, input); k != "" {
			m["prompt_cache_key"] = k
		}
	}

	// the client's instructions go first as a developer message, Codex's
	// in their place — unless they are Codex's, from Codex itself
	codex := codexInstructions(model)
	if strings.TrimSpace(own) != "" {
		if firstLine(own) == firstLine(codex) {
			codex = own
		} else {
			input = append([]any{map[string]any{"type": "message", "role": "developer",
				"content": []any{map[string]any{"type": "input_text", "text": own}}}}, input...)
		}
	}
	if input == nil {
		input = []any{}
	}
	m["input"] = input
	if codex != "" {
		m["instructions"] = codex
	}
	m["store"] = false
	m["stream"] = true
	m["tool_choice"] = "auto"
	declareSearch(m, false)
	// a call's name the backend refuses, in a history another provider's
	// model wrote, turned the whole request away (codex_names.go)
	codexToolNames(m)
	if _, ok := m["parallel_tool_calls"]; !ok {
		m["parallel_tool_calls"] = true
	}
	if _, ok := m["text"]; !ok {
		m["text"] = map[string]any{"verbosity": "medium"}
	}

	// reasoning, and its encrypted content to carry across turns, unless
	// it is turned off; "ultra" is Codex CLI's name for sending "max"
	reasoning, _ := m["reasoning"].(map[string]any)
	effort, _ := reasoning["effort"].(string)
	if effort == "ultra" {
		reasoning["effort"] = "max"
	}
	if effort == "none" {
		delete(m, "include")
	} else {
		if reasoning != nil {
			if _, ok := reasoning["summary"]; !ok {
				reasoning["summary"] = "auto"
			}
		}
		include, _ := m["include"].([]any)
		if !containsAny(include, "reasoning.encrypted_content") {
			include = append(include, "reasoning.encrypted_content")
		}
		m["include"] = include
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// conversationKey names the conversation a request belongs to, for a client
// that doesn't (Codex sends its thread's id as prompt_cache_key): the
// backend keeps a conversation's cached prompt, and Codex CLI's session
// headers, by it. It is taken from how the conversation starts — the
// client's instructions and the input up to its first user message — which
// every later turn repeats, so each turn gets the same key.
func conversationKey(instructions string, input []any) string {
	h := sha256.New()
	h.Write([]byte(instructions))
	user := false
	for _, item := range input {
		b, _ := json.Marshal(item)
		h.Write(b)
		if it, _ := item.(map[string]any); it["role"] == "user" {
			user = true
			break
		}
	}
	if !user {
		return ""
	}
	s := hex.EncodeToString(h.Sum(nil))
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// BoundCallID is a call's id as the ChatGPT backend takes one: as it is
// when it fits its 64 characters, else the sha256 of it (trimmed, as calls
// and their outputs are paired), 64 hex characters. Foreign providers can
// join two ids into one longer than that (#732, congee949: 86 and 87); the
// same id gives the same hash on every request, so a call and its output
// stay paired, and two ids that differ are never cut down alike.
func BoundCallID(id string) string {
	if len(id) <= 64 {
		return id
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return hex.EncodeToString(sum[:])
}

// codexInput makes input items fit a request that keeps nothing: no
// references to stored items, no ids (nor a message's status, which goes
// with its id), a tool's output without its call
// kept as a message saying what it was, and no system role, which the
// backend rejects (developer keeps what it means in its place).
func codexInput(input []any) []any {
	calls := map[string]string{} // call id → the type of its output
	for _, item := range input {
		it, _ := item.(map[string]any)
		id, _ := it["call_id"].(string)
		switch it["type"] {
		case "function_call", "function_call_output", "local_shell_call", "local_shell_call_output", "custom_tool_call", "custom_tool_call_output", "tool_search_call", "tool_search_output":
			if b := BoundCallID(id); b != id {
				id = b
				it["call_id"] = id
			}
		}
		switch it["type"] {
		case "function_call":
			calls[strings.TrimSpace(id)] = "function_call_output"
		case "local_shell_call":
			calls[strings.TrimSpace(id)] = "local_shell_call_output"
		case "custom_tool_call":
			calls[strings.TrimSpace(id)] = "custom_tool_call_output"
		}
	}
	var out []any
	for _, item := range input {
		it, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		if it["type"] == "item_reference" {
			continue
		}
		// Codex delivers standalone notifications without a call ID. The
		// native backend understands their name/namespace and item ID;
		// they are not historical results whose calls have gone missing.
		if (it["type"] == "function_call_output" || it["type"] == "custom_tool_call_output") && (it["call_id"] == nil || it["call_id"] == "") {
			out = append(out, it)
			continue
		}
		delete(it, "id")
		// A message's status goes only with its id: without one, the
		// backend reads it as an input message, which has no status, and
		// turns the request away 400 "Unknown parameter: 'input[N].status'"
		// (Doby on Discord: Pi gives each assistant message it sends again
		// status "completed"). Codex's own reasoning items carry none.
		if t := it["type"]; t == nil || t == "message" || t == "reasoning" {
			delete(it, "status")
		}
		switch t, _ := it["type"].(string); t {
		case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
			id, _ := it["call_id"].(string)
			id = strings.TrimSpace(id)
			want := calls[id]
			if want == t || t == "function_call_output" && want == "local_shell_call_output" {
				break
			}
			it = orphanOutput(it, id)
		}
		if it["role"] == "system" && (it["type"] == nil || it["type"] == "message") {
			it["role"] = "developer"
		}
		out = append(out, it)
	}
	return out
}

// orphanOutput is a tool's output whose call isn't in the input, told as
// the assistant's note of it.
func orphanOutput(it map[string]any, id string) map[string]any {
	name, _ := it["name"].(string)
	if name == "" {
		name = "tool"
	}
	if id == "" {
		id = "unknown"
	}
	text, ok := it["output"].(string)
	if !ok {
		b, _ := json.Marshal(it["output"])
		text = string(b)
	}
	if len(text) > 16000 {
		text = strings.ToValidUTF8(text[:16000], "") + "\n...[truncated]"
	}
	return map[string]any{"type": "message", "role": "assistant",
		"content": "[Previous " + name + " result; call_id=" + id + "]: " + text}
}

// The ChatGPT backend turns away a request whose input replays a web search
// (a web_search_call item) without the web_search tool among its tools: HTTP
// 200, then only an error event, "response protection is unavailable"
// (#1270). Codex's turns declare the tool when search is on, but not every
// request carrying the conversation does: a compaction Codex makes on its
// own for a custom provider (tools: []), magpie's summary request for a
// compaction_trigger, a thread's description, a turn with function tools
// alone. Each failed so on a real account, and each went through once the
// tool was declared. The tool declared is the cached-only one, which
// fetches nothing live; a request that offered no tools gets tool_choice
// "none", so it still calls none.

// cachedSearch is the web_search tool declared for a replayed search.
func cachedSearch() map[string]any {
	return map[string]any{"type": "web_search", "external_web_access": false}
}

// DeclareSearch is body with the web_search tool declared when its input
// replays a web search and nothing declares the tool, or body as it was.
// lite says the request goes as Codex's Responses Lite, whose tools are
// an additional_tools input item: the backend refuses a web_search among
// a Lite request's tools (400 "only supports function tools, custom tools,
// and client-executed tool search").
func DeclareSearch(body []byte, lite bool) []byte {
	if !bytes.Contains(body, []byte(`"web_search_call"`)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil || !declareSearch(m, lite) {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// declareSearch declares the web_search tool in m, a Responses request,
// when its input replays a web search and neither its tools nor an
// additional_tools item declares one, and says whether it did. A request
// is taken as Lite, too, when it carries an additional_tools item or asks
// for reasoning context "all_turns", which Lite requires; the backend takes
// the item on a request that isn't Lite as well.
func declareSearch(m map[string]any, lite bool) bool {
	input, ok := m["input"].([]any)
	if !ok {
		return false
	}
	searched, offered := false, false
	var additional map[string]any // the first additional_tools item
	for _, raw := range input {
		it, _ := raw.(map[string]any)
		switch it["type"] {
		case "web_search_call":
			searched = true
		case "additional_tools":
			tools, _ := it["tools"].([]any)
			if declaresSearch(tools) {
				return false
			}
			offered = offered || len(tools) > 0
			if additional == nil {
				additional = it
			}
		}
	}
	if !searched {
		return false
	}
	var tools []any
	switch t := m["tools"].(type) {
	case nil:
	case []any:
		tools = t
	default:
		return false // not a list: left for the backend to say so
	}
	if declaresSearch(tools) {
		return false
	}
	offered = offered || len(tools) > 0
	reasoning, _ := m["reasoning"].(map[string]any)
	lite = lite || additional != nil || reasoning["context"] == "all_turns"
	switch {
	case additional != nil:
		have, _ := additional["tools"].([]any)
		additional["tools"] = append(have, cachedSearch())
	case lite:
		m["input"] = append([]any{map[string]any{"type": "additional_tools", "role": "developer",
			"tools": []any{cachedSearch()}}}, input...)
	default:
		m["tools"] = append(tools, cachedSearch())
	}
	if !offered {
		switch c := m["tool_choice"].(type) {
		case nil:
			m["tool_choice"] = "none"
		case string:
			if c == "" || c == "auto" || c == "none" {
				m["tool_choice"] = "none"
			}
		}
	}
	return true
}

// declaresSearch says whether tools has a web search among them: the
// hosted one by any of its names (web_search, web_search_preview, a dated
// one), or Codex's own web.run, a function in the "web" namespace.
//
// Codex declares web.run in place of the hosted tool, never beside it: on a
// model its catalog puts on Responses Lite, or with its standalone web
// search on, it offers the function and leaves web_search out. The ChatGPT
// backend refuses a request that carries both ("a single request can't
// contain both a client-declared function web.run and the hosted tool
// web_search", Savin on Discord, Codex 0.163.0-alpha), so a conversation
// that replays an earlier web_search_call under web.run is sent as Codex
// sent it, with no hosted tool added: web.run is the search the backend
// serves it, through /alpha/search.
func declaresSearch(tools []any) bool {
	for _, raw := range tools {
		t, _ := raw.(map[string]any)
		typ, _ := t["type"].(string)
		name, _ := t["name"].(string)
		switch {
		case strings.HasPrefix(typ, "web_search"):
			return true
		case typ == "function" && name == "web.run":
			return true
		case typ == "namespace" && name == "web":
			inner, _ := t["tools"].([]any)
			for _, raw := range inner {
				f, _ := raw.(map[string]any)
				if f["type"] == "function" && f["name"] == "run" {
					return true
				}
			}
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func containsAny(list []any, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

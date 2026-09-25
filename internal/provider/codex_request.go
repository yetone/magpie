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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
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
		req.Header.Set("originator", "codex_cli_rs")
		req.Header.Set("User-Agent", codexUserAgent())
		if body == nil {
			return nil
		}
		req.Header.Set("Accept", "text/event-stream")
		var v struct {
			Key string `json:"prompt_cache_key"`
		}
		json.Unmarshal(body, &v)
		if v.Key != "" {
			req.Header.Set("session_id", v.Key)
			req.Header.Set("conversation_id", v.Key)
		} else {
			req.Header.Del("session_id")
			req.Header.Del("conversation_id")
		}
		return nil
	}
}

var codexUA struct {
	sync.Once
	os string
}

// codexUserAgent is the User-Agent Codex CLI sends, of the version magpie
// asks the model list with:
// "codex_cli_rs/0.155.1 (Mac OS 26.6.0; arm64) Apple_Terminal/455".
func codexUserAgent() string {
	codexUA.Do(func() { codexUA.os = codexOS() })
	arch := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
	if arch == "" {
		arch = runtime.GOARCH
	}
	term := map[string]string{"darwin": "Apple_Terminal/455", "windows": "WindowsTerminal"}[runtime.GOOS]
	if term == "" {
		term = "xterm-256color"
	}
	return "codex_cli_rs/" + codexVersion() + " (" + codexUA.os + "; " + arch + ") " + term
}

var versionRE = regexp.MustCompile(`\d+(\.\d+)+`)

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
// when magpie hasn't asked yet, else Codex's generic prompt.
func codexInstructions(model string) string {
	if s, ok := savedPrompts.get(codexPromptsPath(), model, func(b []byte) map[string]string {
		var m map[string]string
		json.Unmarshal(b, &m)
		return m
	}); ok {
		return s
	}
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".codex")
	}
	if s, ok := codexCLIPrompts.get(filepath.Join(dir, "models_cache.json"), model, codexPrompts); ok {
		return s
	}
	return codexcat.Prompt
}

// codexBody makes a Responses request the one Codex CLI would send: it
// streams only, keeps nothing (so its items carry no ids), carries Codex's
// instructions — the client's own riding first in the input — and rejects
// the sampling knobs.
func codexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	for _, k := range []string{"max_output_tokens", "max_completion_tokens", "temperature", "top_p", "previous_response_id", "user", "safety_identifier", "service_tier"} {
		delete(m, k)
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

// codexInput makes input items fit a request that keeps nothing: no
// references to stored items, no ids, a tool's output without its call
// kept as a message saying what it was, and no system role, which the
// backend rejects (developer keeps what it means in its place).
func codexInput(input []any) []any {
	calls := map[string]string{} // call id → the type of its output
	for _, item := range input {
		it, _ := item.(map[string]any)
		id, _ := it["call_id"].(string)
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
		delete(it, "id")
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

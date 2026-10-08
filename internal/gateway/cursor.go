package gateway

// PLUGIN-SERVED (see AGENTS.md): Cursor ("cursor") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-cursor-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/cursor) and raise the
// mover's min in internal/provider/migrate_side.go.

// A Cursor subscription is served through the API cursor-agent talks to —
// AgentService/Run, a Connect stream both ways over HTTP/2 — with the CLI's
// sign-in, the way a Devin one is (devin.go).
//
// Cursor keeps a conversation on its client: each message of the prompt is
// an AI SDK message in JSON, a blob named by its sha256, which the server
// asks the client for as it reads them. So each request goes whole, as such
// a conversation, the caller's system prompt at its head, and the server
// adds nothing of its own; the Run only resumes it. The caller's tools are
// MCP tools, which the model calls through Cursor's CallDynamicTool: the
// server hands each call to the client to run, and once it has said how
// many a step made, the stream is closed and the calls go to the caller,
// whose results come back in the next request's conversation. Nothing else
// the server asks the client to run — a shell, a file read — is run.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cursorToken and cursorVersion are the sign-in and the CLI version the
// API is told of, cursorAPI the API that says where a team's agent API is,
// cursorAgent the one used when it can't say, and cursorVariants the ids a
// model magpie offers stands for, cursorBaseOf the other way round; vars
// so tests can stand in for them.
var (
	cursorToken    = provider.CursorToken
	cursorVersion  = provider.CursorClientVersion
	cursorAPI      = "https://api2.cursor.sh"
	cursorAgent    = "https://agentn.global.api5.cursor.sh"
	cursorVariants = provider.CursorVariants
	cursorBaseOf   = provider.CursorBase
)

// cursorEndpoint is the agent API last picked, for the token (by its
// hash) it was picked for.
var cursorEndpoint struct {
	sync.Mutex
	key    string
	url    string
	at     time.Time
	listed bool // the server config named it; else it is cursorAgent
}

// cursorAgentURL is the agent API to run on with this token. As
// cursor-agent does, it is what ServerConfigService/GetServerConfig names
// in its agentUrlConfig — a team may be served in one region only, and
// the global API turns it away — asked once a token, and again when fresh
// is set; the global one while it can't be had.
func (s *Server) cursorAgentURL(ctx context.Context, tok string, fresh bool) string {
	h := sha256.Sum256([]byte(tok))
	key := hex.EncodeToString(h[:])
	c := &cursorEndpoint
	c.Lock()
	defer c.Unlock()
	if !fresh && c.key == key && (c.listed || time.Since(c.at) < time.Minute) {
		return c.url
	}
	u, err := s.cursorServerAgent(ctx, tok)
	if err != nil {
		if ctx.Err() != nil { // the caller gone: nothing learned
			return cursorAgent
		}
		u = cursorAgent
	}
	c.key, c.url, c.at, c.listed = key, u, time.Now(), err == nil
	return u
}

// cursorServerAgent is the agent API Cursor's server config names. The CLI
// takes agentUrl in privacy mode, which the Run always asks for, and
// agentnUrl otherwise.
func (s *Server) cursorServerAgent(ctx context.Context, tok string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorAPI+"/aiserver.v1.ServerConfigService/GetServerConfig", strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Connect-Protocol-Version", "1")
	hr.Header.Set("Authorization", "Bearer "+tok)
	hr.Header.Set("x-cursor-client-version", cursorVersion())
	hr.Header.Set("x-cursor-client-type", "cli")
	hr.Header.Set("x-ghost-mode", "true")
	res, err := s.client.Do(hr)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode/100 != 2 {
		return "", errors.New(res.Status)
	}
	var cfg struct {
		AgentURLConfig struct {
			AgentURL  string `json:"agentUrl"`
			AgentnURL string `json:"agentnUrl"`
		} `json:"agentUrlConfig"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", err
	}
	for _, raw := range []string{cfg.AgentURLConfig.AgentURL, cfg.AgentURLConfig.AgentnURL} {
		if u, err := url.Parse(raw); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			return strings.TrimRight(raw, "/"), nil
		}
	}
	return "", errors.New("no agent URL in the server config")
}

// cursorRegional tells an error of the region a team may be served in:
// "This region is not yet available for your team".
func cursorRegional(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "region")
}

// cursorModelID is Cursor's id for a model magpie offers, at the effort
// asked for: the family's variant at it; else, for an effort between the
// ones it has, the one Cursor picks by default (gpt-5.2 is low, high and
// one without an effort); else the nearest. Fast, when asked, is from the
// family's fast one. An id of Cursor's own at an effort (grok-4.7-low, one
// an agent was set to before) is at the effort asked for, and fast, where
// Cursor has that id; else it goes as it is.
func cursorModelID(model, effort string, fast bool) string {
	if base, at, ok := cursorBaseOf(model); ok {
		if effort == "" {
			effort = at
		}
		if fast && !strings.HasSuffix(base, "-fast") {
			if _, ok := cursorVariants(base + "-fast"); ok {
				base += "-fast"
			}
		}
		if vs, ok := cursorVariants(base); ok && vs[effort] != "" && effort != "" {
			return vs[effort]
		}
		return model
	}
	if fast && !strings.HasSuffix(model, "-fast") {
		if _, ok := cursorVariants(model + "-fast"); ok {
			model += "-fast"
		}
	}
	vs, ok := cursorVariants(model)
	if !ok {
		return model
	}
	if effort == "" {
		return vs[""]
	}
	if id := vs[effort]; id != "" {
		return id
	}
	var levels []string
	unnamed := true // the default has no effort of its own
	for _, l := range effortRank {
		if id := vs[l]; id != "" {
			levels = append(levels, l)
			unnamed = unnamed && id != vs[""]
		}
	}
	if len(levels) == 0 {
		return vs[""]
	}
	at := slices.Index(effortRank, effort)
	if unnamed && at > slices.Index(effortRank, levels[0]) && at < slices.Index(effortRank, levels[len(levels)-1]) {
		return vs[""]
	}
	return vs[fitEffort(effort, levels)]
}

// cursorCall is how the model calls an MCP tool.
const cursorCall = "CallDynamicTool"

// serveCursor answers a request through Cursor's API.
func (s *Server) serveCursor(w http.ResponseWriter, r *http.Request, from provider.Protocol, model string, body []byte, usage *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	// the vendor's own API is told the answer's format in words
	req = req.inSystem()
	req.Model = model
	if conv := cursorConversation(r.Header, req.CacheKey, body); conv != "" {
		r = r.WithContext(context.WithValue(r.Context(), cursorConvKey{}, conv))
	}
	ask := s.askCursor(model)
	if req.WebSearch && !searching(r.Context()) {
		if canSearch() {
			return s.searchReply(w, r, from, "Cursor", req, usage, ask)
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, status, msg := ask(ctx, req)
	if events == nil {
		return writeError(w, from, status, msg), msg
	}
	return relay(w, r, from, "Cursor", req, events, usage, cancel, nil, func(string, string, bool) {})
}

// askCursor is a round for Cursor's API.
func (s *Server) askCursor(model string) round {
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		tok, err := cursorToken()
		if err != nil {
			return nil, 401, "Cursor: " + err.Error()
		}
		id := cursorModelID(model, req.Effort, req.Fast)
		if id == "auto" { // Cursor's pick, which its API calls default
			id = "default"
		}
		base := s.cursorAgentURL(ctx, tok, false)
		_, maxMode := cursorMaxOnly.Load(id)
		events, status, msg := s.cursorRun(ctx, req, model, id, tok, base, maxMode)
		if events == nil && cursorRegional(msg) {
			// the team moved, or the config was kept from before: once more
			// with what the config says now
			if fresh := s.cursorAgentURL(ctx, tok, true); fresh != base {
				base = fresh
				events, status, msg = s.cursorRun(ctx, req, model, id, tok, base, maxMode)
			}
		}
		if events == nil && !maxMode && cursorMaxRequired(msg) {
			// a model Cursor serves only in Max Mode: in Max Mode, as
			// cursor-agent turns it on for such a model, and so from now
			// on. An account Max Mode isn't open to gets Cursor's answer.
			cursorMaxOnly.Store(id, true)
			events, status, msg = s.cursorRun(ctx, req, model, id, tok, base, true)
		}
		return events, status, msg
	}
}

// cursorMaxOnly are the ids Cursor said it serves in Max Mode only.
var cursorMaxOnly sync.Map

// cursorMaxRequired is Cursor's refusal of a model asked for without Max
// Mode (cursor-agent's MAX_MODE_REQUIRED): "Max Mode Required: The model
// "gpt-5.6-luna-low" requires Max Mode to be enabled. …" (ARNO on Discord).
func cursorMaxRequired(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "max mode required") || strings.Contains(low, "max_mode_required") || strings.Contains(low, "requires max mode")
}

// cursorRun is one Run of the request on the agent API at base, in Max
// Mode when maxMode is set.
func (s *Server) cursorRun(ctx context.Context, req *Request, model, id, tok, base string, maxMode bool) (<-chan Event, int, string) {
	tools := bridgeTools(req)
	msgs := cursorMessages(req, tools)
	conv, _ := ctx.Value(cursorConvKey{}).(string)
	if conv == "" { // a new one: the Run's usage event is found by it
		conv = cursorUUID()
	}
	since := time.Now()
	run, blobs := buildCursorRun(msgs, cursorLastUser(req), tools, id, conv, maxMode)

	// the Run ends with the turn, or once the calls are made
	rctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	st := &cursorStream{pw: pw, blobs: blobs}
	hr, err := http.NewRequestWithContext(rctx, http.MethodPost, base+"/agent.v1.AgentService/Run", pr)
	if err != nil {
		cancel()
		return nil, 500, "Cursor: " + err.Error()
	}
	hr.Header.Set("Content-Type", "application/connect+proto")
	hr.Header.Set("Connect-Protocol-Version", "1")
	hr.Header.Set("Authorization", "Bearer "+tok)
	hr.Header.Set("x-cursor-client-version", cursorVersion())
	hr.Header.Set("x-cursor-client-type", "cli") // else Cursor adds a prompt of its own
	hr.Header.Set("x-ghost-mode", "true")        // privacy mode: nothing kept for training
	hr.Header.Set("x-request-id", cursorUUID())
	// none of Cursor's own tools, only the caller's
	hr.Header.Set("x-cursor-agent-allowed-tools", "mcp_tool_call,get_mcp_tools_tool_call")
	go st.send(run)
	go st.heartbeat(rctx)
	res, err := s.client.Do(hr)
	if err != nil {
		cancel()
		pw.Close()
		return nil, 502, "Cursor: " + err.Error()
	}
	res = notAnAPIReply(res, "") // a web page served 200 (#1012)
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		cancel()
		pw.Close()
		status, msg := cursorFailure(res.StatusCode, b)
		return nil, status, "Cursor: " + msg
	}
	raw := make(chan Event, 16)
	st.done = func() {
		res.Body.Close()
		pw.Close()
	}
	st.step = func(ctx context.Context) (Usage, bool) {
		return s.cursorStepUsage(ctx, tok, conv, since)
	}
	go func() {
		defer res.Body.Close()
		defer pw.Close()
		defer cancel()
		st.decode(rctx, bufio.NewReaderSize(res.Body, 64<<10), raw, tools)
	}()
	// an error comes before anything of the answer: out of quota, a
	// model the plan doesn't have — answered with its own status
	first, ok := <-raw
	if !ok {
		return nil, 502, "Cursor: an empty reply"
	}
	if first.Kind == KError {
		cancel()
		status := st.status
		if status == 0 {
			status = 502
		}
		return nil, status, "Cursor: " + first.Text
	}
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		send := func(ev Event) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(Event{Kind: KStart, MsgID: "msg_" + randomToken()[:24], Model: model}) || !send(first) {
			return
		}
		for ev := range raw {
			// a guess only when Cursor counted nothing: a prompt read whole
			// from the cache leaves no uncached rest
			if u := ev.Usage; ev.Kind == KUsage && u.Input == 0 && u.CacheRead == 0 && u.CacheWrite == 0 {
				ev.Usage.Input = estimate(req)
			}
			if !send(ev) {
				return
			}
		}
	}()
	return out, 0, ""
}

// cursorFailure is the status and message for Cursor's Connect error,
// whose details say it best: {"code", "message", "details": [{"debug":
// {"error", "details": {"title", "detail"}}}]}, or the same under "error"
// at a stream's end.
func cursorFailure(status int, body []byte) (int, string) {
	type failure struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Debug struct {
				Error   string `json:"error"`
				Details struct {
					Title  string `json:"title"`
					Detail string `json:"detail"`
				} `json:"details"`
			} `json:"debug"`
		} `json:"details"`
	}
	var e struct {
		failure
		Error *failure `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return cursorStatus(status, "", strings.TrimSpace(string(body)))
	}
	f := e.failure
	if e.Error != nil {
		f = *e.Error
	}
	msg := f.Message
	for _, d := range f.Details {
		if t := strings.TrimSpace(d.Debug.Details.Title + ": " + d.Debug.Details.Detail); t != ":" {
			msg = strings.Trim(t, ": ")
		}
	}
	if msg == "" || msg == "Error" {
		msg = f.Code
	}
	if f.Code == "" && status/100 == 2 { // a stream that ended well
		return status, msg
	}
	return cursorStatus(status, f.Code, msg)
}

// cursorStatus is the status and words for Cursor's error of this code and
// message. A region the team isn't served in says so, not to sign in: a
// sign-in doesn't change it.
func cursorStatus(status int, code, msg string) (int, string) {
	if msg == "" {
		msg = http.StatusText(status)
	}
	lower := strings.ToLower(msg)
	switch {
	case cursorRegional(msg):
		return 403, msg + " — Cursor serves your team only in some regions and turned this request away; signing in again won't change that"
	case code == "permission_denied":
		return 403, msg
	case code == "unauthenticated" || status == 401 || strings.Contains(lower, "expired"):
		return 401, msg + " — sign in to Cursor again in magpie"
	case code == "resource_exhausted" || strings.Contains(lower, "quota") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "usage limit"):
		return 429, "usage limit reached: " + msg
	case strings.Contains(lower, "too long") || strings.Contains(lower, "context length") || strings.Contains(lower, "too many tokens"):
		return 400, "input is too long for the model's context: " + msg
	case code == "invalid_argument":
		return 400, msg
	case code == "unavailable":
		return 503, msg
	case code != "":
		return 502, msg
	}
	return status, msg
}

// ---- the conversation ----------------------------------------------------

// cursorMessages is the conversation as AI SDK messages, in JSON: the
// system prompt with the caller's tools listed, then each turn. A tool
// result answers a call of the reply before it; a call left unanswered is
// answered for, as the API wants every call to have its result.
func cursorMessages(r *Request, tools []bridgeTool) [][]byte {
	var out [][]byte
	add := func(m map[string]any) {
		b, _ := json.Marshal(m)
		out = append(out, b)
	}
	if sys := r.System + cursorCatalog(tools); sys != "" {
		add(map[string]any{"role": "system", "content": sys})
	}
	var pending []string // calls the last reply made, not yet answered
	answer := func(results []any) {
		for _, id := range pending {
			results = append(results, cursorResult(id, devinNoResult, true))
		}
		pending = nil
		if len(results) > 0 {
			add(map[string]any{"role": "tool", "content": results})
		}
	}
	for _, m := range joinSplitCalls(r.Messages) {
		if m.Role == "assistant" {
			answer(nil)
			var content []any
			for _, p := range m.Parts {
				switch p.Kind {
				case Text:
					if p.Text != "" {
						content = append(content, map[string]any{"type": "text", "text": p.Text})
					}
				case ToolCall:
					var args any = map[string]any{}
					if json.Valid(p.Args) {
						args = json.RawMessage(p.Args)
					}
					content = append(content, map[string]any{"type": "tool-call", "toolCallId": cursorCallID(p.ID), "toolName": cursorCall,
						"args": map[string]any{"namespace": "magpie", "toolName": p.Name, "arguments": args}})
					pending = append(pending, p.ID)
				}
			}
			if len(content) > 0 {
				add(map[string]any{"role": "assistant", "content": content})
			}
			continue
		}
		var results, content []any
		for _, p := range m.Parts {
			switch p.Kind {
			case ToolResult:
				if i := indexOf(pending, p.CallID); i >= 0 {
					pending = append(pending[:i], pending[i+1:]...)
					results = append(results, cursorResult(p.CallID, p.Text, p.IsError))
				}
			case Text:
				if p.Text != "" {
					content = append(content, map[string]any{"type": "text", "text": p.Text})
				}
			case Image:
				if b, err := base64.StdEncoding.DecodeString(p.Data); err == nil && p.Data != "" {
					content = append(content, map[string]any{"type": "image", "mimeType": p.MediaType,
						"image": map[string]any{"__type": "Uint8Array", "hex": hex.EncodeToString(b)}})
				} else {
					content = append(content, map[string]any{"type": "text", "text": attachmentText(p)})
				}
			case File:
				content = append(content, map[string]any{"type": "text", "text": attachmentText(p)})
			}
		}
		answer(results)
		if len(content) > 0 {
			add(map[string]any{"role": "user", "content": content})
		}
	}
	answer(nil)
	return out
}

func indexOf(ids []string, id string) int {
	for i, x := range ids {
		if x == id {
			return i
		}
	}
	return -1
}

// cursorResult is a tool's result, as the model is shown it.
func cursorResult(id, text string, isError bool) map[string]any {
	var result any = text
	if json.Valid([]byte(text)) {
		result = json.RawMessage(text)
	}
	r := map[string]any{"type": "tool-result", "toolCallId": cursorCallID(id), "toolName": cursorCall, "result": result,
		"experimental_content": []any{map[string]any{"type": "text", "text": text}}}
	if isError {
		r["isError"] = true
	}
	return r
}

// cursorCatalog lists the caller's tools for the model. It sees MCP tools
// only through Cursor's GetDynamicTools and CallDynamicTool, and Cursor
// names them in a prompt of its own, which this conversation goes without:
// without the list the model says it has no such tool.
func cursorCatalog(tools []bridgeTool) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<dynamic_tool_catalog>\nThe tools below are available in the MCP namespace \"magpie\". Call one with `" + cursorCall + "` (namespace \"magpie\", toolName, arguments). Their schemas are given here, so there is no need to call `GetDynamicTools` first.\n")
	for _, t := range tools {
		b.WriteString("<tool name=\"" + t.Name + "\">\n" + t.Description + "\ninput schema: " + string(t.InputSchema) + "\n</tool>\n")
	}
	b.WriteString("</dynamic_tool_catalog>")
	return b.String()
}

// cursorLastUser is the user's last words, which the turn is named by.
func cursorLastUser(r *Request) string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role != "user" {
			continue
		}
		for _, p := range r.Messages[i].Parts {
			if p.Kind == Text && p.Text != "" {
				return p.Text
			}
		}
	}
	return "."
}

// cursorCallID is a call's id as Cursor gave it, before the line in it
// was sent to the caller as __.
func cursorCallID(id string) string {
	if strings.HasPrefix(id, "call_") {
		return strings.Replace(id, "__fc_", "\nfc_", 1)
	}
	return id
}

// ---- the request ---------------------------------------------------------

func cursorBlobID(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func cursorUUID() string {
	b, _ := hex.DecodeString(randomToken()[:32])
	return uuidOf(b)
}

// uuidOf is 16 bytes as a version 4 UUID, the shape of every id Cursor's
// own client sends.
func uuidOf(b []byte) string {
	b = slices.Clone(b[:16])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// cursorConvKey holds, in a request's context, the conversation_id its
// Runs go with.
type cursorConvKey struct{}

// cursorConversation is the AgentRunRequest's conversation_id for a
// conversation: the same on every request of it, so that Cursor's backend
// keeps sending it to the machine that has its prompt cached — the
// x-grok-conv-id grokSigned sends xAI for Grok, which caches by machine
// (#498). Cursor's own client keeps one conversationId per agent session,
// so it is made from what names the session — the client's
// prompt_cache_key (Codex's thread id), else the agent's own session
// header (Claude Code's, OpenCode's) — together with the conversation's
// first user message, which every later request repeats: subagents
// running at once under one session (Claude Code's Task agents share its
// session id) are separate conversations to Cursor, as they are to its
// own client, and never one conversation sent twice at the same time.
// With nothing naming the session it is "", and each Run gets a new id
// as before: a first message alone ("hi") would put strangers'
// conversations under one id.
func cursorConversation(in http.Header, cacheKey string, body []byte) string {
	key := cacheKey
	if key == "" {
		key = nativeSessionOf(in)
	}
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cursor conversation\x00" + key + "\x00" + conversationID(http.Header{}, body)))
	return uuidOf(sum[:])
}

// buildCursorRun is the Run's first message, an AgentClientMessage with its
// run_request, and the blobs it names. The conversation state is the
// messages and one turn, which the server wants there to sample at all.
// conv is the conversation_id (cursorConversation), a new one when "".
// maxMode asks for the model in Max Mode, as cursor-agent does for a model
// Cursor serves only that way: ModelDetails' max_mode (7) and
// RequestedModel's (2).
func buildCursorRun(msgs [][]byte, lastUser string, tools []bridgeTool, model, conv string, maxMode bool) ([]byte, map[string][]byte) {
	if conv == "" {
		conv = cursorUUID()
	}
	blobs := map[string][]byte{}
	put := func(b []byte) []byte {
		id := cursorBlobID(b)
		blobs[string(id)] = b
		return id
	}
	var state pb
	for _, m := range msgs {
		state = state.bytes(1, put(m))
	}
	mid := cursorUUID()
	user := pb{}.str(1, lastUser).str(2, mid).varint(4, 1) // mode: agent
	turn := pb{}.bytes(1, pb{}.bytes(1, put(user)).str(10, mid))
	state = state.bytes(8, put(turn)).varint(10, 1).str(22, "cli")

	var defs []pb
	for _, t := range tools {
		defs = append(defs, cursorToolDef(t))
	}
	env := pb{}.str(1, runtime.GOOS).str(2, os.TempDir()).str(10, "UTC")
	rc := pb{}.bytes(4, env)
	var mcp pb
	for _, d := range defs {
		rc = rc.bytes(7, d)
		mcp = mcp.bytes(1, d)
	}
	action := pb{}.bytes(2, pb{}.bytes(2, rc)) // resume_action
	details, requested := pb{}.str(1, model).str(3, model).str(4, model), pb{}.str(1, model)
	if maxMode {
		details, requested = details.varint(7, 1), requested.varint(2, 1)
	}
	rr := pb{}.bytes(1, state).bytes(2, action).
		bytes(3, details).
		bytes(4, mcp).str(5, conv).
		bytes(9, requested).
		varint(19, 1) // inline images
	return pb{}.bytes(1, rr), blobs
}

// cursorUsage is a TurnEndedUpdate as magpie counts usage. Its
// input_tokens is the whole prompt, what was read from the cache and
// written to it included, as Cursor's own client has it (it takes both out
// to get the prompt's uncached rest); Usage.Input is that rest, as
// Anthropic's count and every other provider here has it, prompt() adding
// the cache back (#498). Its reasoning_tokens is kept as Reasoning.
func cursorUsage(uf []pbField) Usage {
	in, cr, cw := int(pbNum(uf, 1)), int(pbNum(uf, 3)), int(pbNum(uf, 4))
	return Usage{Input: max(in-cr-cw, 0), Output: int(pbNum(uf, 2)), CacheRead: cr, CacheWrite: cw, Reasoning: int(pbNum(uf, 5))}
}

// A step that calls the caller's tools has no TurnEndedUpdate: Cursor ends
// the turn, and tells what it used, only once the client has run the tools
// and sent their results in the same Run, and the caller runs them after
// the Run is closed (#676). Before that it sends nothing more than the
// conversation's checkpoint and heartbeats, and a cancelled Run ends with
// an error and no usage either. The dashboard's usage events count every
// Run, by its conversation_id, the uncached input apart from what was read
// from the cache and written to it, as TurnEndedUpdate does; one shows
// about 2.5 s after the Run is closed. cursorStepWait is how long a step's
// is looked for: first after that long, then every so often until the
// end, after which it is guessed as before.
var cursorStepWait = struct{ first, every, until time.Duration }{1500 * time.Millisecond, 500 * time.Millisecond, 6 * time.Second}

// cursorClaimed are the usage events already counted, by conversation and
// time, so that two steps of one conversation never count the same one.
var cursorClaimed = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// cursorStepUsage is the usage of the Run of conversation conv started at
// since, as the dashboard's usage events count it; false when none shows
// in time.
func (s *Server) cursorStepUsage(ctx context.Context, tok, conv string, since time.Time) (Usage, bool) {
	end := time.Now().Add(cursorStepWait.until)
	cursorClaimed.Lock()
	for k, at := range cursorClaimed.at {
		if time.Since(at) > 10*time.Minute {
			delete(cursorClaimed.at, k)
		}
	}
	cursorClaimed.Unlock()
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !wait(cursorStepWait.first) {
		return Usage{}, false
	}
	for {
		u, ok, stop := s.cursorUsageEvent(ctx, tok, conv, since)
		if ok {
			return u, true
		}
		if stop || time.Now().Add(cursorStepWait.every).After(end) || !wait(cursorStepWait.every) {
			return Usage{}, false
		}
	}
}

// cursorUsageEvent is one look for the Run's usage event: the earliest of
// conv's not counted yet since the Run began; stop when the account can't
// read them.
func (s *Server) cursorUsageEvent(ctx context.Context, tok, conv string, since time.Time) (u Usage, ok, stop bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	q, _ := json.Marshal(map[string]any{
		"teamId": 0, "page": 1, "pageSize": 50,
		"startDate": strconv.FormatInt(since.Add(-time.Minute).UnixMilli(), 10),
		"endDate":   strconv.FormatInt(time.Now().Add(time.Minute).UnixMilli(), 10),
	})
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorAPI+"/aiserver.v1.DashboardService/GetFilteredUsageEvents", bytes.NewReader(q))
	if err != nil {
		return Usage{}, false, true
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Connect-Protocol-Version", "1")
	hr.Header.Set("Authorization", "Bearer "+tok)
	hr.Header.Set("x-cursor-client-version", cursorVersion())
	hr.Header.Set("x-cursor-client-type", "cli")
	hr.Header.Set("x-ghost-mode", "true")
	res, err := s.client.Do(hr)
	if err != nil {
		return Usage{}, false, false
	}
	defer res.Body.Close()
	if res.StatusCode/100 == 4 && res.StatusCode != http.StatusTooManyRequests {
		return Usage{}, false, true // not this account's to read
	}
	var out struct {
		Events []struct {
			Timestamp      string `json:"timestamp"`
			ConversationID string `json:"conversationId"`
			TokenUsage     *struct {
				Input      float64 `json:"inputTokens"`
				Output     float64 `json:"outputTokens"`
				CacheRead  float64 `json:"cacheReadTokens"`
				CacheWrite float64 `json:"cacheWriteTokens"`
			} `json:"tokenUsage"`
		} `json:"usageEventsDisplay"`
	}
	if res.StatusCode/100 != 2 || json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&out) != nil {
		return Usage{}, false, false
	}
	cursorClaimed.Lock()
	defer cursorClaimed.Unlock()
	best, bestAt := -1, int64(0)
	for i, e := range out.Events {
		at, err := strconv.ParseInt(e.Timestamp, 10, 64)
		if err != nil || e.ConversationID != conv || e.TokenUsage == nil || at < since.UnixMilli()-1000 {
			continue
		}
		if _, taken := cursorClaimed.at[conv+"\x00"+e.Timestamp]; taken {
			continue
		}
		if best < 0 || at < bestAt {
			best, bestAt = i, at
		}
	}
	if best < 0 {
		return Usage{}, false, false
	}
	e := out.Events[best]
	cursorClaimed.at[conv+"\x00"+e.Timestamp] = time.Now()
	t := e.TokenUsage
	return Usage{Input: int(t.Input), Output: int(t.Output), CacheRead: int(t.CacheRead), CacheWrite: int(t.CacheWrite)}, true, false
}

// cursorToolDef is an McpToolDefinition.
func cursorToolDef(t bridgeTool) pb {
	d := pb{}.str(1, t.Name)
	if t.Description != "" {
		d = d.str(2, t.Description)
	}
	var schema any
	if json.Unmarshal(t.InputSchema, &schema) == nil {
		d = d.bytes(3, pbValue(schema))
	}
	return d.str(4, "magpie").str(5, t.Name).str(6, string(t.InputSchema))
}

// pbValue is a google.protobuf.Value.
func pbValue(v any) pb {
	switch x := v.(type) {
	case nil:
		return pb{}.varint(1, 0)
	case float64:
		return pb{}.double(2, x)
	case string:
		return pb{}.str(3, x)
	case bool:
		n := uint64(0)
		if x {
			n = 1
		}
		return pb{}.varint(4, n)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var st pb
		for _, k := range keys {
			st = st.bytes(1, pb{}.str(1, k).bytes(2, pbValue(x[k])))
		}
		return pb{}.bytes(5, st)
	case []any:
		var l pb
		for _, e := range x {
			l = l.bytes(1, pbValue(e))
		}
		return pb{}.bytes(6, l)
	}
	return pb{}.varint(1, 0)
}

// pbAny reads a google.protobuf.Value.
func pbAny(b []byte) any {
	for _, f := range pbFields(b) {
		switch f.num {
		case 1:
			return nil
		case 2:
			return math.Float64frombits(f.n)
		case 3:
			return string(f.data)
		case 4:
			return f.n != 0
		case 5:
			m := map[string]any{}
			for _, e := range pbFields(f.data) {
				if e.num != 1 {
					continue
				}
				var k string
				var v any
				for _, kv := range pbFields(e.data) {
					switch kv.num {
					case 1:
						k = string(kv.data)
					case 2:
						v = pbAny(kv.data)
					}
				}
				m[k] = v
			}
			return m
		case 6:
			l := []any{}
			for _, e := range pbFields(f.data) {
				if e.num == 1 {
					l = append(l, pbAny(e.data))
				}
			}
			return l
		}
	}
	return nil
}

// ---- the stream ----------------------------------------------------------

// cursorStream is one Run: what the client sends, and the blobs the server
// may ask for.
type cursorStream struct {
	mu     sync.Mutex
	pw     *io.PipeWriter
	blobs  map[string][]byte
	status int // the status an error at the head is answered with
	// done closes the Run; step is the usage of a step that called tools,
	// which Cursor never tells the Run (cursorStepUsage)
	done func()
	step func(context.Context) (Usage, bool)
}

func (st *cursorStream) send(msg []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pw.Write(connectFrame(msg))
}

// heartbeat tells the server the client is still there, as the CLI does.
func (st *cursorStream) heartbeat(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st.send(pb{}.bytes(7, nil))
		}
	}
}

// closeExec ends the server's request id, as every answer to one is.
func (st *cursorStream) closeExec(id uint64) {
	st.send(pb{}.bytes(5, pb{}.bytes(1, pb{}.varint(1, id))))
}

// decode follows the server's messages into events until the turn ends, or
// the model has made its tool calls.
func (st *cursorStream) decode(ctx context.Context, br *bufio.Reader, out chan<- Event, tools []bridgeTool) {
	defer close(out)
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	fail := func(status int, msg string) {
		st.status = status
		send(Event{Kind: KError, Text: msg})
	}
	var calls, listed, said int
	var usage Usage
	ended := false // Cursor said what the turn used
	var finish func()
	// settle is finish once a step that called tools has its usage: the
	// dashboard's count, the Run closed first, when the turn didn't end
	settle := func() {
		if calls > 0 && !ended && st.step != nil {
			if st.done != nil {
				st.done()
			}
			if u, ok := st.step(ctx); ok {
				usage = u
			}
		}
		finish()
	}
	finish = func() {
		if usage.Output == 0 {
			usage.Output = (said + 3) / 4
		}
		send(Event{Kind: KUsage, Usage: usage})
		stop := "stop"
		if calls > 0 {
			stop = "tool"
		}
		send(Event{Kind: KStop, Stop: stop})
	}
	for {
		f, err := readConnectFrame(br)
		if err != nil {
			if ctx.Err() == nil {
				if errors.Is(err, io.EOF) && calls > 0 {
					settle()
					return
				}
				fail(502, "the reply broke off: "+err.Error())
			}
			return
		}
		if f.end {
			if status, msg := cursorFailure(200, f.data); status/100 != 2 {
				fail(status, msg)
			} else if said == 0 && calls == 0 {
				fail(502, "an empty reply")
			} else {
				settle()
			}
			return
		}
		for _, m := range pbFields(f.data) {
			switch m.num {
			case 1: // interaction_update
				for _, u := range pbFields(m.data) {
					uf := pbFields(u.data)
					switch u.num {
					case 1: // text
						if t := pbStr(uf, 1); t != "" {
							said += len(t)
							if !send(Event{Kind: KText, Text: t}) {
								return
							}
						}
					case 4: // thinking
						if t := pbStr(uf, 1); t != "" {
							said += len(t)
							if !send(Event{Kind: KThink, Text: t}) {
								return
							}
						}
					case 14: // turn ended, with what it used
						usage, ended = cursorUsage(uf), true
						if calls == 0 {
							finish()
							return
						}
					case 27: // how many tool calls the step makes, sent before them
						if listed = int(pbNum(uf, 1)); listed > 0 && calls >= listed {
							settle()
							return
						}
					}
				}
			case 2: // exec_server_message: the server asks the client to run something
				if !st.exec(m.data, tools, func(id, name string, args []byte) bool {
					calls++
					said += len(args)
					return send(Event{Kind: KToolStart, ID: id, Name: name}) && send(Event{Kind: KToolArgs, Text: string(args)})
				}) {
					return
				}
				if listed > 0 && calls >= listed { // every call made: the caller runs them
					settle()
					return
				}
			case 4: // kv_server_message: a blob wanted, or one to keep
				kv := pbFields(m.data)
				id := pbNum(kv, 1)
				for _, k := range kv {
					switch k.num {
					case 2:
						res := pb{}
						if b, ok := st.blobs[pbStr(pbFields(k.data), 1)]; ok {
							res = res.bytes(1, b)
						} else {
							res = res.bytes(2, pb{}.str(1, "blob not found"))
						}
						st.send(pb{}.bytes(3, pb{}.varint(1, id).bytes(2, res)))
					case 3: // the server's own record of the turn, not needed
						st.send(pb{}.bytes(3, pb{}.varint(1, id).bytes(3, nil)))
					}
				}
			}
		}
	}
}

// exec answers what the server asks the client to run: a call of the
// caller's tools goes to call, the list of them is given, and anything
// else is refused. It is false once the caller has gone.
func (st *cursorStream) exec(msg []byte, tools []bridgeTool, call func(id, name string, args []byte) bool) bool {
	es := pbFields(msg)
	id := pbNum(es, 1)
	execID := pbStr(es, 15)
	answer := func(num int, result pb) {
		st.send(pb{}.bytes(2, pb{}.varint(1, id).str(15, execID).bytes(num, result)))
		st.closeExec(id)
	}
	for _, e := range es {
		switch e.num {
		case 11: // mcp_args: a call of the caller's tools
			a := pbFields(e.data)
			args := map[string]any{}
			for _, kv := range a {
				if kv.num != 2 {
					continue
				}
				ef := pbFields(kv.data)
				var v any
				for _, x := range ef {
					if x.num == 2 {
						v = pbAny(x.data)
					}
				}
				args[pbStr(ef, 1)] = v
			}
			js, _ := json.Marshal(args)
			name := pbStr(a, 5)
			if name == "" {
				name = strings.TrimPrefix(pbStr(a, 1), "magpie-")
			}
			// an OpenAI model's id is its call's and its item's, a line apart,
			// which no caller would take as an id: the line goes as __
			callID := strings.ReplaceAll(pbStr(a, 3), "\n", "__")
			if callID == "" {
				callID = "call_" + randomToken()[:24]
			}
			return call(callID, name, js)
		case 36: // mcp_state_exec_args: the tools there are
			srv := pb{}.str(1, "magpie").str(2, "magpie").str(7, "connected")
			for _, t := range tools {
				srv = srv.bytes(5, cursorToolDef(t))
			}
			answer(36, pb{}.bytes(1, pb{}.bytes(1, srv)))
			return true
		case 10: // request_context_args
			env := pb{}.str(1, runtime.GOOS).str(2, os.TempDir()).str(10, "UTC")
			answer(10, pb{}.bytes(1, pb{}.bytes(1, pb{}.bytes(4, env))))
			return true
		}
	}
	// a shell, a file read or an edit of Cursor's own: never run
	st.send(pb{}.bytes(5, pb{}.bytes(2, pb{}.varint(1, id).str(2, "not available"))))
	st.closeExec(id)
	return true
}

// pbStr and pbNum are a field of a message read, "" or 0 when it has none.
func pbStr(fs []pbField, num int) string {
	for _, f := range fs {
		if f.num == num {
			return string(f.data)
		}
	}
	return ""
}

func pbNum(fs []pbField, num int) uint64 {
	for _, f := range fs {
		if f.num == num {
			return f.n
		}
	}
	return 0
}

package gateway

// PLUGIN-SERVED (see AGENTS.md): Command Code's plan ("commandcode-plan") is
// a deprecated built-in subscription served by its plugin,
// @magpie-community/opencode-commandcode-auth, once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's sign-ins,
// models, requests and usage are all the plugin's, never this code's (only
// the move, in migrate*.go, still reads its accounts). A fix here alone
// doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/commandcode) and raise the
// mover's min in internal/provider/migrate_side.go.

// A Command Code account on the Go plan has no Provider API: its key is
// only taken where the CLI itself asks, POST /alpha/generate. The request
// is written as the CLI (command-code 1.72.2) writes it — its
// createModelClient:
//
//	{config:{workingDir,date,environment,structure,isGitRepo,…},memory:null,
//	 taste:null,skills:null,permissionMode:"standard",
//	 params:{model,messages,tools,system,max_tokens,stream:true,
//	         temperature?,reasoning_effort?}}
//
// the messages in the AI SDK's parts (toWireMessages): an assistant's
// text, tool-call and reasoning; a user's tool results as a message of
// role "tool" before its text and images. The reply is a JSON object a
// line (readLines, consumeStream): text-delta, reasoning-delta, tool-call
// with its input whole, finish with the usage, error. The headers are
// buildCommandAuthHeaders'. Every other plan is served on the Provider
// API, as before (provider/commandcode_plan.go).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// cmdGenerate says whether an account is asked at /alpha/generate, where,
// and with which key; a var so tests can stand in for it.
var cmdGenerate = provider.CommandCodeGenerate

// cmdCLIVersion is the CLI the requests say they come from.
const cmdCLIVersion = "1.72.2"

// cmdMaxTokens is the CLI's max_tokens when none is asked (Hk=64e3).
const cmdMaxTokens = 64000

// cmdSession is the x-session-id magpie's requests carry: one for as long
// as magpie runs, as one CLI session has one.
var cmdSession = sync.OnceValue(newUUID)

// cmdGoing says whether p is a Go account, asked at /alpha/generate.
func cmdGoing(ctx context.Context, p provider.Provider) bool {
	_, _, ok := cmdGenerate(p.Via(ctx), p)
	return ok
}

// serveCommandCode answers a request through /alpha/generate, for a Go
// account.
func (s *Server) serveCommandCode(w http.ResponseWriter, r *http.Request, from provider.Protocol, p provider.Provider, api, key, model string, body []byte, usage *Usage) (int, string) {
	req, err := parse(from, body)
	if err != nil {
		return writeError(w, from, 400, err.Error()), err.Error()
	}
	// the vendor's own API is told the answer's format in words
	req = req.inSystem()
	req.Model = model
	if req.Effort != "" && !req.ThinkOff {
		req.Effort = fitEffort(req.Effort, p.Efforts(model))
	}
	ask := s.askCommandCode(api, key, model)
	if req.WebSearch && !searching(r.Context()) {
		if canSearch() {
			return s.searchReply(w, r, from, "Command Code", req, usage, ask)
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, status, msg := ask(ctx, req)
	if events == nil {
		return writeError(w, from, status, msg), msg
	}
	// its failures keep their statuses (out of credits is another
	// account's turn), and a reply cut short is an error
	return relayStatus(w, from, "Command Code", req, events, usage, cancel)
}

// ---- the request ----------------------------------------------------------

type cmdPart struct {
	Type       string          `json:"type"`
	Text       *string         `json:"text,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     *cmdOutput      `json:"output,omitempty"`
	Image      string          `json:"image,omitempty"`
	MimeType   string          `json:"mimeType,omitempty"`
}

type cmdOutput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type cmdMessage struct {
	Role    string    `json:"role"`
	Content []cmdPart `json:"content"`
}

type cmdTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func cmdText(kind, s string) cmdPart { return cmdPart{Type: kind, Text: &s} }

// cmdImage is an image as the CLI sends one: a data URL, and its type.
func cmdImage(p Part) (cmdPart, bool) {
	mt := p.MediaType
	if mt == "" {
		mt = "image/png"
	}
	switch {
	case p.Data != "":
		return cmdPart{Type: "image", Image: "data:" + mt + ";base64," + p.Data, MimeType: mt}, true
	case p.URL != "":
		return cmdPart{Type: "image", Image: p.URL, MimeType: p.MediaType}, true
	}
	return cmdPart{}, false
}

// cmdMessages is the conversation in the CLI's wire parts.
func cmdMessages(req *Request) []cmdMessage {
	out := []cmdMessage{}
	names := map[string]string{} // a tool call's id → its tool
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			var parts []cmdPart
			for _, p := range m.Parts {
				switch p.Kind {
				case Text:
					if p.Text != "" {
						parts = append(parts, cmdText("text", p.Text))
					}
				case ToolCall:
					names[p.ID] = p.Name
					parts = append(parts, cmdPart{Type: "tool-call", ToolCallID: p.ID, ToolName: p.Name, Input: argsOf(p)})
				case Thinking:
					if p.Text != "" {
						parts = append(parts, cmdText("reasoning", p.Text))
					}
				}
			}
			if len(parts) > 0 {
				out = append(out, cmdMessage{"assistant", parts})
			}
			continue
		}
		var results, said []cmdPart
		for _, p := range m.Parts {
			switch p.Kind {
			case ToolResult:
				name := names[p.CallID]
				if name == "" {
					name = "unknown"
				}
				results = append(results, cmdPart{Type: "tool-result", ToolCallID: p.CallID, ToolName: name,
					Output: &cmdOutput{Type: "text", Value: p.Text}})
				// the CLI's tool results are text alone; the images a tool
				// returned go with the user's turn after them
				for _, im := range p.Images {
					if c, ok := cmdImage(im); ok {
						said = append(said, c)
					}
				}
			case Text:
				if p.Text != "" {
					said = append(said, cmdText("text", p.Text))
				}
			case Image:
				if c, ok := cmdImage(p); ok {
					said = append(said, c)
				}
			case File:
				if p.Text != "" {
					said = append(said, cmdText("text", p.Text))
				}
			}
		}
		if len(results) > 0 {
			out = append(out, cmdMessage{"tool", results})
		}
		if len(said) > 0 {
			out = append(out, cmdMessage{"user", said})
		}
	}
	return out
}

// cmdPlatform is the machine as Node names it (process.platform).
func cmdPlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// cmdRequest is req as the CLI asks /alpha/generate. There is no project
// behind a request through magpie: the config is the CLI's outside a git
// repository (buildServerConfig), in the user's home.
func cmdRequest(req *Request, model string) []byte {
	tools := []cmdTool{}
	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, cmdTool{t.Name, t.Description, schema})
	}
	params := map[string]any{
		"model": model, "messages": cmdMessages(req), "tools": tools, "system": req.System,
		"max_tokens": cmdMaxTokens, "stream": true,
	}
	if req.MaxTokens > 0 {
		// within what the model gives and Command Code takes: more is
		// refused, the whole request with it
		params["max_tokens"] = min(req.MaxTokens, provider.CommandCodeOutputOf(model))
	}
	if req.Temp != nil {
		params["temperature"] = *req.Temp
	}
	if req.Effort != "" && !req.ThinkOff {
		params["reasoning_effort"] = req.Effort
	}
	home, _ := os.UserHomeDir()
	b, _ := json.Marshal(map[string]any{
		"config": map[string]any{
			"workingDir": home, "date": time.Now().Format("2006-01-02"), "environment": cmdPlatform(),
			"structure": []string{}, "isGitRepo": false, "currentBranch": "", "mainBranch": "", "gitStatus": "",
			"recentCommits": []string{},
		},
		"memory": nil, "taste": nil, "skills": nil,
		"permissionMode": "standard",
		"params":         params,
	})
	return b
}

// cmdHeaders are the CLI's own (buildCommandAuthHeaders).
func cmdHeaders(h http.Header, key string) {
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "cli")
	h.Set("x-command-code-version", cmdCLIVersion)
	h.Set("x-cli-environment", "production")
	h.Set("x-project-slug", "magpie")
	h.Set("x-taste-learning", "false")
	h.Set("x-session-id", cmdSession())
	h.Set("Authorization", "Bearer "+key)
}

// ---- the reply ------------------------------------------------------------

// askCommandCode is a round for /alpha/generate.
func (s *Server) askCommandCode(api, key, model string) round {
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(api, "/")+"/alpha/generate", bytes.NewReader(cmdRequest(req, model)))
		if err != nil {
			return nil, 500, "Command Code: " + err.Error()
		}
		cmdHeaders(hr.Header, key)
		res, err := s.client.Do(hr)
		if err != nil {
			return nil, 502, "Command Code: " + err.Error()
		}
		res = notAnAPIReply(res, "") // a web page served 200 (#1012)
		if res.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			status, msg := cmdFailure(res.StatusCode, b)
			return nil, status, msg
		}
		events := make(chan Event, 16)
		go func() {
			defer close(events)
			defer res.Body.Close()
			decodeCommandCode(ctx, res.Body, events, model)
		}()
		return events, 0, ""
	}
}

// cmdFailure is the status and message for a failure, from Command
// Code's {"error":{"type","message"}} (parseEmbeddedErrorJSON) or its
// text: a model the plan hasn't is a 403, as another account's plan may
// have it; credits run out a 402, a window's limit a 429.
func cmdFailure(status int, b []byte) (int, string) {
	msg := strings.TrimSpace(firstString(gjson.GetBytes(b, "error.message").String(), gjson.GetBytes(b, "message").String(),
		gjson.GetBytes(b, "error").String(), string(b)))
	if msg == "" {
		msg = http.StatusText(status)
	}
	if len(msg) > 600 {
		msg = msg[:600]
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "MODEL_NOT_IN_PLAN:"):
		status = 403
		msg = "Model not in plan: " + strings.TrimSpace(strings.Replace(msg, "MODEL_NOT_IN_PLAN:", "", 1))
	case strings.Contains(low, "model_not_in_plan"):
		status = 403
	case strings.Contains(low, "premium_credits_exhausted"), strings.Contains(low, "insufficient credits"):
		status = 402
		msg = strings.TrimSpace(strings.Replace(msg, "PREMIUM_CREDITS_EXHAUSTED:", "", 1))
		if !quotaWords.MatchString(msg) {
			msg = "out of credits: " + msg
		}
	case strings.Contains(msg, "RATE_LIMITED"), strings.Contains(low, "usage limit"), strings.Contains(low, "window_limit"):
		if status < 400 || status == 500 {
			status = 429
		}
	}
	if status < 400 || status > 599 {
		status = 502
	}
	return status, "Command Code: " + msg
}

func firstString(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// cmdLine is one line of the reply, the AI SDK's stream parts as the
// server passes them on.
type cmdLine struct {
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	ToolCallID       string          `json:"toolCallId"`
	ToolName         string          `json:"toolName"`
	Input            json.RawMessage `json:"input"`
	Args             json.RawMessage `json:"args"`
	ProviderExecuted bool            `json:"providerExecuted"`
	FinishReason     string          `json:"finishReason"`
	RawFinishReason  string          `json:"rawFinishReason"`
	TotalUsage       *struct {
		Input   int `json:"inputTokens"`
		Output  int `json:"outputTokens"`
		Details *struct {
			NoCache    *int `json:"noCacheTokens"`
			CacheRead  int  `json:"cacheReadTokens"`
			CacheWrite int  `json:"cacheWriteTokens"`
		} `json:"inputTokenDetails"`
		OutDetails *struct {
			Reasoning int `json:"reasoningTokens"`
		} `json:"outputTokenDetails"`
	} `json:"totalUsage"`
	CacheWriteTokens int             `json:"cacheWriteTokens"`
	Error            json.RawMessage `json:"error"`
}

// cmdInput is a tool call's input: an object, or the object as a string.
func cmdInput(l cmdLine) json.RawMessage {
	raw := l.Input
	if len(raw) == 0 || string(raw) == "null" {
		raw = l.Args
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return parseArgs(s)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	return parseArgs(string(raw))
}

// cmdStreamError is an error line's message and status: a string, or
// {message, statusCode}, whose message may hold the HTTP error it was.
func cmdStreamError(raw json.RawMessage) (int, string) {
	e := gjson.ParseBytes(raw)
	msg, status := e.String(), 0
	if e.IsObject() {
		msg, status = e.Get("message").String(), int(e.Get("statusCode").Int())
	}
	if msg == "" {
		msg = "Stream error"
	}
	// "429 {"error":{…}}", as parseEmbeddedErrorJSON reads it
	if i := strings.Index(msg, "{"); i >= 0 && gjson.Valid(msg[i:]) {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(msg[:i]), "%d", &n); err == nil && n >= 400 {
			status = n
		}
		if m := gjson.Get(msg[i:], "error.message").String(); m != "" {
			msg = m
		}
	}
	if status == 0 {
		status = 500
	}
	return cmdFailure(status, []byte(msg))
}

// decodeCommandCode turns the reply's lines into events. A reply with no
// finish line was cut short (the CLI's "stream ended without a finish
// event"): an error, not a shorter answer.
func decodeCommandCode(ctx context.Context, body io.Reader, out chan<- Event, model string) {
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(Event{Kind: KStart, MsgID: "msg_" + randomToken()[:24], Model: model}) {
		return
	}
	var u Usage
	stop, tools, finished := "stop", 0, false
	br := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := br.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			var l cmdLine
			if json.Unmarshal([]byte(s), &l) == nil {
				switch l.Type {
				case "text-delta":
					if l.Text != "" && !send(Event{Kind: KText, Text: l.Text}) {
						return
					}
				case "reasoning-delta":
					if l.Text != "" && !send(Event{Kind: KThink, Text: l.Text}) {
						return
					}
				case "tool-call":
					// one run by the server has its result beside it: the
					// client has nothing to run
					if l.ProviderExecuted {
						break
					}
					tools++
					id := l.ToolCallID
					if id == "" {
						id = "toolu_" + randomToken()[:24]
					}
					if !send(Event{Kind: KToolStart, ID: id, Name: l.ToolName}) ||
						!send(Event{Kind: KToolArgs, Text: string(cmdInput(l))}) {
						return
					}
				case "cache-write-tokens":
					if l.CacheWriteTokens > u.CacheWrite {
						u.CacheWrite = l.CacheWriteTokens
					}
				case "finish":
					finished = true
					if t := l.TotalUsage; t != nil {
						u.Output = t.Output
						input := t.Input
						if d := t.Details; d != nil {
							u.CacheRead = d.CacheRead
							u.CacheWrite = max(u.CacheWrite, d.CacheWrite)
							// the AI SDK's inputTokens counts the cache in
							// it; magpie's Input, as Anthropic's, doesn't
							if d.NoCache != nil {
								input = *d.NoCache
							} else {
								input = max(0, input-d.CacheRead-d.CacheWrite)
							}
						}
						u.Input = input
						if t.OutDetails != nil {
							u.Reasoning = t.OutDetails.Reasoning
						}
					}
					switch l.FinishReason {
					case "tool-calls":
						stop = "tool"
					case "length":
						stop = "length"
					case "content-filter":
						stop = "filter"
					}
				case "abort":
					finished = true
				case "error":
					status, msg := cmdStreamError(l.Error)
					send(Event{Kind: KError, Status: status, Text: msg})
					return
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				if ctx.Err() == nil {
					send(Event{Kind: KError, Text: "Command Code: the reply broke off: " + err.Error()})
				}
				return
			}
			break
		}
	}
	if !finished {
		send(Event{Kind: KError, Text: "Command Code: the reply ended before it was complete"})
		return
	}
	if tools > 0 && stop == "stop" {
		stop = "tool"
	}
	send(Event{Kind: KUsage, Usage: u})
	send(Event{Kind: KStop, Stop: stop})
}

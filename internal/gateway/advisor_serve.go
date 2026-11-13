package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Code's advisor (/advisor, --advisor, settings.json's
// advisorModel) is Anthropic's server tool: the model calls advisor() with
// no arguments, Anthropic's server asks the advisor model about the whole
// conversation, and the reply carries both, a server_tool_use block and an
// advisor_tool_result with the advice, before the model goes on. Claude
// Code sends it for any model with CLAUDE_CODE_ENABLE_EXPERIMENTAL_ADVISOR_TOOL
// set (2.1.316), which magpie's advisor field writes (internal/agent).
//
// An upstream that runs it itself (a Claude model on Anthropic's API, its
// advisor a Claude model of the same provider) is given it as it is
// (advisorTools). For any other, where the advisor model is one magpie
// serves, magpie runs it: the model is offered an advisor tool of its own,
// a call of it is answered by asking the advisor model (advise), and the
// client is told of it in the blocks Anthropic's server writes. Where
// magpie can't (no such model), it is left out as before (advisorTools,
// readAnthropic). Claude Code's own instructions for the advisor stay as
// it wrote them either way: nothing about it is added to the prompt the
// model is sent beside the tool's description.

// advisorTool is the name Claude Code gives its advisor tool, and the one
// the model calls it by.
const advisorTool = "advisor"

// AdvisorAgent is the User-Agent of the advisor's requests magpie makes.
const AdvisorAgent = "magpie-advisor/1"

// advisorUses is how many times one reply may call the advisor; one more
// is answered max_uses_exceeded, as Anthropic's server answers it.
const advisorUses = 3

// advisedHere tells whether magpie runs the advisor of a Messages request
// it serves on p's model on API to: Claude Code asked for one, the
// upstream doesn't run it itself, and magpie serves its model.
func advisedHere(p provider.Provider, from, to provider.Protocol, model string, body []byte, wires map[string]string) bool {
	if from != provider.Anthropic || !bytes.Contains(body, []byte(`"advisor_`)) {
		return false
	}
	adv := ""
	for _, t := range gjson.GetBytes(body, "tools").Array() {
		typ := t.Get("type").String()
		if strings.HasPrefix(typ, "advisor_") {
			if adv == "" {
				adv = t.Get("model").String()
			}
			continue
		}
		if t.Get("name").String() == advisorTool {
			return false // a tool of the client's own goes by the name
		}
	}
	if adv == "" {
		return false
	}
	if to == provider.Anthropic && anthropicModel.MatchString(provider.UpstreamNameIn(wires, p.ID, model)) {
		if _, ok := advisorModel(p, adv, wires); ok {
			return false // Anthropic's server runs it (advisorTools)
		}
	}
	_, _, ok := provider.Resolve(adv)
	return ok
}

// advisorToolDef is the tool the model is offered for the advisor. It
// names no vendor and no gateway: it goes upstream as the client's tools
// do.
func advisorToolDef() Tool {
	return Tool{Name: advisorTool,
		Description: "Ask a stronger reviewer model for advice. Takes no parameters: the whole conversation so far is passed to it, and its advice comes back as the result.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`)}
}

// adviceOf is the advice in an advisor_tool_result's content, and whether
// it is an error's.
func adviceOf(raw json.RawMessage) (string, bool) {
	c := gjson.ParseBytes(raw)
	switch c.Get("type").String() {
	case "advisor_result":
		return c.Get("text").String(), false
	case "advisor_redacted_result":
		return "The advice is not readable here.", false
	}
	code := c.Get("error_code").String()
	if code == "" {
		code = "unavailable"
	}
	return "The advisor failed: " + code, true
}

// adviceBlock is an advisor_tool_result as Anthropic writes it: the
// advice, or the error_code of a call that failed.
func adviceBlock(id, text, code string) map[string]any {
	content := map[string]any{"type": "advisor_result", "text": text}
	if code != "" {
		content = map[string]any{"type": "advisor_tool_result_error", "error_code": code}
	}
	return map[string]any{"type": "advisor_tool_result", "tool_use_id": id, "content": content}
}

// adviceOutID is the id an advisor call is given to the client by, which
// adviceInID turns back: Anthropic's own are srvtoolu_…
func adviceOutID(id string) string { return "srvtoolu_" + anthropicOutID(id) }

func adviceInID(id string) string { return anthropicInID(strings.TrimPrefix(id, "srvtoolu_")) }

// adviceApart is an assistant message of Claude Code's history as the
// model's turns: Anthropic's server writes the advisor's result in the
// assistant's message, after its call, where every other API has a tool's
// result in a message of its own. A call with no result, or a result with
// no call, is left out.
func adviceApart(m Message) []Message {
	called, answered := map[string]bool{}, map[string]bool{}
	for _, p := range m.Parts {
		switch {
		case p.Kind == ToolCall && p.Name == advisorTool:
			called[p.ID] = true
		case p.Kind == ToolResult:
			answered[p.CallID] = true
		}
	}
	if len(called) == 0 && len(answered) == 0 {
		return []Message{m}
	}
	var out []Message
	var said, results []Part
	flush := func() {
		if len(said) > 0 {
			out = append(out, Message{Role: "assistant", Parts: said})
			said = nil
		}
		if len(results) > 0 {
			out = append(out, Message{Role: "user", Parts: results})
			results = nil
		}
	}
	for _, p := range m.Parts {
		switch {
		case p.Kind == ToolCall && p.Name == advisorTool:
			if !answered[p.ID] {
				continue
			}
		case p.Kind == ToolResult:
			if called[p.CallID] {
				results = append(results, p)
			}
			continue
		}
		if len(results) > 0 {
			flush()
		}
		said = append(said, p)
	}
	flush()
	if len(out) == 0 {
		return []Message{{Role: "assistant"}}
	}
	return out
}

// advisorAsk is what the advisor model is asked after the conversation.
const advisorAsk = "You are an advisor reviewing the conversation above, in which an assistant works on the user's task. Give the assistant brief, concrete advice for what to do next: the approach to take, what to check first, and any mistake or risk you see. Write plain text addressed to the assistant; do not call tools."

// adviceFor is the request the advisor model is asked: the model's own,
// its system prompt and conversation, with what the model said before the
// call (said), and the ask after. Its tools are offered with no call
// allowed, as the conversation calls them. Reasoning is left out: another
// model's signatures are nothing to it.
func adviceFor(q *Request, said []Part) *Request {
	c := *q
	c.Advisor, c.WebSearch, c.Stream, c.Format = "", false, false, nil
	c.Thinking, c.ThinkOff, c.Effort = false, false, ""
	c.Metadata = nil
	c.MaxTokens = 4096
	c.ToolChoice = "none"
	c.Tools = slices.DeleteFunc(slices.Clone(q.Tools), func(t Tool) bool { return t.Name == advisorTool })
	var msgs []Message
	keep := func(m Message) Message {
		m.Parts = slices.DeleteFunc(slices.Clone(m.Parts), func(p Part) bool { return p.Kind == Thinking })
		return m
	}
	for _, m := range q.Messages {
		if m = keep(m); len(m.Parts) > 0 {
			msgs = append(msgs, m)
		}
	}
	// what it said, its words only: a call in the same round has no
	// result yet
	said = slices.DeleteFunc(slices.Clone(said), func(p Part) bool { return p.Kind != Text })
	if m := keep(Message{Role: "assistant", Parts: said}); len(m.Parts) > 0 {
		msgs = append(msgs, m)
	}
	ask := Part{Kind: Text, Text: advisorAsk}
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs[n-1].Parts = append(slices.Clone(msgs[n-1].Parts), ask)
	} else {
		msgs = append(msgs, Message{Role: "user", Parts: []Part{ask}})
	}
	c.Messages = msgs
	return &c
}

// errAdvice is an advisor call that failed, with the error_code the client
// is told.
type errAdvice struct {
	code string
	err  error
}

func (e errAdvice) Error() string { return e.err.Error() }

// advise asks the advisor model, model, about q, the model's request, and
// what it said before calling the advisor.
func (s *Server) advise(ctx context.Context, q *Request, said []Part, model string) (string, error) {
	body := buildAnthropic(adviceFor(q, said), model)
	// the caller named the advisor's model, not magpie's settings, so it is
	// asked as the caller: the key's models and limits hold it (no
	// magpieChose, which a search's or a title's model takes)
	r, err := http.NewRequestWithContext(ownAsk(ctx), http.MethodPost, "http://magpie/v1/messages", nil)
	if err != nil {
		return "", errAdvice{"unavailable", err}
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", AdvisorAgent)
	w := httptest.NewRecorder()
	s.serve(w, r, provider.Anthropic, body)
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code >= 300 || out.Error != nil {
		msg := http.StatusText(w.Code)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		code := "unavailable"
		switch {
		case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
			code = "execution_time_exceeded"
		case w.Code == http.StatusRequestEntityTooLarge:
			code = "prompt_too_long"
		case w.Code == 529 || w.Code == http.StatusServiceUnavailable:
			code = "overloaded"
		default:
			if failure(w.Code, w.Body.Bytes()) == failRate {
				code = "too_many_requests"
			}
		}
		return "", errAdvice{code, fmt.Errorf("%s: %s", model, msg)}
	}
	var text strings.Builder
	for _, b := range out.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	advice := strings.TrimSpace(text.String())
	if advice == "" {
		return "", errAdvice{"unavailable", fmt.Errorf("%s said nothing", model)}
	}
	return advice, nil
}

// adviceRound answers the model's nth advisor call, c, made after said:
// the result the model is given, and the event the client is told of it
// by.
func (s *Server) adviceRound(ctx context.Context, q *Request, said []Part, c Part, n int) (Part, Event) {
	res := Part{Kind: ToolResult, CallID: c.ID}
	ev := Event{Kind: KAdvice, ID: adviceOutID(c.ID)}
	if n > advisorUses {
		ev.Code = "max_uses_exceeded"
		res.Text, res.IsError = "The advisor failed: max_uses_exceeded. Go on without it.", true
		return res, ev
	}
	advice, err := s.advise(ctx, q, said, q.Advisor)
	if err != nil {
		var e errAdvice
		ev.Code = "unavailable"
		if errors.As(err, &e) {
			ev.Code = e.code
		}
		res.Text, res.IsError = "The advisor failed: "+ev.Code, true
		return res, ev
	}
	ev.Text, res.Text = advice, advice
	return res, ev
}

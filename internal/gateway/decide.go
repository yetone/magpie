package gateway

// A group whose classifier is a decision provider's model (Jev, from
// TypeSafe) asks it in one call, as a user's turn begins, what the
// classifier model would be asked — which of the rules' intents the
// message is — and, for a group with Effort "auto", how hard the turn is
// to think about. Jev answers each with how likely every option was
// rather than with words: an intent it isn't sure of counts as none, and
// the effort is the level the likelihoods weigh out to. The turn's
// requests then ask their model for that effort, where the agent asked
// for reasoning at all.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// jevSure is how confident Jev must be of an intent for it to count.
// Confidence runs from 0, every option as likely, to 1, one certain.
const jevSure = 0.4

// noIntent is the option Jev picks for a message that is none of the
// intents.
const noIntent = "none of these"

// jevLevels are the efforts Jev chooses among, with what each is for,
// lowest first.
var jevLevels = []struct{ effort, what string }{
	{"low", "Little to think about: a greeting, a question answered from what is already known, a mechanical or one-line change, running a command"},
	{"medium", "Some thought: an ordinary bug fix or a small feature in code already understood"},
	{"high", "Careful thought: a change across several files, a bug whose cause is not known yet, a design choice with trade-offs"},
	{"xhigh", "Deep thought: a subtle bug (concurrency, performance, security), an architecture or algorithm to design, a long multi-step plan"},
}

// jevBody is the System One request asking which of intents text is
// (when there are any) and, when effort, how hard it is. What was said of
// the turn before (prev) is in the state: a message that only carries on
// from it is of its kind and wants its reasoning.
func jevBody(model string, intents []string, prev before, effort bool, text string) []byte {
	qs := map[string]any{}
	if len(intents) > 0 {
		// Kinds may be topics, which a message can be none of, or levels
		// (how hard or big a request is), which every message has one
		// of. Jev takes "none of these" over any level for a greeting,
		// and without it puts a poem in some topic, sure of it; so it is
		// asked both ways, and whether the kinds are levels (jevLevelled)
		// says which answer counts (readJev).
		criteria := map[string]any{noIntent: "The message is none of the other kinds"}
		levels := map[string]any{}
		for _, in := range intents {
			criteria[in], levels[in] = nil, nil
		}
		qs["intent"] = map[string]any{"type": "choice", "instructions": intentAsk(prev), "criteria": criteria}
		qs["level"] = map[string]any{"type": "choice", "instructions": intentAsk(prev), "criteria": levels}

	}
	if effort {
		levels := make([]string, len(jevLevels))
		for i, l := range jevLevels {
			levels[i] = l.what
		}
		qs["effort"] = map[string]any{
			"type":         "score",
			"instructions": effortAsk(prev),
			"criteria":     levels,
		}
	}
	b, _ := json.Marshal(map[string]any{
		"model":     model,
		"state":     jevState(prev, effort, text),
		"questions": qs,
	})
	return b
}

func jevState(prev before, effort bool, text string) map[string]string {
	st := map[string]string{"message": text}
	if prev.Intent != "" {
		st["previous_message_kind"] = prev.Intent
	}
	if effort && prev.Effort != "" {
		st["previous_message_reasoning"] = prev.Effort
	}
	return st
}

func intentAsk(prev before) string {
	q := "The `message` is what a user asked a coding assistant. Which kind of request fits it best?"
	if prev.Intent != "" {
		q += " A message that only carries on from the user's message before it (go on, yes, do it, fix that) is of `previous_message_kind`; one that asks for something of its own is of the kind that fits it."
	}
	return q
}

func effortAsk(prev before) string {
	q := "How much reasoning does a coding assistant need to handle the `message` well?"
	if prev.Effort != "" {
		q += " A message that only carries on from the user's message before it (go on, yes, do it) needs what that one did, `previous_message_reasoning` (low, medium, high or xhigh)."
	}
	return q
}

// readJev is the verdict in a System One reply to jevBody, the answer
// without "none of these" counting when the intents are levels.
func readJev(b []byte, intents []string, levels bool, prev before) (verdict, error) {
	var out struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Choice     string  `json:"choice"`
			Score      float64 `json:"score"`
			Confidence float64 `json:"confidence"`
		} `json:"answers"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return verdict{}, answerError{fmt.Errorf("not a System One answer")}
	}
	var v verdict
	if a, ok := out.Answers["intent"]; ok && len(intents) > 0 && !levels {
		v.Sure = a.Confidence
		if a.Choice != noIntent && a.Confidence >= jevSure {
			v.Intent = intentNamed(intents, a.Choice)
		}
	}
	if a, ok := out.Answers["level"]; ok && len(intents) > 0 && levels {
		// every message is at one of them, however unsure Jev is: when it
		// is torn, the conversation stays at the level of the turn before
		// (继续，再加一个基准测试 after a 复杂任务 came out 0.54 to 0.46),
		// and a turn with none before takes the likelier
		v.Sure = a.Confidence
		v.Intent = intentNamed(intents, a.Choice)
		if was := intentNamed(intents, prev.Intent); a.Confidence < jevSure && was != "" {
			v.Intent = was
		}
	}
	if a, ok := out.Answers["effort"]; ok {
		i := int(math.Round(a.Score))
		i = max(0, min(i, len(jevLevels)-1))
		v.Effort, v.Score = jevLevels[i].effort, a.Score
	}
	return v, nil
}

// intentNamed is the one of intents named, or "".
func intentNamed(intents []string, name string) string {
	for _, in := range intents {
		if in == name {
			return in
		}
	}
	return ""
}

// levelled is what Jev said of each set of intents: whether they are
// levels every message is at (jevLevelled). It holds as long as the
// intents do.
var levelled = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// jevLevelled asks Jev whether intents are levels of one scale — how hard
// or big a request is — rather than topics a message may be none of,
// once for each set of them.
func (s *Server) jevLevelled(ctx context.Context, p provider.Provider, model string, intents []string) (bool, error) {
	key := model + "\x00" + strings.ToLower(strings.Join(intents, "\x00"))
	levelled.Lock()
	l, ok := levelled.m[key]
	levelled.Unlock()
	if ok {
		return l, nil
	}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"state": map[string]any{"kinds": intents},
		"questions": map[string]any{"levels": map[string]any{"type": "noul",
			"instructions": "Are these `kinds` levels of one scale that every request to a coding assistant is at, such as how hard, how big or how urgent it is — rather than topics or tasks that a request may be none of?"}},
	})
	b, err := s.systemOne(ctx, p, model, body)
	if err != nil {
		return false, err
	}
	var out struct {
		Answers struct {
			Levels *struct {
				Noul float64 `json:"noul"`
			} `json:"levels"`
		} `json:"answers"`
	}
	if json.Unmarshal(b, &out) != nil || out.Answers.Levels == nil {
		return false, answerError{fmt.Errorf("not a System One answer")}
	}
	l = out.Answers.Levels.Noul >= 0.5
	levelled.Lock()
	levelled.m[key] = l
	levelled.Unlock()
	return l, nil
}

// askJev asks a decision provider's model through its System One API.
func (s *Server) askJev(p provider.Provider, model string, intents []string, prev before, effort bool, text string) (verdict, error) {
	ctx, cancel := context.WithTimeout(context.Background(), classifyTimeout)
	defer cancel()
	if model == "" {
		model = provider.JevLatest
	}
	levels := false
	if len(intents) > 0 {
		var err error
		if levels, err = s.jevLevelled(ctx, p, model, intents); err != nil {
			return verdict{}, err
		}
	}
	b, err := s.systemOne(ctx, p, model, jevBody(model, intents, prev, effort, text))
	if err != nil {
		return verdict{}, err
	}
	return readJev(b, intents, levels, prev)
}

// systemOne posts body to p's System One API and returns the answer,
// counting it in the usage as magpie's own.
func (s *Server) systemOne(ctx context.Context, p provider.Provider, model string, body []byte) ([]byte, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Decide+"/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", RouterAgent)
	if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
		return nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s gave no answer in %s", p.Name, classifyTimeout)
		}
		return nil, fmt.Errorf("%s: %v", p.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", p.Name, provider.APIError(b, res.Status))
	}
	var u struct {
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(b, &u)
	usage.Append(usage.Record{Time: start, Agent: usage.AgentOf(RouterAgent), Provider: p.ID, Host: p.Where(), Model: model,
		Input: u.Usage.Input, Output: u.Usage.Output, Millis: time.Since(start).Milliseconds(), Status: res.StatusCode})
	return b, nil
}

// withEffort asks a request, in the client's own API, for reasoning at
// effort, in place of what the agent asked. Only a request that asked for
// reasoning is changed: one that didn't (a session title) stays without.
func withEffort(proto provider.Protocol, body []byte, effort string) []byte {
	var v struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Reasoning       *struct {
			Effort  string `json:"effort"`
			Summary string `json:"summary"`
		} `json:"reasoning"`
		Thinking *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		OutputConfig map[string]any `json:"output_config"`
		MaxTokens    int            `json:"max_tokens"`
	}
	if effort == "" || json.Unmarshal(body, &v) != nil {
		return body
	}
	switch proto {
	case provider.Chat:
		if v.ReasoningEffort == "" || v.ReasoningEffort == "none" {
			return body
		}
		return withFields(body, map[string]any{"reasoning_effort": effort})
	case provider.Responses:
		if v.Reasoning == nil || v.Reasoning.Effort == "" || v.Reasoning.Effort == "none" {
			return body
		}
		r := map[string]any{"effort": effort}
		if v.Reasoning.Summary != "" {
			r["summary"] = v.Reasoning.Summary
		}
		return withFields(body, map[string]any{"reasoning": r})
	case provider.Anthropic:
		if v.Thinking == nil {
			return body
		}
		switch v.Thinking.Type {
		case "adaptive":
			oc := v.OutputConfig
			if oc == nil {
				oc = map[string]any{}
			}
			oc["effort"] = effort
			return withFields(body, map[string]any{"output_config": oc})
		case "enabled":
			// the budget must leave room for the answer
			budget := budgetOf(effort)
			if v.MaxTokens > 0 {
				budget = min(budget, v.MaxTokens-1)
			}
			if budget < 1024 {
				return body
			}
			fields := map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": budget}}
			if oc := v.OutputConfig; oc != nil && oc["effort"] != nil {
				oc["effort"] = effort
				fields["output_config"] = oc
			}
			return withFields(body, fields)
		}
	}
	return body
}

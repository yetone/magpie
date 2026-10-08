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
	"slices"
	"sort"
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

// levelWork is how much work a request is, least first: what a level of
// intents is told to be for (levelsOf), as Jev rated each level.
var levelWork = []string{
	"little work: a question, an explanation, a one-line or mechanical change, a command to run",
	"some work: an ordinary bug fix or a small feature in code already understood",
	"much work: a change across several files, a bug whose cause is not known yet, a design choice",
	"the most work: a new program, app or game, an architecture, a long multi-step plan",
}

// levelsOf is what each of intents, levels of one scale, is for: each of
// levelWork goes to the level Jev rated nearest it (scores, 0 to 3), so
// that two levels share them all. Told only their names, Jev was torn
// over short messages: 写个纸牌游戏 came out 复杂任务 at 0.62, too unsure
// to hold, and after a 简单任务 turn 简单任务 at 0.57, so it stayed there;
// told this, 复杂任务 at 0.99 either way.
func levelsOf(intents []string, scores []float64) map[string]string {
	if len(intents) < 2 || len(scores) != len(intents) {
		return nil
	}
	order := make([]int, len(intents))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] < scores[order[b]] })
	owns := make([][]string, len(intents))
	for w, what := range levelWork {
		near := 0
		for i := range intents {
			if math.Abs(scores[i]-float64(w)) < math.Abs(scores[near]-float64(w)) {
				near = i
			}
		}
		owns[near] = append(owns[near], what)
	}
	out := map[string]string{}
	for pos, i := range order {
		at := fmt.Sprintf("Level %d of %d, lowest first", pos+1, len(intents))
		switch pos {
		case 0:
			at = "The lowest level"
		case len(intents) - 1:
			at = "The highest level"
		}
		if len(owns[i]) > 0 {
			at += ": " + strings.Join(owns[i], "; ")
		}
		out[intents[i]] = at
	}
	return out
}

// jevBody is the System One request asking which of intents text is
// (when there are any) and, when effort, how hard it is. What was said of
// the turn before (prev) is in the state: a message that only carries on
// from it is of its kind and wants its reasoning. work is what each
// intent is for when they are levels (levelsOf), nil when not known.
func jevBody(model string, intents []string, work map[string]string, prev before, effort bool, text string) []byte {
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
			if w, ok := work[in]; ok {
				levels[in] = w
			}
		}
		qs["intent"] = map[string]any{"type": "choice", "instructions": intentAsk(prev), "criteria": criteria}
		qs["level"] = map[string]any{"type": "choice", "instructions": levelAsk(prev, work != nil), "criteria": levels}

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

// levelAsk is intentAsk for levels, which, told what each is for, are
// judged by the work asked for.
func levelAsk(prev before, work bool) string {
	q := intentAsk(prev)
	if work {
		q += " The kinds are levels of how much work a request is: judge the work the message asks for, not how short it is — a few words can ask for a whole program."
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
// levels every message is at, and what each is for (jevLevelled). It
// holds as long as the intents do.
var levelled = struct {
	sync.Mutex
	m map[string]levelling
}{m: map[string]levelling{}}

type levelling struct {
	levels bool
	work   map[string]string // levelsOf; nil when Jev didn't rate them
}

// jevLevelled asks Jev whether intents are levels of one scale — how hard
// or big a request is — rather than topics a message may be none of, and
// in the same call how much work a request at each is, once for each set
// of them.
func (s *Server) jevLevelled(ctx context.Context, p provider.Provider, model string, intents []string) (levelling, error) {
	key := model + "\x00" + strings.ToLower(strings.Join(intents, "\x00"))
	levelled.Lock()
	l, ok := levelled.m[key]
	levelled.Unlock()
	if ok {
		return l, nil
	}
	qs := map[string]any{"levels": map[string]any{"type": "noul",
		"instructions": "Are these `kinds` levels of one scale that every request to a coding assistant is at, such as how hard, how big or how urgent it is — rather than topics or tasks that a request may be none of?"}}
	for i := range intents {
		qs[fmt.Sprintf("work%d", i)] = map[string]any{"type": "score",
			"instructions": fmt.Sprintf("Taking `kinds` as levels of one scale for requests to a coding assistant, how much work is a request at the level `kinds[%d]`?", i),
			"criteria":     levelWork}
	}
	body, _ := json.Marshal(map[string]any{"model": model, "state": map[string]any{"kinds": intents}, "questions": qs})
	b, err := s.systemOne(ctx, p, model, body)
	if err != nil {
		return levelling{}, err
	}
	var out struct {
		Answers map[string]struct {
			Noul  *float64 `json:"noul"`
			Score *float64 `json:"score"`
		} `json:"answers"`
	}
	if json.Unmarshal(b, &out) != nil || out.Answers["levels"].Noul == nil {
		return levelling{}, answerError{fmt.Errorf("not a System One answer")}
	}
	l.levels = *out.Answers["levels"].Noul >= 0.5
	if l.levels {
		scores := make([]float64, 0, len(intents))
		for i := range intents {
			if a := out.Answers[fmt.Sprintf("work%d", i)]; a.Score != nil {
				scores = append(scores, *a.Score)
			}
		}
		l.work = levelsOf(intents, scores)
	}
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
		model = p.Jev()
	}
	var l levelling
	if len(intents) > 0 {
		var err error
		if l, err = s.jevLevelled(ctx, p, model, intents); err != nil {
			return verdict{}, err
		}
	}
	b, err := s.systemOne(ctx, p, model, jevBody(model, intents, l.work, prev, effort, text))
	if err != nil {
		return verdict{}, err
	}
	return readJev(b, intents, l.levels, prev)
}

// systemOne posts body to p's System One API and returns the answer,
// counting it in the usage as magpie's own. A gateway serving Jev is asked
// and answers in its own way (decideAsk, decideAnswer).
func (s *Server) systemOne(ctx context.Context, p provider.Provider, model string, body []byte) ([]byte, error) {
	start := time.Now()
	status, b, _, err := s.postDecide(ctx, p, model, body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s gave no answer in %s", p.Name, classifyTimeout)
		}
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("%s: %s", p.Name, provider.APIError(b, fmt.Sprintf("%d %s", status, http.StatusText(status))))
	}
	var use struct {
		Model string `json:"model"` // the one that answered, when the reply says
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(b, &use)
	var keyID, keyName string
	if p.Account == nil && p.Key != "" {
		keyID, keyName = provider.KeyID(p.Key), p.KeyName
	}
	usage.Append(usage.Record{Time: start, Agent: usage.AgentOf(RouterAgent), Provider: p.ID, Host: p.Where(), Model: model, Requested: model, Served: use.Model,
		ProviderKeyID: keyID, ProviderKeyName: keyName, ProviderAccount: accountOf(p),
		Input: use.Usage.Input, Output: use.Usage.Output, Millis: time.Since(start).Milliseconds(), Status: status})
	return b, nil
}

// postDecide posts body to p's decision API and returns the status, body
// and Content-Type. A success is rewritten into System One's shape when
// the provider answers in another one. DecideURL and Sign errors are as
// they are; a transport error is named with p.
func (s *Server) postDecide(ctx context.Context, p provider.Provider, model string, body []byte) (int, []byte, string, error) {
	ctx = s.metered(p.Via(ctx), p, "") // counted against its MaxRPM (rpm.go)
	u, err := p.DecideModelURL(ctx, model)
	if err != nil {
		return 0, nil, "", err
	}
	via := p.DecideVia()
	body = provider.DecideAsk(via, model, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", RouterAgent)
	if p.IsRemoteMagpie() {
		req.Header.Set("User-Agent", "magpie/"+Version)
		passOnCaller(ctx, req)
	}
	if via == provider.ViaVercelEval {
		// what the AI SDK's gateway provider sends with an evaluation
		req.Header.Set("ai-gateway-protocol-version", "0.0.1")
		req.Header.Set("ai-gateway-auth-method", "api-key")
		req.Header.Set("ai-model-id", model)
		req.Header.Set("ai-evaluation-model-specification-version", "4")
	}
	if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
		return 0, nil, "", err
	}
	res, err := p.Do(s.client, req)
	if err != nil {
		return 0, nil, "", fmt.Errorf("%s: %v", p.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 300 {
		b = provider.DecideAnswer(via, b)
	}
	return res.StatusCode, b, res.Header.Get("Content-Type"), nil
}

// maxSystemOneBody is the most a System One request may be; larger is 413
// rather than a truncated body parsed as not a System One request. A
// Clef's request may carry up to four embedded images, 13 MiB in all
// (ARNO on Discord: clef模型是支持图像输入的).
const maxSystemOneBody = 16 << 20

// serveSystemOne is magpie's own System One API. The model's prefix names
// the Jev provider (gptload-jev/jev-latest); the rest is what that provider
// is asked, at its /v1/systemone. The call is a route of its own, as a
// conversation is: the Routing view and the day's jsonl would otherwise
// never see Jev, which answers no /v1/chat/completions.
func (s *Server) serveSystemOne(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readRequestBody(w, r, provider.Chat, maxSystemOneBody)
	if !ok {
		return
	}
	var q struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &q) != nil {
		writeError(w, provider.Chat, http.StatusBadRequest, "not a System One request")
		return
	}
	p, model, err := provider.RouteDecider(q.Model)
	if err != nil {
		writeError(w, provider.Chat, provider.DecideRouteStatus(err), err.Error())
		return
	}
	body = withModel(body, model)
	start := time.Now()
	asked := q.Model
	if asked == "" {
		asked = p.ID + "/" + model
	}
	// a gateway key held to some models (#882) is refused another; a
	// decision model is a model, and System One asks one directly
	if keyWho, held := keyHolds(r); held && !modelAllowed(keyWho, p, model) {
		writeError(w, provider.Chat, http.StatusForbidden, keyModelError(keyWho, asked))
		return
	}
	// and held to some accounts (#905): the decision model's provider must
	// have one the key may use, the call spend landing on it as any's —
	// and asked on one such key, not the provider's first, which may be
	// one the key may not: of the keys in use, the first the key may use
	// and that is let serve the model, the request refused without one.
	// A provider with no key asks as it is: an account's tokens, or none
	// at all
	if accWho, accHeld := accountHolds(r); accHeld {
		if !accountServes(accWho, p, model) {
			writeError(w, provider.Chat, http.StatusForbidden, keyAccountsError(accWho, asked))
			return
		}
		if p.Account == nil && p.Key != "" {
			kept := false
			for _, k := range p.KeysOn() {
				if q := p.WithKey(k); accountAllowed(accWho, candidate{p: q, model: model}) && p.AccountServes(q.AccountID(), model) {
					p, kept = q, true
					break
				}
			}
			if !kept {
				writeError(w, provider.Chat, http.StatusForbidden, keyAccountsError(accWho, asked))
				return
			}
		}
	}
	seat := decideSeat(p, model)
	var used Usage
	who := callerOf(r)
	tr := s.trace.begin(Route{Time: start, Agent: who.agent, Session: sessionOf(r.Header), Model: asked, Provider: p.ID,
		Order: []Weighed{seat}, Tries: []Try{{ID: seat.ID, Model: model, Start: start}}})
	end := func(status int, msg string, tokens int) {
		ms := time.Since(start).Milliseconds()
		s.trace.update(tr, func(t *Route) {
			try := &t.Tries[0]
			try.Done, try.Status, try.Millis, try.Error = true, status, ms, msg
			if status >= 400 {
				try.Fail = failure(status, []byte(msg))
			}
			t.Done, t.Status, t.Error, t.Millis, t.Tokens = true, status, msg, ms, tokens
			t.Usage = routeUsage(p.ID, model, used)
		})
	}
	status, b, ctype, err := s.postDecide(r.Context(), p, model, body)
	if err != nil {
		writeError(w, provider.Chat, http.StatusBadGateway, err.Error())
		end(http.StatusBadGateway, err.Error(), 0)
		return
	}
	var use struct {
		Model string `json:"model"` // the one that answered, when the reply says
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(b, &use)
	errMsg := ""
	if status >= 300 {
		errMsg = provider.APIError(b, fmt.Sprintf("%d %s", status, http.StatusText(status)))
	}
	tokens := use.Usage.Input + use.Usage.Output
	used = Usage{Input: use.Usage.Input, Output: use.Usage.Output}
	var keyID, keyName string
	if p.Account == nil && p.Key != "" {
		keyID, keyName = provider.KeyID(p.Key), p.KeyName
	}
	appendUsage(r, usage.Record{RouteID: tr.ID, Time: start, Agent: who.agent, Via: who.via, Provider: p.ID, Host: p.Where(), Model: model, Requested: asked, Served: use.Model,
		ProviderKeyID: keyID, ProviderKeyName: keyName, ProviderAccount: accountOf(p),
		Input: use.Usage.Input, Output: use.Usage.Output, Millis: time.Since(start).Milliseconds(), Status: status})
	end(status, errMsg, tokens)
	if ctype == "" || status < 300 {
		ctype = "application/json"
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(status)
	w.Write(b)
}

// decideSeat is the one account a System One request is sent to: a decision
// provider has no pool of conversation keys, only the key it is asked with.
func decideSeat(p provider.Provider, model string) Weighed {
	w := Weighed{ID: p.ID, Provider: p.ID, Name: p.Name, Icon: p.Icon, Preset: p.Preset, Model: model, Kind: "provider"}
	if p.Key != "" {
		w.Kind, w.Who = "key", p.KeyName
		if w.Who == "" {
			w.Who = provider.Mask(p.Key)
		}
	}
	return w
}

// withEffort asks a request, in the client's own API, for reasoning at
// effort, in place of what the agent asked. Only a request that asked for
// reasoning is changed: one that didn't (a session title) stays without.
func withEffort(proto provider.Protocol, body []byte, effort string) []byte {
	var v struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Reasoning       *struct {
			Effort string `json:"effort"`
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
		// the effort alone: the rest of reasoning goes as the agent sent
		// it — Codex's Responses Lite asks for context "all_turns", which
		// the ChatGPT backend wants with its X-OpenAI-Internal-Codex-
		// Responses-Lite header ("requires `reasoning.context` to be
		// `all_turns`", #534)
		return withBodyEffort(proto, body, effort)
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

// requestEffort is the reasoning a request asks its model for, in its own
// API's words: reasoning_effort, reasoning.effort, or — for Anthropic, when
// it asks to think — output_config's effort or thinking's budget as the
// level it is nearest. "" when it asks for none.
func requestEffort(proto provider.Protocol, body []byte) string {
	if proto != provider.Anthropic {
		return bodyEffort(proto, body)
	}
	var v struct {
		Thinking *struct {
			Type   string `json:"type"`
			Budget int    `json:"budget_tokens"`
		} `json:"thinking"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	if json.Unmarshal(body, &v) != nil || v.Thinking == nil || v.Thinking.Type != "enabled" && v.Thinking.Type != "adaptive" {
		return ""
	}
	if e := effortOf(v.OutputConfig.Effort); e != "" {
		return e
	}
	return effortOfBudget(v.Thinking.Budget)
}

// sentEffort is the reasoning a request goes to model at: what it asks
// for, fitted to the model's levels as it is fitted on the way out.
func sentEffort(proto provider.Protocol, body []byte, p provider.Provider, model string) string {
	e := requestEffort(proto, body)
	if e == "" {
		return ""
	}
	return fitFor(p, model, e)
}

// fitFor is the effort a request asking for want goes to model at, at p:
// the level of its own nearest (fitEffort), and for a model known to have
// none to pick from — a thinking switch alone, as Xiaomi's mimo-v2.6-flash
// has — no more than high, which it takes where it turns max away (#214).
// A model whose levels just aren't known is asked as the agent asked.
func fitFor(p provider.Provider, model, want string) string {
	levels := p.Efforts(model)
	e := fitEffort(want, levels)
	if len(levels) == 0 && slices.Index(effortRank, e) > slices.Index(effortRank, "high") && p.Levelless(model) {
		return "high"
	}
	return e
}

// fitLevel is the level of the model's own nearest the one picked for it
// (fitEffort); one whose levels aren't known isn't asked for more than
// high, which every vendor with levels takes.
func fitLevel(want string, levels []string) string {
	level := fitEffort(want, levels)
	if len(levels) == 0 && level == "xhigh" {
		level = "high"
	}
	return level
}

// withFixedEffort asks a request, in the client's own API, for reasoning
// at effort — that of a group's member fixed at it (#189) — whatever the
// agent asked: unlike withEffort, a request that asked for none is asked
// for it too, and "none" turns reasoning off.
func withFixedEffort(proto provider.Protocol, body []byte, effort string) []byte {
	if effort == "" {
		return body
	}
	switch proto {
	case provider.Chat:
		return withFields(body, map[string]any{"reasoning_effort": effort})
	case provider.Responses:
		var v struct {
			Reasoning map[string]any `json:"reasoning"`
		}
		if json.Unmarshal(body, &v) != nil {
			return body
		}
		if v.Reasoning == nil {
			v.Reasoning = map[string]any{}
		}
		v.Reasoning["effort"] = effort
		return withFields(body, map[string]any{"reasoning": v.Reasoning})
	case provider.Anthropic:
		var v struct {
			Thinking *struct {
				Type string `json:"type"`
			} `json:"thinking"`
			MaxTokens int `json:"max_tokens"`
		}
		if json.Unmarshal(body, &v) != nil {
			return body
		}
		if effort == "none" {
			return withFields(body, map[string]any{"thinking": map[string]any{"type": "disabled"}})
		}
		level := effortOf(effort) // in Anthropic's words: minimal is low
		if v.Thinking != nil && (v.Thinking.Type == "enabled" || v.Thinking.Type == "adaptive") {
			return withEffort(proto, body, level)
		}
		// asked to think, with room left for the answer
		budget := budgetOf(level)
		if v.MaxTokens > 0 {
			budget = min(budget, v.MaxTokens-1)
		}
		if budget < 1024 {
			return body
		}
		return withFields(body, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": budget}})
	}
	return body
}

// groupLevels are the reasoning levels the group offers for the member a
// candidate is of, where the user named them (Group.Levels, #295): the
// group's own, else those of the outermost group in it that names some.
// nil when none is named. They bound what the candidate is asked for
// (withinLevels): the agent, a model's suffix or the turn's pick asking
// for a level the group doesn't offer is sent the nearest it does (#671).
func groupLevels(g provider.Group, ms []provider.Member, c candidate) []string {
	if len(g.Levels) > 0 {
		return g.Levels
	}
	for _, m := range ms {
		if m.Provider.ID != c.p.ID || m.Model != c.model || m.Effort != c.effort {
			continue
		}
		if i := slices.IndexFunc(m.Via, func(v provider.Group) bool { return len(v.Levels) > 0 }); i >= 0 {
			return m.Via[i].Levels
		}
	}
	return nil
}

// withinLevels is want, when levels offer it, else the level of levels
// nearest it (fitEffort). Codex's ultra is max with agents of its own, so
// a group that offers max offers it too.
func withinLevels(want string, levels []string) string {
	if want == "" || len(levels) == 0 || slices.Contains(levels, want) || want == "ultra" && slices.Contains(levels, "max") {
		return want
	}
	return fitEffort(want, levels)
}

// agentEffort is the reasoning level a request asks for in its own API's
// words: Chat's and Responses' as the agent named it (minimal and none
// kept), Anthropic's as requestEffort reads it. "" when it names none.
func agentEffort(proto provider.Protocol, body []byte) string {
	if proto == provider.Anthropic {
		return requestEffort(proto, body)
	}
	return strings.ToLower(strings.TrimSpace(bodyEffort(proto, body)))
}

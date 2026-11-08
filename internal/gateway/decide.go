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
	"errors"
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

// jevSure is how confident Jev must be of an intent for it to count, a
// group setting its own (provider.Group.IntentSure). Confidence runs from
// 0, every option as likely, to 1, one certain, as System One gives it
// (provider.Confidence), read from the answer's probabilities wherever it
// has them (sureOf).
const jevSure = 0.4

// sureOf is how sure an answer is of its choice on jevSure's scale: from
// the probability it gave each option where it gave two or more, else the
// confidence it said. A decision model's own confidence field is
// calibrated its own way: Cloudflare's clef-flash says 0.2866 of a
// 提醒我明天下午三点给 Dora 打电话 it put at about 0.68 (@VersunPan on X),
// where the same model run in MLX-Serve says its top probability, and Jev
// says the top over an even spread. Its probabilities say the same thing on
// every one, and an even spread of them is still 0.
func sureOf(confidence float64, ps map[string]any) float64 {
	n := 0
	for _, v := range ps {
		if _, ok := v.(float64); ok {
			n++
		}
	}
	if n < 2 {
		return confidence
	}
	return provider.Confidence(ps)
}

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
// from it is of its kind and wants its reasoning. What was said in it
// (prev.User, prev.Reply) is there too, for a short follow-up. work is what each
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
		// One kind is no scale, and a choice of one option is no question:
		// Clef turns it away, failing the whole call
		if len(intents) > 1 {
			qs["level"] = map[string]any{"type": "choice", "instructions": levelAsk(prev, work != nil), "criteria": levels}
		}

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

// jevState is what Jev is told of the turn: the message, what it said of
// the turn before, and what was said in it (turnBefore), each only when
// there is one. A short follow-up (继续, fix that) said alone came out
// the hardest kind (@HirotaNagai on X).
func jevState(prev before, effort bool, text string) map[string]string {
	st := map[string]string{"message": text}
	if prev.Intent != "" {
		st["previous_message_kind"] = prev.Intent
	}
	if effort && prev.Effort != "" {
		st["previous_message_reasoning"] = prev.Effort
	}
	if prev.User != "" {
		st["previous_user_message"] = prev.User
	}
	if prev.Reply != "" {
		st["previous_assistant_reply"] = prev.Reply
	}
	return st
}

func intentAsk(prev before) string {
	q := "The `message` is what a user asked a coding assistant. Which kind of request fits it best?"
	if prev.Intent != "" {
		q += " A message that only carries on from the user's message before it (go on, yes, do it, fix that) is of `previous_message_kind`; one that asks for something of its own is of the kind that fits it."
	}
	return q + priorAsk(prev)
}

// priorAsk is what Jev is asked to make of what came before the message,
// naming only the state's keys that are there.
func priorAsk(prev before) string {
	var keys []string
	if prev.User != "" {
		keys = append(keys, "`previous_user_message` (the user's message before it)")
	}
	if prev.Reply != "" {
		keys = append(keys, "`previous_assistant_reply` (the assistant's last reply before it)")
	}
	if len(keys) == 0 {
		return ""
	}
	return " " + strings.Join(keys, " and ") + " are context only: judge the `message`, and when it is short or vague (go on, yes, fix that, 继续) judge the work it asks for in that context."
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
	return q + priorAsk(prev)
}

// readJev is the verdict in a System One reply to jevBody, the answer
// without "none of these" counting when the intents are levels. The sure
// an intent needs is the group's own (rulesOf applies it): Jev's likeliest
// intent is kept as it said it (Said), and the intents' own answer as Said
// with Topic marking it a topic — a message may be none of those — rather
// than a level every message is at, whose torn pick holds (the level half
// below). Whether an intent came of it is decided where the group's own
// sure is known, so the answer still tells why it went as it did.
func readJev(b []byte, intents []string, levels bool, prev before) (verdict, error) {
	var out struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Choice        string         `json:"choice"`
			Score         float64        `json:"score"`
			Confidence    float64        `json:"confidence"`
			Probabilities map[string]any `json:"probabilities"`
		} `json:"answers"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := decodeJSON(b, &out); err != nil {
		return verdict{}, answerError{fmt.Errorf("not a System One answer")}
	}
	var v verdict
	if a, ok := out.Answers["intent"]; ok && len(intents) > 0 && !levels {
		v.Sure = sureOf(a.Confidence, a.Probabilities)
		v.Said = a.Choice // as it said it, no intent come of it or not (#4598)
		v.Topic = true
		if a.Choice != noIntent {
			v.Intent = intentNamed(intents, a.Choice)
		}
	}
	if a, ok := out.Answers["level"]; ok && len(intents) > 0 && levels {
		// every message is at one of them, however unsure Jev is: when it
		// is torn, the conversation stays at the level of the turn before
		// (继续，再加一个基准测试 after a 复杂任务 came out 0.54 to 0.46),
		// and a turn with none before takes the likelier
		v.Sure = sureOf(a.Confidence, a.Probabilities)
		v.Said = a.Choice
		v.Intent = intentNamed(intents, a.Choice)
		if was := intentNamed(intents, prev.Intent); v.Sure < jevSure && was != "" {
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

// sureNeeded is how sure a group asks its classifier to be of an intent:
// the group's own (IntentSure), or 0.4 as groups have always asked.
func sureNeeded(g provider.Group) float64 {
	if g.IntentSure != nil {
		return *g.IntentSure
	}
	return jevSure
}

// intentOf v is the intent of v's answer at the group's own sure
// (sureNeeded): a topic answer counts once Jev is sure enough of it —
// "none of these", said or picked, never being an intent — while a level
// is the level it said, every message being at one. A topic answer unsure
// at the group's sure is no intent.
func intentOf(v verdict, g provider.Group) string {
	if !v.Topic {
		return v.Intent
	}
	if v.Sure < sureNeeded(g) || v.Said == noIntent || v.Intent == "" {
		return ""
	}
	return v.Intent
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
	if decodeJSON(b, &out) != nil || out.Answers["levels"].Noul == nil {
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

// errNoAnswer is a System One call the classifier's wait cut off; askJev
// says which provider and how long it was given.
var errNoAnswer = errors.New("no answer in the classifier's wait")

// askJev asks a decision provider's model through its System One API,
// within ctx: the classifier's wait, which is wait long, that every try
// of askDecideTries shares.
func (s *Server) askJev(ctx context.Context, wait time.Duration, p provider.Provider, model string, intents []string, prev before, effort bool, text string) (v verdict, err error) {
	defer func() {
		// only a call cut off by the wait is reworded: a refusal or an
		// answer read as the wait ran out keeps its own type, which
		// askDecideTries rests the account by
		if errors.Is(err, errNoAnswer) {
			err = fmt.Errorf("%s gave no answer in %s", p.Name, wait)
		}
	}()
	if model == "" {
		model = p.Jev()
	}
	// what Jev reads goes masked unless Jev is itself where the user set
	// requests to go unmasked: a group whose every member skips redaction
	// hands rules the body as the agent wrote it (unredactedRoute), and a
	// remote Jev is none of those members
	if !p.SkipsRedaction() {
		var err error
		if text, prev.User, prev.Reply, err = maskedState(ctx, text, prev.User, prev.Reply); err != nil {
			return verdict{}, err // fails closed: never sent unmasked
		}
	}
	var l levelling
	if len(intents) > 1 { // one kind is a topic: a message may be none of it
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

// maskedState masks what Jev is told of a turn, each as maskedText does.
func maskedState(ctx context.Context, text, user, reply string) (string, string, string, error) {
	out := [3]string{text, user, reply}
	var all maskHits
	for i, v := range out {
		if v == "" {
			continue
		}
		_, m, h, err := maskTextHits(ctx, v)
		if err != nil {
			return "", "", "", err
		}
		out[i] = m
		all.add(h)
	}
	noteMasked(nil, "decide", all, strings.Join(out[:], "\n"), 0, "", "")
	return out[0], out[1], out[2], nil
}

// systemOne posts body to p's System One API and returns the answer,
// counting it in the usage as magpie's own. A gateway serving Jev is asked
// and answers in its own way (decideAsk, decideAnswer). With the request
// archive on it keeps the call as it keeps one a client sent (wangb on
// Discord: 请求存档开启, 但是 systemone 的请求没有记录下来), its usage row
// naming it.
func (s *Server) systemOne(ctx context.Context, p provider.Provider, model string, body []byte) ([]byte, error) {
	start := time.Now()
	d, err := s.postDecideSent(ctx, p, model, body)
	status, b, header := d.status, d.answer, d.header
	archived := ""
	if d.sent != nil {
		c := Call{Time: start, From: provider.Chat, To: provider.Chat, Model: p.ID + "/" + model, Provider: p.ID,
			Agent: usage.AgentOf(RouterAgent), Kind: "routing", Millis: time.Since(start).Milliseconds()}
		if err != nil {
			c.Error = err.Error()
		} else if status >= 300 {
			c.Error = provider.APIError(b, fmt.Sprintf("%d %s", status, http.StatusText(status)))
		}
		archived = archiveOwn(c, d.sent, d.body, status, header, b)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, errNoAnswer
		}
		return nil, err
	}
	if status >= 300 {
		return nil, decideRefused{fmt.Errorf("%s: %s", p.Name, provider.APIError(b, fmt.Sprintf("%d %s", status, http.StatusText(status)))), status, header, b}
	}
	var use struct {
		Model string `json:"model"` // the one that answered, when the reply says
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = decodeJSON(b, &use)
	var keyID, keyName string
	if p.Account == nil && p.Key != "" {
		keyID, keyName = provider.KeyID(p.Key), p.KeyName
	}
	usage.Append(usage.Record{Time: start, Agent: usage.AgentOf(RouterAgent), Kind: "routing", Provider: p.ID, Host: p.Where(), Model: model, Requested: model, Served: use.Model,
		ProviderKeyID: keyID, ProviderKeyName: keyName, ProviderAccount: accountOf(p),
		Input: use.Usage.Input, Output: use.Usage.Output, Millis: time.Since(start).Milliseconds(), Status: status, Archive: archived})
	return b, nil
}

// decided is a decision call as it went and came back: the request magpie
// sent and its body (in the provider's own shape), for the request archive,
// and the answer's status, body and headers.
type decided struct {
	sent   *http.Request
	body   []byte
	status int
	answer []byte
	header http.Header
}

// postDecideSent posts body to p's System One API as model (DecideAsk),
// and turns its answer back into Jev's shape when the provider answers in
// another one. DecideURL and Sign errors are as they are; a transport
// error is named with p. The request it sent is there once one was.
func (s *Server) postDecideSent(ctx context.Context, p provider.Provider, model string, body []byte) (decided, error) {
	// the headers the user gave the decision model
	ctx = provider.WithModel(ctx, p.ID, model)
	// every decision call goes through here — System One, a group rule's
	// classifier, a decision group's members: a model the user disabled
	// (#1867) is never asked
	if msg, off := modelOff(p, model, p.ID+"/"+model); off {
		return decided{}, errors.New(msg)
	}
	ctx = s.metered(p.Via(ctx), p, "") // counted against its MaxRPM (rpm.go)
	u, err := p.DecideModelURL(ctx, model)
	if err != nil {
		return decided{}, err
	}
	via := p.DecideVia()
	body = provider.DecideAsk(via, model, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return decided{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", RouterAgent)
	if p.IsRemoteMagpie() {
		req.Header.Set("User-Agent", "magpie/"+Version)
		passOnCaller(ctx, req)
	}
	p.DecideHeaders(req.Header)
	if via == provider.ViaVercelEval {
		// what the AI SDK's gateway provider sends with an evaluation
		req.Header.Set("ai-gateway-protocol-version", "0.0.1")
		req.Header.Set("ai-gateway-auth-method", "api-key")
		req.Header.Set("ai-model-id", model)
		req.Header.Set("ai-evaluation-model-specification-version", "4")
	}
	if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
		return decided{}, err
	}
	res, err := p.Do(s.client, req)
	if err != nil {
		return decided{sent: req, body: body}, fmt.Errorf("%s: %v", p.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 300 {
		b = provider.DecideAnswer(via, b)
	}
	return decided{sent: req, body: body, status: res.StatusCode, answer: b, header: res.Header}, nil
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
	s.serveSystemOneBody(w, r, body)
}

// serveSystemOneBody answers a System One request read: from
// /v1/systemone, or one /v1/decisions was translated to
// (decisions_systemone.go).
func (s *Server) serveSystemOneBody(w http.ResponseWriter, r *http.Request, body []byte) {
	var q struct {
		Model string `json:"model"`
	}
	if decodeJSON(body, &q) != nil {
		writeError(w, provider.Chat, http.StatusBadRequest, "not a System One request")
		return
	}
	// a decision model the user disabled (#1867), or a group of them all
	// disabled, is refused as a chat model is
	if refuseOff(w, provider.Chat, q.Model) {
		return
	}
	// a routing group of decision models: each of them in turn
	// (decide_group.go)
	if g, ms, ok, status, err := decisionGroupOf(q.Model); err != nil {
		writeError(w, provider.Chat, status, err.Error())
		return
	} else if ok {
		s.serveDecisionGroup(w, r, body, q.Model, g, ms)
		return
	}
	p, model, err := provider.RouteDecider(q.Model)
	if err != nil {
		// no decision model by that name, but one OpenAI's Decisions
		// API serves: asked there, the request and its answer
		// translated (decisions_systemone.go)
		if st := provider.DecideRouteStatus(err); st == http.StatusNotFound && s.systemOneByDecisions(w, r, body, q.Model) {
			return
		}
		writeError(w, provider.Chat, provider.DecideRouteStatus(err), err.Error())
		return
	}
	body = withModel(body, model)
	start := time.Now()
	asked := q.Model
	if asked == "" {
		asked = p.ID + "/" + model
	}
	if msg, off := modelOff(p, model, asked); off {
		writeError(w, provider.Chat, http.StatusForbidden, msg)
		return
	}
	// a gateway key held to some models (#882) is refused another; a
	// decision model is a model, and System One asks one directly
	if keyWho, held := keyHolds(r); held && !modelAllowed(keyWho, p, model) {
		st, msg := keyModelRefusal(w, keyWho, p.ID+"/"+model)
		writeError(w, provider.Chat, st, msg)
		return
	}
	// its keys or accounts in its routing's order, each asked until one
	// answers (decideKeys): one out of quota rests, and the next takes the
	// request (fflake33 on Discord). One the user set not to serve the
	// model is never asked; with every one so, the request is refused
	cs, pl := s.decideKeys(p, model)
	barred := len(cs) == 0 && len(pl.left) > 0 && !slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred })
	// and held to some accounts (#905): the decision model's provider must
	// have one the key may use and that is let serve the model, the call
	// spend landing on it as any's, and only those are asked
	if accWho, accHeld := accountHolds(r); accHeld {
		if !accountServes(accWho, p, model) || barred {
			writeError(w, provider.Chat, http.StatusForbidden, keyAccountsError(accWho, asked))
			return
		}
		var held bool
		if cs, pl, held = allowedCandidates(accWho, cs, pl, nil); len(cs) == 0 && held {
			writeError(w, provider.Chat, http.StatusForbidden, keyAccountsError(accWho, asked))
			return
		}
	}
	if barred {
		writeError(w, provider.Chat, http.StatusForbidden, barredError(asked, pl.left))
		return
	}
	cs = probeOne(cs) // of those resting, only the one back soonest (#1799)
	if len(cs) == 0 {
		writeError(w, provider.Chat, http.StatusServiceUnavailable, fmt.Sprintf("no key or account of %s may be asked %s now", p.Name, model))
		return
	}
	s.serveDecideTries(w, r, body, asked, start, cs, pl, nil)
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
	if effort == "" || decodeJSON(body, &v) != nil {
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
	if decodeJSON(body, &v) != nil || v.Thinking == nil || v.Thinking.Type != "enabled" && v.Thinking.Type != "adaptive" {
		return ""
	}
	if e := effortOf(v.OutputConfig.Effort); e != "" {
		return e
	}
	return effortOfBudget(v.Thinking.Budget)
}

// outputConfigEffort is the effort an Anthropic request's output_config
// names, whatever it asks of thinking; "" when it names none.
func outputConfigEffort(body []byte) string {
	var v struct {
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
	}
	if decodeJSON(body, &v) != nil {
		return ""
	}
	return effortOf(v.OutputConfig.Effort)
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
		if decodeJSON(body, &v) != nil {
			return body
		}
		if v.Reasoning == nil {
			v.Reasoning = map[string]any{}
		}
		v.Reasoning["effort"] = effort
		// and the agent's last change of effort mid-thread, which an
		// upstream reading the items would take over the top's
		return withEffortUpdated(withFields(body, map[string]any{"reasoning": v.Reasoning}), effort)
	case provider.Anthropic:
		var v struct {
			Thinking *struct {
				Type string `json:"type"`
			} `json:"thinking"`
			MaxTokens int `json:"max_tokens"`
		}
		if decodeJSON(body, &v) != nil {
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
// nil when none is named. A member given several levels of its own
// ("provider/model:low+high") is bounded by those instead. They bound what the candidate is asked for
// (withinLevels): the agent, a model's suffix or the turn's pick asking
// for a level the group doesn't offer is sent the nearest it does (#671).
func groupLevels(g provider.Group, ms []provider.Member, c candidate) []string {
	if len(c.levels) > 0 {
		// the member's own (Member.Levels): the agent picked among the
		// group's, and this member is sent the nearest of those it was
		// given
		return c.levels
	}
	if len(g.Levels) > 0 {
		return g.Levels
	}
	for _, m := range ms {
		if m.Provider.ID != c.p.ID || m.Model != c.model || m.Effort != c.effort || !slices.Equal(m.Levels, c.levels) {
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

// askedLevel is the reasoning level a request asks for (agentEffort),
// and "none" for an Anthropic one that turns thinking off: a member's
// EffortMaps may send it a level all the same.
func askedLevel(proto provider.Protocol, body []byte) string {
	if e := agentEffort(proto, body); e != "" || proto != provider.Anthropic {
		return e
	}
	var v struct {
		Thinking *struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	if decodeJSON(body, &v) == nil && v.Thinking != nil && v.Thinking.Type == "disabled" {
		return "none"
	}
	return ""
}

// mappedLevel is the level a member's map sends in place of in, and
// whether it names one. Codex's ultra is max with agents of its own, so
// a map from max takes it too.
func mappedLevel(to map[string]string, in string) (string, bool) {
	if in == "" {
		return "", false
	}
	if l, ok := to[in]; ok {
		return l, true
	}
	if in == "ultra" {
		l, ok := to["max"]
		return l, ok
	}
	return "", false
}

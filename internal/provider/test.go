package provider

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/probelog"
)

// Result is what a probe of one endpoint came back with.
type Result struct {
	Account  string   `json:"account,omitempty"`
	Protocol Protocol `json:"protocol"`
	OK       bool     `json:"ok"`
	Status   int      `json:"status,omitempty"`
	Millis   int64    `json:"ms"`
	Model    string   `json:"model,omitempty"`
	Error    string   `json:"error,omitempty"`
	// otherAPI: the vendor said the model isn't served on the API asked
	// (catalog.WrongEndpoint), so another may be asked instead
	otherAPI bool
	// V1 is the base the request was answered at once the /v1 it was
	// typed without was added (missingV1): the editor's URL takes it
	V1 string `json:"v1,omitempty"`
}

// Test sends the smallest possible request to each endpoint the vendor
// serves, signed with a key made for it and asking for a model that key
// sees, and reports what came back.
func (p Provider) Test(ctx context.Context) []Result {
	ctx = withUnreached(p.ownRequests(p.Via(ctx), probelog.KindTest))
	if p.DecideOnly() {
		return p.testDecide(ctx)
	}
	_, listErr := p.Fetch(ctx)
	if client := testClient(ctx); client != "" {
		// the one endpoint that agent's requests go to
		proto := clientProto(client, p.Speaks())
		q, model, ok := p.testWith(proto)
		if why := p.TestsAs(client); why != "" {
			return []Result{{Protocol: proto, Model: model, Error: why}}
		}
		if !ok {
			return []Result{{Protocol: proto, Model: model, Error: "no key is on for this endpoint"}}
		}
		if model == "" && q.zenPublic() {
			return []Result{{Protocol: proto, Error: zenPublicNone}}
		}
		url, body := tiny(q, proto, UpstreamName(p, model))
		ctx, b := q.tested(p.loggedAs(ctx, q), url, asClient(client, proto, body))
		return []Result{probe(ctx, q, proto, url, b, model, testWait)}
	}
	if p.isClaudeAccount() {
		model := p.testModel(p, Anthropic)
		q := p.routedTo(model)
		return []Result{q.testClaude(p.loggedAs(ctx, q), model)}
	}
	if p.testsTranslated() {
		model := p.testModel(p, CodeAssist)
		q := p.routedTo(model)
		return []Result{q.testTranslated(p.loggedAs(ctx, q), model)}
	}
	var out []Result
	for _, proto := range p.Speaks() {
		if p.IsVertex() && (proto == Chat || proto == Anthropic) {
			// a partner model, and Claude, answer only once the project
			// has turned them on in Model Garden: the provider is tested
			// on Gemini, and such a model by its own test (TestModel)
			continue
		}
		q, model, ok := p.testWith(proto)
		if !ok {
			out = append(out, Result{Protocol: proto, Model: model, Error: "no key is on for this endpoint"})
			continue
		}
		if model == "" && q.zenPublic() {
			// an API Zen serves no free model on: public reaches none
			// of its models, which is no fault of the key's
			continue
		}
		url, body := tiny(q, proto, UpstreamName(p, model))
		ctx, b := q.tested(p.loggedAs(ctx, q), url, body)
		out = append(out, probe(ctx, q, proto, url, b, model, testWait))
	}
	if len(out) == 0 && p.zenPublic() {
		out = append(out, Result{Protocol: Chat, Error: zenPublicNone})
	}
	if p.IsRemoteMagpie() {
		// The model list already checked its key and named its decisions.
		if model := p.Jev(); listErr == nil && model != "" {
			out = append(out, Result{Protocol: "decide", Model: model, OK: true, Status: http.StatusOK})
		}
	} else if p.Decides() {
		out = append(out, p.testDecide(ctx)...)
	}
	return out
}

// tiny is the smallest request for model on proto's endpoint, streamed
// to a backend that only streams.
func tiny(q Provider, proto Protocol, model string) (url, body string) {
	if kind := retrievalOf(model); kind != "" && proto == Chat && q.Account == nil {
		// an embedding or rerank model chats nowhere: it is asked where
		// the gateway sends it (retrieve), the OpenAI-style base's
		// /embeddings or /rerank (heatonz on Discord: a relay serving
		// only those detected nothing, asked on /chat/completions)
		return tinyRetrieval(q.OpenAIBase(), kind, model)
	}
	url, body = tinyBody(q, proto, model)
	if q.Account != nil && q.Account.Stream && body != "" {
		body = strings.TrimSuffix(body, "}") + `,"stream":true}`
	}
	if q.OpenCodeFree(model) && body != "" {
		url, body = OpenCodeFreeURL(url, proto), zenFreeProbe(proto, body)
	}
	if q.IsCline() && body != "" {
		body = clineProbe(body)
	}
	return url, body
}

// retrievalOf is the retrieval API model is made for, as its id says
// (catalog.ServesOf): embeddings or rerank, else "".
func retrievalOf(model string) string {
	switch s := catalog.ServesOf(model); s {
	case catalog.ServesEmbedding, catalog.ServesRerank:
		return s
	}
	return ""
}

// tinyRetrieval is the smallest request for a model of kind's at an
// OpenAI-style base: one input to embed, or one document to rank.
func tinyRetrieval(base, kind, model string) (url, body string) {
	base = strings.TrimRight(base, "/")
	if kind == catalog.ServesRerank {
		return base + "/rerank", fmt.Sprintf(`{"model":%q,"query":"hi","documents":["hi"]}`, model)
	}
	return base + "/embeddings", fmt.Sprintf(`{"model":%q,"input":"hi"}`, model)
}

// clineProbe is the smallest request asked as Cline's own clients ask:
// streamed, with room for the model to think before it answers. Cline's
// free models reason first, and asked for 16 tokens without a stream the
// Cline API answered 500 "empty response content" (ARNO on Discord) while
// an agent's request to the same model was answered. probe stops reading
// at the model's first word, so the room is not spent.
func clineProbe(body string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return body
	}
	m["stream"] = true
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if _, ok := m[k]; ok {
			m[k] = 1024
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return string(b)
}

// ModelTest says why p's models can't each be sent a test request ("" when
// they can): "decide" for a decision API whose models can't each be sent
// a System One question (AsksDecideModels), whose endpoint Test asks it
// for them; "own-api" for a sign-in reached
// through its agent's own API (Cursor, Devin, Kiro, Zed, Qoder, a Google
// sign-in), which the gateway translates every request for, so a probe
// has no endpoint to go to. A Google sign-in's are asked through the
// gateway's translator (testsTranslated). Vertex AI's generateContent is
// asked as it is.
func (p Provider) ModelTest() string {
	if p.DecideOnly() {
		if p.AsksDecideModels() {
			return ""
		}
		return "decide"
	}
	if p.isClaudeAccount() || p.testsTranslated() || p.IsVertex() {
		return ""
	}
	for _, pr := range p.Speaks() {
		if pr == Chat || pr == Responses || pr == Anthropic || pr == Gemini && p.Gemini != "" {
			return ""
		}
	}
	return "own-api"
}

// AsksDecideModels reports whether each of p's decision models can be
// sent a System One question of its own (TestModels): where the API is
// System One's, TypeSafe's or a gateway's that serves it as it is
// (Vercel's TypeSafe API, OpenRouter's), or Workers AI's, which DecideAsk
// wraps it for (ARNO on Discord: cloudflare-jev's models couldn't be
// tested from their right-click); not Vercel's evaluation models, which
// name the model in headers of their own.
func (p Provider) AsksDecideModels() bool {
	v := p.DecideVia()
	return p.Decides() && (v == ViaSystemOne || v == ViaVercel || v == ViaCloudflare)
}

// mantleBase is base for model at the prefix Mantle serves it under
// (MantleBase), for a Mantle provider; base for any other.
func (p Provider) mantleBase(base, model string) string {
	if !p.IsBedrockMantle() {
		return base
	}
	return MantleBase(base, model, false)
}

func tinyBody(q Provider, proto Protocol, model string) (url, body string) {
	switch proto {
	case Chat:
		if q.IsBedrock() || q.IsAzure() || OpenAIHost(HostOf(q.Base(Chat))) {
			// OpenAI's own API, Azure's and Bedrock's turn max_tokens away
			// on their reasoning models (GoxxKintBlade on X: every model
			// of an OpenAI key red, "Use 'max_completion_tokens' instead")
			return q.mantleBase(q.Chat, model) + "/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_completion_tokens":16}`, model)
		}
		return q.Base(Chat) + "/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model)
	case Responses:
		return q.mantleBase(q.Responses, model) + "/responses", fmt.Sprintf(`{"model":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":16}`, model)
	case Anthropic:
		if q.IsVertex() {
			// Claude at the project, as Anthropic's Vertex SDK asks it
			// (VertexClaudeRequest)
			at, b, _ := VertexClaudeRequest("/v1/messages", fmt.Appendf(nil, `{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model))
			return q.Base(Anthropic) + at, string(b)
		}
		return q.Anthropic + "/v1/messages", fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
	case Gemini:
		if q.IsVertex() {
			return vertexTest(q, model)
		}
		if q.FactoryGemini() {
			// Factory's generate route. droid sends no stream field.
			return q.Base(Gemini) + "/generate", fmt.Sprintf(`{"model":%q,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, model)
		}
		return q.Gemini + GeminiPath(model, false), `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":16}}`
	}
	return "", ""
}

// testWait is how long a probe waits for an answer; drawWait, for a
// picture, which takes an images API longer than a word takes chat;
// pluginTestWait, for a plugin's provider, whose fetch may renew the
// sign-in or ask its vendor more than once before it answers, as it does
// for the gateway, which gives it no such bound (mingxia_16024 on
// Discord: every Qoder and WorkBuddy model's test ended at 20s while
// agents were answered), and for a remote magpie, whose gateway serves
// such plugins' models in turn and asks again where its own retries do
// (plugins#827: a plugin model tested in 60s here had 20s through it).
// connectWait is how long a probe waits for its connection (a TLS
// handshake done, through any proxy) before it gives up on the host: an
// address nothing answers at is said in that time, not the whole wait's
// (RIck on Discord: a failing test took too long to say so).
var (
	testWait       = 20 * time.Second
	drawWait       = 2 * time.Minute
	pluginTestWait = time.Minute
	connectWait    = 10 * time.Second
)

// unreached is the hosts one test run couldn't connect to, with why
// (host:port → string): its other requests there fail at once with the
// same words instead of each waiting out its own connectWait. An answer
// that is slow, or a model that doesn't answer, is the model's and is
// never put here.
type unreached struct{ sync.Map }

type unreachedKey struct{}

func withUnreached(ctx context.Context) context.Context {
	if ctx.Value(unreachedKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, unreachedKey{}, &unreached{})
}

// errNoConnect is the cause a probe's request is ended with when its
// connection wasn't had in connectWait.
var errNoConnect = errors.New("no connection")

// hostOnly reports whether err says the host can't be reached at all,
// whatever is asked of it: its name doesn't resolve, nothing takes the
// connection, the connection wasn't had in connectWait, or its
// certificate isn't trusted.
func hostOnly(err error) bool {
	var dns *net.DNSError
	var op *net.OpError
	var cert *tls.CertificateVerificationError
	return errors.Is(err, errNoConnect) || errors.As(err, &dns) || errors.As(err, &cert) || errors.As(err, &op) && op.Op == "dial"
}

// hostPort is u's host with its port, the scheme's when none is written.
func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "http" {
		return net.JoinHostPort(u.Hostname(), "80")
	}
	return net.JoinHostPort(u.Hostname(), "443")
}

// connecting is ctx ended with errNoConnect when no connection was had
// in connectWait; stop ends its timer.
func connecting(ctx context.Context) (_ context.Context, stop func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	t := time.AfterFunc(connectWait, func() { cancel(errNoConnect) })
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { t.Stop() }})
	return ctx, func() { t.Stop(); cancel(nil) }
}

// drawsOnImages is whether model is asked on p's images API, as the
// gateway draws with it there: chat would be turned away, or answered in
// no shape a chat test knows. OpenRouter's image-only models use its /images
// endpoint; its chat image models still use chat completions.
func (p Provider) drawsOnImages(model string) bool {
	return p.Account == nil && p.Chat != "" && catalog.ImagesAPI(model)
}

// chatGPTDraws is whether model is one of the GPT Image family a ChatGPT
// account draws with at its Codex backend's images API (the gateway's
// drawsCodex and drawsFamily): the backend takes any gpt-image-* name.
func (p Provider) chatGPTDraws(model string) bool {
	return p.Account != nil && p.Account.Agent == "codex" && p.Base(Responses) != "" && strings.HasPrefix(model, "gpt-image-")
}

// tinyDrawing is the smallest images request for model: one picture of
// next to nothing, at the lowest quality gpt-image offers, at the size
// the vendor draws by default (the smallest one takes differs by model).
func tinyDrawing(q Provider, model string) (url, body string) {
	req := map[string]any{"model": model, "prompt": "a dot", "n": 1}
	if m := strings.ToLower(model); strings.Contains(m, "gpt-image") || strings.Contains(m, "chatgpt-image") {
		req["quality"] = "low"
	}
	b, _ := json.Marshal(req)
	path := "/images/generations"
	if HostOf(q.Chat) == "openrouter.ai" {
		path = "/images"
	}
	return strings.TrimRight(q.Chat, "/") + path, string(b)
}

// TestModels sends each of models the smallest request, a few at a time,
// on the endpoint it's served on and with a key that sees it: whether each
// answers, not only whether the vendor does. Results are in models' order.
func (p Provider) TestModels(ctx context.Context, models []string) []Result {
	ctx = withUnreached(p.ownRequests(p.Via(ctx), probelog.KindTest))
	out := make([]Result, len(models))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, model := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := context.Cause(ctx); err != nil {
				// stopped, or out of time, before its turn came
				out[i] = Result{Model: model, Error: "not asked: " + err.Error()}
				return
			}
			out[i] = p.testOne(ctx, model)
		}()
	}
	wg.Wait()
	return out
}

func (p Provider) testOne(ctx context.Context, model string) Result {
	if pat, off := ModelDisabled(p.ID, model); off {
		// the user disabled it (#1867): not even a test reaches it
		return Result{Model: model, Error: fmt.Sprintf("model %s/%s is disabled in magpie (it matches %q in Settings › Models › Disabled models)", p.ID, model, pat)}
	}
	if p.DecidesModel(model) && p.AsksDecideModels() {
		// a decision model is sent the smallest System One question, a
		// yes-or-no (ARNO on Discord: a System One model typed in by hand
		// couldn't be tested from its right-click)
		t0 := time.Now()
		r := Result{Protocol: "decide", Model: model}
		err := p.AskSystemOne(ctx, model)
		r.Millis = time.Since(t0).Milliseconds()
		if err != nil {
			r.Error = err.Error()
			return r
		}
		r.OK, r.Status = true, http.StatusOK
		return r
	}
	client := testClient(ctx)
	if why := p.TestsAs(client); why != "" {
		return Result{Protocol: clientProto(client, p.Speaks()), Model: model, Error: why}
	}
	// the account a request for it goes to, not the pool's first: one
	// the user lets serve it whose own list has it, as routing takes it
	// (AccountsFor; #4992, scutken: a newapi pool's grok-4.7, served by
	// its third account alone, was asked of the first and failed 503 "no
	// available channel" while real requests were answered)
	acct, ok := p.accountFor(model)
	if !ok {
		return Result{Model: model, Error: "every account that's on is set not to serve this model"}
	}
	ctx, p = p.loggedAs(ctx, acct), acct
	if p.isClaudeAccount() {
		return p.testClaude(ctx, model)
	}
	if client == "" && p.chatGPTDraws(model) {
		// a ChatGPT account's GPT Image draws on its Codex backend's images
		// API, as the gateway draws it (drawImages): asked as a chat on
		// /responses it is refused, "The 'gpt-image-2.5' model is not
		// supported when using Codex with a ChatGPT account" (ChangQiu on
		// Discord)
		b, _ := json.Marshal(map[string]any{"model": model, "prompt": "a dot", "n": 1, "quality": "low"})
		return probe(ctx, p, Chat, strings.TrimRight(p.Base(Responses), "/")+"/images/generations", b, model, drawWait)
	}
	if p.testsTranslated() || client == "" && p.pluginTranslated(model) {
		return p.testTranslated(ctx, model)
	}
	var protos []Protocol
	for _, pr := range p.Speaks() {
		if pr == Chat || pr == Responses || pr == Anthropic || pr == Gemini {
			protos = append(protos, pr)
		}
	}
	if len(protos) == 0 || p.DecidesModel(model) {
		return Result{Model: model, Error: "this provider can't be sent a test request"}
	}
	// the endpoint the vendor's list says serves it, else Anthropic's for a
	// Claude model, else the one it prefers; an image model draws on the
	// chat endpoint's images API
	proto := protos[0]
	draws := client == "" && p.drawsOnImages(model) && slices.Contains(protos, Chat)
	if client != "" {
		// on the API that agent's requests go to (TestAs)
		proto = clientProto(client, protos)
	} else if draws {
		proto = Chat
	} else if retrievalOf(model) != "" && slices.Contains(protos, Chat) {
		// an embedding or rerank model, at the OpenAI-style base's
		// /embeddings or /rerank, where the gateway sends it (tiny)
		proto = Chat
	} else if apis := p.APIs(model); apis != nil {
		if i := slices.IndexFunc(protos, func(pr Protocol) bool { return slices.Contains(apis, pr) }); i >= 0 {
			proto = protos[i]
		}
	} else if isClaude(model) && slices.Contains(protos, Anthropic) {
		proto = Anthropic
	}
	// a key made for that endpoint, else one made for any, that sees it
	q, ok := p.keySeeing(proto, model)
	if !ok {
		return Result{Protocol: proto, Model: model, Error: "no key that's on sees this model"}
	}
	if draws {
		// as the gateway does, one the images API doesn't serve is tried
		// in chat, and only its answer said when that fails too. Both go
		// out under the name magpie knows the model by: the gateway builds
		// those bodies itself and an upstream name is never written into
		// one, so asking for the model's own here is what tests a drawing
		// the way a real one is made — a name the vendor serves drawings
		// by is not one magpie sends them as
		url, body := tinyDrawing(q, model)
		r := probe(ctx, q, proto, url, []byte(body), model, drawWait)
		if !r.OK && ((r.Status == 404 || r.Status == 405) && HostOf(q.Chat) != "openrouter.ai" || r.Status == 400 && catalog.PointsToChat(r.Error)) {
			url, body := tiny(q, proto, model)
			ctx, b := q.tested(ctx, url, body)
			if c := probe(ctx, q, proto, url, b, model, testWait); c.OK {
				return c
			}
		}
		return r
	}
	r := q.testOn(ctx, client, proto, model, UpstreamName(p, model))
	if !r.OK && client == "" && proto == Chat && r.Status == 400 && catalog.DrawsID(model) && catalog.PointsToImagesAPI(r.Error) {
		// an image model its name doesn't place on the images API
		// (agnes-image-2.5-flash, #1837), which the vendor turns away in
		// chat naming that API: the gateway draws with it there, so it is
		// tested there
		url, body := tinyDrawing(q, model)
		if d := probe(ctx, q, proto, url, []byte(body), model, drawWait); d.OK {
			return d
		}
	}
	if !r.OK && r.otherAPI && client != ClientClaudeCode && retrievalOf(model) == "" {
		// the vendor's word that the model isn't served on that API: it
		// is asked on the next one that may serve it, as the gateway asks
		// a request there (usable), and that one's answer is the model's
		// (#3766, char1eslu: models AgentRouter turns away on chat
		// completions were red there while the gateway answered them on
		// Responses). As Codex it is asked with Codex's headers, as the
		// gateway sends them on Responses and Chat; as Claude Code it
		// stays, since the gateway sends Claude Code's on Messages alone.
		apis := p.APIs(model)
		for _, next := range protos {
			if next == proto || !(apis == nil && !p.NamedOnlyOn(next) || slices.Contains(apis, next)) {
				continue
			}
			q, ok := p.keySeeing(next, model)
			if !ok {
				continue
			}
			if n := q.testOn(ctx, client, next, model, UpstreamName(p, model)); n.OK || !n.otherAPI {
				return n
			}
		}
	}
	return r
}

// keySeeing is p with the key a test of model on proto goes with: of
// the keys routing takes the model to (KeysFor: one the user lets serve
// it, whose plan and list have it), one made for that API, else one made
// for any. A provider without keys is left as it is.
func (p Provider) keySeeing(proto Protocol, model string) (Provider, bool) {
	if len(p.KeysOn()) == 0 {
		return p, true
	}
	keys, _, _, _ := p.KeysFor(model, nil)
	for _, want := range []Protocol{proto, ""} {
		for _, k := range keys {
			if k.Protocol == want {
				return p.WithKey(k.KeyAccount), true
			}
		}
	}
	return p, false
}

// testWith is the key or account an endpoint's test goes with, and the
// model it asks: the first key on made for proto, else for any, with a
// model of its to test there (one the user lets it serve, AccountModels),
// else the first such key with none; a provider without keys, with the
// model picked from every account's, as the account that serves it
// (routedTo). ok is false when no key is on for proto.
func (p Provider) testWith(proto Protocol) (Provider, string, bool) {
	keys := p.KeysOn()
	if len(keys) == 0 {
		model := p.testModel(p, proto)
		return p.routedTo(model), model, true
	}
	var first Provider
	found := false
	for _, want := range []Protocol{proto, ""} {
		for _, k := range keys {
			if k.Protocol != want {
				continue
			}
			q := p.WithKey(k)
			if model := p.testModel(q, proto); model != "" {
				return q, model, true
			}
			if !found {
				first, found = q, true
			}
		}
	}
	if found {
		return first, "", true
	}
	return p, "", false
}

// accountFor is p as the account of its accounts on a request for model
// goes to first (AccountsFor); ok is false when the user set every one
// not to serve it. A provider of keys is left as it is.
func (p Provider) accountFor(model string) (Provider, bool) {
	if p.Account == nil {
		return p, true
	}
	out, _, _ := p.AccountsFor(model, nil)
	if len(out) == 0 {
		return p, false
	}
	return out[0].Provider, true
}

// routedTo is q, the key or account an endpoint's test goes with, as the
// account a request for model goes to: the model is picked from every
// account's on, and asked of one that serves it (#4992). With no model,
// or none that may serve it, q is left as it is.
func (q Provider) routedTo(model string) Provider {
	if model == "" {
		return q
	}
	if a, ok := q.accountFor(model); ok {
		return a
	}
	return q
}

// loggedAs is ctx with its test's row in the request log named for q,
// the account the test asked, when that is another of p's than p (the
// row says who answered, as the gateway's rows do).
func (p Provider) loggedAs(ctx context.Context, q Provider) context.Context {
	if q.Account == nil || p.Account != nil && q.Account.User == p.Account.User {
		return ctx
	}
	return q.ownRequests(ctx, probelog.KindTest)
}

// testOn is model's test on proto's endpoint with p's key, asked for by
// its upstream name, as client would ask it when one is named (TestAs).
func (p Provider) testOn(ctx context.Context, client string, proto Protocol, model, upstream string) Result {
	url, body := tiny(p, proto, upstream)
	if client != "" {
		body = asClient(client, proto, body)
	}
	ctx, b := p.tested(ctx, url, body)
	return probe(ctx, p, proto, url, b, model, testWait)
}

// keyFor is p using the first key on that works with proto: one made for
// it, else one made for any. A provider without keys is left as it is.
func (p Provider) keyFor(proto Protocol) (Provider, bool) {
	keys := p.KeysOn()
	if len(keys) == 0 {
		return p, true
	}
	for _, want := range []Protocol{proto, ""} {
		for _, k := range keys {
			if k.Protocol == want {
				return p.WithKey(k), true
			}
		}
	}
	return p, false
}

// zenPublic is whether p is OpenCode Zen asked with the key OpenCode
// sends signed out (public), which Zen's free models alone answer.
func (p Provider) zenPublic() bool {
	return p.IsOpenCode() && p.Key == OpenCodeAnonymousKey
}

// zenPublicNone is why Zen's public key has nothing to test.
const zenPublicNone = "no free model of OpenCode Zen is listed for this API: the public key reaches only the free ones (refresh the model list, or add a Zen key for the rest)"

// testModel is the model a probe of proto's endpoint asks for: the first
// exposed one q's key sees, else the first it sees at all — preferring a
// Claude model on the Anthropic endpoint, and one that chats to one that
// draws on an images API. On OpenCode's gateway a model models.dev names
// no API for is served on chat alone, so no other endpoint is tried with
// it; and with Zen's public key only a free model is tried, on an API it
// is served on, or none (z'x on Discord: Test asked public for
// claude-fable-5 on Messages, gemini-3-flash on Gemini and big-pickle on
// Responses, and every one failed, though big-pickle answers on chat).
func (p Provider) testModel(q Provider, proto Protocol) string {
	k := q.first()
	// a model the key's vendor lists it, and the user lets it serve
	// (AccountModels, #474), as routing has it
	sees := func(id string) bool {
		return k.Key == "" || p.Serves(k, id) && p.AccountServes(keyID(k.Key), id)
	}
	public := q.zenPublic()
	off := disabledNow() // a model the user disabled is never asked (#1867)
	var pools [][]catalog.Model
	if ms := p.Exposed(); len(ms) > 0 {
		pools = append(pools, ms)
	}
	pools = append(pools, p.Available())
	for _, want := range []func(string) bool{
		func(id string) bool {
			if p.drawsOnImages(id) {
				return false
			}
			if apis := p.APIs(id); apis != nil {
				return slices.Contains(apis, proto)
			}
			if p.IsOpenCode() {
				// each model on one API, chat for the rest (openCodeEndpoints)
				return proto == Chat || proto == Anthropic && isClaude(id)
			}
			return (proto != Anthropic || isClaude(id)) && !p.NamedOnlyOn(proto)
		},
		func(string) bool { return !public },
	} {
		for _, pool := range pools {
			for _, m := range pool {
				if public && !p.OpenCodeFree(m.ID) {
					continue
				}
				if !p.DecidesModel(m.ID) && !off(p.ID, m.ID) && want(m.ID) && sees(m.ID) {
					return m.ID
				}
			}
		}
	}
	if proto == Chat && q.Account == nil && !public {
		// none to chat with: an embedding or rerank model its list
		// keeps apart (catalog.LiveServing), asked where the gateway
		// sends it (heatonz on Discord: a provider serving only those)
		// or one its preset names (Voyage AI, which lists none)
		for _, m := range append(catalog.LiveServing(p.ID, ""), p.PlanRetrieval("")...) {
			if retrievalOf(m.ID) != "" && !off(p.ID, m.ID) && sees(m.ID) {
				return m.ID
			}
		}
	}
	return ""
}

func isClaude(id string) bool {
	id = strings.ToLower(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(id, "claude")
}

// AuthHeaders is how a request to the vendor proves who it is. Anthropic's
// own API wants x-api-key alone, and so does Bedrock's, which turns away a
// request with both (#176); other compatible vendors take either, so both.
// Azure OpenAI takes a key in api-key alone: a Bearer there is an Entra ID
// token, and the key sent as one is turned away.
func AuthHeaders(p Provider, proto Protocol) map[string]string {
	// Vertex AI's is the Google token Sign sets, which a key would replace
	if p.Key == "" || p.IsVertex() {
		return map[string]string{}
	}
	if p.IsAzure() {
		if proto == Anthropic {
			return map[string]string{"x-api-key": p.Key}
		}
		return map[string]string{"api-key": p.Key}
	}
	if proto == Gemini && !p.FactoryGemini() {
		// Google's Gemini API takes an API key here, and turns one away as
		// a Bearer token ("Expected OAuth 2 access token"); relays that
		// answer as it does read it here too
		return map[string]string{"x-goog-api-key": p.Key}
	}
	if proto == Anthropic {
		if strings.HasSuffix(p.Host(), "anthropic.com") || p.IsBedrock() {
			return map[string]string{"x-api-key": p.Key}
		}
		// a vendor that takes one alone (AuthHeader, RetryOneAuth)
		switch p.AuthHeader {
		case AuthXAPIKey:
			return map[string]string{"x-api-key": p.Key}
		case AuthBearer:
			return map[string]string{"Authorization": "Bearer " + p.Key}
		}
		return map[string]string{"x-api-key": p.Key, "Authorization": "Bearer " + p.Key}
	}
	return map[string]string{"Authorization": "Bearer " + p.Key}
}

// ownRequests is ctx with the requests sent under it written into the
// request log as magpie's own, of kind (probelog), for p.
func (p Provider) ownRequests(ctx context.Context, kind string) context.Context {
	account := ""
	if p.Account != nil {
		account = p.Account.User
	}
	return probelog.With(ctx, kind, p.ID, account)
}

// The test is one row of the request log, written here with what came of
// it: the vendor's error, a stream's error after its 200 too. Do would
// write the status alone, so a failed test's row said nothing of why
// (ysm119 on Discord: "no answer", and no error to find anywhere).
func probe(ctx context.Context, p Provider, proto Protocol, url string, body []byte, model string, wait time.Duration) Result {
	r := probeAt(ctx, p, proto, url, body, model, wait)
	if r.Status != http.StatusNotFound {
		return r
	}
	// a relay given without the /v1 it serves OpenAI's API under
	// (Cyrus_4433 on X: claude-relay-service's …/openai, whose chat is at
	// …/openai/v1/chat/completions, answered every model 404 Not Found)
	// is asked there too, and an answer there sets the base right, so
	// the gateway's requests go there as well
	if base, at, ok := p.missingV1(proto, url); ok {
		if v := probeAt(ctx, p, proto, at, body, model, wait); v.OK {
			p.setV1(base)
			v.V1 = base + "/v1"
			return v
		}
	}
	return r
}

func probeAt(ctx context.Context, p Provider, proto Protocol, url string, body []byte, model string, wait time.Duration) Result {
	r := probeOnce(ctx, p, proto, url, body, model, wait)
	if proto == Chat && r.Status == http.StatusBadRequest {
		// a vendor whose 400 names the other reply-length field as the
		// one to send (a relay in front of OpenAI's reasoning models, or
		// one that reads max_tokens alone) is asked once more with it, as
		// the gateway asks it (forward)
		if b, ok := WithTokenField(body, TokenFieldWanted([]byte(r.Error))); ok {
			return probeOnce(ctx, p, proto, url, b, model, wait)
		}
	}
	if (r.Status == http.StatusBadRequest || r.Status == http.StatusUnprocessableEntity) && StreamRequired([]byte(r.Error)) {
		// a backend that only streams (a claude-relay-service's
		// Responses, Cyrus_4433 on X: "Stream must be set to true") is
		// asked once more streamed, as the gateway asks it
		if b, ok := WithStream(body); ok {
			return probeOnce(ctx, p, proto, url, b, model, wait)
		}
	}
	return r
}

// missingV1 is the base of url, proto's endpoint at an OpenAI-style base
// with no version in its path, and the same endpoint under that base's
// /v1. Only a key's own base is asked so: a sign-in, a plugin, Azure,
// Bedrock and Vertex AI have paths of their own, and a versioned base
// (…/api/plan/v3) is the vendor's API as written (fixV1).
func (p Provider) missingV1(proto Protocol, url string) (base, at string, ok bool) {
	end := map[Protocol]string{Chat: "/chat/completions", Responses: "/responses"}[proto]
	if end == "" || p.Account != nil || p.IsAzure() || p.IsBedrock() || p.IsVertex() {
		return "", "", false
	}
	base, found := strings.CutSuffix(url, end)
	if !found || base == "" || catalog.Versioned(base) || strings.TrimRight(base, "/") != strings.TrimRight(p.Base(proto), "/") {
		return "", "", false
	}
	base = strings.TrimRight(base, "/")
	return base, base + "/v1" + end, true
}

func probeOnce(ctx context.Context, p Provider, proto Protocol, url string, body []byte, model string, wait time.Duration) (r Result) {
	ctx = WithModel(ctx, p.ID, model) // the model's own headers, as its requests carry
	r = Result{Protocol: proto, Model: model}
	if model == "" {
		r.Error = "no model to try: expose one, or refresh the model list"
		return r
	}
	if p.IsPlugin() {
		// the plugin host started and the account's loader run (a
		// sign-in renewed) under the caller's time, as a warm host
		// answers the gateway, not the probe's: a cold host took the
		// whole of it (mingxia_16024)
		p.warmPlugin(ctx)
		wait = max(wait, pluginTestWait)
	}
	if p.IsRemoteMagpie() {
		// it serves plugins' models too, and retries for them
		// (pluginTestWait)
		wait = max(wait, pluginTestWait)
	}
	ctx, cancel := context.WithTimeout(probelog.WithModel(ctx, model), wait)
	defer cancel()
	rctx := ctx
	logged := probelog.Pending(ctx)
	if logged {
		rctx = probelog.Taken(ctx)
	}
	if !p.IsPlugin() && (p.Account == nil || p.Account.transport == nil && p.Account.clientFor == nil) {
		// a plugin's or an account's own client may make no connection
		// that tells the trace
		var stop func()
		rctx, stop = connecting(rctx)
		defer stop()
	}
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	host := hostPort(req.URL)
	dead, _ := ctx.Value(unreachedKey{}).(*unreached)
	if dead != nil {
		if why, ok := dead.Load(host); ok {
			r.Error = why.(string)
			return r
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if proto == Anthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if OpenCodeURL(url) {
		OpenCodeClient(req.Header, "", "") // a session of the probe's own, from the home folder
	}
	if p.IsCline() {
		ClineClient(req.Header)
	}
	if p.IsKilo() {
		KiloClient(req.Header, p.Key, "")
	}
	if client := testClient(ctx); client != "" {
		clientHeaders(client, req.Header)
	}
	if p.IsPlugin() {
		// a model its vendor has queued is said so at once, not waited
		// out for minutes as an agent's turn is (Qoder's free models)
		req.Header.Set(FallbackHeader, "1")
	}
	if p.IsRemoteMagpie() {
		// its gateway takes no pause that ends past the test's wait, and
		// the test is told the error (WaitHeader, plugins#827)
		req.Header.Set(WaitHeader, strconv.Itoa(int(wait/time.Second)))
	}
	if err := p.Sign(ctx, req, proto, body); err != nil {
		r.Error = err.Error()
		return r
	}
	if p.chatGPTDraws(model) && strings.HasSuffix(req.URL.Path, "/images/generations") {
		// as the gateway asks it: the images API answers in JSON, not the
		// stream Codex's signing asks of its Responses, with Codex CLI's
		// own ids (codex-rs ext/image-generation)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-codex-imagegen-request-id", randomUUID())
		req.Header.Set("x-codex-image-turn-id", randomUUID())
	}
	start := time.Now()
	// each request sent, a retry's too, is a row; the last one carries
	// what the test came to
	type try struct {
		url    string
		start  time.Time
		status int
		err    error
	}
	var tries []try
	send := func(q *http.Request) (*http.Response, error) {
		t := try{url: q.URL.String(), start: time.Now()}
		res, err := p.Do(http.DefaultClient, q)
		if t.err = err; err == nil {
			t.status = res.StatusCode
		}
		tries = append(tries, t)
		return res, err
	}
	if logged {
		defer func() {
			for i, t := range tries {
				if i == len(tries)-1 {
					if r.Status != 0 {
						t.status = r.Status
					}
					if r.Error != "" {
						t.err = errors.New(r.Error)
					}
				}
				probelog.Note(ctx, t.url, http.MethodPost, t.start, t.status, t.err)
			}
		}()
	}
	res, err := send(req)
	if err == nil && proto == Anthropic {
		// a vendor that takes one of the two key headers alone
		res, err = RetryOneAuth(p, req, res, send)
	}
	if err == nil && proto == Chat {
		// a GLM Coding Plan's key at Zhipu's pay-as-you-go endpoint
		res, err = RetryZhipuCoding(p, req, res, send)
	}
	if err == nil {
		// Zhipu's Responses API refuses a key 200, in an envelope: the
		// refusal it is, not a green check (@xanahopper on X)
		res = ZhipuEnveloped(req, res)
	}
	r.Millis = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = strings.TrimPrefix(err.Error(), "Post \""+url+"\": ")
		if context.Cause(rctx) == errNoConnect {
			err, r.Error = errNoConnect, fmt.Sprintf("couldn't connect to %s in %s", req.URL.Host, connectWait)
		} else if ctx.Err() == context.DeadlineExceeded {
			r.Error = fmt.Sprintf("no answer in %s", time.Since(start).Round(time.Second))
		}
		if dead != nil && hostOnly(err) {
			dead.Store(host, r.Error)
		}
		return r
	}
	defer res.Body.Close()
	r.Status = res.StatusCode
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		// a stream is answered 200 before the model has said anything, and
		// can still fail in it: read on to its first word or its error
		if streams(body) {
			if msg, failed := streamAnswer(res.Body); failed {
				r.Error = msg
				if ctx.Err() == context.DeadlineExceeded {
					r.Error = fmt.Sprintf("no answer in %s", time.Since(start).Round(time.Second))
				}
				return r
			}
		}
		r.OK = true
		return r
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	r.Error = APIError(b, res.Status)
	r.otherAPI = catalog.WrongEndpoint(res.StatusCode, b)
	return r
}

// streams reports whether a request body asks for a stream.
func streams(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &v) == nil && v.Stream
}

// streamAnswer reads a streamed answer up to the first thing the model
// says — a word, a thought or a tool call, in Chat's, Responses' or
// Anthropic's events — and reports an error the stream gives before that.
// A stream that ends with neither was answered, as a 200 always was; one
// broken off, or held open past the probe's wait, was not.
func streamAnswer(r io.Reader) (msg string, failed bool) {
	sc := bufio.NewScanner(io.LimitReader(r, 1<<20))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev struct {
			Type     string          `json:"type"`
			Error    json.RawMessage `json:"error"`
			Response struct {
				Error json.RawMessage `json:"error"`
			} `json:"response"`
			Choices []struct {
				Delta map[string]any `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if ev.Type == "error" || len(ev.Error) > 0 && string(ev.Error) != "null" {
			return APIError([]byte(data), "error in the stream"), true
		}
		if ev.Type == "response.failed" {
			if len(ev.Response.Error) > 0 && string(ev.Response.Error) != "null" {
				return APIError([]byte(`{"error":`+string(ev.Response.Error)+`}`), "response failed"), true
			}
			return "response failed", true
		}
		if ev.Type == "content_block_start" || ev.Type == "content_block_delta" || ev.Type == "response.output_item.added" ||
			strings.HasPrefix(ev.Type, "response.") && strings.HasSuffix(ev.Type, ".delta") {
			return "", false
		}
		for _, c := range ev.Choices {
			for _, k := range []string{"content", "reasoning", "reasoning_content", "tool_calls"} {
				if v, ok := c.Delta[k]; ok && v != nil && v != "" {
					return "", false
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "the stream broke off before the model answered: " + err.Error(), true
	}
	return "", false
}

// BlockedHint is what a vendor's edge firewall blocking magpie's address
// means, in plain words: Alibaba Cloud's (ESA, in front of zcode.z.ai)
// answers a 405 HTML page, "Sorry, your request has been blocked due to
// unusual activity", linking errors.aliyun.com. Nothing in the request is
// at fault and magpie changes nothing about it: the address is.
const BlockedHint = "the provider's network firewall blocked requests from this IP; wait a while, or switch to another network or proxy"

// edgeBlocked matches such a block page, whoever's firewall served it:
// Alibaba Cloud's, and Cloudflare's — its WAF's "Sorry, you have been
// blocked" (titled "Attention Required! | Cloudflare"), its "Access denied
// | … used Cloudflare to restrict access" page, and the plain-text
// "error code: 1020" (and the other access-denied codes, 1005 to 1012) it
// answers a client that takes no HTML.
var edgeBlocked = lazyre.New(`(?i)request has been blocked|errors\.aliyun\.com|you have been blocked|used cloudflare to restrict access|error code: 10(0[5-9]|1[0-2]|20)\b`)

// EdgeBlocked says whether an error body is a firewall's block page rather
// than the vendor's API answering, or one already put in plain words.
func EdgeBlocked(b []byte) bool {
	return edgeBlocked.Match(b) || bytes.Contains(b, []byte(BlockedHint))
}

// restrictedHinted says an error body is one a plugin already put in
// magpie's words as an account its vendor restricted: ZCode's Start Plan
// 3012 (ZCodeRestrictedHint) keeps ZCode's own "request has been blocked
// due to unusual activity" in front of the hint, which edgeBlocked reads
// as a firewall's page. Told as that, the hint was cut, the agent was told
// to change networks, and the gateway rested the account as any failure,
// never as restricted: the 405 windy_66454 met (Discord, 10-31) read
// "405 Method Not Allowed — " BlockedHint, the same words ZCode's 3012
// and Alibaba Cloud's page were both told in.
func restrictedHinted(b []byte) bool {
	return bytes.Contains(b, []byte(ZCodeRestrictedHint)) || bytes.Contains(b, []byte(WBRestrictedHint))
}

// APIError pulls the human message out of an error body when there is one.
// A firewall's block page is put in plain words (BlockedHint); Google's
// "verify your account" refusal also says what to do about it, with the
// link it gave.
func APIError(b []byte, fallback string) string {
	if edgeBlocked.Match(b) && !restrictedHinted(b) {
		if said, ok := WebPage(b); ok && fallback != "" {
			return fallback + ": " + said + " — " + BlockedHint
		}
		if fallback == "" {
			return BlockedHint
		}
		return fallback + " — " + BlockedHint
	}
	if link, ok := Verification(b); ok {
		return VerifyMessage(apiError(b, fallback), link)
	}
	if cmdGoNoAPI.Match(b) {
		return apiError(b, fallback) + " — " + CommandCodeGoHint
	}
	return apiError(b, fallback)
}

// cmdGoNoAPI is Command Code's Provider API refusing a Go plan's key:
// "Your Go plan doesn't include API access. Upgrade to Provider or higher
// …" (#969). Go's key is only taken where Command Code's CLI asks, which
// the Command Code plugin does; an API-key provider (the "commandcode"
// preset) never can.
var cmdGoNoAPI = lazyre.New(`(?i)\bGo plan doesn.t include API access`)

// CommandCodeGoHint says where a Go plan's key works.
const CommandCodeGoHint = "a Go plan's key works in magpie through the Command Code plugin (Plugins › Command Code, then sign in with the key), which asks Command Code as its CLI does; a Command Code API-key provider can't use it"

// verifyWords is a refusal asking for the account to be verified, put in
// words rather than as VALIDATION_REQUIRED — or already by VerifyMessage.
var verifyWords = lazyre.New(`(?i)verify your account|account verification required|` + verifyAdvice)

// verifyLink is the link in a message VerifyMessage put in words.
var verifyLink = lazyre.New(verifyAdvice + `: open (https://[^\s"\\]+) in a browser`)

// Verification says whether an error body is Google's refusal of an account
// it wants verified first — Cloud Code Assist's 403 whose details say
// VALIDATION_REQUIRED (#152) — and the https link to verify it at, when the
// body has one. The link is taken as Gemini CLI takes it
// (classifyValidationRequiredError): the google.rpc.Help detail's link
// first, past a "Learn more" one, then the ErrorInfo's validation_link,
// then its validation_url.
func Verification(b []byte) (link string, ok bool) {
	var v struct {
		Error struct {
			Details []struct {
				Reason   string         `json:"reason"`
				Metadata map[string]any `json:"metadata"`
				Links    []struct {
					Description string `json:"description"`
					URL         string `json:"url"`
				} `json:"links"` // google.rpc.Help
			} `json:"details"`
		} `json:"error"`
	}
	parsed := json.Unmarshal(b, &v) == nil
	if parsed {
		help, meta := "", ""
		said := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
		for _, d := range v.Error.Details {
			if d.Reason == "VALIDATION_REQUIRED" {
				ok = true
				meta = cmp.Or(meta, said(d.Metadata, "validation_link"), said(d.Metadata, "validation_url"), said(d.Metadata, "validationUrl"))
			}
			for _, l := range d.Links {
				if !learnMore(l.Description, l.URL) {
					help = cmp.Or(help, l.URL)
				}
			}
		}
		if ok {
			link = cmp.Or(help, meta)
		}
	}
	if !ok && !verifyWords.Match(b) {
		return "", false
	}
	if link == "" {
		// said already, by VerifyMessage: in the body's words, which a
		// JSON error body holds escaped (& as \u0026, cut at the \ when
		// read raw)
		words := b
		if parsed {
			words = []byte(apiError(b, ""))
		}
		if m := verifyLink.FindSubmatch(words); m != nil {
			link = string(m[1])
		} else if m := verifyLink.FindSubmatch(b); m != nil {
			link = string(m[1])
		}
	}
	if u, err := url.Parse(link); err != nil || u.Scheme != "https" || u.Host == "" {
		link = ""
	}
	return link, true
}

// learnMore is a google.rpc.Help link to Google's help on verifying, which
// Gemini CLI tells from the link to verify at by its words or its host.
func learnMore(desc, link string) bool {
	if strings.EqualFold(strings.TrimSpace(desc), "learn more") {
		return true
	}
	u, err := url.Parse(link)
	return err == nil && u.Hostname() == "support.google.com"
}

// verifyAdvice marks a message VerifyMessage has already added to.
const verifyAdvice = "this Google account needs to be verified"

// VerifyMessage is a verification refusal as the agent and the app show it:
// the vendor's words, then what to do.
func VerifyMessage(said, link string) string {
	said = strings.TrimSpace(said)
	if strings.Contains(said, verifyAdvice) {
		return said
	}
	if link != "" {
		return said + " — " + verifyAdvice + ": open " + link + " in a browser signed in to it, verify it, then try again"
	}
	return said + " — " + verifyAdvice + ": open the Antigravity app (or Gemini CLI) signed in to it and do what it asks, then try again"
}

// ErrorType is the kind of error a vendor's body names — the error's type
// (rate_limit_error, usage_limit_reached), else its code — or "" when it
// names none: what to set beside the status when a request failed.
func ErrorType(b []byte) string {
	var v struct {
		Error json.RawMessage `json:"error"`
		Type  string          `json:"type"`
		Code  json.RawMessage `json:"code"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	name := func(raw json.RawMessage) string {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return strings.TrimSpace(s)
		}
		var n json.Number
		if json.Unmarshal(raw, &n) == nil {
			return n.String()
		}
		return ""
	}
	var e struct {
		Type string          `json:"type"`
		Code json.RawMessage `json:"code"`
	}
	if json.Unmarshal(v.Error, &e) == nil {
		if e.Type != "" {
			return e.Type
		}
		if c := name(e.Code); c != "" {
			return c
		}
	}
	if c := name(v.Code); c != "" {
		return c
	}
	if v.Type != "" && v.Type != "error" {
		return v.Type
	}
	return ""
}

func apiError(b []byte, fallback string) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Msg     string          `json:"msg"`    // Tencent's (WorkBuddy): {code, msg}
		Detail  json.RawMessage `json:"detail"` // FastAPI's (TypeSafe)
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"` // Cloudflare's
	}
	if json.Unmarshal(b, &v) == nil {
		if len(v.Detail) > 0 {
			v.Error = v.Detail
		}
		if len(v.Errors) > 0 && v.Errors[0].Message != "" {
			return v.Errors[0].Message
		}
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(v.Error, &e) == nil && e.Message != "" {
			return e.Message
		}
		var s string
		if json.Unmarshal(v.Error, &s) == nil && s != "" {
			return s
		}
		if m := cmp.Or(v.Message, v.Msg); m != "" {
			return m
		}
	}
	// a web page in place of the API's answer is said for what it is, and
	// an HTTP error with an empty body as refused with no details (a bare
	// "403 Forbidden" told the user nothing of a relay's firewall,
	// maralica on Discord); an empty stream event is no such refusal
	if said, ok := WebPage(b); ok {
		if fallback == "" {
			return said
		}
		return fallback + ": " + said
	}
	if len(bytes.TrimSpace(b)) == 0 {
		if statusLine(fallback) {
			return fallback + ": " + NoDetails
		}
		return fallback
	}
	// a body in no shape known is shown as it is, cut short, so what the
	// vendor said isn't lost; other markup (an XML error, a page's
	// fragment) by its words, its tags taken out
	s := strings.Join(strings.Fields(string(b)), " ")
	if strings.HasPrefix(s, "<") {
		if s = MarkupText(b); s == "" {
			return fallback
		}
	}
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return fallback + ": " + s
}

// NoDetails is what an error with an empty body is said as: the upstream
// refused and gave no reason, which an API seldom does and a firewall or
// proxy in front of it often does.
const NoDetails = "refused with no details (an empty reply, often from a firewall or proxy in front of the API rather than the API itself)"

// pageStart is how a web page begins, or a fragment of one that a proxy
// serves as its error.
var pageStart = lazyre.New(`(?i)^<(!doctype\s+html|html[\s>]|head[\s>]|body[\s>]|title[\s>]|meta[\s>])`)

// pageTitle is a web page's title.
var pageTitle = lazyre.New(`(?is)<title[^>]*>(.*?)</title>`)

// PageTitle is a web page's title as plain words, cut to 120 runes: no
// markup, entities read, whitespace folded. "" when there is none.
func PageTitle(b []byte) string {
	m := pageTitle.FindSubmatch(b)
	if m == nil {
		return ""
	}
	t := strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(string(m[1]), " "))), " ")
	if r := []rune(t); len(r) > 120 {
		t = string(r[:120]) + "…"
	}
	return t
}

// tagRe is a markup tag, taken out of a title that holds one.
var tagRe = lazyre.New(`<[^>]*>`)

// statusLine says a fallback is an HTTP error's status ("403 Forbidden",
// or its text alone, "Forbidden"), what an error body was answered with,
// not a stream event's or another reader's words.
func statusLine(s string) bool {
	if len(s) >= 3 && s[0] >= '1' && s[0] <= '5' && s[1] >= '0' && s[1] <= '9' && s[2] >= '0' && s[2] <= '9' && (len(s) == 3 || s[3] == ' ') {
		return true
	}
	for c := 400; c < 600; c++ {
		if t := http.StatusText(c); t != "" && t == s {
			return true
		}
	}
	return false
}

// Unsaid describes an HTTP error's body that holds no message of its
// own: an empty one (NoDetails), or a web page (WebPage). ok is false for
// any other body, whose message is the vendor's to give.
func Unsaid(b []byte) (string, bool) {
	if len(bytes.TrimSpace(b)) == 0 {
		return NoDetails, true
	}
	return WebPage(b)
}

// WebPage describes a web page served in place of the API's answer — a
// firewall's, a CDN's, a relay's own — by its title. ok is false for a
// body that isn't one.
func WebPage(b []byte) (string, bool) {
	t := bytes.TrimLeft(bytes.TrimSpace(b), "\ufeff")
	if !pageStart.Match(t) {
		return "", false
	}
	if title := PageTitle(t); title != "" {
		return "a web page, not an API reply: “" + title + "”", true
	}
	// one with no title by its words, which may say why (a relay's "only
	// … clients are allowed"), cut as a title is
	if said := MarkupText(t); said != "" {
		if r := []rune(said); len(r) > 120 {
			said = string(r[:120]) + "…"
		}
		return "a web page, not an API reply: “" + said + "”", true
	}
	return "a web page, not an API reply", true
}

// unshown is markup whose words aren't a reader's: a script, a style
// sheet, a comment, an XML declaration.
var unshown = lazyre.New(`(?is)<script[^>]*>.*?</script\s*>|<style[^>]*>.*?</style\s*>|<!--.*?-->|<\?.*?\?>|<!doctype[^>]*>`)

// MarkupText is the words markup holds, as plain words: scripts, styles
// and comments out, then every tag, entities read, whitespace folded, cut
// to 300 runes. "" when it holds none.
func MarkupText(b []byte) string {
	s := unshown.ReplaceAllString(string(b), " ")
	s = strings.Join(strings.Fields(html.UnescapeString(tagRe.ReplaceAllString(s, " "))), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

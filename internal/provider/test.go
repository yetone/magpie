package provider

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Result is what a probe of one endpoint came back with.
type Result struct {
	Protocol Protocol `json:"protocol"`
	OK       bool     `json:"ok"`
	Status   int      `json:"status,omitempty"`
	Millis   int64    `json:"ms"`
	Model    string   `json:"model,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Test sends the smallest possible request to each endpoint the vendor
// serves, signed with a key made for it and asking for a model that key
// sees, and reports what came back.
func (p Provider) Test(ctx context.Context) []Result {
	ctx = p.Via(ctx)
	if p.Decides() {
		return p.testDecide(ctx)
	}
	p.Fetch(ctx)
	if p.isClaudeAccount() {
		return []Result{p.testClaude(ctx, p.testModel(p, Anthropic))}
	}
	var out []Result
	for _, proto := range p.Speaks() {
		q, ok := p.keyFor(proto)
		model := p.testModel(q, proto)
		if !ok {
			out = append(out, Result{Protocol: proto, Model: model, Error: "no key is on for this endpoint"})
			continue
		}
		url, body := tiny(q, proto, UpstreamName(p, model))
		out = append(out, probe(ctx, q, proto, url, q.Prepare([]byte(body)), model, testWait))
	}
	return out
}

// tiny is the smallest request for model on proto's endpoint, streamed
// to a backend that only streams.
func tiny(q Provider, proto Protocol, model string) (url, body string) {
	url, body = tinyBody(q, proto, model)
	if q.Account != nil && q.Account.Stream && body != "" {
		body = strings.TrimSuffix(body, "}") + `,"stream":true}`
	}
	return url, body
}

func tinyBody(q Provider, proto Protocol, model string) (url, body string) {
	switch proto {
	case Chat:
		if q.IsBedrock() || q.IsAzure() {
			return q.Chat + "/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_completion_tokens":16}`, model)
		}
		return q.Chat + "/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, model)
	case Responses:
		return q.Responses + "/responses", fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":16}`, model)
	case Anthropic:
		return q.Anthropic + "/v1/messages", fmt.Sprintf(`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, model)
	}
	return "", ""
}

// testWait is how long a probe waits for an answer; drawWait, for a
// picture, which takes an images API longer than a word takes chat.
const (
	testWait = 20 * time.Second
	drawWait = 2 * time.Minute
)

// drawsOnImages is whether model is asked on p's images API, as the
// gateway draws with it there: chat would be turned away, or answered in
// no shape a chat test knows. OpenRouter draws everything in chat.
func (p Provider) drawsOnImages(model string) bool {
	return p.Account == nil && p.Chat != "" && HostOf(p.Chat) != "openrouter.ai" && catalog.ImagesAPI(model)
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
	return strings.TrimRight(q.Chat, "/") + "/images/generations", string(b)
}

// TestModels sends each of models the smallest request, a few at a time,
// on the endpoint it's served on and with a key that sees it: whether each
// answers, not only whether the vendor does. Results are in models' order.
func (p Provider) TestModels(ctx context.Context, models []string) []Result {
	ctx = p.Via(ctx)
	out := make([]Result, len(models))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, model := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = p.testOne(ctx, model)
		}()
	}
	wg.Wait()
	return out
}

func (p Provider) testOne(ctx context.Context, model string) Result {
	if p.isClaudeAccount() {
		return p.testClaude(ctx, model)
	}
	var protos []Protocol
	for _, pr := range p.Speaks() {
		if pr == Chat || pr == Responses || pr == Anthropic {
			protos = append(protos, pr)
		}
	}
	if len(protos) == 0 {
		return Result{Model: model, Error: "this provider can't be sent a test request"}
	}
	// the endpoint the vendor's list says serves it, else Anthropic's for a
	// Claude model, else the one it prefers; an image model draws on the
	// chat endpoint's images API
	proto := protos[0]
	draws := p.drawsOnImages(model) && slices.Contains(protos, Chat)
	if draws {
		proto = Chat
	} else if apis := p.APIs(model); apis != nil {
		if i := slices.IndexFunc(protos, func(pr Protocol) bool { return slices.Contains(apis, pr) }); i >= 0 {
			proto = protos[i]
		}
	} else if isClaude(model) && slices.Contains(protos, Anthropic) {
		proto = Anthropic
	}
	// a key made for that endpoint, else one made for any, that sees it
	q, ok := p, true
	if keys := p.KeysOn(); len(keys) > 0 {
		ok = false
	pick:
		for _, want := range []Protocol{proto, ""} {
			for _, k := range keys {
				if k.Protocol == want && p.Serves(k, model) {
					q, ok = p.WithKey(k), true
					break pick
				}
			}
		}
	}
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
		if !r.OK && (r.Status == 404 || r.Status == 405) {
			url, body := tiny(q, proto, model)
			if c := probe(ctx, q, proto, url, q.Prepare([]byte(body)), model, testWait); c.OK {
				return c
			}
		}
		return r
	}
	url, body := tiny(q, proto, UpstreamName(p, model))
	return probe(ctx, q, proto, url, q.Prepare([]byte(body)), model, testWait)
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

// testModel is the model a probe of proto's endpoint asks for: the first
// exposed one q's key sees, else the first it sees at all — preferring a
// Claude model on the Anthropic endpoint, and one that chats to one that
// draws on an images API.
func (p Provider) testModel(q Provider, proto Protocol) string {
	k := q.first()
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
			return proto != Anthropic || isClaude(id)
		},
		func(string) bool { return true },
	} {
		for _, pool := range pools {
			for _, m := range pool {
				if want(m.ID) && (k.Key == "" || p.Serves(k, m.ID)) {
					return m.ID
				}
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
	if p.Key == "" {
		return map[string]string{}
	}
	if p.IsAzure() {
		if proto == Anthropic {
			return map[string]string{"x-api-key": p.Key}
		}
		return map[string]string{"api-key": p.Key}
	}
	if proto == Anthropic {
		if strings.HasSuffix(p.Host(), "anthropic.com") || p.IsBedrock() {
			return map[string]string{"x-api-key": p.Key}
		}
		return map[string]string{"x-api-key": p.Key, "Authorization": "Bearer " + p.Key}
	}
	return map[string]string{"Authorization": "Bearer " + p.Key}
}

func probe(ctx context.Context, p Provider, proto Protocol, url string, body []byte, model string, wait time.Duration) Result {
	r := Result{Protocol: proto, Model: model}
	if model == "" {
		r.Error = "no model to try: expose one, or refresh the model list"
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if p.IsOpenCode() {
		OpenCodeClient(req.Header, "")
	}
	if err := p.Sign(ctx, req, proto, body); err != nil {
		r.Error = err.Error()
		return r
	}
	start := time.Now()
	res, err := p.Do(http.DefaultClient, req)
	r.Millis = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = strings.TrimPrefix(err.Error(), "Post \""+url+"\": ")
		return r
	}
	defer res.Body.Close()
	r.Status = res.StatusCode
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		r.OK = true
		return r
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	r.Error = APIError(b, res.Status)
	return r
}

// BlockedHint is what a vendor's edge firewall blocking magpie's address
// means, in plain words: Alibaba Cloud's (ESA, in front of zcode.z.ai)
// answers a 405 HTML page, "Sorry, your request has been blocked due to
// unusual activity", linking errors.aliyun.com. Nothing in the request is
// at fault and magpie changes nothing about it: the address is.
const BlockedHint = "the provider's network firewall blocked requests from this IP; wait a while, or switch to another network or proxy"

// edgeBlocked matches such a block page, whoever's firewall served it.
var edgeBlocked = regexp.MustCompile(`(?i)request has been blocked|errors\.aliyun\.com`)

// EdgeBlocked says whether an error body is a firewall's block page rather
// than the vendor's API answering, or one already put in plain words.
func EdgeBlocked(b []byte) bool {
	return edgeBlocked.Match(b) || bytes.Contains(b, []byte(BlockedHint)) || bytes.Contains(b, []byte(ZCodeStartBlockedHint))
}

// APIError pulls the human message out of an error body when there is one.
// A firewall's block page is put in plain words (BlockedHint); Google's
// "verify your account" refusal also says what to do about it, with the
// link it gave.
func APIError(b []byte, fallback string) string {
	if edgeBlocked.Match(b) {
		if fallback == "" {
			return BlockedHint
		}
		return fallback + " — " + BlockedHint
	}
	if link, ok := Verification(b); ok {
		return VerifyMessage(apiError(b, fallback), link)
	}
	return apiError(b, fallback)
}

// verifyWords is a refusal asking for the account to be verified, put in
// words rather than as VALIDATION_REQUIRED — or already by VerifyMessage.
var verifyWords = regexp.MustCompile(`(?i)verify your account|account verification required|` + verifyAdvice)

// verifyLink is the link in a message VerifyMessage put in words.
var verifyLink = regexp.MustCompile(verifyAdvice + `: open (https://[^\s"\\]+) in a browser`)

// Verification says whether an error body is Google's refusal of an account
// it wants verified first — Cloud Code Assist's 403 whose details say
// VALIDATION_REQUIRED with a validation_url in their metadata (#152) — and
// the https link to verify it at, when the body has one.
func Verification(b []byte) (link string, ok bool) {
	var v struct {
		Error struct {
			Details []struct {
				Reason   string            `json:"reason"`
				Metadata map[string]string `json:"metadata"`
				Links    []struct {
					URL string `json:"url"`
				} `json:"links"` // google.rpc.Help
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &v) == nil {
		help := ""
		for _, d := range v.Error.Details {
			if d.Reason == "VALIDATION_REQUIRED" {
				ok = true
				link = cmp.Or(link, d.Metadata["validation_url"], d.Metadata["validationUrl"])
			}
			for _, l := range d.Links {
				help = cmp.Or(help, l.URL)
			}
		}
		if ok && link == "" {
			link = help
		}
	}
	if !ok && !verifyWords.Match(b) {
		return "", false
	}
	if m := verifyLink.FindSubmatch(b); link == "" && m != nil {
		link = string(m[1]) // said already, by VerifyMessage
	}
	if u, err := url.Parse(link); err != nil || u.Scheme != "https" || u.Host == "" {
		link = ""
	}
	return link, true
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
	// a body in no shape known is shown as it is, cut short, so what the
	// vendor said isn't lost; an HTML page says nothing worth showing
	s := strings.Join(strings.Fields(string(b)), " ")
	if s == "" || strings.HasPrefix(s, "<") {
		return fallback
	}
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return fallback + ": " + s
}

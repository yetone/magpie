package provider

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Which APIs a vendor or relay serves behind the one URL the user typed is
// found by asking (01huadalang on Discord: 一键检测支持什么协议，发一个最小
// 请求看是否返回200): each of the three magpie speaks upstream — Chat
// Completions, Responses and Anthropic Messages — is sent the smallest
// request Test sends, at that URL as the API takes it, and what answered
// is what the provider can be given. Since a custom provider can keep a
// Gemini API's URL (#1346), Gemini's generateContent is asked too, at the
// …/v1beta a relay serves it under (NagaseMinato: 「检测」不会探测 Gemini
// 端点，为什么呢？).

// detectProtocols are the APIs a detection asks, in the order it says
// what they answered: magpie's three, then Gemini's.
var detectProtocols = []Protocol{Chat, Responses, Anthropic, Gemini}

// Detection is what one API answered at the URL it was asked at.
type Detection struct {
	Result
	// Base is the URL the provider keeps for it, as the editor's fields
	// take it: …/v1 for OpenAI's two, the root for Anthropic's, …/v1beta
	// for Gemini's.
	Base string `json:"base"`
}

// DetectBase is base as proto takes it: an endpoint pasted whole (…/v1/chat/
// completions, …/models/gemini-2.5-pro:generateContent) cut to its base, then the root for Anthropic, which adds
// /v1/messages itself, …/v1 for OpenAI's two when base is a bare host, and
// for Gemini's the …/v1beta beside a …/v1 or on a bare host (GeminiBase) —
// the editor's respellURL, in Go.
func DetectBase(base string, proto Protocol) string {
	u := strings.TrimRight(strings.TrimSpace(base), "/")
	for _, end := range []string{"/chat/completions", "/responses", "/v1/messages", "/messages"} {
		u = strings.TrimSuffix(u, end)
	}
	// a Gemini endpoint or a model list pasted whole
	u = strings.TrimRight(geminiMethod.ReplaceAllString(u, ""), "/")
	if u == "" {
		return ""
	}
	switch proto {
	case Anthropic:
		return strings.TrimSuffix(u, "/v1")
	case Gemini:
		if pu, err := url.Parse(u); err == nil && pu.Host != "" && strings.Trim(pu.Path, "/") == "v1" {
			return u + "beta"
		}
		return GeminiBase(u)
	}
	if pu, err := url.Parse(u); err == nil && strings.Trim(pu.Path, "/") == "" {
		return u + "/v1"
	}
	return u
}

// ErrNoURL is a detection with no URL to ask at.
var ErrNoURL = errors.New("type the base URL first")

// Detect asks each of Chat, Responses, Anthropic and Gemini at the URL p has for
// it, else at base as it takes it (DetectBase), the smallest request, and
// says what each answered, in that order. model is asked on all three;
// with none, one from p's list is picked for each — a Claude model on
// Anthropic's, an OpenAI one on Responses — and with no list, the vendor
// is asked for one first (a GET of its models, which costs nothing).
// Only those URLs are asked, and only a key's provider: a sign-in's API
// is its agent's.
func (p Provider) Detect(ctx context.Context, base, model string) ([]Detection, error) {
	q, err := p.detecting(base)
	if err != nil {
		return nil, err
	}
	ctx = q.Via(ctx)
	model = strings.TrimSpace(model)
	var ids []string
	if model == "" {
		ids = q.detectModels(ctx)
	}
	out := make([]Detection, len(detectProtocols))
	var wg sync.WaitGroup
	for i, proto := range detectProtocols {
		m := model
		if m == "" {
			m = detectModel(ids, proto)
		}
		var ask func(context.Context) Result
		out[i], ask = q.detectOne(proto, m, testWait)
		if ask == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i].Result = ask(ctx)
		}()
	}
	wg.Wait()
	return out, nil
}

// detecting is p with the URL it would be asked at for each API: its own,
// else base as that API takes it.
func (p Provider) detecting(base string) (Provider, error) {
	if p.Account != nil {
		return p, errors.New("a sign-in is asked on its agent's own API; there is nothing to detect")
	}
	q := p
	for _, proto := range detectProtocols {
		u := strings.TrimSpace(p.Base(proto))
		if u == "" {
			u = DetectBase(base, proto)
		}
		if u != "" {
			if pu, err := url.Parse(u); err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
				return p, errors.New("the base URL must start with http:// or https://, not " + u)
			}
		}
		switch proto {
		case Chat:
			q.Chat = u
		case Responses:
			q.Responses = u
		case Anthropic:
			q.Anthropic = u
		case Gemini:
			q.Gemini = u
		}
	}
	if len(q.Speaks()) == 0 {
		return p, ErrNoURL
	}
	return q, nil
}

// detectOne is model's detection on proto as far as it can be told
// without asking, and, when it can be asked, the ask (nil when not).
func (q Provider) detectOne(proto Protocol, m string, wait time.Duration) (Detection, func(context.Context) Result) {
	d := Detection{Result: Result{Protocol: proto, Model: m}, Base: q.Base(proto)}
	if d.Base == "" {
		d.Error = "no URL to ask"
		return d, nil
	}
	if m == "" {
		d.Error = "no model to try: type one the vendor serves"
		return d, nil
	}
	k, ok := q.keyFor(proto)
	if !ok {
		d.Error = "no key is on for this endpoint"
		return d, nil
	}
	return d, func(ctx context.Context) Result {
		u, body := tiny(k, proto, UpstreamName(q, m))
		return probe(ctx, k, proto, u, k.Prepare([]byte(body)), m, wait)
	}
}

// DetectDecide asks the System One API at p's decision URL (…/systemone
// pasted whole is cut to its root) the smallest question for model, or
// the one p is asked with when none is given, and says how it answered.
func (p Provider) DetectDecide(ctx context.Context, model string) Detection {
	p.Decide = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(p.Decide), "/"), "/systemone")
	if p.Decide != "" && !strings.Contains(p.Decide, "://") {
		p.Decide = "https://" + p.Decide
	}
	if model = strings.TrimSpace(model); model == "" {
		model = p.Jev()
	}
	d := Detection{Result: Result{Protocol: "decide", Model: model}, Base: p.Decide}
	switch {
	case p.Decide == "":
		d.Error = "no URL to ask"
		return d
	case strings.Contains(p.Decide, WorkspaceID):
		d.Error = "give the workspace ID first"
		return d
	}
	t0 := time.Now()
	err := p.AskSystemOne(p.Via(ctx), model)
	d.Millis = time.Since(t0).Milliseconds()
	if err != nil {
		d.Error = err.Error()
		return d
	}
	d.OK, d.Status = true, 200
	return d
}

// ModelDetection is what each API answered for one model
// (01huadalang on Discord: 应该能看出来选择的模型支持情况…有的仅支持
// response 有的双协议).
type ModelDetection struct {
	Model   string      `json:"model"`
	Results []Detection `json:"results"`
}

// A detection of many models asks at most DetectMax of them, DetectWide
// requests at a time, each waiting detectWait for its answer.
const (
	DetectMax  = 30
	DetectWide = 8
	detectWait = 12 * time.Second
)

// DetectModels asks each of models (the first DetectMax, an image model
// left out: it is asked on the images API) on each of Chat, Responses,
// Anthropic and Gemini at the URL p has for it, else at base as it takes it, and
// says what each answered, model by model; and, by API, the first answer
// from a model it served, else the first model's — what Detect says for
// one. A probe not started before ctx ends says so, unasked.
func (p Provider) DetectModels(ctx context.Context, base string, models []string) ([]ModelDetection, []Detection, error) {
	q, err := p.detecting(base)
	if err != nil {
		return nil, nil, err
	}
	var ids []string
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(ids, m) {
			ids = append(ids, m)
		}
	}
	if len(ids) == 0 {
		return nil, nil, errors.New("no model to try: type one the vendor serves")
	}
	if len(ids) > DetectMax {
		ids = ids[:DetectMax]
	}
	ctx = q.Via(ctx)
	out := make([]ModelDetection, len(ids))
	sem := make(chan struct{}, DetectWide)
	var wg sync.WaitGroup
	for i, m := range ids {
		out[i] = ModelDetection{Model: m, Results: make([]Detection, len(detectProtocols))}
		for j, proto := range detectProtocols {
			d, ask := q.detectOne(proto, m, detectWait)
			if ask != nil && catalog.ImagesAPI(m) {
				d.Error, ask = "an image model: asked on the images API, not here", nil
			}
			out[i].Results[j] = d
			if ask == nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					out[i].Results[j].Error = "not asked: out of time"
					return
				}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					out[i].Results[j].Error = "not asked: out of time"
					return
				}
				out[i].Results[j].Result = ask(ctx)
			}()
		}
	}
	wg.Wait()
	sum := make([]Detection, len(detectProtocols))
	for j := range detectProtocols {
		sum[j] = out[0].Results[j]
		for _, md := range out {
			if md.Results[j].OK {
				sum[j] = md.Results[j]
				break
			}
		}
	}
	return out, sum, nil
}

// detectModels are the models a detection may ask for: those p exposes and
// lists, else the vendor's list, asked for now and not kept.
func (p Provider) detectModels(ctx context.Context) []string {
	var ids []string
	if p.ID != "" {
		for _, ms := range [][]catalog.Model{p.Exposed(), p.Available()} {
			for _, m := range ms {
				if !slices.Contains(ids, m.ID) {
					ids = append(ids, m.ID)
				}
			}
		}
	}
	if len(ids) > 0 {
		return ids
	}
	ms, _, err := p.fetchOne(ctx)
	if err != nil {
		return nil
	}
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

// detectModel is the model of ids to ask proto for: one made for it — a
// Claude model on Anthropic's, an OpenAI one on Responses, a Gemini one on
// Gemini's, any other on Chat — else the first that chats.
func detectModel(ids []string, proto Protocol) string {
	made := func(id string) bool {
		switch proto {
		case Anthropic:
			return isClaude(id)
		case Responses:
			return openAIModel(id)
		case Gemini:
			return strings.Contains(strings.ToLower(id), "gemini")
		}
		return !isClaude(id)
	}
	for _, want := range []func(string) bool{made, func(string) bool { return true }} {
		for _, id := range ids {
			if want(id) && !catalog.ImagesAPI(id) {
				return id
			}
		}
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

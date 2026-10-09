package provider

// A provider with a decision API (Provider.Decide) — TypeSafe's System One,
// which Jev answers, or Jev as Vercel's AI Gateway or Cloudflare's Workers
// AI serve it — answers typed questions about a message with a choice and
// how likely each option was, so a
// routing group can ask it, as a user's turn begins, which of its rules'
// intents the message is and how hard the turn is to think about (see
// gateway/decide.go). A provider may also serve conversations; only its
// decision models are excluded from agents' lists and group members.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// JevLatest is the Jev a decision provider is asked with when the user
// picked none: TypeSafe's latest stable one.
const JevLatest = "jev-latest"

// BailianDecision is Alibaba Cloud Bailian's decision model, asked on the
// same System One API as Jev (#647).
const BailianDecision = "decision-model-preview"

// bailianDecides reports whether base is Bailian's: a workspace's host or
// the Token Plan's, <x>.<region>.maas.aliyuncs.com. Its /models lists
// Qwen's chat models, never the decision model.
func bailianDecides(base string) bool {
	h := HostOf(base)
	return strings.HasSuffix(h, ".maas.aliyuncs.com") || strings.HasSuffix(h, ".maas.qianwenaiapi.com")
}

// ownDecideModels are a System One provider's models that its vendor's
// list may not name: the ones picked for a decision-only provider (a
// vendor's own decision model needn't be called jev-…), else Bailian's.
func (p Provider) ownDecideModels() []catalog.Model {
	var out []catalog.Model
	if p.DecideOnly() {
		for _, m := range p.Models {
			if m = strings.TrimSpace(m); m != "" {
				out = append(out, catalog.Model{ID: m})
			}
		}
	}
	if len(out) == 0 && bailianDecides(p.Decide) {
		out = []catalog.Model{{ID: BailianDecision, Name: "Decision model (preview)"}}
	}
	return out
}

// Decides reports whether the provider is a decision API.
func (p Provider) Decides() bool { return p.Decide != "" }

// DecideOnly reports whether the provider has no conversation endpoint.
func (p Provider) DecideOnly() bool {
	return p.Decides() && p.Chat == "" && p.Responses == "" && p.Anthropic == "" && p.Gemini == ""
}

// DecidesModel distinguishes Jev from the conversation models a gateway
// also serves. A dedicated decision API may use any model name.
func (p Provider) DecidesModel(model string) bool {
	if !p.Decides() {
		return false
	}
	if p.DecideOnly() {
		return true
	}
	if p.listsDecisions() {
		// OpenRouter's Jev Router (typesafe/jev-router) is a chat model
		return slices.ContainsFunc(p.decideListed(), func(m catalog.Model) bool { return m.ID == model })
	}
	return jevID(model) || p.DecideVia() == ViaCloudflare && CloudflareClef(model)
}

// OpenRouter lists its decision models apart from its chat models (ARNO
// on Discord): its /models has the chat ones alone, Jev Router among them
// (typesafe/jev-router, a chat model that picks one), and
// /models?output_modalities=decisions the ones that answer System One's
// questions at /systemone (liquid/d1, cloudflare/clef, ~typesafe/jev-latest
// …), each with its context_length and input_modalities.
func openRouterDecisions(base string) string {
	if HostOf(base) != "openrouter.ai" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/models?output_modalities=decisions"
}

// listsDecisions reports whether p's decision models are a list of their
// own, OpenRouter's, rather than Jev's ids among its chat models.
func (p Provider) listsDecisions() bool { return openRouterDecisions(p.Decide) != "" }

// decisionsID is where a provider that serves conversations too keeps the
// list of its decision models, beside its chat models' list.
func decisionsID(id string) string { return id + ".decisions" }

// DecisionModels are the decision models a provider that serves
// conversations too lists apart from them (OpenRouter's), for its editor
// to show beside its chat models; nil for any other provider.
func (p Provider) DecisionModels() []catalog.Model {
	if p.DecideOnly() || !p.listsDecisions() {
		return nil
	}
	return p.decideModels()
}

// decideListDue reports whether p's list of decision models at OpenRouter
// is still to be fetched, or was fetched before magpie kept each model's
// window and input (they show on the Providers page and the Gateway list
// only once it is fetched again).
func (p Provider) decideListDue() bool {
	if !p.On() || !p.Decides() {
		return false
	}
	id := p.ID
	if p.DecideOnly() {
		if !p.listsDecisions() && HostOf(strings.TrimSpace(p.ModelsURL)) != "openrouter.ai" {
			return false
		}
	} else if p.listsDecisions() {
		id = decisionsID(p.ID)
	} else {
		return false
	}
	live, _, ok := catalog.Live(id)
	return !ok || !slices.ContainsFunc(live, func(m catalog.Model) bool { return m.Context > 0 || m.ImageInput != nil })
}

// Cloudflare's own decision models on Workers AI (ARNO on Discord): Clef
// and Clef Flash answer System One's questions as Jev does, but each is
// run at its own address (…/ai/run/@cf/cloudflare/clef) and takes the
// request as it is, its model named "clef" or "clef-flash", where Jev is
// a run of typesafe/jev with the questions as its input.
// OpenRouterJev is Jev as OpenRouter names it, the latest one.
const OpenRouterJev = "~typesafe/jev-latest"

const (
	CloudflareJev       = "typesafe/jev"
	CloudflareClefModel = "@cf/cloudflare/clef"
	CloudflareClefFlash = "@cf/cloudflare/clef-flash"
)

// CloudflareClef reports whether model is one of Cloudflare's Clef models.
func CloudflareClef(model string) bool {
	return model == CloudflareClefModel || model == CloudflareClefFlash
}

// cloudflareDecideModels are the decision models Workers AI serves.
var cloudflareDecideModels = []catalog.Model{
	{ID: CloudflareJev, Name: "Jev"},
	{ID: CloudflareClefModel, Name: "Clef"},
	{ID: CloudflareClefFlash, Name: "Clef Flash"},
}

// The ways a decision API is asked, by where it is (DecideVia): TypeSafe's
// own System One; Vercel's AI Gateway, at its TypeSafe API (System One's
// questions and answers, with its own name for Jev) or, where an older
// setup points, at the AI SDK's evaluation models (a boolean for a noul
// and probabilities for a confidence); or Workers AI (System One's
// questions and answers, in Cloudflare's envelope and at an account's
// address).
const (
	ViaSystemOne  = "systemone"
	ViaVercel     = "vercel"
	ViaVercelEval = "vercel-eval"
	ViaCloudflare = "cloudflare"
)

// DecideVia is the way p's decision API is asked, known by its host or by
// the gateway's own path (Vercel's /typesafe or /v4/ai, Cloudflare's
// /client/v4).
func (p Provider) DecideVia() string {
	base := strings.TrimRight(p.Decide, "/")
	switch h := HostOf(base); {
	case strings.HasSuffix(base, "/v4/ai"):
		return ViaVercelEval
	case h == "ai-gateway.vercel.sh" || strings.HasSuffix(base, "/typesafe") || strings.Contains(base, "/typesafe/v1"):
		return ViaVercel
	case h == "api.cloudflare.com" || strings.Contains(base+"/", "/client/v4/"):
		return ViaCloudflare
	}
	return ViaSystemOne
}

// Jev is the model a decision provider is asked with when the user picked
// none: TypeSafe's latest stable one, or the one Jev a gateway serves.
func (p Provider) Jev() string {
	switch p.DecideVia() {
	case ViaVercel, ViaVercelEval:
		return "typesafe-ai/jev"
	case ViaCloudflare:
		return CloudflareJev
	}
	if own := p.ownDecideModels(); len(own) > 0 {
		return own[0].ID
	}
	// OpenRouter's Jev is ~typesafe/jev-latest, in its list of decision
	// models; its Jev Router is a chat model
	if p.listsDecisions() || p.DecideOnly() && HostOf(strings.TrimSpace(p.ModelsURL)) == "openrouter.ai" {
		ms := p.decideListed()
		if i := slices.IndexFunc(ms, func(m catalog.Model) bool { return jevID(m.ID) }); i >= 0 {
			return ms[i].ID
		}
		return OpenRouterJev
	}
	// a gateway that serves conversations too names its Jev its own way
	// (OpenCode Zen's jev-1.13): the one its list has, the free one where
	// it is asked with no key of the user's
	if !p.DecideOnly() {
		if live, _, ok := catalog.Live(p.ID); ok {
			var jevs []string
			for _, m := range live {
				if jevID(m.ID) {
					jevs = append(jevs, m.ID)
				}
			}
			if len(jevs) > 0 {
				if p.IsOpenCode() && p.Key == OpenCodeAnonymousKey {
					if i := slices.IndexFunc(jevs, func(id string) bool { return strings.HasSuffix(id, "-free") }); i >= 0 {
						return jevs[i]
					}
				}
				return jevs[0]
			}
		}
	}
	return JevLatest
}

// decideModels are the models a decision provider offers: the vendor's
// list when fetched, else Jev's aliases (a gateway's one Jev), each with
// its window and whether it takes images where its vendor's docs say.
func (p Provider) decideModels() []catalog.Model {
	return withDecideFacts(p.decideListed())
}

// A decision model's window and whether it reads images, as its vendor's
// docs give them where its list doesn't (ARNO on Discord: decision models
// showed neither on the Gateway list): Jev holds 32k tokens of state and
// reads text alone (TypeSafe's docs; OpenRouter's and Vercel's lists say
// 32000 too), Cloudflare's Clef and Clef Flash 65,536 and up to 4 images,
// Bailian's decision model 65,536 and text alone.
type decideFact struct {
	context int
	images  bool
}

func decideFactOf(id string) (decideFact, bool) {
	last := strings.ToLower(id[strings.LastIndex(id, "/")+1:])
	last = strings.TrimPrefix(last, "~")
	switch {
	case jevID(id):
		return decideFact{context: 32_000}, true
	case last == "clef" || last == "clef-flash":
		return decideFact{context: 65_536, images: true}, true
	case last == BailianDecision:
		return decideFact{context: 65_536}, true
	}
	return decideFact{}, false
}

// withDecideFacts fills in the window and images of the decision models
// it knows that a list left unsaid; what a list said is kept.
func withDecideFacts(ms []catalog.Model) []catalog.Model {
	ms = slices.Clone(ms)
	for i, m := range ms {
		f, ok := decideFactOf(m.ID)
		if !ok {
			continue
		}
		if m.Context == 0 {
			ms[i].Context = f.context
		}
		if m.ImageInput == nil {
			in := f.images
			ms[i].ImageInput, ms[i].Images = &in, in
		}
	}
	return ms
}

func (p Provider) decideListed() []catalog.Model {
	if p.listsDecisions() && !p.DecideOnly() {
		if live, _, ok := catalog.Live(decisionsID(p.ID)); ok && len(live) > 0 {
			return live
		}
		return []catalog.Model{{ID: OpenRouterJev, Name: "Jev"}}
	}
	if live, _, ok := catalog.Live(p.ID); ok && len(live) > 0 {
		if p.DecideOnly() {
			return live
		}
		var out []catalog.Model
		for _, m := range live {
			if p.DecidesModel(m.ID) {
				out = append(out, m)
			}
		}
		return out
	}
	switch p.DecideVia() {
	case ViaCloudflare:
		return slices.Clone(cloudflareDecideModels)
	case ViaVercel, ViaVercelEval:
		return []catalog.Model{{ID: p.Jev(), Name: "Jev"}}
	}
	if own := p.ownDecideModels(); len(own) > 0 {
		return own
	}
	return []catalog.Model{{ID: JevLatest, Name: "Jev"}, {ID: "jev-preview", Name: "Jev (preview)"}}
}

// vercelTypeSafe is the root of Vercel's TypeSafe API that p's base names,
// as its docs give it (…/typesafe) or with /v1 or /v1/systemone after it;
// the gateway's bare host, or its OpenAI /v1, is taken for its /typesafe.
func (p Provider) vercelTypeSafe() string {
	base := strings.TrimRight(p.Decide, "/")
	for _, s := range []string{"/systemone", "/models", "/v1"} {
		base = strings.TrimSuffix(base, s)
	}
	if u, err := url.Parse(base); err == nil && strings.Trim(u.Path, "/") == "" {
		base = strings.TrimRight(base, "/") + "/typesafe"
	}
	return base
}

// DecideURL is where a question for p's decision API is posted. Workers
// AI's is under an account: the one its base names, else the one the
// token belongs to, looked up once.
func (p Provider) DecideURL(ctx context.Context) (string, error) {
	ctx = p.Via(ctx)
	switch p.DecideVia() {
	case ViaVercel:
		return p.vercelTypeSafe() + "/v1/systemone", nil
	case ViaVercelEval:
		return strings.TrimRight(p.Decide, "/") + "/evaluation-model", nil
	case ViaCloudflare:
		api, acct := p.cloudflareBase()
		if acct == "" {
			var err error
			if acct, err = p.cloudflareAccount(ctx); err != nil {
				return "", err
			}
		}
		return api + "/accounts/" + acct + "/ai/run", nil
	}
	return p.Decide + "/systemone", nil
}

// DecideModelURL is where a question for model is posted: DecideURL, or,
// for a Clef on Workers AI, its own run under it.
func (p Provider) DecideModelURL(ctx context.Context, model string) (string, error) {
	u, err := p.DecideURL(ctx)
	if err == nil && p.DecideVia() == ViaCloudflare && CloudflareClef(model) {
		u += "/" + model
	}
	return u, err
}

// cloudflareBase splits p's Workers AI base into Cloudflare's API
// (…/client/v4) and the account it names, if any. Cloudflare's docs give
// …/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/ai/run, and a token made
// for Workers AI alone may not list /accounts, so a base with an account
// in it, /ai/run after it or not, is asked there as it is. A placeholder
// left in ($CLOUDFLARE_ACCOUNT_ID, {account_id}, <id>, :id) names none.
func (p Provider) cloudflareBase() (api, account string) {
	base := strings.TrimRight(p.Decide, "/")
	i := strings.Index(base, "/accounts")
	if i < 0 {
		return base, ""
	}
	api = base[:i]
	rest := strings.TrimPrefix(strings.TrimPrefix(base[i:], "/accounts"), "/")
	account, _, _ = strings.Cut(rest, "/")
	if account == "" || strings.ContainsAny(account[:1], "$:{<") {
		return api, ""
	}
	return api, account
}

// cfAccounts are the Cloudflare accounts found for each token.
var cfAccounts = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// cfNoAccount says how to name the account when the token can't list it.
const cfNoAccount = "put your account ID in the endpoint: https://api.cloudflare.com/client/v4/accounts/<account ID>/ai/run"

// cloudflareAccount is the account p's API token works in: the first it
// can see, which for a token made for one account is that one.
func (p Provider) cloudflareAccount(ctx context.Context) (string, error) {
	cfAccounts.Lock()
	id, ok := cfAccounts.m[p.Decide+"\x00"+p.Key]
	cfAccounts.Unlock()
	if ok {
		return id, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	api, _ := p.cloudflareBase()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/accounts", nil)
	if err != nil {
		return "", err
	}
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: %v", p.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusForbidden {
		// a token that may not list accounts, as one for Workers AI alone
		return "", fmt.Errorf("%s: %s; for a token only for Workers AI, %s", p.Name, APIError(b, res.Status), cfNoAccount)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", p.Name, APIError(b, res.Status))
	}
	var out struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &out) != nil || len(out.Result) == 0 || out.Result[0].ID == "" {
		return "", fmt.Errorf("%s: the API token lists no account; make one with Workers AI access, or %s", p.Name, cfNoAccount)
	}
	id = out.Result[0].ID
	cfAccounts.Lock()
	cfAccounts.m[p.Decide+"\x00"+p.Key] = id
	cfAccounts.Unlock()
	return id, nil
}

// Deciders are the models of the decision providers ready now, as a
// group's classifier names them.
func Deciders() []Entry {
	var out []Entry
	for _, p := range All() {
		if !p.Decides() || !p.On() {
			continue
		}
		ms := p.Exposed()
		if !p.DecideOnly() && (len(p.Models) == 0 || p.listsDecisions()) {
			ms = p.decideModels() // the conversation picker's limit does not hide Jev
		}
		for _, m := range ms {
			if p.DecidesModel(m.ID) {
				out = append(out, Entry{ID: p.ID + "/" + m.ID, Model: m.ID, Name: m.Name, Provider: p})
			}
		}
	}
	return out
}

// IsDecider reports whether a classifier ("provider/model") is a decision
// provider's model.
func IsDecider(id string) bool {
	p, model, ok := Resolve(id)
	return ok && p.DecidesModel(model)
}

// RouteDecider is the decision provider a System One model names, and the
// model that provider is asked with. The name's prefix is the provider
// (gptload-jev/jev-latest, vercel-jev/typesafe-ai/jev). A bare Jev id goes
// to the only provider that can answer it, as that provider names Jev.
func RouteDecider(model string) (Provider, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return Provider{}, "", decideBadRequest("a System One request names no model")
	}
	var on []Provider
	for _, p := range All() {
		if p.Decides() && p.On() {
			on = append(on, p)
		}
	}
	if pid, rest, ok := strings.Cut(model, "/"); ok && rest != "" {
		if p, ok := deciderByID(pid); ok {
			if !p.On() {
				return Provider{}, "", decideBadRequest("%s is switched off in magpie", p.Name)
			}
			m, ok := resolveDecideModel(p, rest)
			if !ok {
				return Provider{}, "", decideBadRequest("%s is not a model of %s", rest, p.Name)
			}
			return p, m, nil
		}
	}
	if p, ok := deciderByID(model); ok {
		if !p.On() {
			return Provider{}, "", decideBadRequest("%s is switched off in magpie", p.Name)
		}
		return p, p.Jev(), nil
	}
	var listed []Provider
	for _, p := range on {
		for _, m := range p.decideModels() {
			if m.ID == model {
				listed = append(listed, p)
				break
			}
		}
	}
	switch len(listed) {
	case 1:
		return listed[0], model, nil
	case 0:
	default:
		return Provider{}, "", decideAmbiguous(model, listed)
	}
	type hit struct {
		p Provider
		m string
	}
	var hits []hit
	for _, p := range on {
		if m, ok := resolveDecideModel(p, model); ok {
			hits = append(hits, hit{p, m})
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].p, hits[0].m, nil
	case 0:
		if len(on) == 0 {
			return Provider{}, "", decideNotFound("magpie has no Jev provider")
		}
		return Provider{}, "", decideNotFound("magpie knows no Jev model %q; name the provider before it, like %s/%s", model, on[0].ID, on[0].Jev())
	default:
		ps := make([]Provider, len(hits))
		for i, h := range hits {
			ps[i] = h.p
		}
		return Provider{}, "", decideAmbiguous(model, ps)
	}
}

func decideAmbiguous(model string, ps []Provider) error {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.ID + "/" + model
	}
	sort.Strings(names)
	return decideBadRequest("%s is served by more than one Jev provider; name it %s", model, strings.Join(names, " or "))
}

// decideRouteError is a System One routing failure, with the HTTP status
// magpie answers.
type decideRouteError struct {
	msg    string
	status int
}

func (e *decideRouteError) Error() string { return e.msg }

func decideBadRequest(format string, a ...any) error {
	return &decideRouteError{fmt.Sprintf(format, a...), http.StatusBadRequest}
}

func decideNotFound(format string, a ...any) error {
	return &decideRouteError{fmt.Sprintf(format, a...), http.StatusNotFound}
}

// DecideRouteStatus is the HTTP status a System One request gets for err
// from RouteDecider: 400 when the model is named badly, 404 when magpie
// has no such Jev provider.
func DecideRouteStatus(err error) int {
	var e *decideRouteError
	if errors.As(err, &e) {
		return e.status
	}
	if err == nil {
		return http.StatusOK
	}
	return http.StatusNotFound
}

// resolveDecideModel is the model p is asked with for name: one it lists,
// else the one name a gateway serves Jev as when the request used TypeSafe's
// jev-latest (Vercel: typesafe-ai/jev, Cloudflare: typesafe/jev). Preview
// and other unlisted names are none: the channel has no such model.
func resolveDecideModel(p Provider, name string) (string, bool) {
	if !p.DecidesModel(name) {
		return "", false
	}
	for _, m := range p.decideModels() {
		if m.ID == name {
			return name, true
		}
	}
	if name == p.Jev() {
		return name, true
	}
	// a gateway lists one Jev, named its own way; jev-latest is that model
	if p.DecideVia() != ViaSystemOne && strings.EqualFold(name, JevLatest) {
		return p.Jev(), true
	}
	return "", false
}

// deciderByID is the decision provider whose id is id, on or off.
func deciderByID(id string) (Provider, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range All() {
		if p.Decides() && p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// fetchDecide lists the models a decision provider's key can use:
// {"models":[{"name":…,"description":…}]}. A gateway lists Jev among
// every other model, so its key is checked instead (Vercel's by its
// TypeSafe API's models, or its credits; Cloudflare's by the named
// account's Workers AI models, or by finding its account) and Jev is the
// model.
func (p Provider) fetchDecide(ctx context.Context) ([]catalog.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch p.DecideVia() {
	case ViaVercel:
		if err := p.checkKey(ctx, p.vercelTypeSafe()+"/v1/models"); err != nil {
			return nil, err
		}
		return p.decideModels(), nil
	case ViaVercelEval:
		if err := p.checkKey(ctx, strings.TrimSuffix(strings.TrimRight(p.Decide, "/"), "/v4/ai")+"/v1/credits"); err != nil {
			return nil, err
		}
		return p.decideModels(), nil
	case ViaCloudflare:
		if api, acct := p.cloudflareBase(); acct != "" {
			if err := p.checkKey(ctx, api+"/accounts/"+acct+"/ai/models/search?per_page=1"); err != nil {
				return nil, err
			}
		} else if _, err := p.cloudflareAccount(ctx); err != nil {
			return nil, err
		}
		return p.decideModels(), nil
	}
	// the list where the user said its models are, asked as given, query
	// and all (ARNO on Discord: OpenRouter's decision models are at
	// …/models?output_modalities=decisions; its /models lists chat models
	// only, and only Jev's of those were kept): every model in it is one
	list, all := p.Decide+"/models", false
	if u := strings.TrimSpace(p.ModelsURL); u != "" && p.DecideOnly() {
		list, all = u, true
	} else if u := openRouterDecisions(p.Decide); u != "" {
		list, all = u, true
	} else if bailianDecides(p.Decide) {
		// Bailian lists its chat models only: its decision model is asked
		return p.decideAsked(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, list, nil)
	if err != nil {
		return nil, err
	}
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	// a vendor's own decision model, picked by name, is asked when its
	// list isn't there or doesn't name it
	own := len(p.ownDecideModels()) > 0
	if res.StatusCode != http.StatusOK {
		if own && res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden {
			return p.decideAsked(ctx)
		}
		return nil, fmt.Errorf("%s: %s", p.Name, APIError(b, res.Status))
	}
	ms, err := listedDecide(b, all)
	if (err != nil || len(ms) == 0) && own {
		return p.decideAsked(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %s", p.Name, err)
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("%s lists no models", p.Name)
	}
	if !p.DecideOnly() {
		if p.listsDecisions() {
			return ms, catalog.SaveLive(decisionsID(p.ID), p.Decide, ms)
		}
		return ms, nil // do not replace a mixed provider's full model list
	}
	return ms, catalog.SaveLive(p.ID, p.Decide, ms)
}

// decideAsked is a System One provider's own models once the first of
// them answered the smallest question, a yes-or-no, at POST …/systemone:
// what checks its key and model when no list names them.
func (p Provider) decideAsked(ctx context.Context) ([]catalog.Model, error) {
	ms := p.ownDecideModels()
	if len(ms) == 0 {
		ms = []catalog.Model{{ID: p.Jev()}}
	}
	if err := p.AskSystemOne(ctx, ms[0].ID); err != nil {
		return nil, err
	}
	if !p.DecideOnly() {
		return ms, nil
	}
	return ms, catalog.SaveLive(p.ID, p.Decide, ms)
}

// AskSystemOne sends model the smallest System One question at p's
// decision API, and says why it wasn't answered.
func (p Provider) AskSystemOne(ctx context.Context, model string) error {
	u, err := p.DecideModelURL(ctx, model)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"model": model,
		"state":     map[string]any{"text": "ping"},
		"questions": map[string]any{"ok": map[string]any{"type": "noul", "instructions": "Is this a test?"}}})
	via := p.DecideVia()
	body = DecideAsk(via, model, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %v", p.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", p.Name, APIError(b, res.Status))
	}
	b = DecideAnswer(via, b)
	var out struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if json.Unmarshal(b, &out) != nil || len(out.Answers) == 0 {
		return fmt.Errorf("%s: no System One answers at %s", p.Name, u)
	}
	return nil
}

// listedDecide reads a decision provider's model list. TypeSafe's is
// {"models":[{"name":…}]}. A gateway in front of Jev often answers
// OpenAI's instead, {"data":[{"id":…}]}, and that list is every model
// it serves, so an id counts only when it is Jev's: "jev…" or "…/jev…".
// Names, when the list has any, are kept as they are. With all, every id
// counts: a list the user pointed at is of decision models already.
func listedDecide(b []byte, all bool) ([]catalog.Model, error) {
	var out struct {
		Models []struct {
			Name string `json:"name"`
			listedFacts
		} `json:"models"`
		Data []listedFacts `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("not a model list")
	}
	var ms []catalog.Model
	for _, m := range out.Models {
		if m.Name != "" {
			ms = append(ms, m.model(m.Name))
		}
	}
	if len(ms) > 0 {
		return ms, nil
	}
	seen := map[string]bool{}
	take := func(m listedFacts) {
		if m.ID == "" || seen[m.ID] || !all && !jevID(m.ID) {
			return
		}
		seen[m.ID] = true
		ms = append(ms, m.model(m.ID))
	}
	for _, m := range out.Data {
		take(m)
	}
	for _, m := range out.Models {
		take(m.listedFacts)
	}
	return ms, nil
}

// listedFacts is a decision model as an OpenAI-shaped list gives it, with
// its window and input as OpenRouter's (context_length,
// architecture.input_modalities) or Vercel's (context_window,
// modalities.input) say them, where they do.
type listedFacts struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	ContextWindow int    `json:"context_window"`
	Architecture  struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
}

func (f listedFacts) model(name string) catalog.Model {
	m := catalog.Model{ID: name, Name: name, Context: cmp.Or(f.ContextLength, f.ContextWindow)}
	if in := cmp.Or(len(f.Architecture.InputModalities), len(f.Modalities.Input)); in > 0 {
		images := slices.Contains(f.Architecture.InputModalities, "image") || slices.Contains(f.Modalities.Input, "image")
		m.ImageInput, m.Images = &images, images
	}
	return m
}

// jevID reports whether id names Jev: a path segment that is "jev" or
// "jev-…" (jev-latest, typesafe/jev, typesafe-ai/jev), not jevons or jevx.
func jevID(id string) bool {
	id = strings.ToLower(id)
	for {
		seg, rest, ok := strings.Cut(id, "/")
		if seg == "jev" || strings.HasPrefix(seg, "jev-") {
			return true
		}
		if !ok {
			return false
		}
		id = rest
	}
}

// checkKey gets u with p's key, which answers only a key that works.
func (p Provider) checkKey(ctx context.Context, u string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %v", p.Name, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", p.Name, APIError(b, res.Status))
	}
	return nil
}

// testDecide asks the decision API for its models (or checks its key),
// the one call it answers that costs nothing.
func (p Provider) testDecide(ctx context.Context) []Result {
	t0 := time.Now()
	r := Result{Protocol: "decide", Model: p.Jev()}
	ms, err := p.fetchDecide(ctx)
	r.Millis = time.Since(t0).Milliseconds()
	if err != nil {
		r.Error = err.Error()
		return []Result{r}
	}
	r.OK, r.Status = true, http.StatusOK
	if len(ms) > 0 {
		r.Model = ms[0].ID
	}
	return []Result{r}
}

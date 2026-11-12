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
	"github.com/yetone/magpie/internal/probelog"
	"github.com/yetone/magpie/internal/settings"
)

// JevLatest is the Jev a decision provider is asked with when the user
// picked none: TypeSafe's latest stable one.
const JevLatest = "jev-latest"

// BailianDecision is Alibaba Cloud Bailian's decision model, asked on the
// same System One API as Jev (#647).
const BailianDecision = "decision-model-preview"

// bailianDecides reports whether base is Bailian's: a workspace's host or
// the Token Plan's, <x>.<region>.maas.aliyuncs.com, or the Qwen AI
// platform's, maas.qianwenaiapi.com and its plan's under it (#1506). Its
// /models lists Qwen's chat models, never the decision model.
func bailianDecides(base string) bool {
	h := HostOf(base)
	return strings.HasSuffix(h, ".maas.aliyuncs.com") || h == "maas.qianwenaiapi.com" || strings.HasSuffix(h, ".maas.qianwenaiapi.com")
}

// ArkDecision is the model Volcengine Ark answers System One with, in its
// beta (axelburks on Discord): doubao-seed-2-1-lite-260915. Asked on
// 11-01, Ark's other Seed models it serves the account
// (doubao-seed-2-1-pro-260628, doubao-seed-2-0-lite-260428) and
// deepseek-v4-1-flash were each refused 400 "does not support the
// SystemOne API".
const ArkDecision = "doubao-seed-2-1-lite-260915"

// ArkSystemOneHeader is the header Ark's System One API is turned on by
// while it is in beta: without it, POST …/systemone is a bare 404.
const ArkSystemOneHeader = "ark-beta-systemone-api"

// arkDecides reports whether base is Volcengine Ark's own API host,
// ark.<region>.volces.com, whose pay-as-you-go /api/v1 and /api/v3 answer
// System One at …/systemone. Its Coding and Agent Plans' paths
// (/api/coding/v3, /api/plan/v3) answer it nothing (404), and neither
// /models lists the decision model.
func arkDecides(base string) bool {
	h := HostOf(base)
	return strings.HasPrefix(h, "ark.") && strings.HasSuffix(h, ".volces.com")
}

// arkSystemOne is the System One base of a key's provider asked at
// Volcengine Ark's pay as you go (ark.<region>.volces.com/api/v3, as chat
// completions or Responses): …/api/v1 on the same host, where
// volcengine-decision asks it; "" for any other provider. The Coding and
// Agent Plans' paths answer System One nothing, and a signed-in account,
// a plugin, a remote magpie and Vertex say their own.
func (p Provider) arkSystemOne() string {
	if p.Account != nil || p.IsRemoteMagpie() || p.IsVertex() {
		return ""
	}
	for _, raw := range []string{p.Chat, p.Responses} {
		if !arkDecides(raw) {
			continue
		}
		u, err := url.Parse(raw)
		if err == nil && strings.TrimRight(u.Path, "/") == "/api/v3" {
			return u.Scheme + "://" + u.Host + "/api/v1"
		}
	}
	return ""
}

// vendorDecision is the decision model of the vendor whose System One
// API base is, Bailian's or Ark's, which its model list never names.
func vendorDecision(base string) (catalog.Model, bool) {
	switch {
	case bailianDecides(base):
		return catalog.Model{ID: BailianDecision, Name: "Decision model (preview)"}, true
	case arkDecides(base):
		return catalog.Model{ID: ArkDecision, Name: "Doubao Seed 2.1 Lite"}, true
	}
	return catalog.Model{}, false
}

// DecideHeaders sets on h what p's decision API needs besides its key:
// Ark's beta switch (ArkSystemOneHeader) on Ark's host.
func (p Provider) DecideHeaders(h http.Header) {
	if arkDecides(p.Decide) {
		h.Set(ArkSystemOneHeader, "true")
	}
}

// bailianApart reports whether p serves conversations and a vendor's own
// decision model both (Bailian's Token Plan, #1506, or Ark's pay as you
// go with its decision URL given): its chat models' list never names the
// decision model, which is kept apart from it.
func (p Provider) bailianApart() bool {
	_, ok := vendorDecision(p.Decide)
	return !p.DecideOnly() && ok
}

// ownDecideModels are a System One provider's models that its vendor's
// list may not name: the ones picked for a decision-only provider (a
// vendor's own decision model needn't be called jev-…), else Bailian's
// or Ark's.
func (p Provider) ownDecideModels() []catalog.Model {
	var out []catalog.Model
	if p.DecideOnly() {
		for _, m := range p.Models {
			if m = strings.TrimSpace(m); m != "" {
				out = append(out, catalog.Model{ID: m})
			}
		}
	}
	if m, ok := vendorDecision(p.Decide); len(out) == 0 && ok {
		out = []catalog.Model{m}
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
// also serves. A dedicated decision API may use any model name, and a
// plugin's are the ones it marks besides Jev's (#1514).
func (p Provider) DecidesModel(model string) bool {
	if !p.Decides() {
		return false
	}
	if p.IsPlugin() {
		return p.pluginMarks(model) || jevID(model)
	}
	if p.DecideOnly() {
		return true
	}
	// one the user set to be asked on System One (DecideAPI)
	if p.decideSet(model) {
		return true
	}
	if p.bailianApart() {
		m, _ := vendorDecision(p.Decide)
		return model == m.ID
	}
	// decideListed's answer, without its list copied for each model
	// asked about: a provider's every model was asked, a copy of the whole
	// list each time (0.4 GB of a real config's 4 GB over 20 polls)
	if p.IsRemoteMagpie() {
		return slices.ContainsFunc(catalog.LiveByID(p.ID, model), func(m catalog.Model) bool { return m.Decides })
	}
	if p.listsDecisions() {
		// OpenRouter's Jev Router (typesafe/jev-router) is a chat model
		if id := decisionsID(p.ID); catalog.LiveListed(id) {
			return len(catalog.LiveByID(id, model)) > 0 || p.openRouterVariant(model)
		}
		return !p.serverMarksDecisions() && model == OpenRouterJev
	}
	return jevID(model) || p.DecideVia() == ViaCloudflare && CloudflareClef(model)
}

// isDecision uses a listed remote model's marker without reading its whole
// list again. Other providers keep their endpoint and name-based rules.
func (p Provider) isDecision(m catalog.Model) bool {
	if p.IsRemoteMagpie() {
		return m.Decides
	}
	if p.IsPlugin() && p.Decides() && m.Decides {
		return true
	}
	return p.DecidesModel(m.ID)
}

// pluginMarks reports whether p's plugin marks model as a decision model
// (plugin.Model.Decides), as it last listed its models.
func (p Provider) pluginMarks(model string) bool {
	pp := *p.Account.plugin
	if cur, ok := PluginOf(p.ID); ok {
		pp = cur
	}
	m, ok := pluginModel(pp, model)
	return ok && m.Decides
}

// pluginDecisions are the decision models a plugin's account lists, in
// its order: the ones it marks, and Jev's by name.
func (p Provider) pluginDecisions() []catalog.Model {
	var out []catalog.Model
	for _, m := range p.available() {
		if p.isDecision(m) {
			out = append(out, m)
		}
	}
	return out
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
// own, OpenRouter's or a server's that marks them (serverMarksDecisions),
// rather than Jev's ids among its chat models.
func (p Provider) listsDecisions() bool {
	return openRouterDecisions(p.Decide) != "" || p.serverMarksDecisions()
}

// serverMarksDecisions reports whether p's server says which of the models
// it serves are decision models, oMLX's (omlxDecides), MLX-Serve's
// (mlxServeDecides) or Ollama's (ollamaDecides): it has no Jev of its own,
// and lists none while it has none.
func (p Provider) serverMarksDecisions() bool {
	return p.omlxDecides() || p.mlxServeDecides() || p.ollamaDecides()
}

// openRouterVariant reports whether model is one of the decision models
// OpenRouter lists with a variant after it (inception/mercury-decide:free,
// …:nitro): OpenRouter's list names the model once, and a variant goes to
// the same decisions endpoint, never chat/completions (coeo91 on Discord:
// inception/mercury-decide:free, picked by hand, was sent as a chat model
// and refused 400 "is a decisions model"). Only OpenRouter's ids take a
// variant so; an Ollama decision model's :tag is a name of its own.
func (p Provider) openRouterVariant(model string) bool {
	if openRouterDecisions(p.Decide) == "" {
		return false
	}
	i := strings.LastIndexByte(model, ':')
	if i <= 0 || i == len(model)-1 || strings.Contains(model[i:], "/") {
		return false
	}
	return len(catalog.LiveByID(decisionsID(p.ID), model[:i])) > 0
}

// decisionsID is where a provider that serves conversations too keeps the
// list of its decision models, beside its chat models' list.
func decisionsID(id string) string { return id + ".decisions" }

// DecisionModels are the decision models listed separately from a provider's
// conversations: OpenRouter's own list, a Bailian Token Plan's decision
// model, or a remote magpie's or a plugin's marked models.
func (p Provider) DecisionModels() []catalog.Model {
	if p.IsPlugin() {
		if !p.Decides() {
			return nil
		}
		return p.decideModels()
	}
	if !p.IsRemoteMagpie() && (p.DecideOnly() || !p.listsDecisions() && !p.bailianApart()) {
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
	if p.serverMarksDecisions() {
		// a server with no decision model loaded lists none, for good
		return !ok
	}
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
	if p.IsRemoteMagpie() {
		return ViaSystemOne
	}
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
	if p.IsRemoteMagpie() || p.IsPlugin() {
		if models := p.decideListed(); len(models) > 0 {
			return models[0].ID
		}
		return ""
	}
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
	if p.serverMarksDecisions() {
		// oMLX, MLX-Serve and Ollama have no Jev of their own: their first decision
		// model, if any
		if ms := p.decideListed(); len(ms) > 0 {
			return ms[0].ID
		}
		return ""
	}
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
	ms := p.decideListed()
	// and the ones the user set to be asked on System One, first
	if set := p.decideSetModels(); len(set) > 0 {
		set = slices.DeleteFunc(set, func(m catalog.Model) bool {
			return slices.ContainsFunc(ms, func(l catalog.Model) bool { return l.ID == m.ID })
		})
		ms = append(set, ms...)
	}
	return withDecideFacts(ms)
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
	case last == ArkDecision:
		// its window on System One is not documented; an image in the
		// request was left unread on 11-01 (a red square asked "is it
		// red?" came back 0.48, its input tokens as for no image)
		return decideFact{}, true
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
	if p.IsRemoteMagpie() {
		live, _, _ := catalog.Live(p.ID)
		// Live returns a filtered copy, so it can be compacted in place.
		return slices.DeleteFunc(live, func(m catalog.Model) bool { return !m.Decides })
	}
	if p.bailianApart() {
		return p.ownDecideModels()
	}
	if p.IsPlugin() {
		// the plugin's list is the account's, never a guessed Jev alias
		return p.pluginDecisions()
	}
	if p.listsDecisions() && !p.DecideOnly() {
		if live, _, ok := catalog.Live(decisionsID(p.ID)); ok && len(live) > 0 {
			return live
		}
		if p.serverMarksDecisions() {
			return nil // none until its server says which
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
	res, err := Keyed(http.DefaultClient).Do(req)
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
// group's classifier names them: every one the gateway answers.
func Deciders() []Entry {
	return deciders(nil)
}

// OfferedDeciders are the Deciders a routing group's pickers and templates
// offer: of a provider whose decision models are listed beside its chat
// models (OpenRouter's, a plugin's, a remote magpie's), with picks saved,
// those among the picks alone, as its editor shows the
// rest unpicked (coeo91 on Discord: two OpenRouter models picked, and a
// new group's picker listed OpenRouter's twenty-odd decision models after
// them). With none picked every one is offered, as before. keep, when
// given, says a decision model a group names already, offered whatever
// the picks, so a group made of one before reads as it did. A vendor's
// own one decision model beside its chat models (a Bailian plan's, Ark's
// pay as you go) is offered whatever the picks.
func OfferedDeciders(keep func(id string) bool) []Entry {
	if keep == nil {
		keep = func(string) bool { return false }
	}
	return deciders(keep)
}

// deciders are Deciders, less the unpicked ones keep doesn't name when
// keep is given.
func deciders(keep func(id string) bool) []Entry {
	var out []Entry
	s := settings.Load()
	off := disabledNow()
	for _, p := range All() {
		if !p.Decides() || !p.On() || p.ServesNone() && !p.DecideOnly() {
			continue
		}
		ms := p.Exposed()
		if !p.DecideOnly() && (len(p.Models) == 0 || p.listsDecisions() || p.bailianApart() || p.IsRemoteMagpie() || p.IsPlugin()) {
			ms = p.decideModels() // the conversation picker's limit does not hide Jev
			// a vendor's own one decision model (Bailian's, Ark's) is no
			// list to pick from: its chat models picked, it is offered
			// still, as nothing in the picks says it isn't wanted
			if keep != nil && len(p.Models) > 0 && !p.bailianApart() {
				ms = slices.DeleteFunc(slices.Clone(ms), func(m catalog.Model) bool {
					return !slices.Contains(p.Models, m.ID) && !keep(p.ID+"/"+m.ID)
				})
			}
		}
		for _, m := range ms {
			if p.isDecision(m) && !off(p.ID, m.ID) { // a disabled one isn't offered (#1867)
				if p.IsRemoteMagpie() {
					out = append(out, sharedEntry(&p, m, s))
				} else {
					out = append(out, Entry{ID: p.ID + "/" + m.ID, Model: m.ID, Name: m.Name, Provider: &p})
				}
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
		if m := p.Jev(); m != "" {
			return p, m, nil
		}
		return Provider{}, "", decideBadRequest("%s lists no decision models", p.Name)
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
	if name == p.Jev() || p.openRouterVariant(name) {
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
	if p.IsRemoteMagpie() {
		if _, err := p.fetch(ctx); err != nil {
			return nil, err
		}
		return p.decideModels(), nil
	}
	if p.IsPlugin() {
		// a plugin lists its decision models with its others and answers
		// no list of its own at the decision base: the first is asked the
		// smallest question, through its fetch, which is what checks it
		ms := p.decideModels()
		if len(ms) == 0 {
			return nil, fmt.Errorf("%s's plugin lists no decision models", p.Name)
		}
		if err := p.AskSystemOne(ctx, ms[0].ID); err != nil {
			return nil, err
		}
		return ms, nil
	}
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
	} else if p.omlxDecides() {
		list = omlxDecisions(p.Decide)
	} else if p.mlxServeDecides() {
		list = mlxServeDecisions(p.Decide)
	} else if p.ollamaDecides() {
		list = ollamaDecisions(p.Decide)
	} else if _, ok := vendorDecision(p.Decide); ok {
		// Bailian and Ark list their chat models only: the decision model
		// is asked
		return p.decideAsked(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, list, nil)
	if err != nil {
		return nil, err
	}
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		return nil, err
	}
	res, err := Keyed(http.DefaultClient).Do(req)
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
	if p.omlxDecides() {
		// none is an answer too: the server has no decision model now
		ms, err := listedOMLXDecide(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %s", p.Name, err)
		}
		return ms, catalog.SaveLive(decisionsID(p.ID), p.Decide, ms)
	}
	if p.mlxServeDecides() {
		// none is an answer too: the server has no decision model now
		ms, err := listedMLXServeDecide(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %s", p.Name, err)
		}
		return ms, catalog.SaveLive(decisionsID(p.ID), p.Decide, ms)
	}
	if p.ollamaDecides() {
		// none is an answer too: no model of the server's is one
		ms, err := listedOllamaDecide(p.Decide, b)
		if err != nil {
			return nil, fmt.Errorf("%s: %s", p.Name, err)
		}
		return ms, catalog.SaveLive(decisionsID(p.ID), p.Decide, ms)
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
	// the model's own headers
	ctx = WithModel(ctx, p.ID, model)
	if pat, off := ModelDisabled(p.ID, model); off { // never asked (#1867)
		return fmt.Errorf("model %s/%s is disabled in magpie (it matches %q)", p.ID, model, pat)
	}
	u, err := p.DecideModelURL(ctx, model)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"model": model,
		"state":     map[string]any{"text": "ping"},
		"questions": map[string]any{"ok": map[string]any{"type": "noul", "instructions": "Is this a test?"}}})
	via := p.DecideVia()
	body = DecideAsk(via, model, body)
	req, err := http.NewRequestWithContext(probelog.WithModel(ctx, model), http.MethodPost, u, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	p.DecideHeaders(req.Header)
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		return err
	}
	// a plugin's goes through its fetch (Do), at plugin://<id>/v1/systemone
	res, err := p.Do(http.DefaultClient, req)
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
		images := slices.Contains(catalog.Kinds(f.Architecture.InputModalities), "image") || slices.Contains(catalog.Kinds(f.Modalities.Input), "image")
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
	res, err := Keyed(http.DefaultClient).Do(req)
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

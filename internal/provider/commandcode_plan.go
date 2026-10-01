package provider

// A Command Code subscription is a commandcode.ai plan (Pro, GOAT, Max,
// Ultra — every one but Go comes with API access). Signing in to it, as
// `cmd auth login` does, mints an API key for the account; that key is
// served on Command Code's Provider API (/provider/v1: Chat, Responses and
// Anthropic's Messages) and billed against the plan's own credits and its
// 5-hour and weekly limits, not as pay-as-you-go.
//
// Go has no API access: its key is only taken where the CLI itself asks,
// POST /alpha/generate, in the CLI's own format (the gateway's
// commandcode.go). Which plan an account is on is asked of billing/
// subscriptions, and kept a while (cmdPlanNow).
//
// The CLI's own account is read, never changed, from ~/.commandcode/
// auth.json. Further accounts are signed in by magpie with the CLI's own
// browser sign-in: commandcode.ai/studio/auth/cli sends the new key to a
// callback on this machine, which answers as the CLI's does; their keys
// are kept in logins.json.
//
// Its id is commandcode-plan: "commandcode" is the keyed preset's, which a
// provider added with a key from Studio may already have.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// CommandCodePlanID is the subscription's id, and its sign-in's.
const CommandCodePlanID = "commandcode-plan"

// CommandCodeMaxOutput is the most max_tokens Command Code takes at
// /alpha/generate, for any model: more is refused ("Too big: expected
// number to be <=200000 at params.max_tokens"). models.dev gives some of
// its models more (DeepSeek V4's 384000, MiniMax M3's 512000).
const CommandCodeMaxOutput = 200_000

// CommandCodeOutputOf is the most a reply of a Command Code model may be
// asked for: the model's own limit, as models.dev gives it, within
// CommandCodeMaxOutput.
func CommandCodeOutputOf(model string) int {
	if n := catalog.OutputOf(model); n > 0 && n < CommandCodeMaxOutput {
		return n
	}
	return CommandCodeMaxOutput
}

// Where Command Code's API and its sign-in page are; vars so tests can
// point them elsewhere.
var (
	cmdAPI    = "https://api.commandcode.ai"
	cmdStudio = "https://commandcode.ai"
)

// cmdModels are the plan's best-known models, before its list is fetched.
var cmdModels = []catalog.Model{
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Context: 1_000_000},
	{ID: "claude-opus-5-5", Name: "Claude Opus 5.5", Context: 1_000_000},
	{ID: "gpt-6-sol", Name: "GPT-6 Sol", Context: 1_050_000},
	{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro", Context: 1_000_000},
	{ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash", Context: 1_000_000},
	{ID: "moonshotai/Kimi-K3", Name: "Kimi K3", Context: 1_000_000},
	{ID: "zai-org/GLM-5.3", Name: "GLM-5.3", Context: 1_000_000},
	{ID: "MiniMaxAI/MiniMax-M3", Name: "MiniMax M3", Context: 1_000_000},
}

// cmdGoModels are the models the Go plan is let use, as the CLI's own
// table has it (1.73.0): every model its picker shows (wD, less the
// hidden) but the "premium" ones and those it blocks for Go
// (cmdGoRefused). They stand in until Command Code's list is fetched
// (cmdGoFetch), and give that list the reasoning levels and pictures it
// doesn't say.
var cmdGoModels = []catalog.Model{
	{ID: "gpt-6-luna", Name: "GPT-6 Luna", Context: 1_050_000, Images: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", Context: 1_050_000, Images: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro", Context: 1_000_000, Efforts: []string{"high", "max"}},
	{ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash", Context: 1_000_000, Efforts: []string{"high", "max"}},
	{ID: "deepseek/deepseek-v4-flash-vision-exp", Name: "DeepSeek V4 Flash Vision (exp)", Context: 1_000_000, Images: true, Efforts: []string{"high", "max"}},
	{ID: "deepseek/deepseek-v4-flash-fast", Name: "DeepSeek V4 Flash Fast", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "deepseek/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", Context: 1_000_000, Images: true, Efforts: []string{"low", "high", "max"}},
	{ID: "deepseek/deepseek-v4.1-flash-fast", Name: "DeepSeek V4.1 Flash Fast", Context: 1_000_000, Images: true, Efforts: []string{"low", "high", "max"}},
	{ID: "moonshotai/Kimi-K3", Name: "Kimi K3", Context: 1_000_000, Images: true, Efforts: []string{"low", "high", "max"}},
	{ID: "moonshotai/Kimi-K2.7-Code", Name: "Kimi K2.7 Code", Context: 256_000, Images: true},
	{ID: "moonshotai/Kimi-K2.7-Code-Highspeed", Name: "Kimi K2.7 Code HighSpeed", Context: 262_000, Images: true},
	{ID: "moonshotai/Kimi-K2.6", Name: "Kimi K2.6", Context: 256_000, Images: true},
	{ID: "moonshotai/Kimi-K2.5", Name: "Kimi K2.5", Context: 256_000, Images: true},
	{ID: "z-ai/glm-5.3-flash", Name: "GLM-5.3 Flash", Context: 1_048_576, Images: true, Efforts: []string{"low", "high", "max"}},
	{ID: "z-ai/glm-5.3-flashx", Name: "GLM-5.3 FlashX", Context: 1_000_000, Images: true, Efforts: []string{"low", "high", "max"}},
	{ID: "zai-org/GLM-5.3", Name: "GLM-5.3", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{ID: "zai-org/GLM-5.2", Name: "GLM-5.2", Context: 1_000_000, Efforts: []string{"high", "max"}},
	{ID: "zai-org/GLM-5.2-Fast", Name: "GLM-5.2 Fast", Context: 1_000_000},
	{ID: "zai-org/GLM-5.1", Name: "GLM-5.1", Context: 200_000},
	{ID: "zai-org/GLM-5", Name: "GLM-5", Context: 200_000},
	{ID: "MiniMaxAI/MiniMax-M3", Name: "MiniMax M3", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "high"}},
	{ID: "MiniMaxAI/MiniMax-M2.7", Name: "MiniMax M2.7", Context: 200_000},
	{ID: "MiniMaxAI/MiniMax-M2.5", Name: "MiniMax M2.5", Context: 200_000},
	{ID: "xiaomi/mimo-v2.6-pro", Name: "MiMo V2.6 Pro", Context: 1_048_576, Images: true},
	{ID: "xiaomi/mimo-v2.6-flash", Name: "MiMo V2.6 Flash", Context: 1_048_576, Images: true},
	{ID: "xiaomi/mimo-v2.5-pro", Name: "MiMo V2.5 Pro", Context: 1_000_000},
	{ID: "xiaomi/mimo-v2.5", Name: "MiMo V2.5", Context: 1_000_000, Images: true},
	{ID: "Qwen/Qwen3.8-Omni-Flash", Name: "Qwen 3.8 Omni Flash", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "xhigh"}},
	{ID: "Qwen/Qwen3.8-Max-0902", Name: "Qwen 3.8 Max 0902", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "xhigh"}},
	{ID: "Qwen/Qwen3.8-Max", Name: "Qwen 3.8 Max", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "xhigh"}},
	{ID: "Qwen/Qwen3.8-27B", Name: "Qwen 3.8 27B", Context: 262_144, Images: true, Efforts: []string{"low", "medium", "xhigh"}},
	{ID: "Qwen/Qwen3.8-Flash", Name: "Qwen 3.8 Flash", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "xhigh"}},
	{ID: "Qwen/Qwen3.7-Max", Name: "Qwen 3.7 Max", Context: 1_000_000},
	{ID: "Qwen/Qwen3.7-Plus", Name: "Qwen 3.7 Plus", Context: 1_000_000, Images: true},
	{ID: "Qwen/Qwen3.7-Flash", Name: "Qwen 3.7 Flash", Context: 1_000_000, Images: true},
	{ID: "Qwen/Qwen3.6-Max-Preview", Name: "Qwen 3.6 Max Preview", Context: 200_000},
	{ID: "Qwen/Qwen3.6-Plus", Name: "Qwen 3.6 Plus", Context: 200_000, Images: true},
	{ID: "meituan/LongCat-2.0", Name: "LongCat 2.0", Context: 1_048_576},
	{ID: "stepfun/Step-5-Preview", Name: "Step 5 Preview", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "high"}},
	{ID: "stepfun/Step-3.7-Flash", Name: "Step 3.7 Flash", Context: 256_000, Images: true},
	{ID: "stepfun/Step-3.5-Flash", Name: "Step 3.5 Flash", Context: 262_144},
	{ID: "tencent/hy3-paid", Name: "Tencent Hy3", Context: 262_144},
	{ID: "tencent/hy4-preview", Name: "Tencent Hy4 Preview", Context: 1_048_576, Efforts: []string{"low", "medium", "high"}},
	{ID: "google/gemini-3.6-flash", Name: "Gemini 3.6 Flash", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "high"}},
	{ID: "google/gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "high"}},
	{ID: "nvidia/nemotron-3-ultra-550b-a55b", Name: "Nemotron 3 Ultra", Context: 1_000_000},
	{ID: "thinkingmachines/inkling", Name: "Inkling", Context: 256_000, Images: true},
	{ID: "thinkingmachines/inkling-small", Name: "Inkling Small", Context: 1_000_000, Images: true},
	{ID: "stealth/space-bunny-alpha", Name: "Space Bunny Alpha", Context: 1_000_000, Images: true, Efforts: []string{"low", "medium", "high"}},
	{ID: "stealth/pixel-canary", Name: "Pixel Canary", Context: 262_144, Images: true, Efforts: []string{"low", "medium", "xhigh"}},
	{ID: "poolside/laguna-s-2.1-free", Name: "Laguna S 2.1", Context: 256_000},
	{ID: "inclusionai/ling-3.0-flash-free", Name: "Ling 3.0 Flash", Context: 256_000},
	{ID: "inclusionai/ling-3.0-flash-sante:free", Name: "Ling 3.0 Flash Sante", Context: 262_144},
	{ID: "inclusionai/ling-3.1-flash:free", Name: "Ling 3.1 Flash", Context: 262_144, Efforts: []string{"low", "medium", "high"}},
	{ID: "meta/muse-spark-1.2-contributor", Name: "Muse Spark 1.2 Contributor", Context: 1_048_576, Images: true, Efforts: []string{"low", "medium", "high", "xhigh"}},
	{ID: "meta/muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor", Context: 1_048_576, Images: true, Efforts: []string{"low", "medium", "high", "xhigh"}},
	{ID: "xai/grok-4.5", Name: "Grok 4.5", Context: 500_000, Images: true, Efforts: []string{"low", "medium", "high"}},
}

// cmdGoRefused are the models of Command Code's list the Go plan is
// refused, as the CLI's table has it (1.73.0): its "premium" ones, and
// those "individual-go" blocks —
//
//	"individual-go":{allowedCategories:[qo],blockedModels:Xo=[…]}
//
// A model the table doesn't name the CLI lets any plan pick; one the plan
// hasn't after all is refused with MODEL_NOT_IN_PLAN.
var cmdGoRefused = map[string]bool{
	// premium
	"claude-sonnet-5": true, "claude-sonnet-4-6": true, "claude-fable-5-1": true, "claude-fable-5": true,
	"claude-opus-5-5": true, "claude-opus-5": true, "claude-opus-4-8": true, "claude-opus-4-7": true,
	"claude-haiku-4-5-20251001": true, "gpt-6-astra": true, "gpt-6.1-sol": true, "gpt-6-sol": true,
	"gpt-5.6-terra": true, "gpt-5.5": true, "gpt-5.4": true, "gpt-5.3-codex": true, "gpt-5.4-mini": true,
	"google/gemini-3.5-flash": true, "google/gemini-3.1-flash-lite": true, "sakana/fugu-ultra": true,
	"meta/muse-spark-1.1": true,
	// blocked for Go
	"claude-sonnet-5-5": true, "gpt-5.6-sol": true, "xai/grok-4.6": true, "xai/grok-4.7": true,
	"meta/muse-spark-1.2": true, "meta/muse-spark-1.3": true, "xiaomi/mimo-v2.6-pro-ultraspeed": true,
	"google/gemini-3.7-flash": true, "google/gemini-3.8-flash": true,
}

// cmdGoFetch is the Go plan's models: Command Code's list, less what Go
// is refused. The list is the Provider API's, which answers without a key
// (Go's has no Provider API), and takes its reasoning levels and pictures
// from cmdGoModels.
func cmdGoFetch(ctx context.Context) ([]catalog.Model, error) {
	base := cmdAPI + "/provider/v1"
	ms, err := catalog.FetchURL(ctx, base+"/models", "", false, nil)
	if err != nil {
		return nil, err
	}
	var out []catalog.Model
	for _, m := range catalog.Decorate(catalog.Chat(ms), cmdGoModels) {
		if !cmdGoRefused[m.ID] {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("Command Code listed no models for the Go plan")
	}
	cmdMarkFree(out)
	return out, catalog.SaveLive(CommandCodePlanID, base, out)
}

// cmdFree are the models Command Code's CLI marks FREE in its picker
// (1.73.2: badge:"free", "{name} is free and uses shared capacity"); its
// API's list doesn't say so, and Space Bunny Alpha's and Pixel Canary's
// ids name nothing free. Neither says a model is served at a discount.
var cmdFree = map[string]bool{
	"stealth/space-bunny-alpha": true, "stealth/pixel-canary": true,
	"poolside/laguna-s-2.1-free": true, "inclusionai/ling-3.0-flash-free": true,
	"inclusionai/ling-3.0-flash-sante:free": true, "inclusionai/ling-3.1-flash:free": true,
	"MiniMaxAI/MiniMax-M3-Free": true, "minimax/minimax-m3-free": true,
	"minimax/minimax-m2.7-free": true, "meituan/LongCat-2.0:free": true, "tencent/Hy3": true,
}

// cmdMarkFree marks the models of ms the CLI calls free, in place.
func cmdMarkFree(ms []catalog.Model) []catalog.Model {
	for i := range ms {
		if cmdFree[ms[i].ID] {
			ms[i].Free = true
		}
	}
	return ms
}

func init() { cmdMarkFree(cmdGoModels) }

// cmdAuth is an account's key, as auth.json and the sign-in name it.
type cmdAuth struct {
	APIKey   string `json:"apiKey"`
	UserID   string `json:"userId,omitempty"`
	UserName string `json:"userName,omitempty"`
	KeyName  string `json:"keyName,omitempty"`
}

// ---- the CLI's own account ----------------------------------------------------

func cmdAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".commandcode", "auth.json")
}

// cmdOwn is the account Command Code's CLI is signed in to; ok is false
// when it has none.
func cmdOwn() (who string, a cmdAuth, ok bool) {
	if !readJSON(cmdAuthPath(), &a) || strings.TrimSpace(a.APIKey) == "" {
		return "", cmdAuth{}, false
	}
	return cmdWho(a), a, true
}

// cmdWho names an account: its user name, or its id.
func cmdWho(a cmdAuth) string {
	return firstNonEmpty(a.UserName, a.UserID, "Command Code")
}

// ---- the accounts -------------------------------------------------------------

type cmdLogin struct {
	Login
	auth cmdAuth
}

func cmdSaved(l savedLogin) (cmdAuth, bool) {
	var a cmdAuth
	if json.Unmarshal(l.Auth, &a) != nil || a.APIKey == "" {
		return cmdAuth{}, false
	}
	return a, true
}

// cmdLogins is every Command Code account signed in, the first in use first.
func cmdLogins() []cmdLogin {
	ownUser, own, hasOwn := cmdOwn()
	if !hasOwn {
		ownUser = ""
	}
	var out []cmdLogin
	for _, l := range sideLogins(CommandCodePlanID, ownUser, func(l savedLogin) bool {
		_, ok := cmdSaved(l)
		return ok
	}) {
		a := own
		if !l.saved.own() {
			a, _ = cmdSaved(l.saved)
		}
		out = append(out, cmdLogin{l.Login, a})
	}
	return out
}

func cmdSide() []sideLogin {
	var out []sideLogin
	for _, l := range cmdLogins() {
		out = append(out, sideLogin{Login: l.Login})
	}
	return out
}

func cmdLoginList() []Login { return loginsOf(cmdSide()) }

func switchCommandCodeLogin(user string) error {
	return switchSideLogin(CommandCodePlanID, user, cmdSide())
}

func setCommandCodeLoginOn(user string, on bool) error {
	return setSideLoginOn(CommandCodePlanID, user, on, cmdSide())
}

func forgetCommandCodeLogin(user string) error {
	return forgetSideLogin(CommandCodePlanID, user, cmdSide(), nil)
}

func commandCodeAccount() (Provider, bool) {
	ls := cmdLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return cmdProvider(ls[0].User, ls[0].Plan, ls[0].auth), true
}

// commandCodeAlsoOn is the Command Code accounts in use behind the first.
func commandCodeAlsoOn() []Provider {
	var out []Provider
	for _, l := range cmdLogins() {
		if !l.Active && l.On {
			out = append(out, cmdProvider(l.User, l.Plan, l.auth))
		}
	}
	return out
}

func cmdProvider(who, plan string, a cmdAuth) Provider {
	p := Provider{ID: CommandCodePlanID, Name: "Command Code Plan", Icon: "commandcode",
		Chat: cmdAPI + "/provider/v1", Responses: cmdAPI + "/provider/v1", Anthropic: cmdAPI + "/provider",
		Website: cmdStudio}
	acct := &Account{Agent: CommandCodePlanID, User: who, Plan: cmdPlanKnown(a, plan)}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		req.Header.Del("Authorization")
		req.Header.Set("Authorization", "Bearer "+a.APIKey)
		req.Header.Set("x-api-key", a.APIKey)
		return nil
	}
	acct.models = func() []catalog.Model {
		if cmdPlanKnown(a, plan) == "Go" {
			return cmdGoModels
		}
		return cmdModels
	}
	// the plan's list is the Provider API's, with what each model is
	// served on; it is asked as a keyed provider's is. Go's key has no
	// Provider API: its list is the same one, asked without it, less what
	// Go is refused.
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		if cmdPlanNow(ctx, a, plan) == "Go" {
			return cmdGoFetch(ctx)
		}
		keyed := p
		keyed.Account, keyed.Key = nil, a.APIKey
		ms, base, err := keyed.fetchOne(keyed.Via(ctx))
		if err != nil {
			return nil, err
		}
		cmdMarkFree(ms)
		return catalog.Chat(ms), catalog.SaveLive(p.ID, base, ms)
	}
	acct.generate = func(ctx context.Context) (string, bool) {
		return a.APIKey, cmdPlanNow(ctx, a, plan) == "Go"
	}
	p.Account = acct
	return p
}

// CommandCodeGenerate says whether p is a Command Code account on the Go
// plan, which is asked at api+"/alpha/generate" in the CLI's own format
// rather than on the Provider API, with key; ok is false for every other
// account and provider.
func CommandCodeGenerate(ctx context.Context, p Provider) (api, key string, ok bool) {
	if p.Account == nil || p.Account.generate == nil {
		return "", "", false
	}
	if key, ok = p.Account.generate(ctx); !ok {
		return "", "", false
	}
	return cmdAPI, key, true
}

// ---- which plan ---------------------------------------------------------------

// cmdPlansSeen is each key's plan, as billing/subscriptions last said
// ("" when it couldn't be read), and when.
var cmdPlansSeen = struct {
	sync.Mutex
	m map[string]cmdSeen
}{m: map[string]cmdSeen{}}

type cmdSeen struct {
	plan string
	at   time.Time
}

// How long a plan read is kept, and a failure to read it before it is
// asked again.
var (
	cmdPlanKeep  = 10 * time.Minute
	cmdPlanRetry = time.Minute
)

// cmdRemember keeps what billing/subscriptions said of a key's plan.
func cmdRemember(key, plan string) {
	cmdPlansSeen.Lock()
	cmdPlansSeen.m[key] = cmdSeen{plan, time.Now()}
	cmdPlansSeen.Unlock()
}

// cmdPlanKnown is the account's plan as last read, else saved (the one
// its sign-in said), asking no one.
func cmdPlanKnown(a cmdAuth, saved string) string {
	cmdPlansSeen.Lock()
	seen := cmdPlansSeen.m[a.APIKey]
	cmdPlansSeen.Unlock()
	return firstNonEmpty(seen.plan, saved)
}

// cmdPlanNow is the account's plan, read again once what was read is
// older than cmdPlanKeep; saved while it can't be read. The CLI's own
// account has none saved, so it is always asked.
func cmdPlanNow(ctx context.Context, a cmdAuth, saved string) string {
	cmdPlansSeen.Lock()
	seen, ok := cmdPlansSeen.m[a.APIKey]
	cmdPlansSeen.Unlock()
	switch {
	case ok && seen.plan != "" && time.Since(seen.at) < cmdPlanKeep:
		return seen.plan
	case ok && seen.plan == "" && time.Since(seen.at) < cmdPlanRetry:
		return saved
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, plan, _, _, read := cmdSubscription(ctx, a)
	if !read {
		plan = ""
	}
	cmdRemember(a.APIKey, plan)
	return firstNonEmpty(plan, saved)
}

// ---- allowance ----------------------------------------------------------------

// cmdPlans names the plans as the CLI does, by their id's start, longest
// first, with the dollars of credits each gives a month (its getPlanInfo;
// "individual-pro-v1" is the old Pro, $80 of them).
var cmdPlans = []struct {
	id, name string
	monthly  float64
}{
	{"individual-provider", "Provider", 15}, {"individual-pro-v1", "Pro", 80}, {"individual-goat", "GOAT", 70},
	{"individual-ultra", "Ultra", 300}, {"individual-max", "Max", 150}, {"individual-pro", "Pro", 30},
	{"individual-go", "Go", 10}, {"teams-pro", "Teams Pro", 40},
}

func cmdPlanOf(id string) (name string, monthly float64) {
	id = strings.ReplaceAll(strings.ToLower(id), "_", "-")
	for _, p := range cmdPlans {
		if strings.HasPrefix(id, p.id) {
			return p.name, p.monthly
		}
	}
	return "", 0
}

func cmdPlanName(id string) string {
	name, _ := cmdPlanOf(id)
	return name
}

// cmdCredits is /alpha/billing/credits as the CLI's /usage reads it
// (projectUsageView): the dollars left of the month's credits, and of the
// bought and free ones beside them, under "credits"; the plan's windows
// next to it, not in it —
//
//	{"credits":{"planId":"individual-goat-monthly","monthlyCredits":41.2,
//	            "purchasedCredits":5,"freeCredits":0},
//	 "windowLimits":{"limited":true,
//	   "fiveHour":{"used":3.1,"cap":10,"resetAt":1790000000000},
//	   "weekly":{"used":12,"cap":40,"resetAt":1790400000000}},
//	 "sandboxMinutes":{…},"sandboxAccess":false}
//
// resetAt is in milliseconds (the CLI compares it with Date.now()); used
// and cap are only ever divided, one by the other.
type cmdWindow struct {
	Used    any `json:"used"`
	Cap     any `json:"cap"`
	ResetAt any `json:"resetAt"` // unix ms (seconds or a time taken too)
}

type cmdCredits struct {
	Credits struct {
		PlanID    string `json:"planId"`
		Monthly   any    `json:"monthlyCredits"`
		Purchased any    `json:"purchasedCredits"`
		Free      any    `json:"freeCredits"`
	} `json:"credits"`
	Windows *struct {
		FiveHour *cmdWindow `json:"fiveHour"`
		Weekly   *cmdWindow `json:"weekly"`
	} `json:"windowLimits"`
}

// cmdTime reads a reset time: unix seconds or milliseconds, or RFC 3339.
func cmdTime(v any) *time.Time {
	if s, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return &t
		}
	}
	n, ok := number(v)
	if !ok || n <= 0 {
		return nil
	}
	if n < 1e12 {
		n *= 1000
	}
	t := time.UnixMilli(int64(n))
	return &t
}

// cmdWindows is the plan's 5-hour and weekly windows in a credits reply,
// those it gives a cap.
func cmdWindows(c cmdCredits) []QuotaWindow {
	out := []QuotaWindow{}
	if c.Windows == nil {
		return out
	}
	for _, w := range []struct {
		name string
		span time.Duration
		w    *cmdWindow
	}{{"5 hours", 5 * time.Hour, c.Windows.FiveHour}, {"Weekly", 7 * 24 * time.Hour, c.Windows.Weekly}} {
		if w.w == nil {
			continue
		}
		used, ok1 := number(w.w.Used)
		limit, ok2 := number(w.w.Cap)
		if !ok1 || !ok2 || limit <= 0 {
			continue
		}
		out = append(out, QuotaWindow{Name: w.name, Used: min(100, 100*max(0, used)/limit), Span: w.span,
			ResetsAt: cmdTime(w.w.ResetAt)})
	}
	return out
}

// cmdLeft is the dollars left on the account, the month's credits and the
// bought and free ones together (the CLI's totalRemaining), and the
// month's alone; ok is false when the reply tells none of them.
func cmdLeft(c cmdCredits) (monthly, left float64, ok bool) {
	for i, v := range []any{c.Credits.Monthly, c.Credits.Purchased, c.Credits.Free} {
		if n, is := number(v); is {
			n = max(0, n)
			if i == 0 {
				monthly = n
			}
			left += n
			ok = true
		}
	}
	return monthly, left, ok
}

// cmdQuotaOf makes the credits reply into the plan's windows: the 5-hour
// and weekly limits, and the month's credits as one more. A card's
// Balance is shown instead of its windows — it is a key's money, not an
// allowance — so, set beside them, it hid both limits on the Usage page,
// the panel and the TUI. The dollars are a window when the plan is known,
// used of the CLI's pool (the plan's month of credits, or what is left of
// it if more, and the bought and free ones) as its /usage bar is; a reply
// with neither windows nor a plan is a Balance still.
func cmdQuotaOf(q SubscriptionQuota, c cmdCredits) SubscriptionQuota {
	name, planMonthly := cmdPlanOf(c.Credits.PlanID)
	if name != "" {
		q.Plan = name
	}
	q.Windows = cmdWindows(c)
	monthly, left, known := cmdLeft(c)
	switch {
	case !known:
	case planMonthly > 0:
		pool := max(planMonthly, monthly) + left - monthly
		q.Windows = append(q.Windows, QuotaWindow{Name: "Credits", Used: min(100, 100*(pool-left)/pool),
			Display: money("$", pool-left) + " / " + money("$", pool)})
	case len(q.Windows) == 0:
		q.Balance = money("$", left)
	}
	return q
}

func cmdQuota(ctx context.Context, l Login, a cmdAuth) SubscriptionQuota {
	q := SubscriptionQuota{Provider: CommandCodePlanID, Name: "Command Code", Icon: "commandcode", Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	// the plan is said even while the credits can't be read (Command Code
	// answers 503, "Couldn't verify your credit balance just now", at times)
	id, plan, until, renew, planOK := cmdSubscription(ctx, a)
	var c cmdCredits
	if err := accountJSON(ctx, cmdAPI+"/alpha/billing/credits", a.APIKey, nil, &c); err != nil {
		q.Error = err.Error()
	} else {
		// billing/credits leaves the plan out: the month of credits the pool
		// is made of is the subscription's plan's
		if c.Credits.PlanID == "" {
			c.Credits.PlanID = id
		}
		q = cmdQuotaOf(q, c)
	}
	if planOK {
		q.Plan, q.Until, q.Renew = plan, until, renew
		cmdRemember(a.APIKey, plan)
	}
	return q
}

// cmdNoPlan is the plan of an account with no subscription: its key is
// billed against the credits it bought, if any.
const cmdNoPlan = "No plan"

// cmdSubscription is the account's plan (its id, and its name), when its period ends and whether
// it renews then, as billing/subscriptions says; cmdNoPlan when it has
// none. ok is false when that can't be read.
func cmdSubscription(ctx context.Context, a cmdAuth) (id, plan string, until *time.Time, renew string, ok bool) {
	var r struct {
		Success *bool `json:"success"` // false when Command Code couldn't tell ("write CONNECTION_CLOSED …"), though a 200
		Data    *struct {
			PlanID           string `json:"planId"`
			Status           string `json:"status"`
			CurrentPeriodEnd any    `json:"currentPeriodEnd"`
			CancelAtEnd      *bool  `json:"cancelAtPeriodEnd"`
		} `json:"data"`
	}
	if accountJSON(ctx, cmdAPI+"/alpha/billing/subscriptions", a.APIKey, nil, &r) != nil || (r.Success != nil && !*r.Success) {
		return "", "", nil, "", false
	}
	if d := r.Data; d != nil && d.PlanID != "" && d.Status != "canceled" && d.Status != "incomplete_expired" {
		if d.CancelAtEnd != nil {
			renew = map[bool]string{true: "off", false: "auto"}[*d.CancelAtEnd]
		}
		return d.PlanID, firstNonEmpty(cmdPlanName(d.PlanID), d.PlanID), cmdTime(d.CurrentPeriodEnd), renew, true
	}
	return "", cmdNoPlan, nil, "", true
}

func cmdLoginQuota(ctx context.Context, l Login) SubscriptionQuota {
	for _, c := range cmdLogins() {
		if strings.EqualFold(c.User, l.User) {
			return cmdQuota(ctx, l, c.auth)
		}
	}
	return SubscriptionQuota{Provider: CommandCodePlanID, Plan: l.Plan, Windows: []QuotaWindow{}, Error: "not signed in"}
}

// ---- signing in ---------------------------------------------------------------

// startCommandCodeSignIn is the CLI's browser sign-in: Studio asks the
// user to approve a key for this machine, then posts it (a form, or JSON
// from an older Studio) to the callback, which sends the browser on to a
// page saying it is done.
func startCommandCodeSignIn(s *signInFlow) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	q := url.Values{}
	q.Set("callback", fmt.Sprintf("http://127.0.0.1:%d/callback", port))
	q.Set("state", s.state)
	q.Set("mode", "redirect")
	srv := &http.Server{Handler: http.HandlerFunc(s.commandCodeCallback), ReadHeaderTimeout: 10 * time.Second}
	s.mu.Lock()
	s.st.URL = cmdStudio + "/studio/auth/cli?" + q.Encode()
	// Studio's post can't reach a magpie on a server or in Docker: a key
	// made on its keys page and pasted finishes it instead
	s.st.PasteKey, s.st.KeysURL = true, cmdKeysURL
	s.srv = srv
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// cmdWhoamiWait is how long a sign-in waits to ask whoami again.
var cmdWhoamiWait = time.Second

// cmdOrigins are the Studio pages that may post to the callback.
var cmdOrigins = []string{"https://commandcode.ai", "https://staging.commandcode.ai"}

func (s *signInFlow) commandCodeCallback(w http.ResponseWriter, r *http.Request) {
	origin := cmdOrigins[0]
	for _, o := range cmdOrigins {
		if r.Header.Get("Origin") == o {
			origin = o
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
	}
	switch {
	case r.Method == http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
		return
	case r.URL.Path == "/callback/complete" && r.Method == http.MethodGet:
		st := s.status()
		if st.State == "done" {
			signInPage(w, true, "You're signed in", fmt.Sprintf("%s is added to magpie. You can close this tab.", st.User))
		} else {
			signInPage(w, false, "Sign-in didn't finish", firstNonEmpty(st.Error, "Start it again from magpie."))
		}
		return
	case r.URL.Path == "/cancel":
		s.finish(SignInState{State: "canceled"})
		w.WriteHeader(http.StatusNoContent)
		return
	case r.URL.Path != "/callback":
		http.NotFound(w, r)
		return
	case r.Method != http.MethodPost:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 10_000))
	var got struct {
		cmdAuth
		State string `json:"state"`
		Error string `json:"error"`
		Desc  string `json:"error_description"`
	}
	isJSON := strings.HasPrefix(strings.TrimSpace(strings.ToLower(r.Header.Get("Content-Type"))), "application/json")
	if isJSON {
		_ = json.Unmarshal(body, &got)
	} else {
		f, _ := url.ParseQuery(string(body))
		got.APIKey, got.UserID, got.UserName, got.KeyName = f.Get("apiKey"), f.Get("userId"), f.Get("userName"), f.Get("keyName")
		got.State, got.Error, got.Desc = f.Get("state"), f.Get("error"), f.Get("error_description")
	}
	answer := func(ok bool, msg string) {
		if isJSON {
			w.Header().Set("Content-Type", "application/json")
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": ok, "error": msg})
			return
		}
		// as the CLI answers: on to the page that says how it went
		w.Header().Set("Location", "/callback/complete?state="+url.QueryEscape(got.State))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusSeeOther)
	}
	if got.State != s.state {
		// not ours: someone else's page, or a stale tab
		w.WriteHeader(http.StatusForbidden)
		signInPage(w, false, "This link isn't from magpie's sign-in", "Start it again from magpie.")
		return
	}
	if s.status().State != "waiting" {
		answer(false, "this sign-in is over")
		return
	}
	if got.Error != "" {
		msg := firstNonEmpty(got.Desc, got.Error)
		if got.Error == "access_denied" {
			msg = "the sign-in was denied"
		}
		s.finish(SignInState{State: "failed", Error: msg})
		answer(false, msg)
		return
	}
	if got.APIKey == "" {
		answer(false, "Command Code sent back no key")
		return
	}
	if !s.claim() {
		// a key was pasted too, and that one is being kept
		answer(false, "this sign-in is already finishing")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// the browser comes back for its page after this: the server stays up
	// a while for it, where finish would close it within a second
	if err := s.commandCodeKeep(ctx, got.cmdAuth, true); err != nil {
		answer(false, err.Error())
		return
	}
	answer(true, "")
}

// commandCodeKeep names the account a key is Command Code's, keeps it beside
// the others, and finishes the sign-in. keepServer leaves the callback
// server up a while, for the browser to come back for its page.
func (s *signInFlow) commandCodeKeep(ctx context.Context, a cmdAuth, keepServer bool) error {
	who, plan, err := cmdSignedIn(ctx, a)
	if err != nil {
		s.finish(SignInState{State: "failed", Error: err.Error()})
		return err
	}
	a.UserName = who
	auth, _ := json.Marshal(a)
	ownUser, _, hasOwn := cmdOwn()
	if !hasOwn {
		ownUser = ""
	}
	if err := addSideLogin(savedLogin{Agent: CommandCodePlanID, User: who, Plan: plan, Auth: auth}, ownUser, func(savedLogin) {}); err != nil {
		s.finish(SignInState{State: "failed", Error: err.Error()})
		return err
	}
	if keepServer {
		s.mu.Lock()
		srv := s.srv
		s.srv = nil
		s.mu.Unlock()
		if srv != nil {
			time.AfterFunc(10*time.Second, func() { _ = srv.Close() })
		}
	}
	s.finish(SignInState{State: "done", User: who, Plan: plan, Using: hasOwn && strings.EqualFold(ownUser, who)})
	return nil
}

// cmdKeysURL is Studio's page of API keys, where one is made to paste.
var cmdKeysURL = "https://commandcode.ai/settings/keys"

// commandCodeKey finishes a sign-in with a key pasted from Studio's keys
// page, as the CLI takes one ("Authorize in browser, or paste API key
// here"). It is how a magpie the browser can't reach — on a server, in
// Docker — signs in: Studio posts the key to the callback on 127.0.0.1 in
// the background, so the page the browser ends on has nothing in its
// address to paste.
func (s *signInFlow) commandCodeKey(raw string) error {
	key := strings.TrimSpace(raw)
	switch {
	case key == "":
		return errors.New("paste an API key from Command Code's keys page")
	case strings.Contains(key, "://"):
		return errors.New("that's an address: Command Code sends its key in the background, so make a key on its keys page and paste that")
	case strings.ContainsAny(key, " \t\r\n"):
		return errors.New("that doesn't look like a Command Code API key")
	}
	if s.status().State != "waiting" {
		return errors.New("this sign-in is over; start it again")
	}
	if !s.claim() {
		return errors.New("this sign-in is already finishing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.commandCodeKeep(ctx, cmdAuth{APIKey: key}, false)
}

// cmdSignedIn names a new key's account with whoami, and reads its plan.
// Only whoami turning the key down (401, 403) fails the sign-in: the CLI's
// own browser sign-in keeps the key Studio posts without asking whoami at
// all, and whoami has answered a key just made with a 500, so a whoami
// that errs is asked again a few times and then passed over for the name
// Studio sent with the key.
func cmdSignedIn(ctx context.Context, a cmdAuth) (who, plan string, err error) {
	var me struct {
		User struct {
			ID       string `json:"id"`
			UserName string `json:"userName"`
			Name     string `json:"name"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	for try := 0; ; try++ {
		err := accountJSON(ctx, cmdAPI+"/alpha/whoami", a.APIKey, nil, &me)
		var st *accountStatusError
		if err == nil {
			break
		}
		if errors.As(err, &st) && (st.status == http.StatusUnauthorized || st.status == http.StatusForbidden) {
			return "", "", fmt.Errorf("Command Code didn't take the new key: %w", err)
		}
		if try == 2 || ctx.Err() != nil {
			if firstNonEmpty(a.UserName, a.UserID) == "" {
				return "", "", fmt.Errorf("Command Code couldn't say which account signed in: %w", err)
			}
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(cmdWhoamiWait):
		}
	}
	who = firstNonEmpty(me.User.UserName, a.UserName, me.User.Email, me.User.ID, a.UserID)
	if who == "" {
		return "", "", fmt.Errorf("Command Code didn't say which account signed in")
	}
	if _, p, _, _, ok := cmdSubscription(ctx, a); ok {
		plan = p
	}
	return who, plan, nil
}

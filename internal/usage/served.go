package usage

import (
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// Which model answered: a vendor may serve a request with another model
// than the one asked for — a cheaper one when it's busy — and its reply
// says so in its model field. Most echo the name asked for, or its dated
// or pinned version (gpt-5 as gpt-5-2025-08-07, claude-sonnet-4-5 as
// claude-sonnet-4-5-20250929, gemini-2.5-pro as models/gemini-2.5-pro-001),
// which is the same model.

// versionTail is what a vendor puts after a model's name for the version
// it answered with: a date, a build number, Bedrock's v1:0, latest,
// preview. Not v3 alone: deepseek-v3 is another model than deepseek-v2.
var versionTail = regexp.MustCompile(`(?:[-_@:](?:\d{4}-\d{2}-\d{2}|\d{2}-\d{2}|\d{6,8}|\d{3,4}|v\d+:\d+|latest|preview|exp))+$`)

// vendorDot is Bedrock's region and maker before a model's name
// (us.anthropic.claude-…), the geography of an inference profile as
// catalog's bedrockGeos has them: global. and us-gov. among them. The
// makers are those models.dev's bedrock list names ids by (xai.grok-4.7,
// writer.palmyra-x5, moonshot.kimi-k2-thinking, nvidia.nemotron), which
// the reply names as Anthropic's own are, without the geography.
var vendorDot = regexp.MustCompile(`^(?:(?:global|us-gov|[a-z]{2,4})\.)?(?:anthropic|amazon|meta|mistral|cohere|ai21|deepseek|qwen|openai|google|moonshotai|moonshot|minimax|nvidia|writer|xai|zai)\.`)

// contextTail is what Claude Code writes after a model's name for the size of
// its context: claude-opus-5[1m].
var contextTail = regexp.MustCompile(`\[[^\]]*\]$`)

// windowTail is the window magpie offers a model at beside its default
// (grok-4.7@500k, Cursor's grok-4.7@256k): the vendor answers under the
// plain name, which is the same model (#1645).
var windowTail = regexp.MustCompile(`@\d+[km]?$`)

// bareModel is a model's name without its maker or path, its version, the
// size of its context or its case.
func bareModel(m string) string {
	m = contextTail.ReplaceAllString(strings.ToLower(strings.TrimSpace(m)), "")
	m = windowTail.ReplaceAllString(m, "")
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	m = vendorDot.ReplaceAllString(m, "")
	if loc := versionTail.FindStringIndex(m); loc != nil && loc[0] > 0 {
		m = m[:loc[0]]
	}
	return m
}

// Swapped reports whether served is another model than sent: not the same
// name, however dated, pinned or prefixed. A call that went out as one level
// of a model (an Antigravity account's gemini-3.8-flash-medium) answered
// under the model's own name is that model at that level: Antigravity's
// reply names the family, not the variant (#462). Another level is another
// model, as AntigravitySentID has it. A vendor's "auto" (Copilot's,
// Cursor's) asked it to pick, so whichever answers wasn't swapped in, nor
// is the member another magpie's routing group sent it to (GroupRouted),
// nor a vendor's own name for the deployment serving the model
// (ServingName), nor the model a relay sold under a name of its own
// (RelayRenamed), nor the model at a region's deployment (RegionTail).
func Swapped(sent, served string) bool {
	a, b := bareModel(sent), bareModel(served)
	return a != "" && b != "" && squash(a) != squash(b) && a != "auto" && squash(bareModel(provider.EffortFamily(a))) != squash(b) && !GroupRouted(sent, served) && !ServingName(sent, served) && !RelayRenamed(sent, served) && !VendorAlias(sent, served) && !RegionTail(sent, served)
}

// regionTail is a region a vendor's id names its deployment of a model
// by: WorkBuddy AI lists DeepSeek V4.1 Flash as deepseek-v4.1-flash-sg,
// its paid id for the Singapore deployment (the plugin's product config
// names it paidId to the free deepseek-v4.1-flash, one window for both),
// and answers under the model's own name.
var regionTail = regexp.MustCompile(`[-_](?:sg|us|eu|cn|intl|global)$`)

// RegionTail reports whether one of sent and served is the other with a
// region after it: the same model at that region's deployment, not
// another model swapped in (plugins#628, chansee97). A region in both,
// or a name that differs besides it, is not this.
func RegionTail(sent, served string) bool {
	a, b := bareModel(sent), bareModel(served)
	if a == "" || b == "" {
		return false
	}
	ra, rb := regionTail.ReplaceAllString(a, ""), regionTail.ReplaceAllString(b, "")
	return (ra != a) != (rb != b) && ra != "" && squash(ra) == squash(rb)
}

// vendorAlias is a vendor's own alias for one of its models, as the
// vendor's docs state it, spelled as squash spells it: DeepSeek renamed
// its V4.1 Flash deepseek-flash (api-docs.deepseek.com/updates,
// 2026-09-10: "Change the model name to deepseek-flash to call the latest
// V4.1 Flash model", the name its later entries keep answering under), so
// a request for either is answered under whichever name the deployment
// writes (#4593, chowyuan1314). A name the docs retire or route elsewhere
// (deepseek-v4-flash to V4.1 Flash) is another model, not an alias.
var vendorAlias = map[string]string{
	"deepseekflash": "deepseekv41flash",
}

// VendorAlias reports whether served is the vendor's own alias for the
// model sent — an official name its docs give one of its models, not
// another model swapped in. Both ways: a request under the alias answered
// under the name it stands for is the same model too. A vendor may rename
// a model over time, and what its name stands for is its docs' word, so
// only pairs the docs state go here; catalog's SameModel keeps the two ids
// apart (the catalog lists deepseek-flash as an id of its own).
func VendorAlias(sent, served string) bool {
	a, b := squash(bareModel(sent)), squash(bareModel(served))
	if a == "" || b == "" {
		return false
	}
	return vendorAlias[a] == b || vendorAlias[b] == a
}

// ListedAs reports whether served is the name the provider's own model
// list gives the model asked for: Qoder's plugin lists its free Qwen model
// as qfmodel, named Qwen3.8-Flash, and since 0.2.38 (plugins#412) answers
// under that name, which was marked a swap to "actual Qwen3.8-Flash"
// (jzing.draw on Discord, 10-27). The list is the provider's word that
// the two are one model. It is asked only once a reply already reads as
// swapped, and each provider's list is read at most once a minute.
func ListedAs(prov, model, served string) bool {
	if prov == "" || model == "" || served == "" {
		return false
	}
	n := listedNames(prov)[model]
	return n != "" && n != model && SameSpelled(n, served)
}

// listedFor is how long ListedAs keeps a provider's names.
const listedFor = time.Minute

var listed struct {
	sync.Mutex
	by map[string]listedEntry
}

type listedEntry struct {
	names map[string]string
	at    time.Time
}

// listedModels is the provider's models as magpie lists them; a test
// replaces it.
var listedModels = func(prov string) map[string]string {
	p, err := provider.Find(prov)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, m := range p.Available() {
		if m.Name != "" {
			out[m.ID] = m.Name
		}
	}
	return out
}

// ListedForTest gives ListedAs these lists, provider by model id, and
// forgets every list read before, until restore.
func ListedForTest(lists map[string]map[string]string) (restore func()) {
	old := listedModels
	listedModels = func(prov string) map[string]string { return lists[prov] }
	forgetListed()
	return func() { listedModels = old; forgetListed() }
}

func forgetListed() {
	listed.Lock()
	listed.by = nil
	listed.Unlock()
}

func listedNames(prov string) map[string]string {
	listed.Lock()
	e, ok := listed.by[prov]
	listed.Unlock()
	if ok && time.Since(e.at) < listedFor {
		return e.names
	}
	// read outside the lock: a provider's list can take a moment, and
	// another reader of another provider needn't wait on it
	names := listedModels(prov)
	listed.Lock()
	if listed.by == nil {
		listed.by = map[string]listedEntry{}
	}
	listed.by[prov] = listedEntry{names: names, at: time.Now()}
	listed.Unlock()
	return names
}

// RelayRenamed reports whether sent is served's own name with a relay's
// word in front of it: a relay that sells kimi-k3 as moonshot-kimi-k3 is
// answered as kimi-k3 (#1498, liuweifeng), one that sells
// deepseek-v4.1-flash as claude-deepseek-v4.1-flash (#1383) as
// deepseek-v4.1-flash. What is left must name a version, so a bare word
// (flash, mini) is never taken for the model sent.
func RelayRenamed(sent, served string) bool {
	a, b := bareModel(sent), squash(bareModel(served))
	if b == "" || !strings.ContainsAny(b, "0123456789") || !strings.ContainsFunc(b, unicode.IsLetter) {
		return false
	}
	for i := 1; i < len(a); i++ {
		if a[i-1] == '-' && squash(a[i:]) == b {
			return true
		}
	}
	return false
}

// ServingName reports whether served is the vendor's own name for the
// deployment serving the model sent, which no request asks for by: Google's
// for a Gemini model (GeminiServing), xAI's for a Grok one (GrokServing),
// ClinePass's for one of its models (ClineServing).
func ServingName(sent, served string) bool {
	return GeminiServing(sent, served) || GrokServing(sent, served) || ClineServing(sent, served)
}

// ClineServing reports whether served is ClinePass's name for the virtual
// model config ("vmc/") serving the cline-pass/ model sent: the model's
// own name, its version's dot written as "p". ClinePass answers
// cline-pass/deepseek-v4.1-flash-1017 as vmc/deepseek-v4p1-flash-1017
// whichever upstream served it, DeepSeek's own API included
// (finalProvider "deepseek": hy125245 on X, 10-23), and the swap it was
// marked read as the request going to another provider (zRain on
// Discord, 10-24). It holds for ClinePass's models only, and for the same
// model: vmc/deepseek-v4p1-flash for cline-pass/deepseek-v4-pro is still
// a swap, as is a vmc/ name for a model not asked of ClinePass.
func ClineServing(sent, served string) bool {
	s := strings.ToLower(strings.TrimSpace(served))
	if !strings.HasPrefix(s, "vmc/") || !strings.Contains(strings.ToLower(sent), "cline-pass/") {
		return false
	}
	a := squash(bareModel(sent))
	return a != "" && squash(bareModel(vmcPoint.ReplaceAllString(s, "$1.$2"))) == a
}

// vmcPoint is a version's dot as ClinePass's vmc/ names write it (v4p1
// for v4.1), the part ClineServing reads past
var vmcPoint = regexp.MustCompile(`(\d)p(\d)`)

// GrokServing reports whether served is xAI's name for the build serving
// the Grok model sent: the model's own name with "-build" after its
// version. xAI answers grok-4.7 as grok-4.7-build, and grok-4.6 as
// grok-4.6-build (#1455, cybershape; two relays' logs say the same), and
// its own list (SuperGrok's, 2026-10-02) offers grok-4.7 and its fast
// variant grok-4.7-build-fast, never a grok-4.7-build to ask for. It holds
// for Grok models only, and for the same variant: grok-4.7 answered as
// grok-4.7-build-fast (the fast one), grok-4.6-build or grok-4.7-mini is
// still a swap, as is gpt-5 answered as gpt-5-build.
func GrokServing(sent, served string) bool {
	a := squash(bareModel(provider.EffortFamily(bareModel(sent))))
	b := bareModel(served)
	m := grokBuild.FindStringSubmatchIndex(b)
	if m == nil || !strings.HasPrefix(a, "grok") {
		return false
	}
	return squash(b[:m[2]]+b[m[3]:]) == a
}

// grokBuild is the "-build" after a Grok model's version (grok-4.7-build,
// grok-4.7-build-fast), the part GrokServing reads past.
var grokBuild = regexp.MustCompile(`^grok-[0-9][0-9.]*(-build)(?:-|$)`)

// GeminiServing reports whether served is the name Google's backend gives
// the deployment that serves the Gemini model sent: the model's own name
// with one letter after it (sessions.GeminiDeployment). Antigravity
// answers every level of gemini-3.8-flash (-low, -medium, -high) with
// modelVersion gemini-3.8-flash-n, an id no request can ask for (404) and
// fetchAvailableModels never lists, so it is that model, not another one
// swapped in (dumplings on Discord); gemini-4-argon's is gemini-4-argon-d
// (oioio on Discord). It holds for Gemini models only: gpt-5 answered as
// gpt-5-n is still a swap, as is gemini-3.8-flash answered as
// gemini-3.8-flash-lite or gemini-3.7-flash-n.
func GeminiServing(sent, served string) bool {
	a := squash(bareModel(provider.EffortFamily(bareModel(sent))))
	b, ok := sessions.GeminiDeployment(bareModel(served))
	return ok && strings.HasPrefix(a, "gemini") && squash(b) == a
}

// squash is a bare name without its separators, so a vendor that writes
// the dots of a version as hyphens (Volcengine Ark: deepseek-v4.1-flash
// answered as deepseek-v4-1-flash, vincentzhang on Discord) or underscores
// names the same model.
var squash = strings.NewReplacer(".", "", "-", "", "_", "", " ", "").Replace

// SameSpelled reports whether served is sent spelled with other separators
// or case: a row or route kept marked swapped before Swapped knew it is
// read as not swapped.
func SameSpelled(sent, served string) bool {
	a := squash(bareModel(sent))
	return a != "" && a == squash(bareModel(served))
}

// GroupRouted reports whether sent is a routing group of another magpie
// (a remote magpie's "group/<id>", the one name only a magpie answers
// to) and served the model its reply named: the member the group routed
// the request to, which is the group doing its job, not the vendor
// serving another model than asked (莫 on Discord: group/auto-… answered
// by deepseek/deepseek-v4.1-flash, marked a swap).
func GroupRouted(sent, served string) bool {
	return strings.TrimSpace(served) != "" && remoteGroup(strings.TrimSpace(sent))
}

// remoteGroup reports whether id is a routing group's id as a magpie
// gives it: group/<id>, or auto/<model> for a group it found.
func remoteGroup(id string) bool {
	return strings.HasPrefix(id, provider.GroupPrefix) || strings.HasPrefix(id, provider.AutoPrefix)
}

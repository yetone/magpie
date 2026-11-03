package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Claude Desktop, pointed at a third-party gateway, keeps a model only when
// its id reads as Anthropic's: it says claude, sonnet, opus, haiku, fable,
// mythos or anthropic, and nowhere names another vendor's model. Its check
// (qo in its app.asar, 2.7032) is
//
//	Vxe.test(id) ? false : Ko.test(id) || ["claude",…,"anthropic"].some(w => id.includes(w))
//
// on the lowercased id, with Vxe the list below. It runs on /v1/models rows,
// on Models entries typed in by hand ("model routing must reference an
// Anthropic model") and on the model a session is started with, so
// anthropic/deepseek/deepseek-flash is turned away for its "deepseek".
var (
	desktopDenied = regexp.MustCompile(`ark-code|astron|command-r|deepseek|doubao|gemini|gemma|glm|gpt|grok|hermes|hy3|kimi|lfm|\bling\b|llama|longcat|mimo|minimax|mistral|mixtral|moonshot|nemotron|openai|phi-|qianfan|qwen|tc-code|\bunic\b|yi-|stepfun|step-3|seed-|bytedance|hunyuan|granite|amazon\.nova|nova-|devstral|ministral|ernie|codex|arcee|trinity|abab|phi\d|\bk2\.|\bm2\.|jamba|arctic|solar|mercury|zamba|kat-coder|\bds-|dpsk`)
	desktopTier   = regexp.MustCompile(`^(sonnet|opus|haiku|fable|mythos)(-[\d.]+)?$`)
	desktopWords  = []string{"claude", "sonnet", "opus", "haiku", "fable", "mythos", "anthropic"}
)

// desktopAccepts is Claude Desktop's check of a gateway model id.
func desktopAccepts(id string) bool {
	l := strings.ToLower(id)
	if desktopTier.MatchString(l) {
		return true
	}
	if desktopDenied.MatchString(l) {
		return false
	}
	for _, w := range desktopWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// desktopKnowsMythos says a Claude Desktop build keeps the mythos word its
// 2.7032 filter knows (desktopWords). Older builds have no such word: the
// Store's 1.6608.2 keeps a gateway model only when its id reads as a
// sonnet|opus|haiku tier or has claude, sonnet, opus, haiku or anthropic in
// it (its app.asar has neither mythos nor fable), so every reasoning-level
// model listed by mythos-magpie-<n> vanished from its menu
// (sublatesublate-design). The version a User-Agent names ("Mozilla/5.0 …
// Claude/2.7032.0 Chrome/…") is the only word magpie gets of the build: one
// that says a version below 2 falls back to the plain alias every build
// keeps (no effort picker there, and those builds have no mythos effort
// table either). Anything else keeps the picker alias: 2 and up, and a
// request that names no build at all, as the Code tab's Claude Code
// ("claude-cli/… (external, claude-desktop)", known by its key) whose
// ids are mythos-magpie-<n> already.
var desktopUAVersion = regexp.MustCompile(` Claude/(\d+)`)

func desktopKnowsMythos(ua string) bool {
	m := desktopUAVersion.FindStringSubmatch(ua)
	if m == nil {
		return true
	}
	n, err := strconv.Atoi(m[1])
	return err != nil || n >= 2
}

// desktopAlias is the prefix of the id a model is listed by to Claude
// Desktop when its own id names no Claude model: anthropic/magpie-<number>,
// the number a hash of magpie's id, so it stays the model's while the
// catalog changes around it. Desktop's list of other vendors' names grows
// from release to release, so no part of such a model's id is shown to it;
// its display_name and description carry the model's name and magpie id.
const desktopAlias = "anthropic/magpie-"

// Claude Desktop offers a thinking-effort picker only for a model it knows:
// in a gateway's mode it reads no effort from /v1/models (IIt keeps id,
// display_name, description, supports_1m and anthropic_family_tier), and
// its signed model catalog can't be a gateway's, so its levels come from
// tIt in its app.asar (index.chunk-D3OyLXgG.js, 2.7032):
//
//	let t=IC(e), n=HFt[t] ?? (UFt.test(t) ? VFt : void 0)
//
// HFt its table of Claude models (claude-sonnet-4-6, claude-opus-4-8, …),
// UFt /^(?:claude-)?(?:fable|mythos)(?:-|$)/ with VFt low, medium, high,
// xhigh and max (high recommended, thinking always on), and IC the id
// lowercased with a Bedrock-style "<profile>.anthropic." prefix and a date
// taken off. No provider/model id is either, so no model magpie served had
// the picker (ARNO). A model with reasoning levels is listed by one of
// these instead:
//
//   - a Claude model (claude-opus-4-8 at any provider):
//     magpie-<number>.anthropic.claude-opus-4-8, which IC reads as
//     claude-opus-4-8 and Claude Code as Claude Opus 4.8, as before;
//   - any other: mythos-magpie-<number>, which UFt matches. Nothing in it
//     says haiku, sonnet or opus, so Desktop's small_fast pick is as it
//     was, and Claude Code (which knows claude-mythos-… only) takes it for
//     a model it doesn't know and sends its effort as output_config.effort.
//
// The effort chosen reaches the gateway as thinking plus
// output_config.effort and is fitted to the model's own levels there.
const (
	desktopEffortAlias = "mythos-magpie-"
	desktopClaudeInfix = ".anthropic."
)

// desktopClaude is a model id that is Anthropic's own Claude model, as tIt
// knows it: claude-<tier>-<version>, a date taken off.
var (
	desktopClaude = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable|mythos)-\d+(?:-\d+)?$`)
	desktopDated  = regexp.MustCompile(`-\d{8}$`)
)

func aliasNumber(id string) string {
	h := fnv.New64a()
	h.Write([]byte(id))
	return fmt.Sprintf("%010d", h.Sum64()%1e10)
}

func aliasFor(id string) string { return desktopAlias + aliasNumber(id) }

// claudeModel is the Claude model an entry is, lowercased and without a
// date or a vendor's "anthropic/" in front, or "" when it is none. A version
// a provider writes with a dot (Copilot's claude-opus-5.5) is given with a
// dash, as Claude Desktop and Claude Code know the model (provider.ClaudeDashed).
func claudeModel(e provider.Entry) string {
	m := strings.ToLower(e.Model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	m = desktopDated.ReplaceAllString(provider.ClaudeDashed(m), "")
	if desktopClaude.MatchString(m) {
		return m
	}
	return ""
}

// claudeLooking is a model's id as Claude Desktop is shown it: one that
// gets its effort picker when the model has reasoning levels, else as it is
// when it already reads as a Claude model's, else its alias (unprefixed
// serves each again).
//
// A routing group is never listed as the Claude model its first member
// is: Desktop names an id it finds in its own model catalog by the
// catalog's name, so two groups led by claude-opus-5-5 read "Opus 5.5"
// there like the model itself, and the group's id would change each time
// its members are reordered. Its mythos-magpie-<n> keeps the picker and
// the group's own name.
func claudeLooking(e provider.Entry) string {
	if len(e.Efforts) > 0 {
		if m := claudeModel(e); m != "" && e.Group == "" {
			return "magpie-" + aliasNumber(e.ID) + desktopClaudeInfix + m
		}
		return desktopEffortAlias + aliasNumber(e.ID)
	}
	if desktopAccepts(e.ID) && !strings.HasPrefix(e.ID, desktopAlias) {
		// its Code tab hands it to Claude Code, which reads a dotted
		// Claude version (claude-opus-4.6) as Claude Opus 4
		return provider.ClaudeSpelled(e.ID)
	}
	return aliasFor(e.ID)
}

// DesktopID is the id Claude Desktop is shown e by, and its Code tab hands
// Claude Code (claudeLooking).
func DesktopID(e provider.Entry) string { return claudeLooking(e) }

// DesktopNumber is the number Claude Desktop's menu says beside e, so
// the Providers page can say it too and a row of that menu be matched to
// its provider (#2578): "" for every model Desktop names by our
// display_name, which carries the provider already. Only a Claude model
// with reasoning levels, listed as magpie-<number>.anthropic.<model>
// (claudeLooking), is named after Desktop's own catalog ("Opus 5.5"), so
// two providers' rows read alike and the menu tells them apart by that id
// and by the provider in its description (desktopSaidBy).
// Every other id is the model's own (no number) or an alias Desktop
// doesn't know (mythos-magpie-<n>, anthropic/magpie-<n>), named by
// display_name.
func DesktopNumber(e provider.Entry) string {
	rest, ok := strings.CutPrefix(claudeLooking(e), "magpie-")
	if !ok {
		return ""
	}
	n, _, ok := strings.Cut(rest, desktopClaudeInfix)
	if !ok {
		return ""
	}
	return n
}

// desktopModels is /v1/models as Claude Desktop is shown it: every model
// picked for it (provider.CatalogFor, the Agents page's model list) by an id
// it keeps (claudeLooking), named as the agents' lists name models
// (desktopNames: the Provider in model names setting, so its menu reads
// like Codex's, #1746). Desktop shows the name, not the id, and its
// description after it, so none is given but a 1M entry's (desktop1M) and
// the provider of a row Desktop names after its catalog (desktopSaidBy); it
// folds rows of one name into one entry, so no two of one name read the
// same (desktopNames). A model of 1M or more is listed by its "[1m]" id
// under settings.DesktopLongest, described as Desktop's own 1M entry is
// (desktop1M). A build that has no mythos word (desktopKnowsMythos) is
// shown a reasoning-level model by its plain alias instead of the picker
// one, which such a build would drop whole (#2426).
func desktopModels(entries []provider.Entry, knowsMythos bool) []map[string]any {
	names := desktopNames(entries)
	// the tier each model stands in for: the user's pick only (a model
	// picked for two is tagged with the first, desktopTierTurn sends the
	// other). A Claude model is never tagged with its own tier unasked:
	// Desktop's Claude Code then sent every haiku call (its small, fast
	// model) to whichever provider served a claude-haiku, Leee's while the
	// chat was on AgentRouter's gpt-6.1-sol (cybercaius on Discord).
	// Untagged, Claude Code asks for its own id of the tier, which
	// desktopTurn keeps on the chat's provider (servedBeside).
	tierOf := desktopTierOf(DesktopTiers())
	longest := settings.Load().DesktopLongest
	labels := provider.Labels(entries)
	data := make([]map[string]any, 0, len(entries))
	for i, e := range entries {
		m := modelObject(e)
		m["display_name"] = names[i]
		id := claudeLooking(e)
		if !knowsMythos && strings.HasPrefix(id, desktopEffortAlias) {
			id = aliasFor(e.ID)
		}
		m["id"] = id
		var said []string
		if desktopListed1M(e, longest) {
			m["id"] = m["id"].(string) + "[1m]"
			said = append(said, desktop1MSaid)
		}
		if by := desktopSaidBy(e, labels[i]); by != "" {
			said = append(said, by)
		}
		if len(said) > 0 {
			m["description"] = strings.Join(said, " · ")
		}
		if t := tierOf[e.ID]; t != "" {
			m["anthropic_family_tier"], m["is_family_default"] = t, true
		}
		data = append(data, m)
	}
	return data
}

// desktop1M is the max_input_tokens from which Claude Desktop adds a 1M
// entry beside a gateway model whose id has no "[1m]" (the discovery in its
// app.asar, 2.7032: supports_1m when given, else max_input_tokens >= 1e6;
// one whose id has "[1m]" gets none). Under settings.DesktopLongest such a
// model is listed by that 1M id alone (#1272), which magpie serves as the
// plain one (aliased, provider.Resolve take "[1m]" off), so a session
// saved on the plain id still runs.
//
// Desktop says which entry is the 1M one only on the entry it adds: its
// description is "1M context window" (WLt in JLt, 2.7032). A row of
// magpie's whose id has "[1m]" gets nothing of that (PIt gives supports1m
// to a bare id alone, and OIt folds a "[1m]" row only into a bare one
// beside it), so it was shown as the plain model, by name alone, and the
// switch read as doing the reverse of what it says (杰多夫 on Discord).
// The row is described as Desktop describes its own 1M entry
// (desktop1MSaid); its id, which Desktop hands Claude Code, has the "[1m]"
// that gives a session the 1M window.
const desktop1M = 1_000_000

// desktopListed1M says e is listed to Claude Desktop by its "[1m]" id,
// longest being settings.DesktopLongest.
func desktopListed1M(e provider.Entry, longest bool) bool { return longest && e.Context >= desktop1M }

// DesktopListed1M says Claude Desktop is shown e by its "[1m]" id, which
// its Code tab hands Claude Code, so a session on it has the 1M window.
func DesktopListed1M(e provider.Entry) bool {
	return desktopListed1M(e, settings.Load().DesktopLongest)
}

// desktop1MSaid is the description Claude Desktop gives the 1M entry it
// adds (WLt in its app.asar, 2.7032).
const desktop1MSaid = "1M context window"

// desktopSaidBy is the provider Claude Desktop's menu says under e, where
// the name it shows can't: "" but for a row Desktop names after its own
// model catalog (DesktopNumber), whose label, by the Provider in model
// names setting, has its provider after it.
//
// Desktop 4.4212 labels a picker row by its catalog's name before the
// gateway's display_name (ngn in its app.asar: labelOverride, then the
// catalog's name, then display_name; only an admin's inferenceModels list
// has labelOverride), so every provider's claude-opus-5-5 read "Opus 5.5"
// and the menu told them apart only by the tail of the id, "(…7922287072
// .anthropic.claude-opus-5-5)" (4 v5 on Discord: 老林夕, 新林夕, 百德 and
// chatflowsai's alike). The row's description is the gateway's before the
// catalog's, which has none for these models, and Desktop shows it under
// the name, so the provider goes there. The id stays as it was, for a
// session or a pick saved on it.
func desktopSaidBy(e provider.Entry, label string) string {
	if DesktopNumber(e) == "" || e.Provider.Name == "" || !strings.HasSuffix(label, " · "+e.Provider.Name) {
		return ""
	}
	return e.Provider.Name
}

// Claude Desktop's Code tab runs Claude Code with ANTHROPIC_DEFAULT_<TIER>_MODEL
// set to "" for every tier, unless the gateway's /v1/models tags a model
// with anthropic_family_tier (shortnameIdentityOverrides in its app.asar,
// 2.7032: the first so tagged, or the one also is_family_default). Untagged,
// a subagent on "sonnet" or "haiku" asked for Claude Code's own
// claude-sonnet-… and that, unserved, went to the chat's model: every
// subagent ran on it, whatever its tier (WilianWeng on Discord). The user
// picks a model per tier in magpie; a tier with none picked runs on the
// chat's model, or, when the chat is on a Claude model, on its provider's
// model of that tier (servedBeside).
var DesktopTierNames = []string{"opus", "sonnet", "haiku", "fable"}

func desktopTiersPath() string { return filepath.Join(settings.Dir(), "claude-desktop.tiers.json") }

// DesktopTiers is the catalog id picked for each of Claude Desktop's tiers.
func DesktopTiers() map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(desktopTiersPath())
	if err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// SetDesktopTier picks the model (a catalog id) Claude Desktop runs a tier
// on; "" takes the pick away.
func SetDesktopTier(tier, id string) error {
	// DesktopTiers reads a file that doesn't parse as no pick: what it held,
	// every other tier's pick, is kept aside before it is written over or
	// taken away
	if err := lastgood.KeepUnparsed(desktopTiersPath(), lastgood.Into[map[string]string]); err != nil {
		return err
	}
	tiers := DesktopTiers()
	if id == "" {
		delete(tiers, tier)
	} else {
		tiers[tier] = id
	}
	if len(tiers) == 0 {
		if err := os.Remove(desktopTiersPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(tiers, "", "  ")
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(desktopTiersPath(), append(b, '\n'), 0o600)
}

// DesktopCatalogID is the catalog id of a model as Claude Desktop names it:
// one of its aliases or the catalog id itself.
func DesktopCatalogID(id string) string {
	if c, ok := aliased(id); ok {
		return c
	}
	return id
}

// claudeTier is the tier a model id is a Claude model of (claude-opus-4-8,
// a provider's anthropic/claude-sonnet-5), "" when it is none.
func claudeTier(id string) string {
	m := strings.ToLower(strings.TrimSuffix(id, "[1m]"))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if !claudeFamily.MatchString(m) {
		return ""
	}
	for _, t := range DesktopTierNames {
		if strings.Contains(m, t) {
			return t
		}
	}
	return ""
}

// desktopTierTurn is the model picked for the tier of a Claude model
// Desktop's Claude Code asked for by Claude Code's own id, which magpie
// doesn't serve: a subagent on a tier no model is tagged for, or on one a
// model picked for two tiers isn't tagged with. "" when there is none.
func desktopTierTurn(asked string) string {
	if !unserved(asked) || strings.Contains(asked, "/") {
		return ""
	}
	t := claudeTier(asked)
	if t == "" {
		return ""
	}
	return DesktopTiers()[t]
}

// desktopTierAsked says asked is the model the user picked for one of
// Desktop's tiers. That pick is tagged is_family_default, so it is
// Desktop's own small_fast and the titles it asks for carry the pick's id;
// the pick is the user's choice of what small work runs on (#3264,
// Anarcadia), not the first id with haiku in it Desktop picks when no tier
// is picked — that one, and every other title, still follows the session's
// model, whose first token otherwise misses the title's timeout.
func desktopTierAsked(asked string) bool {
	id := strings.TrimSuffix(asked, "[1m]")
	for _, t := range DesktopTiers() {
		if t != "" && t == id {
			return true
		}
	}
	return false
}

// desktopNames are the names Claude Desktop is shown entries by, in order:
// as the agents' lists name them (provider.Labels, the Provider in model
// names setting: on, every name with its provider's after it; own, a name
// the user gave just as they wrote it; off, names plain — but two of one
// name keep their provider's, so they stay apart), and where that still
// leaves two alike (one provider's models named alike) the whole catalog id
// after it, for Desktop's menu shows the name alone.
func desktopNames(entries []provider.Entry) []string {
	out := provider.Labels(entries)
	same := map[string]int{}
	for _, s := range out {
		same[strings.ToLower(s)]++
	}
	for i, e := range entries {
		if same[strings.ToLower(out[i])] > 1 {
			out[i] += " (" + e.ID + ")"
		}
	}
	return out
}

// aliased is the catalog id an alias Claude Desktop was given stands for:
// anthropic/magpie-<number> (as it was listed before too),
// mythos-magpie-<number> or magpie-<number>.anthropic.<claude model>, with
// or without Claude Code's "[1m]"; or Claude Science's,
// claude-magpie-<number>.
func aliased(id string) (string, bool) {
	number := aliasDigits(id)
	if len(number) != 10 {
		return "", false
	}
	// the catalog Desktop and Science are listed from: a signed-in
	// subscription's model a group listing alone keeps out of Catalog
	// is still theirs (provider.SubscriptionKept)
	for _, e := range provider.CatalogOf("claude-desktop") {
		if aliasNumber(e.ID) == number {
			return e.ID, true
		}
	}
	return "", false
}

// magpieAliasAsked says asked is one of magpie's own alias ids for Claude
// Desktop or Claude Science (aliased's forms, with or without Claude
// Code's "[1m]"): anthropic/magpie-<number>, mythos-magpie-<number>,
// magpie-<number>.anthropic.<Claude model> or claude-magpie-<number>, the
// number one of magpie's own (aliasNumber's ten digits). Whether the
// catalog still lists a model under it is not asked: a session saved on an
// old alias is served all the same, and a reply to it must not name the
// alias either (#4667).
func magpieAliasAsked(id string) bool {
	n := aliasDigits(id)
	return len(n) == 10 && strings.IndexFunc(n, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// aliasDigits is the number in one of magpie's alias ids (aliased's forms,
// with or without Claude Code's "[1m]"), "" when id has none of the forms.
func aliasDigits(id string) string {
	id = strings.TrimSuffix(id, "[1m]")
	if n, ok := strings.CutPrefix(id, desktopAlias); ok {
		return n
	} else if n, ok := strings.CutPrefix(id, desktopEffortAlias); ok {
		return n
	} else if n, ok := strings.CutPrefix(id, scienceAlias); ok {
		// Claude Science's (science.go)
		return n
	} else if rest, ok := strings.CutPrefix(id, "magpie-"); ok {
		if i := strings.Index(rest, desktopClaudeInfix); i > 0 {
			return rest[:i]
		}
	}
	return ""
}

// isClaudeDesktop is a request from Claude Desktop's own gateway client: its
// Electron session's User-Agent ("Mozilla/5.0 … Claude/2.7032.0 Chrome/…"),
// which says nothing of it before the first slash.
func isClaudeDesktop(r *http.Request) bool {
	ua := r.Header.Get("User-Agent")
	return strings.HasPrefix(ua, "Mozilla/") && strings.Contains(ua, " Claude/")
}

// Claude Desktop sends some requests on a model of its own choosing rather
// than the one its session is on. A session's title (and branch name) is
// asked for by one tool-less request whose model is its "small_fast" pick
// from the gateway's list — the first id with haiku in it, else sonnet,
// else opus (_$n in its app.asar, 2.7032), the session's model only when
// none has one — so {"model":"claude-sonnet-5-thinking","max_tokens":200,
// "system":"You write short session titles. …"} went to a Claude model the
// user never picked. Claude Code in its Code tab asks for its own
// claude-haiku-… by name for small tasks too.
//
// A session's turns carry tools; the model the latest one is for is the one
// the user picked. A small tool-less request (a title's max_tokens is 200,
// a turn's tens of thousands) for a model whose id reads as Claude's, and
// any request for a model magpie doesn't serve, goes to that model instead,
// so a chat the user started on a Claude model of their own stays on it,
// unless that model is one the user picked for a tier (desktopTierAsked):
// then it goes as asked, for the pick is what the tier was picked to run
// (#3264). It is kept on disk, so a title asked for before the first turn
// after a restart goes there too; before any turn at all it goes to
// desktopDefault.
var desktopPicked struct {
	sync.Mutex
	model string
	from  string    // the file it was read from
	at    time.Time // when that turn was sent (the file's time once read)
}

func desktopPickedPath() string { return filepath.Join(settings.Dir(), "claude-desktop.model") }

// loadPicked reads desktopPicked from its file when it was read from
// another (a new settings.Dir) or not yet. The caller holds its lock.
func loadPicked() {
	path := desktopPickedPath()
	if desktopPicked.from == path {
		return
	}
	b, _ := os.ReadFile(path)
	desktopPicked.model, desktopPicked.from, desktopPicked.at = strings.TrimSpace(string(b)), path, time.Time{}
	if fi, err := os.Stat(path); err == nil {
		desktopPicked.at = fi.ModTime()
	}
}

// desktopTurn is the model a Claude Desktop request for asked is served by.
// byClaudeCode is a request Claude Code sent (its claude-cli/ User-Agent)
// rather than Desktop's own client. bySubagent is a subagent's request
// (x-claude-code-agent-id): its turns carry tools too, but its model is
// the one its tier or definition asked for, not the user's pick, so it is
// never kept as the session's model. Before, a subagent on the opus tier's
// model took the session's place until the lead's next turn, and the
// titles asked for meanwhile went to it.
//
// Claude Code names the model of each of its own background requests on
// purpose: its auto mode's safety classifier asks for its sonnet with no
// tools and a max_tokens of 64 or 256, its notification classifier with
// 1024 (#1539, tete1030). So a tool-less request from it goes to the
// session's model only when it is Desktop's title, which Desktop asks
// Claude Code for when its own request fails (`claude -p --model <its
// pick> --system-prompt "You write short session titles…" --tools ""`,
// sent with max_tokens 64000); one for a model magpie doesn't serve still
// goes there whoever sent it.
func desktopTurn(asked string, body []byte, byClaudeCode, bySubagent bool) string {
	m, _ := desktopMove(asked, body, byClaudeCode, bySubagent)
	return m
}

// The reasons desktopMove gives for serving a Desktop request on another
// model than it named, kept on its route (Route.Moved) so the Routing
// page says why (Neo on Discord: a row asking for chatflowsai's opus,
// answered by AgentRouter's GPT, read as magpie swapping the model).
const (
	movedTier       = "desktop-tier"        // Claude Code's own id of a tier: the model picked for it
	movedCheck      = "desktop-check"       // Desktop's check that the gateway answers: its menu's model
	movedCheckFirst = "desktop-check-first" // …or, with none chosen, the first model it lists
	movedTitle      = "desktop-title"       // a short call without tools Desktop names a model for: the chat's
	movedTitleFirst = "desktop-title-first" // …or, before any turn, the first model it lists
	movedGone       = "desktop-gone"        // a model magpie doesn't serve: the chat's
	movedGoneFirst  = "desktop-gone-first"  // …or, before any turn, the first model it lists
	movedBeside     = "desktop-beside"      // Claude Code's own id of a Claude model: that tier on the chat's provider
	movedAside      = "desktop-aside"       // Claude Code's own call: the chat's model
)

// desktopMove is desktopTurn and why the model it gives isn't asked, ""
// when it is.
func desktopMove(asked string, body []byte, byClaudeCode, bySubagent bool) (string, string) {
	to, why := desktopMoveTo(asked, body, byClaudeCode, bySubagent)
	if to == asked {
		return to, ""
	}
	return to, why
}

func desktopMoveTo(asked string, body []byte, byClaudeCode, bySubagent bool) (string, string) {
	if asked == "" {
		return asked, ""
	}
	if m := desktopTierTurn(asked); m != "" {
		return m, movedTier
	}
	if desktopProbe(body) {
		// Desktop's check that the gateway answers, as it starts and from
		// Test connection: on the model the user chose in Desktop's own
		// menu (desktopChosen), else its default model; never the model
		// of whichever session last ran (desktopPicked) on its own,
		// which may be days old and on another provider
		if m := desktopChosen(); m != "" {
			return m, movedCheck
		}
		if m := desktopDefault(asked); m != "" {
			return m, movedCheckFirst
		}
		return asked, ""
	}
	tools := hasTools(body)
	desktopPicked.Lock()
	defer desktopPicked.Unlock()
	loadPicked()
	picked := desktopPicked.model
	if asked == picked {
		if tools && !bySubagent {
			// the session goes on on it: newer than a menu changed meanwhile
			desktopPicked.at = time.Now()
		}
		return asked, ""
	}
	aside := small(body)
	if byClaudeCode {
		aside = desktopTitle(body)
	}
	title := !tools && aside && desktopAccepts(asked) && !desktopTierAsked(asked)
	gone := unserved(asked)
	// Claude Code's own id of a Claude model (claude-haiku-5-5), asked for
	// a background call or a subagent, that some provider serves under
	// that model while the chat is on another: it stays with the chat's
	// provider, as an unserved one does. The user picked one model in
	// Desktop and expects one provider billed, not a Leee haiku beside an
	// AgentRouter chat (cybercaius on Discord). A tier the user picked a
	// model for is tagged in /v1/models, so Claude Code asks for that
	// model by its own id, which is a catalog id and stays as asked.
	elsewhere := !gone && !title && picked != "" && (!tools || bySubagent) && claudeOwnID(asked)
	if elsewhere {
		// a tier the user picked takes Claude Code's own id of it, though
		// a provider lists a model by that name (desktopTierTurn takes
		// one no provider serves)
		if m := DesktopTiers()[claudeTier(asked)]; m != "" {
			return m, movedTier
		}
	}
	if (gone || elsewhere) && !title {
		if m := servedBeside(asked, picked, byClaudeCode); m != "" {
			return m, movedBeside
		}
	}
	if elsewhere {
		return picked, movedAside
	}
	if gone || title {
		why, first := movedTitle, movedTitleFirst
		if gone {
			why, first = movedGone, movedGoneFirst
		}
		if picked != "" {
			return picked, why
		}
		if m := desktopDefault(asked); m != "" {
			return m, first
		}
		return asked, ""
	}
	if tools && !bySubagent {
		desktopPicked.model, desktopPicked.at = asked, time.Now()
		if os.MkdirAll(settings.Dir(), 0o755) == nil {
			os.WriteFile(desktopPickedPath(), []byte(asked+"\n"), 0o600)
		}
	}
	return asked, ""
}

// claudeOwnID: asked is a Claude model by Claude's own id
// (claude-haiku-5-5, claude-sonnet-4-6[1m]), which no entry has as its
// catalog id; one may still have it as its model.
func claudeOwnID(asked string) bool {
	bare := strings.TrimSuffix(asked, "[1m]")
	if strings.Contains(bare, "/") || claudeTier(bare) == "" {
		return false
	}
	for _, e := range provider.Catalog() {
		if e.ID == bare {
			return false
		}
	}
	return true
}

// servedBeside is the catalog id the session's provider serves a Claude
// model by that Claude Code asked for by Claude's own id. The provider
// may list it as that model (claude-sonnet-5), or, where no entry has it
// as its id or model: another magpie (a Remote magpie provider) lists
// it under its own provider's prefix, office/claude/claude-sonnet-5 with
// the model claude/claude-sonnet-5. Its auto mode's classifier asks for
// its sonnet by name while the session is on office/claude/claude-opus-5-5,
// and went to that opus, writing an hour's cache for each tiny reply
// (#2187, SuInk). Only the provider picked is looked at, so another
// provider's model of the same name doesn't take it; of that provider's,
// the one beside the picked model (the same subscription there) comes
// first. With no such model, the provider's newest Claude model of the
// same tier stands in (claude-haiku-5-5 for Claude Code's
// claude-haiku-4-5-20251001), so its small calls stay small and on the
// chat's provider. "" when the session's provider serves no model of the
// tier.
//
// A call Claude Code makes on its own (byClaudeCode: a classifier, a
// small task, a subagent on a tier) is given one of the provider's Claude
// models only when the session is on one of them too. Only then has the
// user picked that provider's Claude models: a chat on Leee's DeepSeek
// had Claude Code's sonnet calls sent to Leee's claude-sonnet-5-5, which
// Leee's key may not use (403, CyberCaius on Discord). Such a session's
// calls go to its own model ("" here). Desktop's own client sends no such
// call: a tool-less request of its own that isn't a title is the user's
// chat turn.
func servedBeside(asked, picked string, byClaudeCode bool) string {
	bare := strings.TrimSuffix(asked, "[1m]")
	tier := claudeTier(bare)
	if picked == "" || strings.Contains(bare, "/") || tier == "" {
		return ""
	}
	cat := provider.Catalog()
	var pid, dir string
	chatClaude := false
	picked = strings.TrimSuffix(picked, "[1m]")
	for _, e := range cat {
		if e.ID == picked && e.Group == "" {
			pid, chatClaude = e.Provider.ID, claudeModel(e) != ""
			if i := strings.LastIndex(e.Model, "/"); i >= 0 {
				dir = e.Model[:i+1]
			}
			break
		}
	}
	// a session on a model asked for by its bare name (one an entry has
	// as its model, not its id)
	for _, e := range cat {
		if pid != "" {
			break
		}
		if e.Model == picked && e.Group == "" {
			pid, chatClaude = e.Provider.ID, claudeModel(e) != ""
			if i := strings.LastIndex(e.Model, "/"); i >= 0 {
				dir = e.Model[:i+1]
			}
			break
		}
	}
	if pid == "" || byClaudeCode && !chatClaude {
		return ""
	}
	found := ""
	var tierID, tierModel string
	tierBeside := false
	for _, e := range cat {
		if e.Group != "" || e.Provider.ID != pid {
			continue
		}
		if e.Model == bare || strings.HasSuffix(e.Model, "/"+bare) {
			if e.Model == bare || dir != "" && e.Model == dir+bare {
				return e.ID + strings.TrimPrefix(asked, bare)
			}
			if found == "" {
				found = e.ID + strings.TrimPrefix(asked, bare)
			}
			continue
		}
		// the tier's stand-in: beside the picked model first, then the
		// newest version
		m := claudeModel(e)
		if m == "" || claudeTier(m) != tier {
			continue
		}
		beside := dir != "" && strings.HasPrefix(e.Model, dir)
		if tierID == "" || beside && !tierBeside || beside == tierBeside && m > tierModel {
			tierID, tierModel, tierBeside = e.ID, m, beside
		}
	}
	if found != "" {
		return found
	}
	if tierID != "" {
		return tierID + strings.TrimPrefix(asked, bare)
	}
	return ""
}

// desktopDefault is the model a request Desktop sends before any turn has
// named one goes to: a new session's first message is titled before it is
// sent (ARNO), so nothing is picked yet when the title is asked for. It is
// the model the agent is set to stand in for asked if there is one, else
// the model a new Desktop session starts on — the first row of /v1/models
// (resolveDefaultSessionModel in its app.asar takes the first model it
// doesn't restrict), which is magpie's first model shown to it. "" when
// that is asked itself or magpie shows Desktop no model.
func desktopDefault(asked string) string {
	if StandIn != nil {
		if m := StandIn("claude-desktop", asked); m != "" && m != asked {
			return m
		}
	}
	shown, _ := provider.CatalogFor("claude-desktop")
	if len(shown) == 0 || shown[0].ID == asked {
		return ""
	}
	return shown[0].ID
}

// desktopSelectorRoots are the Claude-3p folders Desktop keeps its model
// menus' choices in (desktopChosen): every set desktopdir finds. A test
// sets its own.
var desktopSelectorRoots = func() []string {
	home, _ := os.UserHomeDir()
	var out []string
	for _, d := range desktopdir.Sets(desktopdir.OS, home, appdir.Getenv) {
		if d.ThreeP != "" {
			out = append(out, d.ThreeP)
		}
	}
	return out
}

// desktopSurfaces are Desktop's model menus (bC in its app.asar 2.7032),
// in the order desktopChosen takes them: its Code tab first, the one
// magpie's users run, then Cowork, which can be turned off, then Chat.
var desktopSurfaces = []string{"code", "cowork", "chat"}

// desktopChosen is the model the user chose in one of Claude Desktop's
// model menus that magpie still serves, by magpie's id for it, "" for
// none. Desktop keeps each menu's choice in its userData (Claude-3p in
// its 3p mode), in
// local-agent-mode-sessions/<account>/<org>/cowork_account_settings.json:
//
//	{"__model_selector_state":{"code":{"model":<id>},"cowork":{…},"chat":{…}}}
//
// (wLt and zLt in its app.asar 2.7032, written as a menu's choice
// changes; the newest such file is read). Desktop's check names a model
// by its name (desktopProbe), so a chat on DeepSeek in the Code tab had
// its check go to the first model listed, another provider's
// (cybercaius on Discord, v0.1.2033). The newer of the two the user did
// last wins: a turn sent after the file was last written goes first
// (desktopPicked, the model that turn was for, while magpie serves it),
// else the menus' choices in desktopSurfaces' order. Before, the last
// session's model came first whenever any menu still held it, so a Code
// tab switched off DeepSeek, its Cowork or Chat menu left on it, had the
// check go on to DeepSeek while the chat went to the new model
// (cybercaius on Discord, v0.1.2212). Read only for the check, as
// Desktop starts and from Test connection; a file not read or not parsed
// is no choice (desktopDefault), and a turn alone is not one either,
// since it can be days old.
func desktopChosen() string {
	var path string
	var newest time.Time
	for _, root := range desktopSelectorRoots() {
		found, _ := filepath.Glob(filepath.Join(root, "local-agent-mode-sessions", "*", "*", "cowork_account_settings.json"))
		for _, f := range found {
			if fi, err := os.Stat(f); err == nil && fi.Mode().IsRegular() && (path == "" || fi.ModTime().After(newest)) {
				path, newest = f, fi.ModTime()
			}
		}
	}
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var v struct {
		State map[string]struct {
			Model string `json:"model"`
		} `json:"__model_selector_state"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	var chosen []string
	for _, s := range desktopSurfaces {
		// Desktop's id for it (claudeLooking), as magpie serves it
		m := unprefixed(strings.TrimSpace(v.State[s].Model))
		if m != "" && !unserved(m) && !slices.Contains(chosen, m) {
			chosen = append(chosen, m)
		}
	}
	if len(chosen) == 0 {
		return ""
	}
	desktopPicked.Lock()
	loadPicked()
	picked, at := desktopPicked.model, desktopPicked.at
	desktopPicked.Unlock()
	// a turn sent after the menus last changed is the model the user is
	// on now: a session switched to another model, or a menu still on the
	// one it left
	if picked != "" && at.After(newest) {
		if m := unprefixed(picked); !unserved(m) {
			return m
		}
	}
	return chosen[0]
}

// desktopProbe: the request is Claude Desktop's check that a gateway
// answers, which it runs as it starts and from its gateway settings' Test
// connection (lWt in its app.asar, 2.7032):
//
//	{"model":<id>,"max_tokens":1,"messages":[{"role":"user","content":"."}]}
//
// on the shortest id of /v1/models with haiku in it, else sonnet, else
// opus (YT): a model Desktop picks by its name, not one the user chose.
// It is asked of the model chosen in Desktop's menus (desktopChosen),
// else of Desktop's default model (desktopDefault, the
// first listed: "the default model (the first listed model)", as its
// settings say). Sent as a title is, to the model the last session ran
// on, it went to a relay's GPT model the user had left days before and
// was turned away there, shown as the haiku rejected (CyberCaius on
// Discord, v0.1.1838).
func desktopProbe(body []byte) bool {
	var v struct {
		MaxTokens int               `json:"max_tokens"`
		System    json.RawMessage   `json:"system"`
		Tools     json.RawMessage   `json:"tools"`
		Messages  []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || v.MaxTokens != 1 || len(v.System) > 0 || len(v.Tools) > 0 || len(v.Messages) != 1 {
		return false
	}
	var m struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	return json.Unmarshal(v.Messages[0], &m) == nil && m.Role == "user" && m.Content == "."
}

// desktopProbeTokens is the reply Desktop's check is asked for upstream:
// OpenAI's models refuse fewer than 16 output tokens, and a relay in front
// of one (AnyRouter: "bad response status code 400") passes on no floor
// to ask again at (tokenFloor). Desktop reads only whether it is answered.
const desktopProbeTokens = 16

// KindDesktopCheck is the kind a Claude Desktop check (desktopProbe) is
// recorded with, so the Routing page and the ledger tell it from a turn.
const KindDesktopCheck = "connection_check"

// desktopTitlePrompt is how the system prompt Claude Desktop asks a
// session's title and branch name with starts (dcr in its app.asar), sent
// by its own client or, when that fails, as Claude Code's --system-prompt.
const desktopTitlePrompt = "You write short session titles."

// desktopTitle: the request's system prompt is Desktop's title prompt, as a
// string (Desktop's client) or one of its text blocks (Claude Code's).
func desktopTitle(body []byte) bool {
	if !bytes.Contains(body, []byte(desktopTitlePrompt)) {
		return false
	}
	var v struct {
		System json.RawMessage `json:"system"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	var text string
	if json.Unmarshal(v.System, &text) == nil {
		return strings.HasPrefix(text, desktopTitlePrompt)
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(v.System, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if strings.HasPrefix(b.Text, desktopTitlePrompt) {
			return true
		}
	}
	return false
}

// small: the request asks for a short answer (max_tokens at most 4096), as
// Desktop's title does, not a session's turn.
func small(body []byte) bool {
	var v struct {
		MaxTokens int `json:"max_tokens"`
	}
	return json.Unmarshal(body, &v) == nil && v.MaxTokens > 0 && v.MaxTokens <= 4096
}

// hasTools: the request offers the model tools (Anthropic's, Chat's and
// Responses' "tools" alike).
func hasTools(body []byte) bool {
	if !bytes.Contains(body, []byte(`"tools"`)) {
		return false
	}
	var v struct {
		Tools []json.RawMessage `json:"tools"`
	}
	return json.Unmarshal(body, &v) == nil && len(v.Tools) > 0
}

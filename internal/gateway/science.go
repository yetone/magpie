package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Claude Science (Anthropic's research app, 0.1.62, "operon" inside) is a
// daemon that serves its UI on localhost and asks Anthropic's Messages API
// at the base URL its environment's ANTHROPIC_BASE_URL names (ky() in its
// bundle: https anywhere, http only on a loopback address), else at
// api.anthropic.com. Magpie starts the daemon with that variable set to
// the gateway (package agent, claudescience.go). It still needs its own
// Claude sign-in: with none it asks nothing ("This build requires signing
// in with your Claude account. API keys are not supported.", yz()), and
// with one it sends Authorization: Bearer sk-ant-oat…, which magpie never
// passes on (claudeSignIn), beside its client's default headers
// X-Anthropic-Surface: operon and anthropic-beta: oauth-2025-04-20. That
// header is how its requests are known here: its User-Agent is the
// Anthropic SDK's, which says nothing of the app.
//
// Its model picker (ops.system.models) lists from GET /v1/models only an
// id that starts with "claude-" whose display_name isn't a lower-case
// slug (H7 and kor in its bundle), so every model is listed to it by an
// alias, claude-magpie-<number> (aliasNumber, as Claude Desktop's), named
// as the agents' lists name it. And it asks for Claude models by their own
// ids where no pick of the user's is in play — its default model before
// one is picked (claude-opus-5), claude-haiku-… for its small tasks — so a
// model magpie doesn't serve goes to the one its last turn was on
// (scienceTurn).

// ScienceID is Claude Science's agent id.
const ScienceID = "claude-science"

// scienceSurface is the X-Anthropic-Surface Claude Science's client sends
// on every request (YYe in its bundle).
const scienceSurface = "operon"

// scienceAlias is the prefix of the id a model is listed to Claude Science
// by: claude-magpie-<number>, the number a hash of magpie's id.
const scienceAlias = "claude-magpie-"

// isClaudeScience is a request from Claude Science's own client.
func isClaudeScience(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Anthropic-Surface")), scienceSurface)
}

// scienceSlug is a name Claude Science takes for an internal model's id
// and leaves out of its picker (kor in its bundle).
var scienceSlug = lazyre.New(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$`)

// scienceName is a model's name as Claude Science is shown it: a slug's
// first letter in upper case, which its picker keeps.
func scienceName(name string) string {
	if !scienceSlug.MatchString(name) {
		return name
	}
	r, n := utf8.DecodeRuneInString(name)
	return string(unicode.ToUpper(r)) + name[n:]
}

// scienceObject is e as Claude Science is shown it: by its alias and a
// name its picker keeps, with the reply limit as max_tokens, which is
// what it reads one by (c5t in its bundle).
func scienceObject(e provider.Entry, name string) map[string]any {
	m := modelObject(e)
	m["id"], m["display_name"] = scienceAlias+aliasNumber(e.ID), scienceName(name)
	if out := e.PublishedOutput(); out > 0 {
		m["max_tokens"] = out
	}
	return m
}

// scienceModels is /v1/models as Claude Science is shown it: every model
// picked for it (provider.CatalogFor) by its alias, named as Claude
// Desktop's are (desktopNames), no two alike.
func scienceModels(entries []provider.Entry) []map[string]any {
	names := desktopNames(ScienceID, entries)
	data := make([]map[string]any, 0, len(entries))
	for i, e := range entries {
		data = append(data, scienceObject(e, names[i]))
	}
	return data
}

// sciencePicked is the model Claude Science's latest turn was on, kept on
// disk so a request after a restart goes there too.
var sciencePicked struct {
	sync.Mutex
	model string
	from  string // the file it was read from
}

func sciencePickedPath() string { return filepath.Join(settings.Dir(), "claude-science.model") }

// scienceTurn is the model a Claude Science request for asked is served
// by: asked when magpie serves it, a turn's (it carries tools) noted as
// the one picked; else the model picked last, else the first one listed
// to it.
func scienceTurn(asked string, body []byte) string {
	sciencePicked.Lock()
	defer sciencePicked.Unlock()
	if path := sciencePickedPath(); sciencePicked.from != path {
		b, _ := os.ReadFile(path)
		sciencePicked.model, sciencePicked.from = strings.TrimSpace(string(b)), path
	}
	picked := sciencePicked.model
	if unserved(asked) {
		if picked != "" && !unserved(picked) {
			return picked
		}
		if shown, _ := provider.CatalogFor(ScienceID); len(shown) > 0 {
			return shown[0].ID
		}
		return asked
	}
	if asked != picked && hasTools(body) {
		sciencePicked.model = asked
		if os.MkdirAll(settings.Dir(), 0o755) == nil {
			os.WriteFile(sciencePickedPath(), []byte(asked+"\n"), 0o600)
		}
	}
	return asked
}

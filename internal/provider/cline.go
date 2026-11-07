package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// ClineVersion is the Cline desktop release magpie says it is to the Cline
// API.
const ClineVersion = "3.5.54"

// clineHeaders are what Cline's desktop app sends the Cline API (cline/cline
// sdk/packages/llms/src/providers/cline-client-headers.ts, with the
// desktop's client type). The cline-free/ models are answered 403 "only
// available via Cline product surfaces" without them, and X-CLIENT-TYPE is
// what the recommended-models feed keys its free list on: cline-desktop is
// given the desktop's.
var clineHeaders = map[string]string{
	"HTTP-Referer":     "https://cline.bot",
	"X-Title":          "Cline",
	"X-IS-MULTIROOT":   "false",
	"X-CLIENT-TYPE":    "cline-desktop",
	"X-CLIENT-VERSION": ClineVersion,
	"User-Agent":       "Cline/" + ClineVersion,
}

// ClineClient makes a request to the Cline API carry the desktop app's
// headers.
func ClineClient(h http.Header) {
	for k, v := range clineHeaders {
		h.Set(k, v)
	}
}

// IsCline reports whether the provider is the Cline API: made from the
// ClinePass preset, or at Cline's host.
func (p Provider) IsCline() bool {
	if p.Preset == "clinepass" {
		return true
	}
	h := p.Host()
	return h == "cline.bot" || strings.HasSuffix(h, ".cline.bot")
}

// clineFreePrefix is what the ids of Cline's free models start with; one
// the feed's free list names may not (stealth/space-bunny-alpha).
const clineFreePrefix = "cline-free/"

// clineFeed is the list Cline's own clients take their pickers from:
// GET {base}/ai/cline/recommended-models, asked with the client's headers
// and no key. Its clinePass models are the plan's, its free ones are served
// at no cost and apart from the plan's quota ("Try with limited usage,
// separate from ClinePass quota"). Paid models are left out, as the
// plan's preset leaves them out of the API's list.
func (p Provider) clineFeed(ctx context.Context) ([]catalog.Model, string, error) {
	u := strings.TrimRight(strings.TrimSpace(p.Chat), "/") + "/ai/cline/recommended-models"
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, u, err
	}
	req.Header.Set("Accept", "application/json")
	ClineClient(req.Header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, u, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return nil, u, errorf("%s: HTTP %d", u, res.StatusCode)
	}
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var feed struct {
		ClinePass []entry `json:"clinePass"`
		Free      []entry `json:"free"`
	}
	if err := json.Unmarshal(b, &feed); err != nil {
		return nil, u, err
	}
	var ms []catalog.Model
	add := func(e entry, free bool) {
		if e.ID == "" {
			return
		}
		for _, m := range ms {
			if m.ID == e.ID {
				return
			}
		}
		name := e.Name
		if name == "" || free && name == e.ID {
			name = clineFreeName(e.ID)
		}
		ms = append(ms, catalog.Model{ID: e.ID, Name: name, Free: free})
	}
	for _, e := range feed.ClinePass {
		add(e, false)
	}
	for _, e := range feed.Free {
		add(e, true)
	}
	if len(ms) == 0 {
		return nil, u, errors.New(u + ": no models listed")
	}
	return p.planModels(ms), u, nil
}

// clineFree are Cline's free models as its desktop app listed them
// (2026-10-02; cline/cline sdk/packages/llms/src/catalog/
// cline-recommended.generated.ts), for when the feed wasn't had.
var clineFree = []catalog.Model{
	{ID: "cline-free/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash"},
	{ID: "stealth/space-bunny-alpha", Name: "Space Bunny Alpha"},
	{ID: "cline-free/mimo-v2.6-flash", Name: "MiMo-V2.6-Flash"},
	{ID: "cline-free/muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor"},
}

// clineFreeName is the fallback list's name for a free model, else its id.
func clineFreeName(id string) string {
	for _, m := range clineFree {
		if m.ID == id {
			return m.Name
		}
	}
	return id
}

// isClineFree reports whether id is a free model by its id alone: a
// cline-free/ one, or one the fallback names.
func isClineFree(id string) bool {
	if strings.HasPrefix(id, clineFreePrefix) {
		return true
	}
	for _, m := range clineFree {
		if m.ID == id {
			return true
		}
	}
	return false
}

// clineFreeModel reports whether model is one of Cline's free models on
// p: one the fetched list marks free, else one free by its id.
func (p Provider) clineFreeModel(model string) bool {
	if !p.IsCline() {
		return false
	}
	if strings.HasPrefix(model, clineFreePrefix) {
		return true
	}
	if ms := p.liveByID(model); len(ms) > 0 {
		return ms[0].Free
	}
	return isClineFree(model)
}

// ClinePinnable reports whether p's requests reach Cline's API and can be
// pinned to an upstream (PinUpstream): the ClinePass provider, one on
// cline.bot, or the Cline plugin's (@magpie-community/opencode-cline-auth),
// whose requests magpie writes before the plugin sends them on.
func (p Provider) ClinePinnable() bool {
	return p.IsCline() || p.PluginProvider() == "cline"
}

// clineMakers maps a model name to its maker's own upstream on the
// router Cline sends it through. Most go through Vercel's AI Gateway,
// pinned by providerOptions.gateway.only with Vercel's provider slug
// (ai-gateway.vercel.sh/v1/models/<model>/endpoints lists "deepseek" for
// deepseek/deepseek-v4-pro and "zai" for zai/glm-5.3). Some go through
// OpenRouter directly, which reads only a top-level provider.only with
// OpenRouter's slug (openrouter.ai/api/v1/providers: Z.AI is "z-ai"):
// sdytman (#feedback, 10-20) found glm-5.3-flash among them, and
// deepseek-v4.1-flash and deepseek-v4.1-flash-1017 going through Vercel.
// An exact entry is a model name; the others match a name's start, the
// first match winning. Only the OpenRouter entries are pinned now: Cline
// refuses providerOptions since 2026-11-06 (ClinePin).
var clineMakers = []struct {
	name       string
	exact      bool
	upstream   string
	openRouter bool
}{
	{name: "glm-5.3-flash", exact: true, upstream: "z-ai", openRouter: true},
	{name: "deepseek", upstream: "deepseek"},
	{name: "glm", upstream: "zai"}, // sdytman on Discord: GLM only through Z.ai
}

// ClinePin is the upstream a request for model is pinned to on p
// (PinUpstream), and whether Cline sends the model through OpenRouter:
// "z-ai" through OpenRouter for glm-5.3-flash, "" for anything else.
// Since 2026-11-06 the Cline API answers a request carrying
// providerOptions with 403 "client-supplied \"providerOptions\" model
// routing is not supported" (#5442: the same request, the same key, 403
// with the field and 200 without), so a model Cline sends through
// Vercel's AI Gateway (DeepSeek's, GLM's but glm-5.3-flash), which only
// providerOptions.gateway.only could pin, goes unpinned whatever the
// tick says: the tick would fail every such request. OpenRouter's
// top-level provider.only is still taken, so glm-5.3-flash keeps it.
// Cline's free models are served where Cline puts them, so they never
// are.
func (p Provider) ClinePin(model string) (upstream string, openRouter bool) {
	if !p.PinUpstream || !p.ClinePinnable() || strings.HasPrefix(model, clineFreePrefix) {
		return "", false
	}
	name := strings.ToLower(model[strings.LastIndex(model, "/")+1:])
	for _, m := range clineMakers {
		if m.exact && name == m.name || !m.exact && strings.HasPrefix(name, m.name) {
			if !m.openRouter {
				return "", false
			}
			return m.upstream, true
		}
	}
	return "", false
}

// ClinePassAsked is the upstream a request for model asked the Cline
// API to serve it from on p, "" when it asked for none: what magpie
// wrote into it (ClinePin). The Routing page says it beside a reply that
// names no provider, so a route ClinePass picked reads as asked for an
// upstream or not (zRain on Discord, 10-24). The Cline plugin's own
// pinUpstream option is not counted: 0.1.13 pins nothing without it,
// and a pin it writes (0.1.12 by default, or the option on) is
// providerOptions, which Cline refuses with a 403, so no reply is ever
// served on it.
func (p Provider) ClinePassAsked(model string) string {
	up, _ := p.ClinePin(model)
	return up
}

package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// The gateway draws: /v1/images/generations and /v1/images/edits take
// OpenAI's images request and answer in its shape, whichever vendor draws.
// A model on an images API (gpt-image, dall-e, imagen, flux, seedream,
// qwen-image…) is asked there. OpenRouter uses its /images endpoint for
// those models; its chat image models (gemini-*-image, gpt-5-image) stay on
// chat completions with modalities. Gemini at Google is asked on its own
// generateContent. A request that names no model draws with the Settings'
// image generation model, or one magpie picks (AutoDrawer).

// DrawAgent is the User-Agent magpie's generate_image tool adds to its own.
const DrawAgent = "magpie-image/1"

// drawTimeout is how long a vendor is given to draw: to answer, and to send
// all of its answer. It is Settings' Image generation timeout (neighbads on
// X: a slow image endpoint needs more than the 5 minutes it was fixed at),
// read as each request comes, in drawUnits.
func drawTimeout() time.Duration {
	return settings.Load().ImageWait() / time.Minute * drawUnit
}

// drawUnit is a minute of drawTimeout: a test makes it shorter, rather than
// wait the minutes out.
var drawUnit = time.Minute

// drawWaitKey marks a request's context as a drawing's (or a video's),
// given the time it holds: the vendor is waited on for that long, past the
// 10 minutes upstreamTransport gives any other request to begin answering
// (an images API answers only once it has drawn), and what ran out of time
// says how long it was given.
type drawWaitKey struct{}

// drawWithin is ctx with d to draw in: its deadline, and the mark.
func drawWithin(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithValue(ctx, drawWaitKey{}, d), d)
}

// drawWaitOf is the time ctx was given to draw in, if it was.
func drawWaitOf(ctx context.Context) (time.Duration, bool) {
	d, ok := ctx.Value(drawWaitKey{}).(time.Duration)
	return d, ok
}

// givenTo says how long the drawing in ctx was given, or, for a request
// that isn't one, how long it ran since start.
func givenTo(ctx context.Context, start time.Time) time.Duration {
	if d, ok := drawWaitOf(ctx); ok {
		return d
	}
	return time.Since(start).Round(time.Second)
}

// maxDrawings is how many images one request may ask for.
const maxDrawings = 4

// drawer is the model a request that names none draws with: the Settings'
// ImageGen while it resolves, else AutoDrawer's. None when it is off or no
// provider draws.
func drawer() (string, bool) {
	switch v := settings.Load().ImageGen; v {
	case "off":
		return "", false
	case "":
	default:
		if drawerMissing(v) == "" {
			return v, true
		}
	}
	m := AutoDrawer()
	return m, m != ""
}

// Drawer is the model a request that names none draws with (drawer), as
// provider/model: "" when image generation is off or no provider draws.
func Drawer() string {
	m, _ := drawer()
	return m
}

// DrawerMissing is the model Settings › Models › Image generation names
// when magpie can't find it any more, so that AutoDrawer's draws in its
// place, as VisionMissing is Image recognition's. "" when none is named,
// it is off, or it resolves.
func DrawerMissing() string { return drawerMissing(settings.Load().ImageGen) }

// drawerMissing is v, an Image generation pick, when magpie can't draw
// with it: pickedMissing's, and a routing group ("group/<id>", 7up6666 on
// X) none of whose members draws any more.
func drawerMissing(v string) string {
	if !provider.IsGroupRef(v) {
		return pickedMissing(v)
	}
	// a group of image models only is no model agents are offered, so
	// Resolve doesn't take it: it is missing when none of it draws
	if !GroupDraws(v) {
		return v
	}
	return ""
}

// GroupDraws is whether the routing group ref ("group/<id>") has a member
// that draws: a model among its provider's Drawers. Such a group may be
// picked in Settings › Images, and draws on those members in its routing's
// order, moving on while one fails (drawingGroup, #2005).
func GroupDraws(ref string) bool {
	_, ms, ok := drawingMembers(ref)
	if !ok {
		return false
	}
	for _, m := range ms {
		if drawsModel(m.Provider, m.Model) {
			return true
		}
	}
	return false
}

// drawingMembers is the routing group ref with its members ready now to
// draw on: FindGroup's, and also an image model of a provider whose picks
// leave it out among them, as the group's editor offers one beside the
// chat models (jzw690 on X).
func drawingMembers(ref string) (provider.Group, []provider.Member, bool) {
	return provider.FindDrawingGroup(ref, drawsModel)
}

// drawsModel is whether model is one of p's Drawers, a ChatGPT account's
// other GPT Image names as its one (ListedDrawer).
func drawsModel(p provider.Provider, model string) bool {
	_, id, _ := strings.Cut(ListedDrawer(p.ID+"/"+model), "/")
	return slices.ContainsFunc(Drawers(p), func(m catalog.Model) bool { return m.ID == id })
}

func resolveDrawing(id string) (provider.Provider, string, bool) {
	if provider.IsGroupRef(id) {
		// its first member that draws: a group of image models only
		// isn't among the models agents are offered, which Resolve reads
		if _, ms, ok := drawingMembers(id); ok {
			for _, m := range ms {
				if drawsModel(m.Provider, m.Model) {
					return m.Provider, m.Model, true
				}
			}
		}
		return provider.Resolve(id)
	}
	if strings.Contains(id, "/") {
		return provider.Resolve(id)
	}
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() {
			continue
		}
		for _, m := range Drawers(p) {
			if m.ID == id {
				return p, m.ID, true
			}
		}
	}
	// a subscription's newer image model, named before magpie lists it
	// (grok-imagine-image-2.0, #1857): its vendor draws it, or says why not
	for _, p := range provider.All() {
		if p.On() && !p.DecideOnly() && drawsFamily(p, id) {
			return p, drawnAs(p, id), true
		}
	}
	return provider.Resolve(id)
}

// drawnAs is the id p is asked to draw id, a bare name of the family
// (drawsFamily), by: the one p's own list names it by under a namespace
// (openai/gpt-image-2 for gpt-image-2, loscoy's relay, #4432), as p
// serves it, else id as asked.
func drawnAs(p provider.Provider, id string) string {
	for _, m := range Drawers(p) {
		if m.ID != id && strings.HasSuffix(m.ID, "/"+id) {
			return m.ID
		}
	}
	return id
}

// drawsFamily is whether a subscription whose image models magpie lists
// itself (codexDrawers, grokDrawers) draws id, a model of the family those
// are of: any grok-imagine-image* on a Grok subscription, any gpt-image-*
// on a ChatGPT account, and any gpt-image-* on a keyed relay at a Responses
// base (a Codex backend relay such as sub2api) whose own list draws one of
// that family already (#4432), by the name past the namespace its list may
// carry (openai/gpt-image-2, loscoy's). The vendor's newer ones work before
// magpie names them, as grok-imagine-video* do for videos; one it doesn't
// serve is refused by the vendor, in its own words. A relay whose list
// draws no GPT Image isn't taken to: a Codex turn on it draws with the
// Settings pick, as it did (TestCodexDrawingFallsBackToImageSetting).
func drawsFamily(p provider.Provider, id string) bool {
	switch {
	case drawsGrok(p):
		return strings.HasPrefix(id, grokDrawerFamily)
	case drawsCodex(p):
		return strings.HasPrefix(id, codexDrawerFamily)
	case p.Account == nil && p.Base(provider.Responses) != "" && strings.HasPrefix(id, codexDrawerFamily):
		return slices.ContainsFunc(Drawers(p), func(m catalog.Model) bool {
			return strings.HasPrefix(m.ID[strings.LastIndexByte(m.ID, '/')+1:], codexDrawerFamily)
		})
	}
	return false
}

// The families of the image models a ChatGPT account and a Grok
// subscription draw with (drawsFamily).
const (
	codexDrawerFamily = "gpt-image-"
	grokDrawerFamily  = "grok-imagine-image"
)

// codexDrawers is the image model a ChatGPT account draws with, at its
// Codex backend's images API, as Codex CLI does (gpt-image-2 is the one it
// asks for). It is one: the backend picks the model itself, and answers
// gpt-image-2, gpt-image-2.5 and a made-up name alike with the same image
// (#1865), so listing 2.5 apart said there was a choice where there is
// none. A gpt-image-2.5 saved when it was listed still draws there
// (drawsFamily) and shows as this one (ListedDrawer).
var codexDrawers = []catalog.Model{
	{ID: "gpt-image-2", Name: "GPT Image", Released: "2026-01-01"},
}

// VendorPicksDrawer is whether p's vendor picks the image model itself,
// whichever is named: a ChatGPT account's images backend (#1865).
func VendorPicksDrawer(p provider.Provider) bool { return drawsCodex(p) }

// ListedDrawer is the listed image model a picked one (provider/model)
// stands for: a ChatGPT account's gpt-image-2.5, or any other GPT Image
// name, is its one GPT Image, as its backend draws the same for any name
// (#1865). ref itself otherwise, and when its provider isn't found. The
// pick itself is kept as it was saved, and draws as before.
func ListedDrawer(ref string) string {
	pid, id, ok := strings.Cut(ref, "/")
	if !ok || !strings.HasPrefix(id, codexDrawerFamily) || id == codexDrawer {
		return ref
	}
	p, err := provider.Find(pid)
	if err != nil || !drawsCodex(*p) {
		return ref
	}
	return pid + "/" + codexDrawer
}

// listsCantName is whether p's image models are ones magpie names itself
// (a ChatGPT account's GPT Image, a Grok subscription's Grok Imagine, a
// WorkBuddy plan's those its config lists), apart from its chat models:
// an account's own list of models (#474), picked in the editor from the
// chat models, can't name them, so it never bars one (heyiwei5 on X).
func listsCantName(p provider.Provider) bool {
	return drawsCodex(p) || drawsGrok(p) || drawsWorkBuddy(p)
}

// codexDrawer is the image model Codex CLI asks a ChatGPT account for, the
// one Automatic picks there.
const codexDrawer = "gpt-image-2"

// grokDrawers are the image models a Grok subscription (SuperGrok, X Premium+)
// draws with, at the Imagine API of the backend Grok Build's image_gen tool
// calls: cli-chat-proxy.grok.com/v1/images/generations.
var grokDrawers = []catalog.Model{
	{ID: "grok-imagine-image", Name: "Grok Imagine"},
	{ID: "grok-imagine-image-quality", Name: "Grok Imagine Quality"},
}

// drawsGrok is whether p is a Grok subscription, which draws at the Imagine
// API of the backend Grok Build talks to: the built-in's, or the Grok
// plugin's, which signs what it is sent there as the built-in does.
func drawsGrok(p provider.Provider) bool {
	return p.Account != nil && (p.Account.Agent == "grok" || p.PluginProvider() == "grok") && p.Base(provider.Responses) != ""
}

// drawsCopilot is whether p is a GitHub Copilot account, whose image models
// (its /models entries that make images) draw at {api}/images/generations
// and /images/edits, as Copilot CLI's image_generation tool does.
func drawsCopilot(p provider.Provider) bool {
	return p.Account != nil && p.Account.Agent == "copilot"
}

// drawsCodex is whether p is a ChatGPT account, which draws at its Codex
// backend's images API.
func drawsCodex(p provider.Provider) bool {
	return p.Account != nil && p.Account.Agent == "codex" && p.Base(provider.Responses) != ""
}

// Drawers are the models a provider can draw with: its catalogs' models
// that make images, those its own model list names that do, those of its
// own picks named for images, and those the user said draw (#1399), less
// those they said make videos; a ChatGPT account's GPT Image, a Grok
// subscription's Grok Imagine, a WorkBuddy plan's those its config lists.
func Drawers(p provider.Provider) []catalog.Model { return DrawersOf(p, p.Available) }

// DrawersOf is Drawers with p's model list from avail, called only when
// it is wanted: the Providers page has each provider's list at hand, and
// reading it again for each was 15 MB of what twenty window polls
// allocated. What avail gives is only read.
func DrawersOf(p provider.Provider, avail func() []catalog.Model) []catalog.Model {
	if drawsWorkBuddy(p) {
		return wbDrawers(p)
	}
	if drawsCodex(p) || drawsGrok(p) {
		out := slices.Clone(codexDrawers)
		if drawsGrok(p) {
			out = slices.Clone(grokDrawers)
		}
		for i := range out {
			out[i].Provider = p.ID
		}
		return out
	}
	if drawsCopilot(p) {
		// the image models the account's /models lists (Copilot CLI's GPT
		// Image 2.5 Flare), drawn at its /images API
		var out []catalog.Model
		for _, m := range catalog.LiveDrawers(p.ID) {
			m.Provider = p.ID
			out = append(out, m)
		}
		return out
	}
	if googleAccount(p) {
		// the image models a Google sign-in lists (Antigravity's
		// gemini-3.1-flash-image), which draw on Code Assist (#757)
		var out []catalog.Model
		for _, m := range avail() {
			if catalog.DrawsID(m.ID) {
				m.Provider = p.ID
				out = append(out, m)
			}
		}
		return out
	}
	if p.IsPlugin() {
		// a plugin in front of an OpenAI-compatible API: the image models
		// its list names that draw on the images API, sent through its
		// fetch under the base its chat goes to (01huadalang on Discord:
		// the SenseNova plugin's sensenova-u1.5-lite)
		// A model the plugin speaks Gemini's API to (@ai-sdk/google) draws
		// as a Google sign-in's do, on generateContent asking for images,
		// when it says it gives images or is named for them (XYenon's
		// Google AI Studio plugin).
		var out []catalog.Model
		for _, m := range avail() {
			if p.ModelKind(m.ID) == provider.KindVideo {
				continue
			}
			if catalog.DrawsID(m.ID) && catalog.ImagesAPI(m.ID) && p.PluginOpenAICompatible(m.ID) ||
				pluginDrawsGemini(p, m.ID) && (catalog.DrawsID(m.ID) || slices.Contains(m.OutputModalities, "image")) {
				m.Provider = p.ID
				out = append(out, m)
			}
		}
		return out
	}
	// any other subscription is asked through its agent's own API, which
	// draws nothing magpie can ask for yet. A Responses-only relay (a Codex
	// backend such as sub2api) has no chat base but serves the images API
	// under the same root as its Responses endpoint, so it draws all the same.
	if p.Account != nil || (p.Base(provider.Chat) == "" && p.Base(provider.Responses) == "") {
		return nil
	}
	out := p.PlanDrawers()
	have := make(map[string]bool, len(out))
	for _, m := range out {
		have[m.ID] = true
	}
	for _, c := range p.Catalogs() {
		for _, m := range catalog.Drawers(c) {
			if !have[m.ID] {
				have[m.ID] = true
				out = append(out, m)
			}
		}
	}
	for _, m := range catalog.LiveDrawers(p.ID) {
		if !have[m.ID] {
			have[m.ID] = true
			m.Provider = p.ID
			out = append(out, m)
		}
	}
	for _, id := range p.Models {
		if !have[id] && catalog.DrawsID(id) {
			have[id] = true
			out = append(out, catalog.Model{ID: id, Provider: p.ID})
		}
	}
	// those the user said draw, whatever their list or name says, and
	// none they said makes videos (#1399)
	for _, id := range p.KindModels(provider.KindImage) {
		if !have[id] {
			have[id] = true
			out = append(out, kindModel(p, id))
		}
	}
	return slices.DeleteFunc(out, func(m catalog.Model) bool { return p.ModelKind(m.ID) == provider.KindVideo })
}

// OfferedDrawers are Drawers less those the user unticked in p's editor
// (provider.HiddenDrawers, 321sssrt on X): the image models a list offers
// to pick from and Automatic picks among. A request that names a hidden
// one still draws with it (resolveDrawing reads Drawers), as does a
// Settings › Images pick of one made before it was hidden.
func OfferedDrawers(p provider.Provider) []catalog.Model {
	ms := Drawers(p)
	if len(p.HiddenDrawers) == 0 {
		return ms
	}
	return slices.DeleteFunc(slices.Clone(ms), func(m catalog.Model) bool { return p.DrawerHidden(m.ID) })
}

// DrawerHidden is whether ref (provider/model), a model Settings › Images
// names, is one the user unticked in its provider's editor: it still draws,
// and the Settings row says it is hidden. A ChatGPT account's other GPT
// Image names stand for its one (ListedDrawer).
func DrawerHidden(ref string) bool {
	pid, id, ok := strings.Cut(ListedDrawer(ref), "/")
	if !ok {
		return false
	}
	p, err := provider.Find(pid)
	return err == nil && p.DrawerHidden(id)
}

// kindModel is p's model id, one the user said makes images or videos
// (provider.SetModelKind), as its list names it.
func kindModel(p provider.Provider, id string) catalog.Model {
	all, _ := catalog.LiveAll(p.ID)
	for _, m := range append(p.Available(), all...) {
		if m.ID == id {
			m.Provider = p.ID
			return m
		}
	}
	return catalog.Model{ID: id, Provider: p.ID}
}

// AutoDrawer is the model magpie draws with when the Settings name none:
// a ChatGPT account's, which its plan pays for, else the cheapest of the
// models the API keys set up can draw with, the newest of those whose price
// isn't known. A GPT Image that a newer one set up supersedes is never its
// choice (StarBits on X): an OpenAI key lists gpt-image-2.6-sunburst with no
// price, so cost alone left Automatic on the priced gpt-image-2. A GPT Image
// with no price is costed as the newest priced one, as OpenAI has priced each
// alike (gpt-image-2 to 2.6: $5 in, $30 out). "" when none can.
func AutoDrawer() string {
	type cand struct {
		ref, released string
		cost          float64
		priced, gen   bool
		key           gptImageGen
	}
	var cands []cand
	newest, newestPriced := gptImageGen{}, gptImageGen{}
	familyCost, haveFamilyCost := 0.0, false
	for _, p := range provider.All() {
		if !p.On() || p.DecideOnly() || drawsWorkBuddy(p) {
			// WorkBuddy's images cost the plan's credits: it draws only
			// with its model picked or named
			continue
		}
		// one unticked in its editor isn't Automatic's choice
		for _, m := range OfferedDrawers(p) {
			if drawsCodex(p) && m.ID != codexDrawer {
				// Codex CLI draws with gpt-image-2 only; a plan may be
				// refused the others (chatgpt.com: 403, #545)
				continue
			}
			c := cand{ref: p.ID + "/" + m.ID, released: m.Released, cost: 1e9}
			switch {
			case drawsCodex(p), drawsGrok(p):
				c.cost, c.priced = 0, true // a ChatGPT or Grok plan's images come with it
			case m.Price != nil:
				c.cost, c.priced = m.Price.Input+m.Price.Output, true
			}
			// a ChatGPT account's name says nothing of the model its
			// backend draws with (#1865), so it is never one superseded
			if k, ok := gptImageGenOf(m.ID); ok && !drawsCodex(p) {
				c.gen, c.key = true, k
				if newest.less(k) {
					newest = k
				}
				if c.priced && (!haveFamilyCost || newestPriced.less(k)) {
					newestPriced, familyCost, haveFamilyCost = k, c.cost, true
				}
			}
			cands = append(cands, c)
		}
	}
	best, bestCost, bestDate := "", 0.0, ""
	for _, c := range cands {
		if c.gen && c.key.less(newest) {
			continue
		}
		if c.gen && !c.priced && haveFamilyCost {
			c.cost = familyCost
		}
		if best == "" || c.cost < bestCost || c.cost == bestCost && c.released > bestDate {
			best, bestCost, bestDate = c.ref, c.cost, c.released
		}
	}
	return best
}

// gptImageGen orders OpenAI's image models: by version (gpt-image-2.6 over
// 2.5 over 2 over 1.5; dall-e and chatgpt-image-latest, unversioned, under
// every gpt-image), then by variant (the base or Sunburst, the most capable,
// over Flare over mini). A dated snapshot is its model's version.
type gptImageGen struct {
	version float64
	variant int
}

func (g gptImageGen) less(o gptImageGen) bool {
	return g.version < o.version || g.version == o.version && g.variant < o.variant
}

// gptImageGenOf is id's place among OpenAI's image models, a namespace
// (openai/gpt-image-2) aside; false for any other model.
func gptImageGenOf(id string) (gptImageGen, bool) {
	id = strings.ToLower(id[strings.LastIndexByte(id, '/')+1:])
	g := gptImageGen{variant: 2}
	switch {
	case strings.Contains(id, "mini"):
		g.variant = 0
	case strings.Contains(id, "flare"):
		g.variant = 1
	}
	rest, ok := strings.CutPrefix(id, "gpt-image-")
	if !ok {
		if strings.HasPrefix(id, "dall-e") || strings.HasPrefix(id, "chatgpt-image") {
			return gptImageGen{}, true
		}
		return gptImageGen{}, false
	}
	n := 0
	for n < len(rest) && (rest[n] >= '0' && rest[n] <= '9' || rest[n] == '.') {
		n++
	}
	v, err := strconv.ParseFloat(rest[:n], 64)
	if err != nil {
		return gptImageGen{}, false
	}
	g.version = v
	return g, true
}

// picture is an image sent along to draw from, or one drawn.
type picture struct {
	Mime string
	Data []byte
	URL  string // an image the vendor keeps, when it gave no bytes
}

func (p picture) dataURL() string {
	if p.URL != "" {
		return p.URL
	}
	return "data:" + p.Mime + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
}

// drawing is one images request, whichever shape it came in.
type drawing struct {
	Model, Prompt, Size, Quality, Background, Format string
	// Want is the response_format the caller asked for: with "b64_json"
	// an image a vendor gave only a URL for is fetched (inline)
	Want   string
	N      int
	Images []picture // to edit, or draw from
	Mask   *picture
	// Markup is OpenAI's markup layer: a transparent PNG laid over the
	// first image to steer the edit, never beside a Mask (#5364)
	Markup *picture
	// Extra are the request's fields magpie doesn't read, which a
	// vendor's own images API that takes them is sent as they came
	// (Ark's sequential_image_generation, optimize_prompt_options,
	// watermark…; drawingRead). Form: they came in a multipart form, as
	// text.
	Extra map[string]any
	Form  bool
}

// drawingRead are the fields of an images request magpie reads itself.
var drawingRead = []string{"model", "prompt", "size", "quality", "background", "output_format", "response_format", "n", "image", "image[]", "images", "mask", "markup", "user"}

// extraOf keeps v as one of d's Extra under k, unless magpie reads k.
func (d *drawing) extraOf(k string, v any) {
	if slices.Contains(drawingRead, k) {
		return
	}
	if d.Extra == nil {
		d.Extra = map[string]any{}
	}
	d.Extra[k] = v
}

// drawn is what a vendor gave back.
type drawn struct {
	Images  []picture
	Text    string // what a chat model said besides
	Revised string // the prompt as an images API rewrote it
	Input   int
	Output  int
}

func (s *Server) images(edit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inputBody, admitted := s.requestBody(w, r, provider.Chat)
		if !admitted {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(inputBody))
		defer func() {
			if r.MultipartForm != nil {
				r.MultipartForm.RemoveAll()
			}
		}()
		start := time.Now()
		d, err := readDrawing(r)
		if err != nil {
			writeError(w, provider.Chat, 400, err.Error())
			return
		}
		if edit && len(d.Images) == 0 {
			writeError(w, provider.Chat, 400, "an edit needs the image to edit")
			return
		}
		who := callerOf(r)
		call := Call{Time: start, From: provider.Chat, Agent: who.agent, Via: who.via, Model: d.Model, headers: headerLogOf(r.Context())}
		usage.Saw(agentOf(r))
		fail := func(code int, msg string) {
			call.Status, call.Error, call.Millis = code, msg, time.Since(start).Milliseconds()
			writeError(w, provider.Chat, code, msg)
			s.record(call)
		}
		named := d.Model != "" // a gateway key's models hold one the caller names (#882)
		if d.Model == "" {
			m, ok := drawer()
			if !ok {
				if v := DrawerMissing(); v != "" {
					fail(400, fmt.Sprintf("no model to draw with: the Image generation model picked in magpie's Settings, %q, isn't set up any more: pick another in Settings → Models → Image generation, or name one", v))
					return
				}
				fail(400, "no model to draw with: pick one in magpie's Settings → Images → Image generation, or name one")
				return
			}
			d.Model, call.Model = m, m
		}
		p, model, ok := s.drawingModel(r, d.Model)
		if !ok {
			if agentOf(r) == "codex" && !strings.Contains(d.Model, "/") {
				fail(400, "no model to draw with: Codex's provider has no matching image model and magpie's Settings → Images → Image generation has none enabled")
				return
			}
			if off, isOff := provider.SwitchedOff(d.Model); isOff {
				fail(404, switchedOff(off, d.Model))
				return
			}
			fail(404, fmt.Sprintf("magpie knows no model %q to draw with", d.Model))
			return
		}
		g, ms, isGroup := drawingGroup(d.Model, p, model)
		if isGroup {
			r = heldByGroup(r, g) // held to the group's accounts, if it is
		}
		keyWho, keyHeld := keyHolds(r)
		if keyHeld && named && (isGroup && !groupAllowed(keyWho, g, ms) || !isGroup && !modelAllowed(keyWho, p, model)) {
			fail(403, keyModelError(keyWho, d.Model))
			return
		}
		if msg, off := modelOff(p, model, d.Model); off { // disabled by the user (#1867)
			fail(403, msg)
			return
		}
		// the models to draw on, the next asked while one fails: a routing
		// group's members in its routing's order (#2005), else the one
		tries := []provider.Member{{Provider: p, Model: model}}
		if isGroup {
			var paused []provider.Paused
			if ms, paused = provider.PausedOut(g.Live(), ms, agentOf(r), "", ruleClock()); len(ms) == 0 && len(paused) > 0 {
				fail(503, pausedError(d.Model, paused))
				return
			}
			var keeps map[string]bool
			if keyHeld {
				keeps = groupKeeps(keyWho, g, ms)
			}
			t := s.drawingTries(g, ms, keeps, drawsModel)
			if len(t) == 0 && keeps != nil {
				fail(403, keyModelError(keyWho, d.Model))
				return
			}
			if len(t) > 0 {
				tries = t
			}
		}
		var unmask func()
		var hits maskHits
		w, d.Prompt, unmask, hits, err = redactedPromptHits(r.Context(), w, d.Prompt)
		defer unmask()
		// the fields passed on as they came are masked as the prompt is,
		// as a video's are
		if err == nil {
			var extra maskHits
			err = maskStrings(r.Context(), d.Extra, &extra)
			hits.add(extra)
		}
		if err != nil {
			fail(maskStatus(err), err.Error())
			return
		}
		// the Routing page lists it as it does a chat, each account or
		// member asked a try of its one route (sideRoute)
		var grp *GroupRef
		if isGroup {
			grp = groupRef(g, ms)
		}
		side := s.beginSide(r, start, call.Model, p.ID, grp)
		side.masked(noteMasked(r, "image", hits, d.Prompt, side.id(), p.ID, call.Model))
		call.Provider, call.To = p.ID, provider.Chat
		ctx, cancel := drawWithin(withSide(r.Context(), side), drawTimeout())
		defer cancel()
		// the account that drew, or refused last, is the one recorded
		var (
			drew    provider.Provider
			out     drawn
			code    int
			capBack time.Time
			skipped []string
		)
		for i, m := range tries {
			call.Provider, model = m.Provider.ID, m.Model
			drew, out, code, capBack, err = s.drawOnAccounts(ctx, r, m.Provider, m.Model, d)
			// a group's next member is asked as a chat's would be: while
			// this one's vendor fails, is out of quota or doesn't serve the
			// model; a refusal of the request itself is the request's
			if err == nil || ctx.Err() != nil || i+1 == len(tries) || !retryable(code, []byte(err.Error())) {
				break
			}
			skipped = append(skipped, m.Provider.ID+"/"+m.Model+": "+err.Error())
		}
		p = drew
		call.Fallback = strings.Join(skipped, "; ")
		call.Millis = time.Since(start).Milliseconds()
		call.Status, call.Usage.Input, call.Usage.Output = code, out.Input, out.Output
		if err == nil && len(out.Images) == 0 {
			code, err = 502, errors.New(model+" drew nothing"+vendorSaid(out.Text))
			call.Status = code
		}
		providerKeyID, providerKeyName := "", ""
		if p.Account == nil && p.Key != "" {
			providerKeyID, providerKeyName = provider.KeyID(p.Key), p.KeyName
		}
		side.end(p, model, call.Status, err, Usage{Input: out.Input, Output: out.Output})
		appendUsage(r, usage.Record{RouteID: side.id(), Operation: "generate_content", Time: start, Agent: call.Agent, Via: call.Via, Provider: p.ID, Host: p.Where(), Model: model, Requested: call.Model, ProviderKeyID: providerKeyID, ProviderKeyName: providerKeyName, ProviderAccount: accountOf(p),
			Input: out.Input, Output: out.Output, Millis: call.Millis, Status: call.Status, Session: sessionOf(r.Header)})
		if err != nil {
			call.Error = err.Error()
			s.record(call)
			if d := time.Until(capBack); d > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
			}
			msg := err.Error()
			if len(skipped) > 0 {
				msg += " (tried first: " + call.Fallback + ")"
			}
			writeError(w, provider.Chat, code, msg)
			return
		}
		s.record(call)
		// asked for the image itself, one the vendor keeps at a URL is
		// fetched through the vendor's proxy (#1576: the 4K image a relay
		// drew in 40 s was lost to generate_image's own download), from
		// public addresses or the vendor's own host only: the URL is in
		// its answer, which the request can steer (vendorClient). One
		// that can't be fetched is answered by its URL all the same, never
		// dropped: it is drawn, and paid for.
		data := make([]map[string]any, 0, len(out.Images))
		for _, im := range out.Images {
			if im.URL != "" && inline(d.Want) {
				if b, mt, err := FetchImage(p.Via(r.Context()), vendorClient(p), im.URL); err == nil {
					im = picture{Mime: mt, Data: b}
				} else if s.debug {
					log.Printf("images: %s's image at %s is answered by its URL: %v", p.ID, im.URL, err)
				}
			}
			e := map[string]any{}
			if im.URL != "" {
				e["url"] = im.URL
			} else {
				e["b64_json"] = base64.StdEncoding.EncodeToString(im.Data)
				e["mime_type"] = im.Mime
			}
			if out.Revised != "" {
				e["revised_prompt"] = out.Revised
			}
			data = append(data, e)
		}
		res := map[string]any{"created": start.Unix(), "model": p.ID + "/" + model, "data": data,
			"usage": map[string]int{"input_tokens": out.Input, "output_tokens": out.Output, "total_tokens": out.Input + out.Output}}
		if out.Text != "" {
			res["text"] = out.Text
		}
		writeJSON(w, 200, res)
	}
}

// drawingGroup is the routing group a drawing names, with its members
// ready now: by its id ("group/<id>"), or by a bare model id the group
// goes by (provider.GroupFor) that resolved to one of its members. Any
// other names a model on its own.
func drawingGroup(asked string, p provider.Provider, model string) (provider.Group, []provider.Member, bool) {
	return groupToMake(asked, p, model, drawingMembers)
}

// groupToMake is drawingGroup with the group's members found by find:
// a drawing's take its image models in (drawingMembers), a video's are
// FindGroup's.
func groupToMake(asked string, p provider.Provider, model string, find func(ref string) (provider.Group, []provider.Member, bool)) (provider.Group, []provider.Member, bool) {
	ref := asked
	if !provider.IsGroupRef(ref) {
		gid, ok := provider.GroupFor(ref)
		if !ok {
			return provider.Group{}, nil, false
		}
		ref = gid
	}
	g, ms, ok := find(ref)
	if !ok || len(ms) == 0 {
		return provider.Group{}, nil, false
	}
	if ref != asked && !slices.ContainsFunc(ms, func(m provider.Member) bool { return m.Provider.ID == p.ID && m.Model == model }) {
		return provider.Group{}, nil, false
	}
	return g, ms, true
}

// drawingTries are a group's members to draw on, once each, in the order
// its routing tries them for a chat (planGroup); each is gone over on its
// own keys or accounts by drawOnAccounts. A member planning leaves out (all
// its keys barred, say) goes last, so its refusal is the one told only
// when every other failed too; one a calling key may not use (keeps, when
// not nil) is never asked. A group with chat models beside the ones that
// make it (makes) is asked on those alone: its chat members are its
// conversations', and asking one to draw sends the request to a model, or
// a provider's images API, the user never put there to draw
// (changqiu4890 on Discord).
func (s *Server) drawingTries(g provider.Group, ms []provider.Member, keeps map[string]bool, makes func(p provider.Provider, model string) bool) []provider.Member {
	if slices.ContainsFunc(ms, func(m provider.Member) bool { return makes(m.Provider, m.Model) }) {
		ms = slices.DeleteFunc(slices.Clone(ms), func(m provider.Member) bool { return !makes(m.Provider, m.Model) })
	}
	byRef := map[string]provider.Member{}
	for _, m := range ms {
		if ref := m.Provider.ID + "/" + m.Model; byRef[ref].Model == "" {
			byRef[ref] = m
		}
	}
	var out []provider.Member
	seen := map[string]bool{}
	add := func(ref string) {
		m, ok := byRef[ref]
		if !ok || seen[ref] || keeps != nil && !keeps[ref] {
			return
		}
		seen[ref] = true
		out = append(out, m)
	}
	cs, _ := s.planGroup(g.Live(), ms, provider.Chat)
	for _, c := range cs {
		add(c.p.ID + "/" + c.model)
	}
	for _, m := range ms {
		add(m.Provider.ID + "/" + m.Model)
	}
	return out
}

func vendorSaid(text string) string {
	if text = strings.TrimSpace(text); text == "" {
		return ""
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return ": " + text
}

// readDrawing reads an images request: JSON, or the multipart form OpenAI's
// edits take.
func readDrawing(r *http.Request) (drawing, error) {
	var d drawing
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			return d, err
		}
		f := r.MultipartForm
		get := func(k string) string {
			if v := f.Value[k]; len(v) > 0 {
				return strings.TrimSpace(v[0])
			}
			return ""
		}
		d.Model, d.Prompt, d.Size, d.Quality, d.Background, d.Format, d.Want = get("model"), get("prompt"), get("size"), get("quality"), get("background"), get("output_format"), get("response_format")
		d.N, _ = strconv.Atoi(get("n"))
		d.Form = true
		for k := range f.Value {
			d.extraOf(k, get(k))
		}
		for _, k := range []string{"image", "image[]"} {
			for _, fh := range f.File[k] {
				pic, err := readPart(fh)
				if err != nil {
					return d, err
				}
				d.Images = append(d.Images, pic)
			}
		}
		if fhs := f.File["mask"]; len(fhs) > 0 {
			pic, err := readPart(fhs[0])
			if err != nil {
				return d, err
			}
			d.Mask = &pic
		}
		if fhs := f.File["markup"]; len(fhs) > 0 {
			pic, err := readPart(fhs[0])
			if err != nil {
				return d, err
			}
			d.Markup = &pic
		}
	} else {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return d, err
		}
		var in struct {
			Model          string          `json:"model"`
			Prompt         string          `json:"prompt"`
			Size           string          `json:"size"`
			Quality        string          `json:"quality"`
			Background     string          `json:"background"`
			Format         string          `json:"output_format"`
			N              int             `json:"n"`
			Image          json.RawMessage `json:"image"`
			Images         json.RawMessage `json:"images"`
			Mask           json.RawMessage `json:"mask"`
			Markup         json.RawMessage `json:"markup"`
			ResponseFormat string          `json:"response_format"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return d, fmt.Errorf("the request isn't JSON: %v", err)
		}
		d = drawing{Model: in.Model, Prompt: in.Prompt, Size: in.Size, Quality: in.Quality, Background: in.Background, Format: in.Format, Want: strings.TrimSpace(in.ResponseFormat), N: in.N}
		var all map[string]any
		json.Unmarshal(body, &all)
		for k, v := range all {
			d.extraOf(k, v)
		}
		for _, raw := range []json.RawMessage{in.Image, in.Images} {
			for _, src := range imageSources(raw) {
				pic, err := pictureOf(src)
				if err != nil {
					return d, err
				}
				d.Images = append(d.Images, pic)
			}
		}
		if srcs := imageSources(in.Mask); len(srcs) > 0 {
			pic, err := pictureOf(srcs[0])
			if err != nil {
				return d, err
			}
			d.Mask = &pic
		}
		if srcs := imageSources(in.Markup); len(srcs) > 0 {
			pic, err := pictureOf(srcs[0])
			if err != nil {
				return d, err
			}
			d.Markup = &pic
		}
	}
	if d.Mask != nil && d.Markup != nil {
		return d, errors.New("give a mask or a markup layer, not both: an edit takes one")
	}
	d.Model = strings.TrimSpace(d.Model)
	// Ark's layer_decomposition splits the image it is given into layers,
	// and takes no prompt
	if strings.TrimSpace(d.Prompt) == "" && (d.Extra["layer_decomposition"] == nil || len(d.Images) == 0) {
		return d, errors.New("say what to draw in prompt")
	}
	if d.N <= 0 {
		d.N = 1
	}
	if d.N > maxDrawings {
		return d, fmt.Errorf("n is at most %d", maxDrawings)
	}
	return d, nil
}

// imageSources are the images a JSON field gives: a URL, {"image_url": …},
// or a list of either.
func imageSources(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var obj struct {
		ImageURL json.RawMessage `json:"image_url"`
		URL      string          `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && (len(obj.ImageURL) > 0 || obj.URL != "") {
		if obj.URL != "" {
			return []string{obj.URL}
		}
		return imageSources(obj.ImageURL)
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var out []string
		for _, e := range list {
			out = append(out, imageSources(e)...)
		}
		return out
	}
	return nil
}

// pictureOf is the image at a data: URL, or an http one left for the vendor
// to fetch.
func pictureOf(src string) (picture, error) {
	src = strings.TrimSpace(src)
	if rest, ok := strings.CutPrefix(src, "data:"); ok {
		meta, data, ok := strings.Cut(rest, ",")
		if !ok || !strings.HasSuffix(meta, ";base64") {
			return picture{}, errors.New("an image's data: URL must be base64")
		}
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return picture{}, fmt.Errorf("an image's data: URL isn't base64: %v", err)
		}
		return picture{Mime: strings.TrimSuffix(meta, ";base64"), Data: b}, nil
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return picture{URL: src}, nil
	}
	return picture{}, errors.New("an image is a data: or http(s) URL")
}

func readPart(fh *multipart.FileHeader) (picture, error) {
	f, err := fh.Open()
	if err != nil {
		return picture{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return picture{}, err
	}
	mt := fh.Header.Get("Content-Type")
	if mt == "" || mt == "application/octet-stream" {
		mt = http.DetectContentType(b)
	}
	return picture{Mime: mt, Data: b}, nil
}

// drawVia is the API a model draws on at a provider.
type drawVia int

const (
	viaImages drawVia = iota // /images/generations, /images/edits
	viaChat                  // chat completions with modalities
	viaGemini                // Google's own generateContent
)

func (v drawVia) String() string {
	return [...]string{"images API", "chat completions", "generateContent"}[v]
}

func isGoogle(p provider.Provider) bool {
	return provider.HostOf(p.Base(provider.Chat)) == "generativelanguage.googleapis.com"
}

func isOpenRouter(p provider.Provider) bool {
	return provider.HostOf(p.Base(provider.Chat)) == "openrouter.ai"
}

// viaFor is where model is best asked to draw at p.
func viaFor(p provider.Provider, model string) drawVia {
	switch {
	case p.IsRemoteMagpie():
		// another magpie's images API asks the model's vendor the way
		// that model draws there
		return viaImages
	case isGoogle(p) && strings.Contains(strings.ToLower(model), "gemini"):
		return viaGemini
	case isOpenRouter(p):
		if catalog.ImagesAPI(model) || answersInImages(p, model) {
			return viaImages
		}
		return viaChat
	case catalog.ImagesAPI(model):
		return viaImages
	}
	return viaChat
}

// answersInImages is whether p's fetched list says model answers with images
// and no text: OpenRouter's Recraft, Riverflow, Krea and MAI Image, whose ids
// name no images API, draw at its /images as FLUX and Seedream do, and only
// its chat image models (Nano Banana, GPT-5 Image) answer in chat.
func answersInImages(p provider.Provider, model string) bool {
	// a drawing request's look, once, through the image models alone
	for _, m := range catalog.LiveDrawers(p.ID) {
		if m.ID == model && slices.Contains(m.OutputModalities, "image") && !slices.Contains(m.OutputModalities, "text") {
			return true
		}
	}
	return false
}

// drawOnAccounts is draw on a subscription's accounts in the order its
// text requests go over them (the provider's candidates, without its
// fallback models): one whose plan won't draw (chatgpt.com's 403
// {"detail":"Forbidden"} for a plan without images) or is out of them
// hands the request to the next, as a Plus account further down draws
// what the pinned one may not (#545). The refusal is of images alone, so
// no account rests for text over it. It answers with the account that
// drew, or the last one's error when every one refused. A provider's keys
// are gone over the same way (#1657): a key whose own list of models
// leaves the image model out never draws, nor is the first key used for
// one only another lists; with every key or account set not to serve the
// model, it is refused with why, as a chat is.
func (s *Server) drawOnAccounts(ctx context.Context, r *http.Request, p provider.Provider, model string, d drawing) (provider.Provider, drawn, int, time.Time, error) {
	if p.Account == nil && p.Key == "" {
		// no key and no account (a local ollama): nothing to go over
		tried := sideOf(ctx).try(candidateOf(p, model))
		out, code, err := s.draw(ctx, p, model, d)
		tried(code, err)
		return p, out, code, time.Time{}, err
	}
	q := p
	q.Fallback = nil
	cs := s.candidates(q, model, provider.Chat)
	// an account whose list lacks the image model is put aside for text
	// when another's list is unknown; for drawing it is asked last
	_, _, left, barred := perKeyBarred(q, model, provider.Chat)
	if listsCantName(q) {
		// its accounts' own lists of models are picked from its chat
		// models, which its image models are never among: a list keeps
		// an account from no drawing, as it couldn't name one
		for _, c := range barred {
			if c.capped == nil {
				left = append(left, c)
			}
		}
		barred = slices.DeleteFunc(barred, func(c candidate) bool { return c.capped == nil })
	}
	for _, c := range left {
		if !slices.ContainsFunc(cs, func(o candidate) bool { return o.p.AccountID() == c.p.AccountID() }) {
			cs = append(cs, c)
		}
	}
	// the accounts the calling key may not use are left out of the whole
	// fan-out (#905): the next one a refusal hands the request to no less
	// than the first, so none it may not use draws
	if keyWho, held := accountHolds(r); held {
		cs = slices.DeleteFunc(cs, func(c candidate) bool { return !accountAllowed(keyWho, c) })
		if len(cs) == 0 {
			return p, drawn{}, http.StatusForbidden, time.Time{}, errors.New(keyAccountsError(keyWho, d.Model))
		}
	}
	if len(cs) == 0 && slices.ContainsFunc(barred, func(c candidate) bool { return c.capped != nil }) {
		// every account is held at its usage cap
		msg, back := cappedError(model, barredOf(barred, q, false, provider.Chat, nil), time.Now())
		return p, drawn{}, http.StatusTooManyRequests, back, errors.New(msg)
	}
	if len(cs) == 0 && len(barred) > 0 {
		// every key or account is set not to serve the model (#474)
		return p, drawn{}, http.StatusForbidden, time.Time{}, errors.New(barredError(d.Model, barredOf(barred, q, false, provider.Chat, nil)))
	}
	if len(cs) == 0 {
		cs = []candidate{{p: p, model: model, rest: p.ID}}
	}
	cs = probeOne(cs) // of those resting, only the one back soonest (#1799)
	var out drawn
	var code int
	var err error
	for i, c := range cs {
		p = c.p
		tried := sideOf(ctx).try(c)
		out, code, err = s.draw(ctx, p, model, d)
		tried(code, err)
		if err != nil && ctx.Err() == nil {
			restDrawer(c, code, err.Error())
		}
		if err == nil || ctx.Err() != nil || i+1 == len(cs) || !accountRefusedDrawing(code) && !keyLacksModel(code, err) {
			break
		}
	}
	return p, out, code, time.Time{}, err
}

// restDrawer rests the image model on c's account when the account is out
// of its quota for it, until the reset the refusal names (Antigravity's
// "Your quota will reset after 3h19m30s.", resetsAt), else quotaRest. Only
// the model rests (restID), never the account: its text models draw on
// allowances of their own. The next drawing then asks the accounts that
// still have it first (restLast in candidates), and a chat on that image
// model goes past it too, where before every drawing asked the spent
// account first again (01huadalang on Discord). A failure of any other
// kind rests nothing, as before: a key, a request refused, a vendor down;
// nor does any on a provider set never to rest for a limit (LimitRestOff).
func restDrawer(c candidate, code int, msg string) {
	if c.p.Account == nil || c.p.LimitRestOff || c.model == "" || failure(code, []byte(msg)) != failQuota {
		return
	}
	now := time.Now()
	r := Rest{Why: failQuota, Status: code, By: "quota", Key: c.restID(), Model: c.model,
		agent: c.p.Account.UsageAgent(), user: c.p.Account.User, said: msg}
	d := quotaRest
	if w := resetsAt([]byte(msg), now); w > 0 {
		d, r.By = min(w, longestQuota), "resets"
	}
	r.Until = now.Add(d)
	restingUntil.Lock()
	restingUntil.m[r.Key] = r.Until
	restingUntil.note[r.Key] = r
	restingUntil.Unlock()
}

// saidStatus is the status an error said inside a drawing's 200 stands
// for, which drawOnAccounts goes by: Antigravity tells a model's quota used
// up as the stream's error event as well as a 429 ("You have exhausted
// your capacity on this model. Your quota will reset after 3h19m30s."),
// and told as a 502 it ended the drawing on the spent account while the
// next one had its quota (01huadalang on Discord). Out of quota, or
// rate limited, it is the 429 it would have been; out of credit, the 402;
// anything else the 502 it was, which asks no other account.
func saidStatus(msg string) int {
	b := wordsOf([]byte(msg))
	switch {
	case usedUpWords.Match(b), quotaCodes.Match(b), rateWords.Match(b):
		return http.StatusTooManyRequests
	case creditWords.Match(b):
		return http.StatusPaymentRequired
	}
	return http.StatusBadGateway
}

// keyLacksModel says a vendor answered 404 that the model isn't one this
// key or account serves: a relay's "Model "gpt-image-2" is not supported by
// any configured account in this group" for a key of its text models
// alone, while the provider's next key serves it (#1657). The next is
// asked; a bare 404, a wrong address, is the request's and asks none.
func keyLacksModel(code int, err error) bool {
	return code == http.StatusNotFound && err != nil && modelRefused(code, []byte(err.Error()))
}

// accountRefusedDrawing says a failure to draw was the account's — its
// sign-in, its plan, its allowance — not the request's, so another
// account of the subscription may draw it.
func accountRefusedDrawing(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return false
}

// draw asks the provider for d's images, on the API model draws on there,
// and on the other when that one isn't served.
func (s *Server) draw(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	if drawsWorkBuddy(p) {
		return s.drawWorkBuddy(ctx, p, model, d)
	}
	if drawsCodex(p) || drawsGrok(p) {
		return s.drawImages(ctx, p, model, d)
	}
	if drawsCopilot(p) {
		return s.drawImages(ctx, p, model, d)
	}
	if googleAccount(p) || pluginDrawsGemini(p, model) {
		return s.drawCodeAssist(ctx, p, model, d)
	}
	if p.Base(provider.Chat) == "" && p.Base(provider.Responses) == "" {
		return drawn{}, 400, fmt.Errorf("%s can't draw: magpie draws only through an OpenAI-compatible API, and %s has none", p.Name, p.Name)
	}
	via := viaFor(p, model)
	out, code, err := s.drawOn(ctx, p, model, d, via)
	if err != nil && via != viaGemini && (((code == 404 || code == 405) && !(isOpenRouter(p) && via == viaImages)) || pointsAway(via, code, err)) {
		other := viaChat
		if via == viaChat {
			other = viaImages
		}
		// the vendor named the other API: its answer there is the one to
		// tell, not the refusal that sent the request on
		if o, c, e := s.drawOn(ctx, p, model, d, other); e == nil || pointsAway(via, code, err) {
			return o, c, e
		}
	}
	return out, code, err
}

// pointsAway is whether a vendor turned a drawing away on one API with
// words that send it to the other: a 400 on chat completions saying "Use
// /v1/images/generations" (Agnes, #1837), or one on the images API naming
// chat completions. The model is then asked there, as when the API it was
// asked on isn't served at all (404, 405).
func pointsAway(via drawVia, code int, err error) bool {
	if code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		return false
	}
	if via == viaChat {
		return catalog.PointsToImagesAPI(err.Error())
	}
	return catalog.PointsToChat(err.Error())
}

func (s *Server) drawOn(ctx context.Context, p provider.Provider, model string, d drawing, via drawVia) (drawn, int, error) {
	switch via {
	case viaGemini:
		return s.drawGemini(ctx, p, model, d)
	case viaChat:
		return s.drawChat(ctx, p, model, d)
	}
	return s.drawImages(ctx, p, model, d)
}

// send posts to the vendor and reads its answer; a failure's code is the
// vendor's, with its own message.
func (s *Server) send(ctx context.Context, p provider.Provider, url, contentType string, body []byte, sign bool) ([]byte, int, error) {
	return s.sendAs(ctx, p, http.MethodPost, url, contentType, body, sign)
}

// sendAs is send with the method: a video's progress is asked with a GET.
func (s *Server) sendAs(ctx context.Context, p provider.Provider, method, url, contentType string, body []byte, sign bool) ([]byte, int, error) {
	return s.sendWith(ctx, p, method, url, contentType, body, sign, nil)
}

// sendWith is sendAs with headers of the vendor's own besides.
func (s *Server) sendWith(ctx context.Context, p provider.Provider, method, url, contentType string, body []byte, sign bool, extra http.Header) ([]byte, int, error) {
	ctx = s.metered(p.Via(ctx), p, "") // counted against its MaxRPM (rpm.go)
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, 500, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// magpie's, or the agent's own where the provider passes it on (#1851)
	stampUA(ctx, p, req.Header)
	if p.IsRemoteMagpie() {
		req.Header.Set("User-Agent", "magpie/"+Version)
		passOnCaller(ctx, req)
	}
	if sign {
		if err := p.Sign(ctx, req, provider.Chat, body); err != nil {
			return nil, 502, err
		}
		if drawsCodex(p) {
			// the images API answers in JSON, not the stream Codex's
			// signing asks of its Responses; the id is Codex CLI's own
			req.Header.Set("Accept", "application/json")
			req.Header.Set("x-codex-imagegen-request-id", newUUID())
			req.Header.Set("x-codex-image-turn-id", newUUID())
		}
	} else {
		// Google's API keys go in their own header
		req.Header.Set("x-goog-api-key", p.Key)
		for k, v := range p.SentHeaders(ctx) {
			catalog.PutUserHeader(req.Header, k, v)
		}
	}
	for k, v := range extra {
		req.Header[k] = v
	}
	asked := time.Now()
	res, err := p.Do(s.client, req) // a plugin's through the plugin
	if err != nil {
		if ctx.Err() != nil {
			return nil, 504, fmt.Errorf("%s didn't answer in %s", p.Name, givenTo(ctx, asked))
		}
		return nil, 502, err
	}
	defer res.Body.Close()
	answered := time.Since(asked)
	rb := &tracked{r: res.Body, last: time.Now()}
	b, err := io.ReadAll(io.LimitReader(rb, 256<<20))
	if err != nil {
		if ctx.Err() != nil {
			// not a bare "context deadline exceeded" (#1576): the vendor
			// began answering, and its answer is what ran out of time
			return nil, 504, fmt.Errorf("%s began answering after %s (%d %s) but hadn't sent all of its answer in %s: %s", p.Name, answered.Round(time.Second), res.StatusCode, http.StatusText(res.StatusCode), givenTo(ctx, asked), stalledAt(b, time.Since(rb.last)))
		}
		return nil, 502, err
	}
	if res.StatusCode >= 300 {
		hint := ""
		if drawsCodex(p) && res.StatusCode == http.StatusForbidden {
			hint = fmt.Sprintf(" — ChatGPT turned %s's images away: its plan or workspace may not draw, or not with this model (Codex CLI draws with %s)", p.Name, codexDrawer)
		}
		return b, res.StatusCode, fmt.Errorf("%s: %d %s%s%s", provider.HostOf(url), res.StatusCode, http.StatusText(res.StatusCode), errorSaid(b), hint)
	}
	return b, res.StatusCode, nil
}

// tracked is a body that notes when its last bytes came.
type tracked struct {
	r    io.Reader
	last time.Time
}

func (t *tracked) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.last = time.Now()
	}
	return n, err
}

// stalledAt says where an answer that didn't finish stopped (#1576): how
// much had come, how long nothing more had, and whether the image was
// coming as a link (so the answer itself stuck, after its start) or as
// b64_json (so it was the image's own bytes that came too slowly). A relay
// may answer one size with either, so the two point at different fixes.
func stalledAt(got []byte, quiet time.Duration) string {
	where := fmt.Sprintf("%s had come, and nothing more for the last %s", sizeOf(len(got)), quiet.Round(time.Second))
	switch {
	case bytes.Contains(got, []byte(`"b64_json"`)):
		return "it was sending the image itself as b64_json; " + where
	case bytes.Contains(got, []byte(`"url"`)):
		return "it was answering with the image's url, not the image; " + where
	}
	return "what came doesn't yet say whether the image was coming as a url or as b64_json; " + where
}

// sizeOf is n bytes for a reader: 386 B, 12.4 KB, 3.0 MB.
func sizeOf(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1000*1000:
		return fmt.Sprintf("%.1f KB", float64(n)/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1000/1000)
}

// vendorMessage is the message of a vendor's error, or its body.
func vendorMessage(b []byte) string {
	var e struct {
		Error  json.RawMessage `json:"error"`
		Errors struct {
			Message string `json:"message"`
		} `json:"errors"` // ModelScope's
		Code json.RawMessage `json:"code"` // WorkBuddy's {code, msg}
		Msg  string          `json:"msg"`
	}
	if json.Unmarshal(b, &e) == nil && e.Errors.Message != "" {
		return e.Errors.Message
	}
	if json.Unmarshal(b, &e) == nil && len(e.Error) == 0 && e.Msg != "" {
		if code := strings.Trim(string(e.Code), `"`); code != "" && code != "0" && code != "null" {
			return e.Msg + " (" + code + ")"
		}
	}
	if json.Unmarshal(b, &e) == nil && len(e.Error) > 0 {
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &m) == nil && m.Message != "" {
			return m.Message
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil {
			return s
		}
	}
	// a web page is said for what it is, not as markup
	if said, ok := provider.WebPage(b); ok {
		return said
	}
	return string(b)
}

// errorSaid is what an HTTP error's body said, for after its status: the
// vendor's message, a web page by its title, or, for an empty body, that
// it was refused with no details.
func errorSaid(b []byte) string {
	if said, ok := provider.Unsaid(b); ok {
		return ": " + said
	}
	return vendorSaid(vendorMessage(b))
}

// drawImages asks an images API: generations, or edits with the images
// sent along.
func (s *Server) drawImages(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	if isOpenRouter(p) {
		return s.drawOpenRouterImages(ctx, p, model, d)
	}
	if isModelScope(p) {
		return s.drawModelScope(ctx, p, model, d)
	}
	if isArk(p) {
		return s.drawArk(ctx, p, model, d)
	}
	if dashScopeDraws(p, model) {
		return s.drawDashScope(ctx, p, model, d)
	}
	base := strings.TrimRight(p.Base(provider.Chat), "/")
	if base == "" {
		// a Responses-only relay serves the images API under the same root
		// as its Responses endpoint
		base = strings.TrimRight(p.Base(provider.Responses), "/")
	}
	if drawsCodex(p) || drawsGrok(p) {
		base = strings.TrimRight(p.Base(provider.Responses), "/")
	}
	var body []byte
	var ct, url string
	m := strings.ToLower(model)
	if drawsGrok(p) {
		// Grok's Imagine API takes an aspect ratio, not a size or a quality
		req := map[string]any{"model": model, "prompt": d.Prompt, "n": d.N, "response_format": "b64_json"}
		if ar := aspectAmong(d.Size, grokAspects); ar != "" {
			req["aspect_ratio"] = ar
		}
		if len(d.Images) > 0 {
			var imgs []map[string]string
			for _, pic := range d.Images {
				imgs = append(imgs, map[string]string{"url": pic.dataURL(), "type": "image_url"})
			}
			req["images"] = imgs
			ct, url = "application/json", base+"/images/edits"
		} else {
			ct, url = "application/json", base+"/images/generations"
		}
		body, _ = json.Marshal(req)
	} else if len(d.Images) == 0 {
		req := map[string]any{"model": model, "prompt": d.Prompt, "n": d.N}
		fields := map[string]string{"size": d.Size, "quality": d.Quality, "background": d.Background, "output_format": d.Format}
		if drawsCopilot(p) {
			// Copilot CLI sends model, prompt, size and quality
			fields = map[string]string{"size": d.Size, "quality": d.Quality}
		}
		for k, v := range fields {
			if v != "" {
				req[k] = v
			}
		}
		// only dall-e gives a URL unless asked; gpt-image turns the field away
		if strings.Contains(m, "dall-e") || senseNovaDrawer(m) {
			req["response_format"] = "b64_json"
		}
		body, _ = json.Marshal(req)
		ct, url = "application/json", base+"/images/generations"
	} else if drawsCodex(p) || senseNovaDrawer(m) {
		// ChatGPT's backend takes an edit only as JSON, each image a data URL;
		// so does SenseNova's for its U1 models, which answer in a URL unless
		// asked for base64
		req := map[string]any{"model": model, "prompt": d.Prompt, "n": d.N}
		if senseNovaDrawer(m) {
			req["response_format"] = "b64_json"
		}
		for k, v := range map[string]string{"size": d.Size, "quality": d.Quality, "background": d.Background, "output_format": d.Format} {
			if v != "" {
				req[k] = v
			}
		}
		var imgs []map[string]string
		for _, pic := range d.Images {
			imgs = append(imgs, map[string]string{"image_url": pic.dataURL()})
		}
		req["images"] = imgs
		if d.Mask != nil {
			req["mask"] = map[string]string{"image_url": d.Mask.dataURL()}
		}
		if d.Markup != nil {
			req["markup"] = map[string]string{"image_url": d.Markup.dataURL()}
		}
		body, _ = json.Marshal(req)
		ct, url = "application/json", base+"/images/edits"
	} else {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range map[string]string{"model": model, "prompt": d.Prompt, "n": strconv.Itoa(d.N), "size": d.Size, "quality": d.Quality, "background": d.Background, "output_format": d.Format} {
			if v != "" {
				mw.WriteField(k, v)
			}
		}
		if strings.Contains(m, "dall-e") {
			mw.WriteField("response_format", "b64_json")
		}
		field := "image"
		if len(d.Images) > 1 {
			field = "image[]"
		}
		pics := append([]picture(nil), d.Images...)
		fields := make([]string, len(pics))
		for i := range pics {
			fields[i] = field
		}
		if d.Mask != nil {
			pics, fields = append(pics, *d.Mask), append(fields, "mask")
		}
		if d.Markup != nil {
			pics, fields = append(pics, *d.Markup), append(fields, "markup")
		}
		for i, pic := range pics {
			if pic.URL != "" {
				b, mt, err := s.fetchPicture(ctx, pic.URL)
				if err != nil {
					return drawn{}, 400, err
				}
				pic = picture{Mime: mt, Data: b}
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename="image%d%s"`, fields[i], i, extOf(pic.Mime)))
			h.Set("Content-Type", pic.Mime)
			part, _ := mw.CreatePart(h)
			part.Write(pic.Data)
		}
		mw.Close()
		body, ct, url = buf.Bytes(), mw.FormDataContentType(), base+"/images/edits"
	}
	b, code, err := s.send(ctx, p, url, ct, body, true)
	if err != nil {
		return drawn{}, code, err
	}
	return readImagesAnswer(p, url, b, code)
}

// senseNovaDrawer is whether model is one of SenseNova's U1 image models
// (sensenova-u1.5-lite, sensenova-u1.5-fast), whose /images/edits takes
// JSON alone, the images as [{"image_url": "data:…"}], and whose answer
// is a link for 24 hours unless response_format asks for b64_json
// (lileo0668 on Discord).
func senseNovaDrawer(model string) bool {
	return strings.Contains(strings.ToLower(model), "sensenova-u1")
}

// drawOpenRouterImages asks OpenRouter's dedicated Images API, which accepts
// reference images as input_references rather than OpenAI's edits multipart.
func (s *Server) drawOpenRouterImages(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	if d.Mask != nil {
		return drawn{}, http.StatusBadRequest, errors.New("OpenRouter's images API does not support mask edits")
	}
	if d.Markup != nil {
		return drawn{}, http.StatusBadRequest, errors.New("OpenRouter's images API does not support a markup layer")
	}
	req := map[string]any{"model": model, "prompt": d.Prompt, "n": d.N}
	for k, v := range map[string]string{"quality": d.Quality, "background": d.Background, "output_format": d.Format} {
		if v != "" {
			req[k] = v
		}
	}
	openRouterSize(req, d.Size)
	if len(d.Images) > 0 {
		refs := make([]map[string]any, 0, len(d.Images))
		for _, pic := range d.Images {
			refs = append(refs, map[string]any{"type": "image_url", "image_url": map[string]string{"url": pic.dataURL()}})
		}
		req["input_references"] = refs
	}
	body, _ := json.Marshal(req)
	url := strings.TrimRight(p.Base(provider.Chat), "/") + "/images"
	b, code, err := s.send(ctx, p, url, "application/json", body, true)
	if err != nil {
		return drawn{}, code, err
	}
	return readImagesAnswer(p, url, b, code)
}

// openRouterSize puts an images API size into req as OpenRouter's Image API
// takes it. Pixels ("1536x1024") go as aspect_ratio, and as resolution
// when they ask for more than 1K: no endpoint of its /images/models lists
// size among what it takes, and Sourceful's Riverflow V2 and xAI's answer
// a pixel size with 400 "does not support explicit pixel sizes. Use
// resolution and aspect_ratio instead." A ratio ("16:9") is an
// aspect_ratio, and a tier ("2K", "auto") goes as the size it is.
func openRouterSize(req map[string]any, size string) {
	size = strings.TrimSpace(size)
	if size == "" {
		return
	}
	if strings.Contains(size, ":") {
		req["aspect_ratio"] = size
		return
	}
	r := aspectAmong(size, []string{"1:1", "2:3", "3:2", "3:4", "4:3", "9:16", "16:9", "21:9"})
	if r == "" {
		req["size"] = size
		return
	}
	req["aspect_ratio"] = r
	if t := imageSizeOf(size); t != "" {
		req["resolution"] = t
	}
}

// readImagesAnswer reads an images API's answer to url: its images, as bytes
// or the URLs the vendor keeps them at, and what they cost.
func readImagesAnswer(p provider.Provider, url string, b []byte, code int) (drawn, int, error) {
	var res struct {
		Data []struct {
			B64       string `json:"b64_json"`
			URL       string `json:"url"`
			Revised   string `json:"revised_prompt"`
			MediaType string `json:"media_type"`
		} `json:"data"`
		OutputFormat string `json:"output_format"`
		Usage        struct {
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return drawn{}, 502, fmt.Errorf("%s's answer isn't an images API's: %v", p.Name, err)
	}
	input, output := res.Usage.Input, res.Usage.Output
	if input == 0 {
		input = res.Usage.Prompt
	}
	if output == 0 {
		output = res.Usage.Completion
	}
	out := drawn{Input: input, Output: output}
	for _, e := range res.Data {
		if e.Revised != "" {
			out.Revised = e.Revised
		}
		if e.B64 != "" {
			data, err := base64.StdEncoding.DecodeString(e.B64)
			if err != nil {
				return drawn{}, 502, fmt.Errorf("%s's image isn't base64: %v", p.Name, err)
			}
			mime := e.MediaType
			if mime == "" {
				mime = http.DetectContentType(data)
			}
			out.Images = append(out.Images, picture{Mime: mime, Data: data})
		} else if e.URL != "" {
			out.Images = append(out.Images, picture{URL: e.URL})
		}
	}
	if len(out.Images) == 0 {
		// an answer with no image says why, or is shown as it came
		return out, saidStatus(vendorMessage(b)), fmt.Errorf("%s answered with no image%s", provider.HostOf(url), vendorSaid(vendorMessage(b)))
	}
	return out, code, nil
}

// fetchPicture downloads an image an edit was given by URL, for an images
// API that takes only files. The URL is the caller's: it is fetched from
// public addresses only (publicClient), from wherever the request came,
// and what comes must be an image.
func (s *Server) fetchPicture(ctx context.Context, url string) ([]byte, string, error) {
	b, _, err := FetchImage(ctx, publicClient, url)
	if err != nil {
		return nil, "", fmt.Errorf("couldn't fetch the image at %s: %v", url, err)
	}
	mt := http.DetectContentType(b)
	if !strings.HasPrefix(mt, "image/") {
		return nil, "", fmt.Errorf("couldn't use the image at %s: it isn't an image (it reads as %s)", url, mt)
	}
	return b, mt, nil
}

// inline says a response_format asks for the image itself.
func inline(format string) bool { return strings.EqualFold(format, "b64_json") }

// ImageStall is how long an image being downloaded may send nothing before
// the download is given up, and ImageLongest how long it may take in all
// at the least: as long as drawTimeout where Settings gives drawing longer,
// as a vendor slow to draw can be as slow to hand the image over. A slow
// download goes on while it moves: a 4K PNG is 10 MB and more, and a cap
// on the whole of it (generate_image's was 2 minutes) lost one that was
// still coming (#1576).
var (
	ImageStall   = time.Minute
	ImageLongest = 5 * time.Minute
)

// imageLongest is how long a download may take in all: ImageLongest, or
// drawTimeout where that is longer.
func imageLongest() time.Duration { return max(ImageLongest, drawTimeout()) }

// maxImageBytes is the most of an image a download reads.
const maxImageBytes = 64 << 20

var errImageStalled = errors.New("the download stalled")

// FetchImage downloads the image at url with c: its bytes, and its type —
// what the bytes are, over what the server says. It gives up when nothing
// comes for ImageStall or it has taken imageLongest, and says which, and
// how much had come.
func FetchImage(ctx context.Context, c *http.Client, url string) ([]byte, string, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, "", fmt.Errorf("%q isn't an http(s) URL", url)
	}
	longest := imageLongest()
	ctx, cancel := context.WithTimeout(ctx, longest)
	defer cancel()
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	stall := time.AfterFunc(ImageStall, func() { stop(errImageStalled) })
	defer stall.Stop()
	got := 0
	why := func(err error) error {
		if ctx.Err() == nil {
			return err
		}
		switch context.Cause(ctx) {
		case errImageStalled:
			return fmt.Errorf("nothing came for %s, after %s", ImageStall, mb(got))
		case context.DeadlineExceeded:
			return fmt.Errorf("only %s came in %s", mb(got), longest)
		}
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, "", why(err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, "", errors.New(res.Status)
	}
	var buf bytes.Buffer
	chunk := make([]byte, 256<<10)
	for {
		stall.Reset(ImageStall)
		n, err := res.Body.Read(chunk)
		buf.Write(chunk[:n])
		if got = buf.Len(); got > maxImageBytes {
			return nil, "", fmt.Errorf("it is over %s", mb(maxImageBytes))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", why(err)
		}
	}
	b := buf.Bytes()
	mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if sniffed := http.DetectContentType(b); strings.HasPrefix(sniffed, "image/") || !strings.HasPrefix(mt, "image/") {
		mt = sniffed
	}
	return b, mt, nil
}

// mb is n bytes as people read it.
func mb(n int) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

func extOf(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/svg+xml":
		// Recraft's vector models at OpenRouter
		return ".svg"
	}
	return ".png"
}

// drawPrompt is the prompt with what an images API takes as parameters
// said in words, for a model asked in chat.
func drawPrompt(d drawing) string {
	var b strings.Builder
	b.WriteString(d.Prompt)
	if r := aspectOf(d.Size); r != "" {
		fmt.Fprintf(&b, "\n\nAspect ratio: %s.", r)
	}
	if d.Background == "transparent" {
		b.WriteString(" Transparent background.")
	}
	return b.String()
}

// drawChat asks a chat model to answer with an image, once for each image
// asked for.
func (s *Server) drawChat(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	content := []map[string]any{{"type": "text", "text": drawPrompt(d)}}
	for _, pic := range d.Images {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": pic.dataURL()}})
	}
	req := map[string]any{"model": model, "stream": false, "modalities": []string{"image", "text"},
		"messages": []map[string]any{{"role": "user", "content": content}}}
	if r := aspectOf(d.Size); r != "" {
		req["image_config"] = map[string]string{"aspect_ratio": r}
	}
	body, _ := json.Marshal(req)
	url := strings.TrimRight(p.Base(provider.Chat), "/") + "/chat/completions"
	return s.eachDrawing(d.N, func() (drawn, int, error) {
		b, code, err := s.send(ctx, p, url, "application/json", p.Prepare(body), true)
		if err != nil {
			return drawn{}, code, err
		}
		var res struct {
			Choices []struct {
				Message struct {
					Content json.RawMessage `json:"content"`
					Images  []struct {
						ImageURL struct {
							URL string `json:"url"`
						} `json:"image_url"`
					} `json:"images"`
					// AIHubMix's Gemini: the parts as Gemini gives them
					MultiMod []struct {
						InlineData struct {
							Data     string `json:"data"`
							MimeType string `json:"mime_type"`
						} `json:"inline_data"`
					} `json:"multi_mod_content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return drawn{}, 502, fmt.Errorf("%s's answer isn't chat completions': %v", p.Name, err)
		}
		out := drawn{Input: res.Usage.Prompt, Output: res.Usage.Completion}
		if len(res.Choices) == 0 {
			return out, code, nil
		}
		msg := res.Choices[0].Message
		var srcs []string
		for _, im := range msg.Images {
			srcs = append(srcs, im.ImageURL.URL)
		}
		for _, part := range msg.MultiMod {
			if in := part.InlineData; in.Data != "" {
				srcs = append(srcs, "data:"+cmp.Or(in.MimeType, "image/png")+";base64,"+in.Data)
			}
		}
		var text string
		if json.Unmarshal(msg.Content, &text) != nil {
			var parts []struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				ImageURL json.RawMessage `json:"image_url"`
			}
			json.Unmarshal(msg.Content, &parts)
			for _, part := range parts {
				text += part.Text
				srcs = append(srcs, imageSources(part.ImageURL)...)
			}
		}
		out.Text = strings.TrimSpace(text)
		// a model that puts the image in its text, as markdown
		if len(srcs) == 0 {
			for _, f := range strings.Split(text, "(data:image/")[1:] {
				if end := strings.IndexByte(f, ')'); end > 0 {
					srcs = append(srcs, "data:image/"+f[:end])
				}
			}
		}
		for _, src := range srcs {
			pic, err := pictureOf(src)
			if err != nil {
				return out, 502, fmt.Errorf("%s's image: %v", p.Name, err)
			}
			out.Images = append(out.Images, pic)
		}
		return out, code, nil
	})
}

// drawGemini asks Gemini at Google on its own API, which answers with
// images where its OpenAI-compatible one doesn't.
func (s *Server) drawGemini(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	parts := []map[string]any{{"text": drawPrompt(d)}}
	for _, pic := range d.Images {
		if pic.URL != "" {
			b, mt, err := s.fetchPicture(ctx, pic.URL)
			if err != nil {
				return drawn{}, 400, err
			}
			pic = picture{Mime: mt, Data: b}
		}
		parts = append(parts, map[string]any{"inlineData": map[string]string{"mimeType": pic.Mime, "data": base64.StdEncoding.EncodeToString(pic.Data)}})
	}
	cfg := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
	if c := imageConfigOf(model, d.Size); c != nil {
		cfg["imageConfig"] = c
	}
	body, _ := json.Marshal(map[string]any{"contents": []map[string]any{{"role": "user", "parts": parts}}, "generationConfig": cfg})
	base := strings.TrimSuffix(strings.TrimRight(p.Base(provider.Chat), "/"), "/openai")
	url := base + "/models/" + model + ":generateContent"
	return s.eachDrawing(d.N, func() (drawn, int, error) {
		b, code, err := s.send(ctx, p, url, "application/json", body, false)
		if err != nil {
			return drawn{}, code, err
		}
		var res struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text       string `json:"text"`
						Thought    bool   `json:"thought"`
						InlineData *struct {
							MimeType string `json:"mimeType"`
							Data     string `json:"data"`
						} `json:"inlineData"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			Usage struct {
				Prompt     int `json:"promptTokenCount"`
				Candidates int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return drawn{}, 502, fmt.Errorf("%s's answer isn't Gemini's: %v", p.Name, err)
		}
		out := drawn{Input: res.Usage.Prompt, Output: res.Usage.Candidates}
		for _, c := range res.Candidates {
			for _, part := range c.Content.Parts {
				switch {
				case part.InlineData != nil && !part.Thought:
					data, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
					if err != nil {
						return out, 502, fmt.Errorf("%s's image isn't base64: %v", p.Name, err)
					}
					out.Images = append(out.Images, picture{Mime: part.InlineData.MimeType, Data: data})
				case part.Text != "" && !part.Thought:
					out.Text += part.Text
				}
			}
		}
		out.Text = strings.TrimSpace(out.Text)
		return out, code, nil
	})
}

// pluginDrawsGemini is whether model is one p, a plugin, speaks Gemini's
// generateContent to (its npm @ai-sdk/google) and isn't an Imagen, which
// draws on :predict: such a model draws through the plugin's fetch as a
// Google sign-in's draws, on drawCodeAssist.
func pluginDrawsGemini(p provider.Provider, model string) bool {
	return p.IsPlugin() && !catalog.ImagesAPI(model) && slices.Equal(p.APIs(model), []provider.Protocol{provider.CodeAssist})
}

// drawCodeAssist asks an image model on a Google sign-in to draw, as it is
// asked to chat: on Code Assist, in the account's envelope, which asks for
// images beside text for an image model (buildCodeAssistSent); what it
// draws comes back as the pictures of its reply.
func (s *Server) drawCodeAssist(ctx context.Context, p provider.Provider, model string, d drawing) (drawn, int, error) {
	parts := []Part{{Kind: Text, Text: drawPrompt(d)}}
	for _, pic := range d.Images {
		if pic.URL != "" {
			b, mt, err := s.fetchPicture(ctx, pic.URL)
			if err != nil {
				return drawn{}, 400, err
			}
			pic = picture{Mime: mt, Data: b}
		}
		parts = append(parts, Part{Kind: Image, MediaType: pic.Mime, Data: base64.StdEncoding.EncodeToString(pic.Data)})
	}
	req := &Request{Messages: []Message{{Role: "user", Parts: parts}}, draws: true}
	if c := imageConfigOf(model, d.Size); c != nil {
		// the size and shape the images API asked, in the field a Gemini
		// client's own imageConfig arrives in, which buildCodeAssistSent
		// puts in generationConfig
		b, _ := json.Marshal(c)
		req.ImageConfig = b
	}
	return s.eachDrawing(d.N, func() (drawn, int, error) {
		// one each: the drawings are asked for at once
		ask := s.askTranslated(p, provider.CodeAssist, model, http.Header{}, http.Header{})
		events, code, msg := ask(ctx, req)
		if events == nil {
			return drawn{}, code, errors.New(msg)
		}
		var out drawn
		var failed string
		for ev := range events {
			switch ev.Kind {
			case KImage:
				data, err := base64.StdEncoding.DecodeString(ev.Text)
				if err != nil {
					failed = fmt.Sprintf("%s's image isn't base64: %v", p.Name, err)
					continue
				}
				out.Images = append(out.Images, picture{Mime: cmp.Or(ev.Name, "image/png"), Data: data})
			case KText:
				out.Text += ev.Text
			case KUsage:
				out.Input += ev.Usage.Input + ev.Usage.CacheRead
				out.Output += ev.Usage.Output
			case KError:
				failed = p.Name + ": " + ev.Text
			}
		}
		out.Text = strings.TrimSpace(out.Text)
		if failed != "" && len(out.Images) == 0 {
			return out, saidStatus(failed), errors.New(failed)
		}
		return out, 200, nil
	})
}

// eachDrawing asks once for each of n images, at once, for a vendor that
// draws one image an answer; what they drew and cost is summed.
func (s *Server) eachDrawing(n int, ask func() (drawn, int, error)) (drawn, int, error) {
	type result struct {
		out  drawn
		code int
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, c, e := ask()
			results[i] = result{o, c, e}
		}()
	}
	wg.Wait()
	var out drawn
	code := 200
	var firstErr error
	for _, r := range results {
		out.Input += r.out.Input
		out.Output += r.out.Output
		out.Images = append(out.Images, r.out.Images...)
		if r.out.Text != "" && out.Text == "" {
			out.Text = r.out.Text
		}
		if r.err != nil && firstErr == nil {
			firstErr, code = r.err, r.code
		}
	}
	// some images drawn is an answer; none, the first failure
	if len(out.Images) == 0 && firstErr != nil {
		return out, code, firstErr
	}
	return out, 200, nil
}

// aspectOf is the ratio an images API size ("1536x1024") is, as the models
// that take a ratio name it: the nearest of those they take. "" for none
// or "auto".
func aspectOf(size string) string {
	return aspectAmong(size, []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"})
}

// imageSizeOf is the Gemini imageSize bucket an images API size asks for:
// the smallest whose long side covers the size asked, and "" when there is
// nothing to ask. Only 2K and 4K go out: a size at or under the model's 1K
// default is drawn at anyway, and imageSize isn't every image model's
// (Vertex lists 1K, 2K, 4K), so the buckets under 1K aren't asked for what
// the default already gives. A ratio given as one ("16:9") says nothing in
// pixels, and is "" too.
func imageSizeOf(size string) string {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return ""
	}
	x, err1 := strconv.ParseFloat(w, 64)
	y, err2 := strconv.ParseFloat(h, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return ""
	}
	switch m := math.Max(x, y); {
	case m > 2048:
		return "4K"
	case m > 1024:
		return "2K"
	}
	return ""
}

// imageConfigOf is the imageConfig an images API size asks a Gemini image
// model to draw at: the ratio the size is nearest, with the imageSize
// beside it when the size asks past the model's 1K default. Gemini 2.x
// image models draw at 1K only and Google documents imageSize for the
// Gemini 3 image models, so a 2.x model is sent the ratio alone, as
// before. nil for none.
func imageConfigOf(model, size string) map[string]string {
	c := map[string]string{}
	if r := aspectOf(size); r != "" {
		c["aspectRatio"] = r
	}
	if s := imageSizeOf(size); s != "" && !strings.HasPrefix(strings.ToLower(model), "gemini-2") {
		c["imageSize"] = s
	}
	if len(c) == 0 {
		return nil
	}
	return c
}

// grokAspects are the aspect ratios Grok's Imagine API takes.
var grokAspects = []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "2:1", "1:2", "19.5:9", "9:19.5", "20:9", "9:20"}

// aspectAmong is size as the nearest of the aspect ratios in ratios: a
// ratio given as one passes through, WIDTHxHEIGHT is matched to the closest.
func aspectAmong(size string, ratios []string) string {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		if strings.Contains(size, ":") {
			return strings.TrimSpace(size)
		}
		return ""
	}
	x, err1 := strconv.ParseFloat(w, 64)
	y, err2 := strconv.ParseFloat(h, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return ""
	}
	best, bestD := "", math.Inf(1)
	for _, r := range ratios {
		a, b, _ := strings.Cut(r, ":")
		ra, _ := strconv.ParseFloat(a, 64)
		rb, _ := strconv.ParseFloat(b, 64)
		if d := math.Abs(math.Log(x/y) - math.Log(ra/rb)); d < bestD {
			best, bestD = r, d
		}
	}
	return best
}

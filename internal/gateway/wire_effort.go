package gateway

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// thinkingBackOn is req with the thinking off it asked for taken back, for
// a model that refused it: at the least the model has, as a request asking
// none was sent before thinking could be turned off (#1636).
func thinkingBackOn(p provider.Provider, model string, req *Request) *Request {
	r := *req
	r.ThinkOff = false
	if r.Effort == "" || offEffort(r.Effort) {
		r.Effort = onEffort(p, model)
	}
	return &r
}

// wireAt is where a translated request leaves the reasoning it was built
// at, for the log (wireEffortOf); set says it was.
type wireAt struct {
	effort string
	set    bool
	// tier: the service tier the try's request went out at, whichever
	// path sent it (noteWireTier)
	tier string
	// model: the model id the plugin the try went to said it sent its
	// vendor (noteWireModel)
	model string
	// gemini: the try asks a Gemini model, whose builder says the level in
	// its own fields, read from the body as it goes out (noteWireLevel)
	gemini bool
}

type wireAtKey struct{}

// withWireAt is ctx carrying w, which forwardTranslated fills.
func withWireAt(ctx context.Context, w *wireAt) context.Context {
	return context.WithValue(ctx, wireAtKey{}, w)
}

// noteWireEffort leaves, where the request's context has a place for it,
// the reasoning req goes to to at, model the id it was built for. Each
// build notes afresh, so a try built again (a field turned away) never
// keeps the level of the body before it.
func noteWireEffort(ctx context.Context, to provider.Protocol, req *Request, model string) {
	w, _ := ctx.Value(wireAtKey{}).(*wireAt)
	if w == nil {
		return
	}
	w.effort, w.set = wireEffort(to, req)
	w.gemini = req.GeminiCompat || geminiBound(to, model)
}

// noteRelayedGemini leaves, where the request's context has a place for
// it, that a request relayed on its own API to to goes to a Gemini model
// whose level the body says (geminiLevelBody refits it on an Anthropic or
// Chat relay), for noteWireLevel to read as it leaves.
func noteRelayedGemini(ctx context.Context, to provider.Protocol, model string) {
	if w, _ := ctx.Value(wireAtKey{}).(*wireAt); w != nil {
		w.gemini = geminiBound(to, model)
	}
}

// geminiBound says a try to to for model asks a Gemini model in fields of
// its own: Gemini's and Code Assist's APIs, and a Gemini 3 or later on an
// Anthropic or Chat relay.
func geminiBound(to provider.Protocol, model string) bool {
	return to == provider.Gemini || to == provider.CodeAssist ||
		(to == provider.Anthropic || to == provider.Chat) && geminiGeneration(strings.ToLower(model)) >= 3
}

// noteWireLevel leaves, where the request's context has a place for it,
// the thinking level the body of a try to a Gemini model names as it goes
// out (#7400). Gemini's builders say a level of their own — a Gemini 3
// Pro asked medium thinks at high, none and minimal at low, xhigh on
// Gemini 3's OpenAI-compatible API goes as high — and a Gemini on an
// Anthropic or Chat relay is fitted to its levels (geminiLevelBody); the
// log said the effort asked while another went. Read after the user's own
// fields are in (WithExtraBody), so what the log says is what left. A
// body naming no level (a budget, the model's own) leaves what was noted
// at build. A plugin is told the effort itself (provider.WithEffort), and
// its body is not what its vendor gets.
func noteWireLevel(ctx context.Context, p provider.Provider, to provider.Protocol, body []byte) {
	w, _ := ctx.Value(wireAtKey{}).(*wireAt)
	if w == nil || !w.gemini || p.IsPlugin() {
		return
	}
	if e, ok := geminiWireLevel(to, body); ok {
		w.effort, w.set = e, true
	}
}

// geminiWireLevel is the thinking level a body to a Gemini model names,
// where each API puts it, in the log's words: generateContent's
// generationConfig.thinkingConfig.thinkingLevel (Code Assist's under its
// request), the OpenAI-compatible API's extra_body thinking_config or
// reasoning_effort, an Anthropic relay's output_config.effort unless
// thinking is off. false when it names none of the levels.
func geminiWireLevel(to provider.Protocol, body []byte) (string, bool) {
	var v gjson.Result
	switch to {
	case provider.Gemini:
		v = topValue(body, "generationConfig").Get("thinkingConfig.thinkingLevel")
	case provider.CodeAssist:
		v = topValue(body, "request").Get("generationConfig.thinkingConfig.thinkingLevel")
	case provider.Chat:
		google := topValue(body, "extra_body").Get("google")
		tc := google.Get("thinking_config")
		if !tc.Exists() {
			tc = google.Get("thinkingConfig")
		}
		if v = tc.Get("thinking_level"); !v.Exists() {
			v = tc.Get("thinkingLevel")
		}
		if !v.Exists() && !tc.Exists() {
			v = topValue(body, "reasoning_effort")
		}
	case provider.Anthropic:
		if topValue(body, "thinking").Get("type").String() == "disabled" {
			return "", false
		}
		v = topValue(body, "output_config").Get("effort")
	}
	if v.Type != gjson.String {
		return "", false
	}
	e := strings.ToLower(strings.TrimSpace(v.String()))
	return e, slices.Contains(provider.Levels, e)
}

// wireEffort is the reasoning a request built from req asks the model for,
// in the words the log uses: what the client asked can go as another
// level once translated — effort none and minimal read as low where the
// model's levels don't say it can stop (effortOf), thinking turned off
// goes as off to Anthropic's API — and the log said the one asked while
// another was sent (#1636). false for an API whose builder says it its own
// way (Gemini's levels and budgets): the level is read from the body as it
// leaves (noteWireLevel), and a budget is left as asked.
func wireEffort(to provider.Protocol, req *Request) (string, bool) {
	switch {
	case req.GeminiCompat:
		return "", false
	case to == provider.Anthropic:
		if req.ThinkOff {
			return "none", true
		}
		return req.Effort, true
	case to == provider.Chat || to == provider.Responses:
		return req.Effort, true
	}
	return "", false
}

// noteWireModel leaves, where the request's context has a place for it, the
// model id a plugin's answer res says it sent its vendor
// (provider.SentModelHeader). Only a plugin is taken at its word: a vendor
// or a relay naming one is not what magpie sent.
func noteWireModel(ctx context.Context, p provider.Provider, res *http.Response) {
	if res == nil || !p.IsPlugin() {
		return
	}
	m := strings.TrimSpace(res.Header.Get(provider.SentModelHeader))
	res.Header.Del(provider.SentModelHeader)
	w, _ := ctx.Value(wireAtKey{}).(*wireAt)
	if w == nil || m == "" || len(m) > 200 {
		return
	}
	w.model = m
}

// wireName is the model id a try went out under, when it is another than
// model, the id magpie asked it for: what the plugin said it sent, else the
// name the user gave the model upstream (sentAs, provider.SentNameOnIn).
func wireName(model, plugin, sentAs string) string {
	if w := cmp.Or(plugin, sentAs); w != model {
		return w
	}
	return ""
}

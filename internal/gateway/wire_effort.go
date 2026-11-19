package gateway

import (
	"cmp"
	"context"
	"net/http"
	"strings"

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
}

type wireAtKey struct{}

// withWireAt is ctx carrying w, which forwardTranslated fills.
func withWireAt(ctx context.Context, w *wireAt) context.Context {
	return context.WithValue(ctx, wireAtKey{}, w)
}

// noteWireEffort leaves, where the request's context has a place for it,
// the reasoning req goes to to at.
func noteWireEffort(ctx context.Context, to provider.Protocol, req *Request) {
	w, _ := ctx.Value(wireAtKey{}).(*wireAt)
	if w == nil {
		return
	}
	if e, ok := wireEffort(to, req); ok {
		w.effort, w.set = e, true
	}
}

// wireEffort is the reasoning a request built from req asks the model for,
// in the words the log uses: what the client asked can go as another
// level once translated — effort none and minimal read as low where the
// model's levels don't say it can stop (effortOf), thinking turned off
// goes as off to Anthropic's API — and the log said the one asked while
// another was sent (#1636). false for an API whose builder says it its own
// way (Gemini's levels and budgets), which the log leaves as asked.
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

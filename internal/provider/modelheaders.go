package provider

// A model's own request headers (axelburks on Discord): a vendor that
// routes one model by a header of its own, beside the headers every model
// of the provider is sent. They are kept in settings' ModelHeaders by
// "<provider id>/<model id>", apart from the provider, so a Refresh, a
// restart or an upgrade leaves them, and go on that provider's requests
// for the model alone: after the provider's Headers, a name the provider
// has too, in any case, sent once, as the model's. Like the provider's,
// they ride on a key's provider only, after its auth, and the agent's own
// header the provider passes on (PassHeaders) still stands over them.
//
// Which model a request is for is said by the gateway (WithModel) on every
// route that asks one of the provider's models: chat on each API, its
// count_tokens, System One's decision, embeddings, images, videos and the
// editor's test of a model. A request that asks no model — a list, a
// balance — is sent the provider's alone.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

type modelCtxKey struct{}

type ctxModel struct{ pid, model string }

// WithModel is ctx carrying the model of provider pid its requests ask,
// for the headers the user gave that model, which Sign puts on pid's
// requests alone, never another provider's made under the same ctx.
func WithModel(ctx context.Context, pid, model string) context.Context {
	if pid == "" || model == "" {
		return ctx
	}
	return context.WithValue(ctx, modelCtxKey{}, ctxModel{pid, model})
}

// modelOf is the model ctx says p's requests ask, "" for none.
func (p Provider) modelOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if m, ok := ctx.Value(modelCtxKey{}).(ctxModel); ok && m.pid == p.ID {
		return m.model
	}
	return ""
}

// ModelHeaders are the headers the user gave provider pid's model.
func ModelHeaders(pid, model string) map[string]string {
	return settings.Load().ModelHeaders[pid+"/"+model]
}

// headersFor are p's Headers with model's after them: a name of the
// model's replaces the provider's of the same name in any case. p.Headers
// itself when the model has none.
func (p Provider) headersFor(model string) map[string]string {
	if model == "" || p.Account != nil {
		return p.Headers
	}
	own := ModelHeaders(p.ID, model)
	if len(own) == 0 {
		return p.Headers
	}
	out := make(map[string]string, len(p.Headers)+len(own))
	for k, v := range p.Headers {
		if !hasFold(own, k) {
			out[k] = v
		}
	}
	maps.Copy(out, own)
	return out
}

// hasFold says h has the header name in any case.
func hasFold(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// SetModelHeaders gives a provider's model, spelt "provider/model", the
// headers h, in place of those it had; none takes them away.
func SetModelHeaders(ref string, h map[string]string) error {
	_, err := setModelHeaders(ref, h)
	return err
}

// maxModelHeaders bounds the headers of one model, as a request carries.
const maxModelHeaders = 32

func setModelHeaders(ref string, h map[string]string) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	h = cleanModelHeaders(h)
	if len(h) > 0 {
		if p.Account != nil {
			return false, errors.New(p.Name + " is signed in: its requests carry its sign-in's headers, not a model's own")
		}
		if !p.serves(model) {
			return false, fmt.Errorf("%s has no model %s (magpie provider %s lists them)", p.ID, model, p.ID)
		}
		if len(h) > maxModelHeaders {
			return false, fmt.Errorf("a model has at most %d headers", maxModelHeaders)
		}
		seen := map[string]string{}
		for k, v := range h {
			if !validHeaderName(k) {
				return false, fmt.Errorf("%q isn't a header name", k)
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				return false, fmt.Errorf("the value of %s is one line", k)
			}
			if o, ok := seen[strings.ToLower(k)]; ok {
				return false, fmt.Errorf("%s and %s are one header", o, k)
			}
			seen[strings.ToLower(k)] = k
		}
	}
	s := settings.Load()
	key := p.ID + "/" + model
	if maps.Equal(s.ModelHeaders[key], h) {
		return false, nil
	}
	if len(h) == 0 {
		delete(s.ModelHeaders, key)
	} else {
		if s.ModelHeaders == nil {
			s.ModelHeaders = map[string]map[string]string{}
		}
		s.ModelHeaders[key] = h
	}
	if err := settings.Save(s); err != nil {
		return false, err
	}
	return true, nil
}

// cleanModelHeaders is h with names and values trimmed and rows with no
// name dropped, nil when none is left.
func cleanModelHeaders(h map[string]string) map[string]string {
	var out map[string]string
	for k, v := range h {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

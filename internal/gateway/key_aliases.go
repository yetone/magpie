package gateway

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/access"
)

// A gateway key may give models names of its own (access.Key.Aliases,
// tsingchao on X): /v1/models lists the name where the model was, and a
// request that asks by the name is the request for the model. The one
// place that happens is identifyCaller, before any route reads the model:
// the body's "model" of a JSON request, a Gemini call's model in its path,
// and /v1/models/<name>. Every route after it, and every check of the
// key's models, sees the real model, so the models the key may use hold
// the name as they hold the model. Replies say the real model.

// maxAliasBody is the largest JSON body read to find its model: a larger
// one goes on as it came, which asks the model by its real id only.
const maxAliasBody = 32 << 20

// aliasRequest is r with the model a key's name stands for in the model's
// place: r as it is when the key gives no names, or r asks none of them.
func aliasRequest(r *http.Request, who access.Identity) *http.Request {
	if len(who.Aliases) == 0 {
		return r
	}
	switch r.Method {
	case http.MethodGet:
		if id, ok := strings.CutPrefix(r.URL.Path, "/v1/models/"); ok {
			if real, ok := who.AliasFor(id); ok {
				return withPath(r, "/v1/models/"+real)
			}
		}
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		if call, ok := strings.CutPrefix(r.URL.Path, "/v1beta/models/"); ok {
			if i := strings.LastIndex(call, ":"); i > 0 {
				if real, ok := who.AliasFor(call[:i]); ok {
					return withPath(r, "/v1beta/models/"+real+call[i:])
				}
			}
			return r
		}
		return aliasBody(r, who)
	}
	return r
}

func withPath(r *http.Request, path string) *http.Request {
	u := url.URL{}
	if r.URL != nil {
		u = *r.URL
	}
	u.Path, u.RawPath = path, ""
	r = r.Clone(r.Context())
	r.URL = &u
	return r
}

// aliasBody swaps the model of a JSON body, every other byte as it was.
func aliasBody(r *http.Request, who access.Identity) *http.Request {
	if r.Body == nil || r.Body == http.NoBody || r.Header.Get("Content-Encoding") != "" {
		return r
	}
	// a form or a multipart body is no JSON; a client that says no type
	// sends it
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "" && !strings.HasSuffix(mt, "json") {
		return r
	}
	orig := r.Body
	buf, err := io.ReadAll(io.LimitReader(orig, maxAliasBody+1))
	rest := io.MultiReader(bytes.NewReader(buf), orig)
	r.Body = struct {
		io.Reader
		io.Closer
	}{rest, orig}
	if err != nil || len(buf) > maxAliasBody {
		return r
	}
	real, ok := who.AliasFor(modelOf(buf))
	if !ok {
		return r
	}
	buf = rewriteModel(buf, real)
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	r.Header = r.Header.Clone()
	r.Header.Set("Content-Length", strconv.Itoa(len(buf)))
	r.GetBody = nil
	return r
}

// aliasNames puts the names a key gives models where the models were in a
// list /v1/models gives it.
func aliasNames(r *http.Request, data []map[string]any) {
	who := access.Caller(r.Context())
	if len(who.Aliases) == 0 {
		return
	}
	for _, m := range data {
		if id, _ := m["id"].(string); id != "" {
			if name, ok := who.AliasOf(id); ok {
				m["id"] = name
			}
		}
	}
}

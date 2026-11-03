package provider

// Headers of the agent's request passed on to the vendor (PassHeaders,
// 3jRzfF52Qx on X): a value that changes from request to request or from
// client to client, which a provider's fixed Headers can't say, goes as the
// agent sent it. The mirror of ReplyHeaders (replyheaders.go).

import (
	"context"
	"net/http"
	"slices"
	"strings"
)

// requestHeadersNever are never passed on, listed or not: magpie's own key
// as an agent sends it (Authorization, x-api-key, x-goog-api-key, api-key)
// and the sign-ins and cookies beside it, the connection's own fields, and
// what magpie writes for the body it sends (a translated body has another
// type, length and encoding, and the transport asks for compression
// itself). The prefixes below go with them.
var requestHeadersNever = map[string]bool{
	"Authorization": true, "X-Api-Key": true, "X-Goog-Api-Key": true, "Api-Key": true,
	"Cookie": true, "Set-Cookie": true, "Set-Cookie2": true, "Www-Authenticate": true, "Authentication-Info": true,
	"Host": true, "Connection": true, "Keep-Alive": true, "Te": true, "Trailer": true, "Transfer-Encoding": true,
	"Upgrade": true, "Expect": true, "Accept": true, "Accept-Encoding": true, "Origin": true,
}

// requestHeadersNeverPrefix: Content-Length, -Type and the rest;
// Proxy-Authorization and the rest; Sec- fetch metadata; and magpie's own,
// which carry who asked and the conversation to a plugin or another
// magpie.
var requestHeadersNeverPrefix = []string{"Content-", "Proxy-", "Sec-", "X-Magpie-"}

// RequestHeaderPassable says whether a header of the agent's request may be
// passed on to the vendor at all.
func RequestHeaderPassable(name string) bool {
	k := http.CanonicalHeaderKey(strings.TrimSpace(name))
	if !validHeaderName(k) || requestHeadersNever[k] {
		return false
	}
	for _, p := range requestHeadersNeverPrefix {
		if strings.HasPrefix(k, p) {
			return false
		}
	}
	return true
}

// RequestHeadersOK says what is wrong with a list of request headers to
// pass on, "" when nothing is.
func RequestHeadersOK(names []string) string {
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !validHeaderName(n) {
			return "\"" + n + "\" isn't a header name"
		}
		if !RequestHeaderPassable(n) {
			return n + " is never passed on: it carries a key, or magpie or the connection writes it"
		}
	}
	return ""
}

// cleanRequestHeaders is the list as saved: names trimmed and canonical,
// each once, what can't be passed on dropped; nil when nothing is left.
func cleanRequestHeaders(names []string) []string {
	var out []string
	for _, n := range names {
		k := http.CanonicalHeaderKey(strings.TrimSpace(n))
		if RequestHeaderPassable(k) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// RequestHeadersNever are the names and prefixes RequestHeaderPassable
// refuses, for the GUI's own check of a list typed.
func RequestHeadersNever() (names, prefixes []string) {
	for k := range requestHeadersNever {
		names = append(names, k)
	}
	slices.Sort(names)
	return names, slices.Clone(requestHeadersNeverPrefix)
}

type agentHeadersKey struct{}

type agentHeaders struct {
	id string
	h  http.Header
}

// WithClientHeaders is ctx carrying h, the headers of the agent's request,
// for Sign to pass on to the provider id those it lists (PassHeaders):
// that provider's request alone, never another's made under the same ctx.
func WithClientHeaders(ctx context.Context, id string, h http.Header) context.Context {
	if len(h) == 0 {
		return ctx
	}
	return context.WithValue(ctx, agentHeadersKey{}, agentHeaders{id: id, h: h})
}

// passClientHeaders puts into h the headers of the agent's request ctx
// carries that p lists, as sent, in place of any value h has under the
// name in any case. It gives the names it put.
func (p Provider) passClientHeaders(ctx context.Context, h http.Header) []string {
	if len(p.PassHeaders) == 0 {
		return nil
	}
	c, ok := ctx.Value(agentHeadersKey{}).(agentHeaders)
	if !ok || c.id != p.ID {
		return nil
	}
	var put []string
	for _, n := range p.PassHeaders {
		k := http.CanonicalHeaderKey(strings.TrimSpace(n))
		if slices.Contains(put, k) || !RequestHeaderPassable(k) {
			continue
		}
		vs := valuesFold(c.h, k)
		if len(vs) == 0 {
			continue
		}
		for hk := range h {
			if strings.EqualFold(hk, k) {
				delete(h, hk)
			}
		}
		h[k] = slices.Clone(vs)
		put = append(put, k)
	}
	return put
}

// valuesFold is h's values under k, whatever the case of h's keys (a
// header map filled by hand).
func valuesFold(h http.Header, k string) []string {
	if vs := h.Values(k); len(vs) > 0 {
		return vs
	}
	for hk, hv := range h {
		if strings.EqualFold(hk, k) {
			return hv
		}
	}
	return nil
}

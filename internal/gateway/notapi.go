package gateway

import (
	"io"
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// notAnAPIReply turns a 2xx reply that can't be a model API's answer into
// the 502 it stands for (provider.NotAnAPIReply, which a provider's test
// reads its answer through too): a web page, an empty body, or one sent
// as JSON that doesn't begin as JSON (#1012). name prefixes the reason,
// for a caller that doesn't put the provider's name before it.
func notAnAPIReply(res *http.Response, name string) *http.Response {
	return provider.NotAnAPIReply(res, name)
}

// eventsStart tells whether a body begins as server-sent events.
func eventsStart(head []byte) bool { return provider.EventsStart(head) }

// htmlStart is how a web page begins.
var htmlStart = provider.HTMLStart

// asBadGateway is res as the 502 it stands for, saying why in an error
// body every reader of one finds its message in.
func asBadGateway(res *http.Response, name, why string) *http.Response {
	return provider.AsBadGateway(res, name, why)
}

// readCloser reads through a buffer in front of a body, and closes the
// body.
type readCloser struct {
	io.Reader
	io.Closer
}

// clipRunes is s cut to at most n runes.
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

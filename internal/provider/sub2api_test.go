package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sub2apiNoDispatch is sub2api's 403 to /v1/messages for a key whose group
// isn't let dispatch Messages, as its handler writes it: every Claude Code
// request of a Sidrune AI China-region key (Axo, 11-20).
const sub2apiNoDispatch = `{"type":"error","error":{"type":"permission_error","message":"This group does not allow /v1/messages dispatch"}}`

// A key served nothing on Messages tests as that, not as a failure of the
// key: its models answer on Chat.
func TestTestGroupWithoutMessages(t *testing.T) {
	sandboxHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = rw.Write([]byte(`{"object":"list","data":[{"id":"claude-sonnet-4-5","object":"model"}]}`))
		case r.URL.Path == "/v1/messages":
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusForbidden)
			_, _ = rw.Write([]byte(sub2apiNoDispatch))
		default:
			_, _ = rw.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	p := Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL, Key: "sk-1"}
	got := map[Protocol]Result{}
	for _, r := range p.Test(context.Background()) {
		got[r.Protocol] = r
	}
	if !got[Chat].OK || got[Chat].Unserved {
		t.Errorf("chat: %+v", got[Chat])
	}
	if a := got[Anthropic]; a.OK || !a.Unserved || a.Status != http.StatusForbidden {
		t.Errorf("anthropic: %+v, want unserved", a)
	}
}

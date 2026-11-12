package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// codexDrawsOnPool draws Codex's own images call (its image tool, sent to
// the ChatGPT backend's /images/generations and /images/edits) on the
// accounts the pool goes over, as the images API does, when magpie has
// more than the sign-in to ask. Relayed as it came, the call spent the
// account Codex is signed in to whatever the pool's order said, and with
// that account's image allowance used up, no image was drawn while the
// text turns went to accounts with room (Richyisaflower on X). Served here
// it goes to the first account that draws, past the ones whose allowance
// is spent (drawOnAccounts), signed with that account's own token and id.
// False leaves the call to the relay: an API key, a request signed by an
// account magpie doesn't have, one named account, or a sign-in alone.
func (s *Server) codexDrawsOnPool(w http.ResponseWriter, r *http.Request, rest string, body []byte) bool {
	if apiKey(r.Header) || r.Header.Get(AccountHeader) != "" {
		return false
	}
	if acct := r.Header.Get("chatgpt-account-id"); acct != "" && acct != provider.CodexSignedInAccount() {
		return false
	}
	p, err := provider.Find("codex")
	if err != nil || p.Account == nil || p.Account.Agent != "codex" || len(p.AlsoOn()) == 0 {
		return false
	}
	model := modelOf(body)
	if model == "" {
		model = "gpt-image-2"
	}
	if strings.Contains(model, "/") || !strings.Contains(r.Header.Get("Content-Type"), "json") {
		return false
	}
	body = withModel(body, "codex/"+model)
	r2 := r.Clone(r.Context())
	r2.URL.Path, r2.URL.RawPath = "/v1"+rest, ""
	r2.RequestURI = ""
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	r2.Header.Del("Content-Encoding")
	r2.Header.Del("Content-Length")
	r2.Header.Set("Content-Type", "application/json")
	s.images(rest == "/images/edits")(w, r2)
	return true
}

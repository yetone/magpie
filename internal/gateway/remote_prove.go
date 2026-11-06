package gateway

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A magpie looking for its Remote magpie at a new address (provider's
// remote_moved.go) asks each address that takes the connection to prove,
// before anything keyed is sent there, that it holds the provider's
// gateway key: it POSTs a nonce of its own and the key's sha256
// (provider.RemoteKeyID) to provider.RemoteProvePath, with no key. This
// gateway answers the HMAC of the nonce and the address it was reached at
// (provider.RemoteProof) under its enabled gateway key of that id, or 404
// when it has none.
//
// The route carries no key, so lanGuard lets it through to here
// (proveAsked) the way it does a CI job's OIDC trade: from another
// machine only while magpie is shared, past the allow list. A web page is
// refused (fromBrowser): only a magpie asks this. An ask for a key this
// gateway doesn't have counts against the failed authentications another
// machine may make (tooManyAuthFails), as a wrong key does, and costs one
// sha256 per key. The answer tells a caller nothing it didn't know: only
// one that already has the key's sha256 gets a proof, and the proof is
// for a nonce and an address of the asker's choosing, under a key it
// doesn't have.

// proveAsked: r is a magpie's ask for a proof, which carries no key.
func proveAsked(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == provider.RemoteProvePath && r.URL.RawPath == "" && !managedKey(r)
}

// isHex: s is hex of between least and most characters.
func isHex(s string, least, most int) bool {
	if len(s) < least || len(s) > most {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (s *Server) prove(w http.ResponseWriter, r *http.Request) {
	if fromBrowser(r) {
		log.Printf("refused %s %s from %s: a browser asked for a proof", r.Method, r.URL.Path, r.RemoteAddr)
		writeError(w, provider.Chat, http.StatusForbidden, "only a magpie asks this")
		return
	}
	var in struct {
		Nonce string `json:"nonce"`
		KeyID string `json:"key_id"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if json.Unmarshal(b, &in) != nil || !isHex(in.Nonce, 32, 128) || !isHex(in.KeyID, 64, 64) {
		writeError(w, provider.Chat, http.StatusBadRequest, `send {"nonce": hex, "key_id": hex sha256 of the key}`)
		return
	}
	at, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	key, ok := access.ProveSecret(in.KeyID)
	if !ok || at == nil {
		if !local(r) {
			if over, retry := tooManyAuthFails(); over {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				writeError(w, provider.Chat, http.StatusTooManyRequests, "too many failed authentications; slow down")
				return
			}
		}
		writeError(w, provider.Chat, http.StatusNotFound, "no enabled gateway key here has that id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"proof": hex.EncodeToString(provider.RemoteProof(key, in.Nonce, at.String()))})
}

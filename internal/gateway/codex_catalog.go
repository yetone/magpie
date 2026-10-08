package gateway

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
)

// CodexCatalogPath is Codex's model list in its own shape, for a Codex that
// names magpie its model_provider (base_url <gateway>/v1) on another
// computer, with a gateway key (#1281): its model_catalog_url. /v1/models
// stays OpenAI's list, which Codex can't read, and CodexPath's list is the
// ChatGPT backend's, for a Codex signed in to ChatGPT here.
const CodexCatalogPath = "/v1/codex/models"

// codexCatalogMost is the most of a catalog Codex reads from a
// model_catalog_url (MAX_MODEL_CATALOG_BYTES in codex-rs's models
// endpoint): past it the whole list fails to load and Codex's picker has
// none of magpie's models.
const codexCatalogMost = 1 << 20

// codexCatalog answers CodexCatalogPath: the models magpie shows Codex, as
// the model_catalog_json magpie writes for a Codex here describes them,
// kept to the models a gateway key may use (keyAllowed, as /v1/models is).
// Codex's ?client_version= changes nothing. Entries that would take the
// answer past codexCatalogMost are left out, the last first, and the
// answer says how many in X-Magpie-Left-Out.
func (s *Server) codexCatalog(w http.ResponseWriter, r *http.Request) {
	// the catalog, the groups and the windows read the providers over and
	// over: held, they are built once (#746)
	defer provider.Hold()()
	shown, _ := provider.CatalogFor("codex")
	ms := provider.CodexCatalog(keyAllowed(r, shown))
	head, tail := []byte(`{"models":[`), []byte("]}\n")
	var b bytes.Buffer
	b.Write(head)
	n, out := 0, 0
	for _, e := range codexcat.Entries(ms, 0) {
		var one bytes.Buffer
		enc := json.NewEncoder(&one)
		enc.SetEscapeHTML(false)
		if enc.Encode(e) != nil {
			continue
		}
		raw := bytes.TrimRight(one.Bytes(), "\n")
		if b.Len()+1+len(raw)+len(tail) > codexCatalogMost {
			out++
			continue
		}
		if n > 0 {
			b.WriteByte(',')
		}
		b.Write(raw)
		n++
	}
	b.Write(tail)
	if out > 0 {
		log.Printf("%s: %d of Codex's %d models left out, past the %d bytes Codex reads of a catalog; pick fewer for Codex on the Agents page, or hold the key to fewer", CodexCatalogPath, out, n+out, codexCatalogMost)
		w.Header().Set("X-Magpie-Left-Out", strconv.Itoa(out))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(b.Bytes())
}

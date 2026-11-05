package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
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
// none of magpie's models. Codex 0.166 reads it whole (models_endpoint.rs
// passes no response_body_limit_bytes), see codexReadsWholeCatalog.
const codexCatalogMost = 1 << 20

// codexReadsWholeCatalog says whether the Codex asking, by the
// ?client_version= it sends with a model_catalog_url, reads the catalog
// whatever its size: 0.166 and later. Every entry carries the instructions
// official Codex sends its model (~21 KB for GPT-6's and for a model Codex
// doesn't know), so 1 MiB holds about 48 of them; a Codex that doesn't say
// its version is held to it.
func codexReadsWholeCatalog(v string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil {
		return false
	}
	return major > 0 || minor >= 166
}

// codexCatalog answers CodexCatalogPath: the models magpie shows Codex, as
// the model_catalog_json magpie writes for a Codex here describes them,
// kept to the models a gateway key may use (keyAllowed, as /v1/models is).
// For a Codex before 0.166 (by its ?client_version=, codexReadsWholeCatalog)
// entries that would take the answer past codexCatalogMost are left out,
// the last first, and the answer says how many in X-Magpie-Left-Out.
func (s *Server) codexCatalog(w http.ResponseWriter, r *http.Request) {
	// the catalog, the groups and the windows read the providers over and
	// over: held, they are built once (#746)
	defer provider.Hold()()
	shown, _ := provider.CatalogFor("codex")
	ms := provider.CodexCatalog(keyListed(r, shown, provider.ModelOff("codex")))
	head, tail := []byte(`{"models":[`), []byte("]}\n")
	var b bytes.Buffer
	b.Write(head)
	n, out := 0, 0
	most := codexCatalogMost
	if codexReadsWholeCatalog(r.URL.Query().Get("client_version")) {
		most = math.MaxInt
	}
	// the Codex app reads the first 100 alone (codexcat.AppList): a ChatGPT
	// account's own models and the ones this key's Codex used are among
	// them, and kept when the end is cut
	entries := codexcat.Entries(ms, 0)
	codexcat.FitOwn(entries, usage.CodexUsedBy(access.Caller(r.Context()).KeyID), codexcat.ChatGPTOwn)
	for _, e := range entries {
		var one bytes.Buffer
		enc := json.NewEncoder(&one)
		enc.SetEscapeHTML(false)
		if enc.Encode(e) != nil {
			continue
		}
		raw := bytes.TrimRight(one.Bytes(), "\n")
		if b.Len()+1+len(raw)+len(tail) > most {
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

// codexTiers are the service tiers Codex here is offered on each of shown,
// by id: Fast on a ChatGPT account's GPT model, the tiers of Codex's own
// entry on a relay's. Another magpie that has this one as its provider
// offers its Codex the same, and sends the tier asked on (#1234).
func codexTiers(shown []provider.Entry) map[string][]any {
	out := map[string][]any{}
	for id, ts := range codexcat.ServiceTiers(provider.CodexCatalog(shown)) {
		for _, t := range ts {
			out[id] = append(out[id], t)
		}
	}
	return out
}

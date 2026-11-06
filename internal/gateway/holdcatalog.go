package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
)

// holdCatalog has a request hold the catalog it built first, and the files it
// read, for every look-up it makes (provider.Hold, as the GUI's /api
// does): without it each Resolve built the providers anew, reading every
// agent's sign-in, every plugin's sign-ins and the settings again, three
// builds a request, and seven agents at once kept magpie at 100–350% CPU
// (#2502). Requests at once share one build a second at most; a write
// magpie makes drops it at once (provider.Changed, plugin.OnChangeNow),
// and a file another program writes is seen within the second.
func holdCatalog(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// a WebSocket lasts a session: each of its turns holds it
		// instead, as each look at the list does for another magpie's
		// wait for its change (modelsOnceChanged)
		if wantsSocket(r) || waitsForList(r) {
			h.ServeHTTP(w, r)
			return
		}
		defer provider.Hold()()
		defer filememo.Hold()()
		h.ServeHTTP(w, r)
	})
}

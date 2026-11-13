package gui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/provider"
)

// An import link (magpie://import?…, see provider.ParseImport) is parsed
// here, parked under a random id, and the window opens on the Providers tab
// with that id; the page fetches it once and asks the user before saving
// anything.

type importJSON struct {
	Provider provider.Provider `json:"provider"`
	Error    string            `json:"error,omitempty"`
	Replaces string            `json:"replaces,omitempty"` // the name of the provider it would replace
}

var imports = struct {
	sync.Mutex
	m map[string]importJSON
}{m: map[string]importJSON{}}

func stashImport(link string) string {
	var in importJSON
	p, err := provider.ParseImport(link)
	if err != nil {
		in.Error = err.Error()
	} else {
		in.Provider = p
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	imports.Lock()
	imports.m[id] = in
	imports.Unlock()
	return id
}

// ImportLink is the link among a launch's arguments, if any.
func ImportLink(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(strings.ToLower(a), provider.Scheme+":") {
			return a
		}
	}
	return ""
}

func importRoutes(mux *http.ServeMux) {
	// Read once: the key is in there, and a reload should not ask again.
	mux.HandleFunc("GET /api/import/{id}", func(rw http.ResponseWriter, r *http.Request) {
		imports.Lock()
		in, ok := imports.m[r.PathValue("id")]
		delete(imports.m, r.PathValue("id"))
		imports.Unlock()
		if !ok {
			http.NotFound(rw, r)
			return
		}
		if in.Error == "" {
			if old, err := provider.Find(in.Provider.ID); err == nil {
				in.Replaces = old.Name
			}
		}
		writeJSON(rw, in)
	})
	// fetch the picture a link named, once the user has the dialog open. The
	// URL is validated again in provider.FetchIcon: this handler only ever
	// fetches into the icons folder and answers with the reference.
	mux.HandleFunc("POST /api/import/icon", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ URL string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		icon, err := provider.FetchIcon(r.Context(), in.URL)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"icon": icon})
	})
}

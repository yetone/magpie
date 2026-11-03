package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/browserext"
	"github.com/yetone/magpie/internal/gateway"
)

// Magpie Browser's extension, as the Library page sets it up: written to a
// folder the user can see, in their home folder, which they drop on their
// own browser's chrome://extensions (or choose with Load unpacked), and
// connected or not as the gateway says.

// the folders it is written to (browserext.Dir, and OldDir while a
// browser still loads it from there)
func browserExtDir() string    { return browserext.Dir() }
func browserExtOldDir() string { return browserext.OldDir() }

type browserJSON struct {
	Dir       string `json:"dir"`
	Written   bool   `json:"written"`
	Version   string `json:"version"`
	Connected bool   `json:"connected"`
	Extension string `json:"extension,omitempty"` // the connected one's version
	Browser   string `json:"browser,omitempty"`
	// Drag: the page may drag the folder out of the window itself
	Drag bool `json:"drag,omitempty"`
}

func browserState(ctx context.Context) browserJSON {
	out := browserJSON{Dir: browserExtDir(), Version: browserext.Version(), Drag: canDragFile}
	for _, dir := range []string{out.Dir, browserExtOldDir()} {
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
			out.Written = out.Written || dir == out.Dir
			// kept as this magpie has it, and pointed at its gateway: the
			// browser reads the folder again when it starts
			browserext.Write(dir, gateway.URL())
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateway.URL()+gateway.BrowserStatusPath, nil)
	if err != nil {
		return out
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var st struct {
		Connected bool   `json:"connected"`
		Extension string `json:"extension"`
		Browser   string `json:"browser"`
	}
	if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&st) == nil {
		out.Connected, out.Extension, out.Browser = st.Connected, st.Extension, st.Browser
	}
	return out
}

func browserRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/library/browser", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, browserState(r.Context()))
	})
	// Set up: the extension written, and its folder shown selected, to
	// drag onto the browser's extensions page or choose with Load unpacked
	mux.HandleFunc("POST /api/library/browser/setup", func(rw http.ResponseWriter, r *http.Request) {
		dir := browserExtDir()
		if err := browserext.Write(dir, gateway.URL()); err != nil {
			fail(rw, err)
			return
		}
		st := browserState(r.Context())
		var reveal string
		if err := revealIn(w, dir); err != nil {
			reveal = err.Error()
		}
		writeJSON(rw, struct {
			browserJSON
			RevealError string `json:"revealError,omitempty"`
		}{st, reveal})
	})
	// the folder dragged out of the window, as the page's mouse drags it
	mux.HandleFunc("POST /api/library/browser/drag", func(rw http.ResponseWriter, r *http.Request) {
		dir := browserExtDir()
		if err := browserext.Write(dir, gateway.URL()); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"dragging": dragFile(dir)})
	})
}

// revealIn shows path selected in its parent folder where the host can,
// and opens it where it can't.
func revealIn(w Windows, path string) error {
	if r, ok := w.(interface{ RevealFolder(string) error }); ok {
		return r.RevealFolder(path)
	}
	return w.OpenFolder(path)
}

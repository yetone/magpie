package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/access"
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
	// Away: the page is looked at from another computer than magpie's, or
	// magpie runs in a container, so the folder magpie writes is out of
	// the browser's reach; the extension is downloaded instead, with a
	// gateway key of its own (browserKey)
	Away bool `json:"away,omitempty"`
	// Outdated: the connected extension is older than Version. Its folder
	// has this magpie's, but the browser runs what it loaded until the
	// extension is reloaded, so the sheet says to reload it
	Outdated bool `json:"outdated,omitempty"`
}

// browserAway is whether r's page is in a browser magpie's folders are out
// of reach of (browserJSON's Away).
func browserAway(r *http.Request) bool {
	return gateway.InContainer() || !loopbackPeer(r.RemoteAddr)
}

// browserKeyName names the gateway key a downloaded extension connects with.
const browserKeyName = "Magpie Browser"

// browserKey is the secret of the gateway key a downloaded extension
// connects with: the one recorded as the user's own browser's
// (gateway.OwnBrowserKey) while it is on, else a new one, recorded. It is
// a browser key (access.Key.Browser), which connects the extension and
// nothing else: the zip it is written into lands in Downloads, and a copy
// of it must not reach the user's models. One a magpie from before made
// with full access is held to that first (access.ScopeBrowser). The
// browser it connects is used by that key's callers, and by this
// computer's when it connects from this computer (gateway/browser.go),
// never by another key's.
func browserKey() (string, error) {
	if id := gateway.OwnBrowserKey(); id != "" && access.On(id) && access.ScopeBrowser(id) == nil {
		if secret, err := access.Update("copy-key", access.Change{Key: id}); err == nil && secret != "" {
			return secret, nil
		}
	}
	secret, err := access.Update("add-key", access.Change{Name: browserKeyName, Browser: true})
	if err != nil {
		return "", err
	}
	who, ok := access.Authenticate(secret)
	if !ok {
		return "", errors.New("the key made for the extension isn't on")
	}
	// this computer's agents use the browser that connects with it from
	// this computer, as they do one on loopback
	if err := gateway.SetOwnBrowserKey(who.KeyID); err != nil {
		return "", err
	}
	return secret, nil
}

// browserAt is the gateway address a downloaded extension dials: the
// page's own (at, its origin, whose port hands the gateway's routes on,
// onePort), else MAGPIE_PUBLIC_URL, else the gateway's.
func browserAt(at string) (string, error) {
	if at == "" {
		if u := gateway.PublicURL(); u != "" {
			return u, nil
		}
		return gateway.URL(), nil
	}
	u, err := url.Parse(at)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return "", errors.New("the extension's gateway is an http or https origin, such as http://localhost:3430")
	}
	return u.Scheme + "://" + u.Host, nil
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
		out.Outdated = st.Connected && st.Extension != "" && browserext.Older(st.Extension, out.Version)
	}
	return out
}

func browserRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/library/browser", func(rw http.ResponseWriter, r *http.Request) {
		st := browserState(r.Context())
		st.Away = browserAway(r)
		writeJSON(rw, st)
	})
	// the extension as a zip, for a browser magpie's folders are out of
	// reach of (browserJSON's Away): pointed at the page's own address,
	// and given a gateway key of its own
	mux.HandleFunc("POST /api/library/browser/extension", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			At string `json:"at"`
		}
		if r.Body != nil {
			json.NewDecoder(http.MaxBytesReader(rw, r.Body, 4<<10)).Decode(&in)
		}
		at, err := browserAt(in.At)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		key, err := browserKey()
		if err != nil {
			fail(rw, err)
			return
		}
		b, err := browserext.Zip(at, key)
		if err != nil {
			fail(rw, err)
			return
		}
		rw.Header().Set("Content-Type", "application/zip")
		rw.Header().Set("Content-Disposition", `attachment; filename="`+browserext.Folder+`.zip"`)
		rw.Header().Set("Cache-Control", "no-store")
		rw.Write(b)
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

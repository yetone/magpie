//go:build dev

package gui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// Development build (`make dev`): the UI is read from internal/gui/assets on
// every request, and the page reloads itself when a file there changes.
//
// `make dev` also splits the app in two, so a Go change doesn't close the
// windows: a shell (MAGPIE_DEV_ROLE=shell) has the windows and the tray and
// passes every request on to a backend (MAGPIE_DEV_ROLE=backend) with the
// API and the gateway. A Go change rebuilds and restarts only the backend;
// the page, told so, refreshes what it shows in place. What the backend asks
// of the windows goes back to the shell's control address.

func assetsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "assets")
}

func staticFS() fs.FS { return os.DirFS(assetsDir()) }

// staticFixed: the UI's files are read from disk, and change under it.
const staticFixed = false

// stamp is the newest modification time under assets, as a string.
func stamp() string {
	var newest time.Time
	filepath.WalkDir(assetsDir(), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		return nil
	})
	return strconv.FormatInt(newest.UnixNano(), 10)
}

// boot tells this process from the one before it.
var boot = strconv.FormatInt(time.Now().UnixNano(), 36)

// A new stamp reloads the page; a new boot (a restarted backend) only
// refreshes what it shows, as coming back to the window does, so the tab
// and anything open stay put.
const reloadJS = `(async () => {
  let seen = "", boot = "";
  for (;;) {
    try {
      const r = await fetch("/api/dev/wait?since=" + seen + "&boot=" + boot, { cache: "no-store" });
      const j = await r.json();
      if (seen && j.stamp !== seen) { location.reload(); return; }
      if (boot && j.boot !== boot && typeof load === "function") load();
      seen = j.stamp; boot = j.boot;
    } catch { await new Promise(r => setTimeout(r, 1000)); }
  }
})();`

func devRoutes(mux *http.ServeMux) {
	// long-poll: answers as soon as the assets change, or after 20s, or at
	// once from a backend the page hasn't seen
	mux.HandleFunc("GET /api/dev/wait", func(rw http.ResponseWriter, r *http.Request) {
		since, was := r.URL.Query().Get("since"), r.URL.Query().Get("boot")
		s := stamp()
		for i := 0; i < 50 && since != "" && s == since && (was == "" || was == boot); i++ {
			time.Sleep(400 * time.Millisecond)
			s = stamp()
		}
		writeJSON(rw, map[string]string{"stamp": s, "boot": boot})
	})
	// the shell's import links, stashed where the page will ask for them
	mux.HandleFunc("POST /api/dev/import", func(rw http.ResponseWriter, r *http.Request) {
		io.WriteString(rw, stashImport(r.FormValue("link")))
	})
	mux.HandleFunc("GET /dev.js", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/javascript")
		rw.Write([]byte(reloadJS))
	})
}

// devPage injects the reload script into index.html and turns off caching.
func devPage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			next.ServeHTTP(rw, r)
			return
		}
		b, err := os.ReadFile(filepath.Join(assetsDir(), "index.html"))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		b = bytes.Replace(b, []byte("</body>"), []byte(`<script src="/dev.js"></script></body>`), 1)
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.Write(b)
	})
}

// devListen also serves the whole UI over plain HTTP when MAGPIE_DEV_UI names
// an address, so it can be opened in a browser with real devtools. The
// window actions become no-ops there; everything else is the real thing.
func devListen(h http.Handler) {
	addr := os.Getenv("MAGPIE_DEV_UI")
	if addr == "" {
		return
	}
	ln, err := gateway.Listen(addr)
	if err != nil {
		log.Println("dev ui:", err)
		return
	}
	log.Println("dev ui:", "http://"+addr+"/?theme=dark")
	devUI = &http.Server{Handler: h}
	go devUI.Serve(ln)
}

// devUI is the page served on MAGPIE_DEV_UI, nil when it isn't.
var devUI *http.Server

// devRole is this process's half of `make dev`: "backend", "shell", or ""
// for the whole app in one process.
func devRole() string { return os.Getenv("MAGPIE_DEV_ROLE") }

// devBackend serves the page and the API on MAGPIE_DEV_BACKEND for the shell.
//
// A restart is a handover (build/dev.sh): the new backend listens on the
// same addresses beside this one, then this one is sent handoverSignal and
// stops taking requests, finishes those in flight — an agent's stream
// through the gateway as long as it takes — and exits, so a Go change no
// longer cuts off the agents using the dev gateway.
func devBackend(handler func(Windows) http.Handler) error {
	gateway.Handover = handoverSignal != nil
	addr := os.Getenv("MAGPIE_DEV_BACKEND")
	h := handler(remoteWindows(os.Getenv("MAGPIE_DEV_CONTROL")))
	ln, err := gateway.Listen(addr)
	if err != nil {
		return err
	}
	log.Println("dev backend:", "http://"+addr)
	srv := &http.Server{Handler: h}
	if handoverSignal == nil {
		return srv.Serve(ln)
	}
	go srv.Serve(ln)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, handoverSignal)
	<-sig
	log.Println("dev backend: handed over · finishing what's in flight")
	// the page's long polls end within 20s; what the gateway streams, when it ends
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go srv.Shutdown(ctx)
	if devUI != nil {
		go devUI.Shutdown(ctx)
	}
	stopServing()
	srv.Shutdown(ctx)
	return nil
}

// remoteWindows are the shell's windows, seen from the backend.
type remoteWindows string // the shell's control address

func (c remoteWindows) do(op, arg string) { c.post(op, url.Values{"arg": {arg}}) }

// post says what the shell answered; a shell from before an op answers nothing.
func (c remoteWindows) post(op string, form url.Values) string {
	res, err := backendClient.PostForm("http://"+string(c)+"/"+op, form)
	if err != nil {
		log.Println("dev shell:", err)
		return ""
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64))
	return string(b)
}

func (c remoteWindows) HidePanel()           { c.do("hide", "") }
func (c remoteWindows) ShowMain(view string) { c.do("main", view) }
func (c remoteWindows) Quit()                { c.do("quit", "") }
func (c remoteWindows) OpenURL(u string) error {
	if s := c.post("open", url.Values{"arg": {u}}); s != "ok" && s != "" {
		return fmt.Errorf("%s", s)
	}
	return nil
}
func (c remoteWindows) OpenFolder(path string) error {
	if s := c.post("reveal", url.Values{"arg": {path}}); s != "ok" {
		return fmt.Errorf("%s", s)
	}
	return nil
}

// ChooseFolder's answer is the folder after "ok:", or what went wrong.
func (c remoteWindows) ChooseFolder(title string) (string, error) {
	res, err := backendClient.PostForm("http://"+string(c)+"/choose", url.Values{"arg": {title}})
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if dir, ok := strings.CutPrefix(string(b), "ok:"); ok {
		return dir, nil
	}
	return "", fmt.Errorf("%s", b)
}
func (c remoteWindows) Copy(text string) bool {
	return c.post("copy", url.Values{"arg": {text}}) == "ok"
}

// The glide rides beside the height, so a shell from before it still sizes.
func (c remoteWindows) FitPanel(height int, g Glide) {
	c.post("fit", url.Values{"arg": {strconv.Itoa(height)}, "ms": {strconv.Itoa(g.MS)}, "ease": {g.ease()}})
}
func (c remoteWindows) TintPanel(rgba [4]uint8, ms int) bool {
	q := url.Values{"c": {fmt.Sprintf("%d,%d,%d,%d", rgba[0], rgba[1], rgba[2], rgba[3])}, "ms": {strconv.Itoa(ms)}}
	return c.post("tint", url.Values{"arg": {q.Encode()}}) == "ok"
}
func (c remoteWindows) TintTitleBar(rgba [4]uint8, dark bool) bool {
	q := url.Values{"c": {fmt.Sprintf("%d,%d,%d,%d", rgba[0], rgba[1], rgba[2], rgba[3])}}
	if dark {
		q.Set("dark", "1")
	}
	return c.post("titlebar", url.Values{"arg": {q.Encode()}}) == "ok"
}
func (c remoteWindows) SetTextSize(percent int) {
	c.post("textsize", url.Values{"arg": {strconv.Itoa(percent)}})
}

// backendClient waits out a backend's restart: a request made while the old
// one is gone connects to the new one once it listens.
var backendClient = &http.Client{Transport: &http.Transport{
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		deadline := time.Now().Add(time.Minute)
		for {
			c, err := d.DialContext(ctx, network, addr)
			if err == nil || time.Now().After(deadline) {
				return c, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	},
}}

// devShell is the shell's handler, a proxy to the backend, and starts the
// control address the backend drives the windows through. nil outside the
// shell.
func devShell(h *host) http.Handler {
	if devRole() != "shell" {
		return nil
	}
	control := http.NewServeMux()
	control.HandleFunc("POST /{op}", func(rw http.ResponseWriter, r *http.Request) {
		arg := r.FormValue("arg")
		switch r.PathValue("op") {
		case "hide":
			h.HidePanel()
		case "main":
			h.ShowMain(arg)
		case "quit":
			h.Quit()
		case "open":
			if err := h.OpenURL(arg); err != nil {
				rw.Write([]byte(err.Error()))
				return
			}
			rw.Write([]byte("ok"))
			return
		case "reveal":
			if err := h.OpenFolder(arg); err != nil {
				rw.Write([]byte(err.Error()))
				return
			}
			rw.Write([]byte("ok"))
			return
		case "choose":
			dir, err := h.ChooseFolder(arg)
			if err != nil {
				rw.Write([]byte(err.Error()))
				return
			}
			rw.Write([]byte("ok:" + dir))
			return
		case "copy":
			if h.Copy(arg) {
				rw.Write([]byte("ok"))
				return
			}
		case "fit":
			q := url.Values{"h": {arg}, "ms": {r.FormValue("ms")}, "ease": {r.FormValue("ease")}}
			if n, g, ok := parseFit(q); ok {
				h.FitPanel(n, g)
			}
		case "textsize":
			if n, err := strconv.Atoi(arg); err == nil {
				h.SetTextSize(n)
			}
		case "tint":
			q, _ := url.ParseQuery(arg)
			if c, ms, ok := parseTint(q); ok && h.TintPanel(c, ms) {
				rw.Write([]byte("ok"))
				return
			}
		case "titlebar":
			q, _ := url.ParseQuery(arg)
			if c, _, ok := parseTint(q); ok && h.TintTitleBar(c, q.Get("dark") == "1") {
				rw.Write([]byte("ok"))
				return
			}
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	go func() {
		if err := http.ListenAndServe(os.Getenv("MAGPIE_DEV_CONTROL"), control); err != nil {
			log.Println("dev control:", err)
		}
	}()
	p := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: os.Getenv("MAGPIE_DEV_BACKEND")})
	p.Transport = backendClient.Transport
	// A restart cuts off whatever was in flight. A read is asked again, of
	// the backend that comes up (the dial waits for it), so the page never
	// sees the cut; a write may have landed, so it fails as it is.
	p.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, err error) {
		n, _ := r.Context().Value(retryKey{}).(int)
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && n < 3 && r.Context().Err() == nil {
			p.ServeHTTP(rw, r.WithContext(context.WithValue(r.Context(), retryKey{}, n+1)))
			return
		}
		rw.WriteHeader(http.StatusBadGateway)
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// The webview's requests often come without a Content-Length, which
		// Wails reads as 0, and a proxy drops a body of length 0: say it's
		// unknown so a POST's body gets through.
		if r.ContentLength == 0 && r.Body != nil && r.Body != http.NoBody && r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.ContentLength = -1
		}
		p.ServeHTTP(rw, r)
	})
}

// stash keeps an import link for the page, in the backend under `make dev`.
func stash(link string) string {
	if devRole() != "shell" {
		return stashImport(link)
	}
	res, err := backendClient.PostForm("http://"+os.Getenv("MAGPIE_DEV_BACKEND")+"/api/dev/import", url.Values{"link": {link}})
	if err != nil {
		log.Println("dev import:", err)
		return ""
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return strings.TrimSpace(string(b))
}

// retryKey counts how often a read cut off by a backend restart was asked
// again.
type retryKey struct{}

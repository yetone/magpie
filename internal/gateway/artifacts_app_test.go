package gateway

import (
	"bytes"
	"encoding/json"
	"image/png"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/settings"
)

// The newest page installs as an app: its shell links a manifest and an
// icon under its _api, and serves them with the service worker scoped
// to the page's folder, in magpie's language. A pinned version links
// the icon only and serves none of it; an unknown name is not found.
func TestArtifactApp(t *testing.T) {
	noAgents(t)
	h := artifacts(t)
	// the package's tests share one settings.json: the language goes back
	st := settings.Load()
	was := st.Lang
	st.Lang = "zh"
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st := settings.Load()
		st.Lang = was
		settings.Save(st)
	})
	id := publishFor(t, h, nil)
	if _, err := artifact.PublishPage(artifact.Publish{ID: id, Page: []byte("<!doctype html><title>Board</title><p>2")}); err != nil {
		t.Fatal(err)
	}
	home := ArtifactPagePath + id + "/"
	api := home + "_api/"

	body := pageGet(h, home, "Sec-Fetch-Dest", "document").Body.String()
	if !strings.Contains(body, `<link rel="manifest" href="`+api+`app.webmanifest">`) || !strings.Contains(body, `<link rel="icon" href="`+api+`app-icon-192.png">`) || !strings.Contains(body, `"app":"`+strings.TrimSuffix(api, "/")+`"`) {
		t.Fatalf("the shell's links: %.600s", body)
	}
	pinned := pageGet(h, ArtifactPagePath+id+"@1/", "Sec-Fetch-Dest", "document").Body.String()
	if strings.Contains(pinned, `rel="manifest"`) || !strings.Contains(pinned, `<link rel="icon" href="`+api+`app-icon-192.png">`) {
		t.Fatalf("a pinned version installs: %.600s", pinned)
	}
	if csp := pageGet(h, home, "Sec-Fetch-Dest", "document").Header().Get("Content-Security-Policy"); !strings.Contains(csp, "manifest-src 'self'") || !strings.Contains(csp, "worker-src 'self'") {
		t.Fatalf("the shell's CSP: %s", csp)
	}

	w := pageGet(h, api+"app.webmanifest", "Sec-Fetch-Dest", "manifest", "Sec-Fetch-Mode", "cors")
	var m struct {
		ID, Name, StartURL, Scope, Display string
		Icons                              []struct{ Src, Sizes string }
	}
	raw := strings.NewReplacer(`"start_url"`, `"starturl"`).Replace(w.Body.String())
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/manifest+json" || json.Unmarshal([]byte(raw), &m) != nil {
		t.Fatalf("manifest: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if m.ID != home || m.StartURL != home || m.Scope != home || m.Display != "standalone" || m.Name != "Board" || len(m.Icons) != 2 || m.Icons[1].Src != api+"app-icon-512.png" || m.Icons[1].Sizes != "512x512" {
		t.Fatalf("manifest: %+v", m)
	}
	for _, n := range []int{192, 512} {
		w := pageGet(h, api+"app-icon-"+itoa(n)+".png", "Sec-Fetch-Dest", "image")
		img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || err != nil || img.Bounds().Dx() != n || img.Bounds().Dy() != n {
			t.Fatalf("icon %d: %d %v %v", n, w.Code, w.Header(), err)
		}
	}

	w = pageGet(h, api+"app-sw.js", "Sec-Fetch-Dest", "serviceworker", "Service-Worker", "script")
	hd := w.Header()
	if w.Code != 200 || !strings.HasPrefix(hd.Get("Content-Type"), "text/javascript") || hd.Get("Service-Worker-Allowed") != home || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Content-Security-Policy") != "default-src 'none'; connect-src 'self'" {
		t.Fatalf("worker: %d %v", w.Code, hd)
	}
	if b := w.Body.String(); !strings.Contains(b, "e.preloadResponse") || !strings.Contains(b, `r.destination !== "document"`) || !strings.Contains(b, "magpie 没有在运行") {
		t.Fatalf("worker: %s", b)
	}

	for _, p := range []string{api + "app-icon-64.png", api + "app-icon-x.png", api + "other", ArtifactPagePath + id + "@1/_api/app.webmanifest", ArtifactPagePath + strings.Repeat("a", 26) + "/_api/app.webmanifest"} {
		if w := pageGet(h, p, "Sec-Fetch-Dest", "manifest"); w.Code != 404 {
			t.Fatalf("%s: %d %.200s", p, w.Code, w.Body)
		}
	}
}

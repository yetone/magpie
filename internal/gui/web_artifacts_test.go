package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/gateway"
)

// #7665 (tossp): magpie web on a server opens an artifact to the browser
// signed in to it, at <page>/a/<id>/, with the gateway's sandbox and none
// of the page's store; a browser that isn't signed in, another page's
// script, a wrong or made-up token, and a write all get nothing.
func TestWebArtifactsSignedIn(t *testing.T) {
	dir := t.TempDir()
	was := artifact.Dir
	artifact.Dir = func() string { return dir }
	t.Cleanup(func() { artifact.Dir = was })
	a, err := artifact.PublishPage(artifact.Publish{Page: []byte("<title>Plan</title><p>first"), Files: map[string][]byte{"app.js": []byte("console.log(1)")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.PublishPage(artifact.Publish{ID: a.ID, Page: []byte("<title>Plan</title><p>second"), Files: map[string][]byte{"app.js": []byte("console.log(2)")}}); err != nil {
		t.Fatal(err)
	}
	other, err := artifact.PublishPage(artifact.Publish{Page: []byte("<p>other")})
	if err != nil {
		t.Fatal(err)
	}

	const key = "0123456789abcdef-7665"
	h := webHandler("3430", key, "", nil, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusTeapot) }))
	do := func(method, path string, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr, r.Host = "198.51.100.7:50000", "magpie.example.com"
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	signedIn := "magpie_web_3430=" + key
	// a tab: on the gateway, this computer's gets the viewer shell; here
	// the page itself is served, as to another computer's tab there
	nav := []string{"Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document", "Accept", "text/html"}

	// not signed in: the sign-in form, a rebound name's page and a
	// sandboxed page alike, and nothing of the artifact
	for _, hdr := range [][]string{nav, {"Sec-Fetch-Mode", "cors", "Origin", "null"}, {"Sec-Fetch-Mode", "cors", "Origin", "https://evil.example"}} {
		if w := do("GET", "/a/"+a.ID+"/", hdr...); w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "second") || strings.Contains(w.Header().Get("Location"), webArtifactPrefix) {
			t.Errorf("not signed in, %v: %d %v %s", hdr, w.Code, w.Header(), w.Body)
		}
	}
	// signed in, but a script's request (a page of the same site with the
	// cookie): no token
	if w := do("GET", "/a/"+a.ID+"/", "Cookie", signedIn, "Sec-Fetch-Mode", "cors"); w.Code != http.StatusForbidden || w.Header().Get("Location") != "" {
		t.Errorf("a script's request: %d %v", w.Code, w.Header())
	}

	// signed in, a tab: on to the files, with the artifact's token
	open := func(ref string) string {
		t.Helper()
		w := do("GET", "/a/"+ref+"/", append([]string{"Cookie", signedIn}, nav...)...)
		loc := w.Header().Get("Location")
		if w.Code != http.StatusFound || !strings.HasPrefix(loc, webArtifactPrefix) || !strings.HasSuffix(loc, "/"+ref+"/") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("signed in, %s: %d %v", ref, w.Code, w.Header())
		}
		return loc
	}
	at := open(a.ID)
	w := do("GET", at, nav...)
	body := w.Body.String()
	csp := w.Header().Get("Content-Security-Policy")
	if w.Code != 200 || !strings.Contains(body, "second") || !strings.HasPrefix(csp, "sandbox ") || strings.Contains(csp, "allow-same-origin") ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("the page: %d %v %s", w.Code, w.Header(), body)
	}
	// its runtime is a shared copy's: no store, no token to it
	tok, err := artifact.PageToken(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"shared":true`) || strings.Contains(body, tok) || strings.Contains(body, "_api") {
		t.Errorf("the page's runtime: %s", body)
	}
	// its own files, asked for from its opaque origin with no cookie
	if w := do("GET", at+"app.js", "Sec-Fetch-Mode", "cors", "Origin", "null"); w.Code != 200 || w.Body.String() != "console.log(2)" || w.Header().Get("Content-Security-Policy") != csp {
		t.Errorf("its script: %d %v %s", w.Code, w.Header(), w.Body)
	}
	// an earlier version
	if w := do("GET", open(a.ID+"@1")+"app.js", "Sec-Fetch-Mode", "cors", "Origin", "null"); w.Code != 200 || w.Body.String() != "console.log(1)" {
		t.Errorf("version 1: %d %s", w.Code, w.Body)
	}
	// its store, and its app's files, aren't served elsewhere
	for _, p := range []string{"_api/db", "_api/" + artifact.AppManifest, "_api/" + artifact.AppWorker} {
		if w := do("GET", at+p, "Sec-Fetch-Mode", "cors", "Origin", "null"); w.Code == 200 {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}

	// the token opens its own artifact alone, read only
	tokA := strings.Split(strings.TrimPrefix(at, webArtifactPrefix), "/")[0]
	for _, p := range []string{
		webArtifactPrefix + tokA + "/" + other.ID + "/",
		webArtifactPrefix + webArtifactToken(other.ID) + "/" + a.ID + "/",
		webArtifactPrefix + "00112233445566778899aabbccddeeff/" + a.ID + "/",
		webArtifactPrefix + "/" + a.ID + "/",
		at + "../" + a.ID + "/meta.json",
		at + "..%2fmeta.json",
		webArtifactPrefix + tokA + "/nope/",
	} {
		if w := do("GET", p, "Sec-Fetch-Mode", "cors", "Origin", "null"); w.Code == 200 || strings.Contains(w.Body.String(), "other") || strings.Contains(w.Body.String(), "second") {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}
	if w := do("POST", at, "Origin", "null"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a POST: %d", w.Code)
	}
	if w := do("POST", "/a/"+a.ID+"/", "Cookie", signedIn); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a POST to the address: %d", w.Code)
	}
	// a tab on a token from before a restart goes back through the sign-in
	if w := do("GET", webArtifactPrefix+"00112233445566778899aabbccddeeff/"+a.ID+"@1/app.js", nav...); w.Code != http.StatusFound || w.Header().Get("Location") != "/a/"+a.ID+"@1/app.js" {
		t.Errorf("an old token: %d %v", w.Code, w.Header())
	}
	// the page's other routes still take the sign-in
	if w := do("GET", "/api/state", "Sec-Fetch-Mode", "cors", "Origin", "null"); w.Code != http.StatusUnauthorized {
		t.Errorf("/api from the artifact's origin: %d", w.Code)
	}
}

// #7665: artifacts' links name magpie web's address when MAGPIE_PUBLIC_URL
// says where it is, and this computer's otherwise. The Library's page
// lists them at this computer's address either way, which artifacts.js
// moves to the page's own in a browser away.
//
// The address is MAGPIE_PUBLIC_URL whole, its scheme, port and path: a
// proxy at https://123.abc:666 was given http://123.abc:3430, its host on
// the page's own scheme and port (#8014). MAGPIE_WEB_URL, the page's own
// address, comes first.
func TestWebArtifactLinks(t *testing.T) {
	for _, c := range []struct{ public, want string }{
		{"https://magpie.example.com", "https://magpie.example.com"}, // 443 implied stays so
		{"https://magpie.example.com/", "https://magpie.example.com"},
		{"https://123.abc:666", "https://123.abc:666"},
		{"https://123.abc:666/magpie/", "https://123.abc:666/magpie"},
		{"https://x.example/magpie", "https://x.example/magpie"},
		{"http://nas.local:3425", "http://nas.local:3425"}, // the gateway, published on the host
		{"", ""},
	} {
		t.Setenv("MAGPIE_PUBLIC_URL", c.public)
		t.Setenv("MAGPIE_WEB_URL", "")
		t.Setenv("LAZYCAT_APP_DOMAIN", "")
		got := ArtifactsAt()
		if got != c.want {
			t.Errorf("%q: %q, want %q", c.public, got, c.want)
		}
		gateway.SetArtifactsAt(got)
		want := c.want + "/a/abc@2/"
		if c.want == "" {
			want = gateway.URL() + "/a/abc@2/"
		}
		if u := gateway.ArtifactURL("abc", 2); u != want {
			t.Errorf("%q: the link %q, want %q", c.public, u, want)
		}
		gateway.SetArtifactsAt("")
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "https://123.abc:666")
	for web, want := range map[string]string{
		"https://page.example:667/": "https://page.example:667",
		"page.example":              "https://page.example",
		"http://u:p@page.example":   "https://123.abc:666", // no plain address: MAGPIE_PUBLIC_URL's
	} {
		t.Setenv("MAGPIE_WEB_URL", web)
		if got := ArtifactsAt(); got != want {
			t.Errorf("MAGPIE_WEB_URL %q: %q, want %q", web, got, want)
		}
	}
	t.Setenv("MAGPIE_WEB_URL", "")

	dir := t.TempDir()
	was := artifact.Dir
	artifact.Dir = func() string { return dir }
	t.Cleanup(func() { artifact.Dir = was })
	a, err := artifact.PublishPage(artifact.Publish{Page: []byte("<p>x")})
	if err != nil {
		t.Fatal(err)
	}
	gateway.SetArtifactsAt("https://magpie.example.com")
	t.Cleanup(func() { gateway.SetArtifactsAt("") })
	rw := httptest.NewRecorder()
	webPageHandler(t, nil, nil).ServeHTTP(rw, httptest.NewRequest("GET", "/api/artifacts", nil))
	var out struct{ Artifacts []artifactJSON }
	if rw.Code != 200 || json.Unmarshal(rw.Body.Bytes(), &out) != nil || len(out.Artifacts) != 1 {
		t.Fatalf("/api/artifacts: %d %s", rw.Code, rw.Body)
	}
	if got := out.Artifacts[0]; got.URL != gateway.URL()+"/a/"+a.ID+"/" || got.Versions[0].URL != gateway.URL()+"/a/"+a.ID+"@1/" {
		t.Errorf("the list: %+v", got)
	}
}

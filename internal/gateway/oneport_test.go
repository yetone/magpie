package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

// Every route Handler registers is either a client route `magpie web`'s
// port hands to the gateway (ClientPath), or one named here that stays on
// the gateway's port alone: its info page at /, the /api/* routes (the
// page's own namespace there) and the Claude helper's callbacks under
// /_magpie/. A route added to Handler and to neither fails this, so the
// one-port page doesn't miss it, or take it, by accident.
func TestClientPathCoversHandler(t *testing.T) {
	// the patterns as routes() registers them, not as its source reads
	n := 0
	for _, pat := range New().routes().patterns {
		if i := strings.IndexByte(pat, ' '); i >= 0 {
			pat = pat[i+1:]
		}
		p := strings.NewReplacer("{$}", "", "{id...}", "x", "{call...}", "x", "{id}", "x", "{token}", "x", "{name}", "x", "{agent}", "x").Replace(pat)
		n++
		stays := p == "/" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/_magpie/")
		if ClientPath(p) == stays {
			t.Errorf("%s (%s): ClientPath %v", pat, p, ClientPath(p))
		}
	}
	if n < 60 {
		t.Fatalf("found %d routes in Handler", n)
	}
}

// A path that isn't clean stays behind the page's key: the gateway's mux
// would redirect it elsewhere, and "/v1/../api/state" is the page's API.
func TestClientPathOnlyClean(t *testing.T) {
	for _, p := range []string{"", "/", "/api/state", "/api/v1/models", "/v1/../api/state", "/v1//models", "/v1/./models", "/v1x", "/v1betax/models", "/boot.js", "/_magpie/sign-in", "/mcpx", "/v1/models/../../api/settings"} {
		if ClientPath(p) {
			t.Errorf("%q is a client path", p)
		}
	}
	for _, p := range []string{"/v1", "/v1/", "/v1/models", "/v1/chat/completions", "/v1beta/models/gemini:generateContent", "/mcp/magpie/tools", "/backend-api/codex/responses", "/messages"} {
		if !ClientPath(p) {
			t.Errorf("%q is not a client path", p)
		}
	}
}

// docs/reference.md's LazyCat manifest makes public (public_path) every
// client route and nothing else. A route it leaves out sends an agent that
// isn't signed in to the box to the box's login page; one of the page's
// own made public there lets a browser that isn't signed in reach magpie
// without being sent to the box's sign-in.
func TestLazyCatManifestListsTheClientRoutes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, doc, ok := strings.Cut(string(b), "\n#### LazyCat\n")
	if !ok {
		t.Fatal("docs/reference.md has no LazyCat section")
	}
	_, list, ok := strings.Cut(doc, "\n  public_path:\n")
	if !ok {
		t.Fatal("its manifest has no public_path")
	}
	var public []string
	for _, l := range strings.Split(list, "\n") {
		p, ok := strings.CutPrefix(strings.TrimSpace(l), "- ")
		if !ok {
			break
		}
		public = append(public, strings.TrimSuffix(p, "/"))
	}
	want := slices.Clone(clientPrefixes)
	slices.Sort(public)
	slices.Sort(want)
	if !slices.Equal(public, want) {
		t.Fatalf("public_path %v, the client routes %v", public, want)
	}
}

// A request the page's port marked (FromPagePort, or the header toGateway
// passes on) is never local, and lanGuard takes the header off before any
// handler, so nothing passes it on upstream. One without the mark, from
// loopback, is an agent here as before.
func TestPagePortMarkIsNeverLocal(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_TRUST_PROXY", "1")
	var sawLocal, reached, sawHeader bool
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawLocal, reached, sawHeader = local(r), true, r.Header.Get(PagePortHeader) != ""
	}))
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.RemoteAddr = "127.0.0.1:50001"
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !sawLocal {
		t.Fatal("an agent here, unmarked: not local")
	}
	if local(FromPagePort(r)) {
		t.Fatal("marked by the page's port: local")
	}
	for _, m := range []*http.Request{FromPagePort(r), takePagePort(FromPagePort(r))} {
		if !viaPagePort(m) {
			t.Fatal("the mark was lost")
		}
	}
	if takePagePort(FromPagePort(r)).Header.Get(PagePortHeader) != "" {
		t.Fatal("takePagePort left the header on")
	}
	reached = false
	w := httptest.NewRecorder()
	h.ServeHTTP(w, FromPagePort(r))
	if w.Code != http.StatusForbidden || reached {
		t.Fatalf("marked, not shared: %d, handler reached %v", w.Code, reached)
	}
	// shared, with a key: answered, the mark kept in the context alone
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3499")
	secret, err := access.Update("add-key", access.Change{Name: "Laptop"})
	if err != nil {
		t.Fatal(err)
	}
	k := FromPagePort(r)
	k.Header.Set("Authorization", "Bearer "+secret)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, k)
	if !reached || sawLocal || sawHeader {
		t.Fatalf("marked, with a key: %d, handler reached %v, saw local %v, the header %v", w.Code, reached, sawLocal, sawHeader)
	}
}

package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/groupshare"
	"github.com/yetone/magpie/internal/provider"
)

// shareSite is usemagpie.ai's groups API, held in memory: one group can
// be shared (abcdefghijkl), and mnopqrstuvwx was taken down.
type shareSite struct {
	*httptest.Server
	mu    sync.Mutex
	group json.RawMessage // what was shared, as posted
	auth  []string        // each PUT's Authorization
}

const (
	shareToken = "tok-gui-share-secret-77"
	sharedID   = "abcdefghijkl"
	goneID     = "mnopqrstuvwx"
)

func newShareSite(t *testing.T) *shareSite {
	t.Helper()
	s := &shareSite{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		send := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		var in struct {
			Group json.RawMessage `json:"group"`
		}
		_ = json.Unmarshal(b, &in)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/groups":
			s.group = in.Group
			send(201, map[string]any{"id": sharedID, "url": s.URL + "/g/" + sharedID, "token": shareToken, "version": 1})
		case r.Method == http.MethodPut && r.URL.Path == "/api/groups/"+sharedID:
			s.auth = append(s.auth, r.Header.Get("Authorization"))
			s.group = in.Group
			send(200, map[string]any{"id": sharedID, "url": s.URL + "/g/" + sharedID, "version": 2})
		case r.Method == http.MethodGet && r.URL.Path == "/api/groups/"+sharedID && s.group != nil:
			send(200, map[string]any{"id": sharedID, "url": s.URL + "/g/" + sharedID, "title": "Duo", "public": true, "version": 1, "group": s.group})
		default:
			send(404, map[string]any{"error": "gone"})
		}
	}))
	t.Cleanup(s.Close)
	t.Setenv("MAGPIE_SHARE_SITE", s.URL)
	return s
}

// A group shared at a link: the token that lets this magpie change the
// link never reaches the page (a window served to another device on the
// LAN has it), yet sharing again still updates the same link with it. A
// link pasted into Import previews and imports the group it names, and
// remembers where it came from, for the author's newer versions. A link
// taken down is said in the reader's language: a code, and the words as
// an i18n.js key.
func TestGroupSharedAtALink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	site := newShareSite(t)
	if err := provider.Save(provider.Provider{ID: "a", Name: "a", Key: "sk-k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "duo", Name: "Duo", Members: []string{"a/m1", "a/m2"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	do := func(method, path string, body any) (int, string) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(string(raw))))
		return w.Code, w.Body.String()
	}
	meta := map[string]any{"title": "Duo", "public": true}

	code, body := do("POST", "/api/groups/publish", map[string]any{"id": "duo", "meta": meta})
	if code != 200 || !strings.Contains(body, site.URL+"/g/"+sharedID) {
		t.Fatalf("publish: %d %s", code, body)
	}
	if strings.Contains(body, shareToken) {
		t.Fatalf("publish answers the page the share's token: %s", body)
	}
	if code, body = do("GET", "/api/groups", nil); code != 200 || !strings.Contains(body, sharedID) || strings.Contains(body, shareToken) {
		t.Fatalf("the groups the page reads: %d, has the link %v, has the token %v", code, strings.Contains(body, sharedID), strings.Contains(body, shareToken))
	}
	// shared again: the same link, changed with the token kept here
	if code, body = do("POST", "/api/groups/publish", map[string]any{"id": "duo", "meta": meta}); code != 200 || strings.Contains(body, shareToken) {
		t.Fatalf("publish again: %d %s", code, body)
	}
	if len(site.auth) != 1 || site.auth[0] != "Bearer "+shareToken {
		t.Fatalf("sharing again sent %q, want the token kept", site.auth)
	}

	link := site.URL + "/g/" + sharedID
	code, body = do("POST", "/api/groups/preview", map[string]any{"text": link})
	var pv previewJSON
	_ = json.Unmarshal([]byte(body), &pv)
	if code != 200 || pv.Source == nil || pv.Source.ID != sharedID || pv.Plan.Name != "Duo 2" || len(pv.Plan.Members) != 2 {
		t.Fatalf("preview of a pasted link: %d %s", code, body)
	}
	before := len(provider.Groups())
	code, body = do("POST", "/api/groups/import", map[string]any{"text": link})
	var st groupsJSON
	_ = json.Unmarshal([]byte(body), &st)
	if code != 200 || st.Imported == nil || len(provider.Groups()) != before+1 {
		t.Fatalf("import of a pasted link: %d %s", code, body)
	}
	if f, ok := groupshare.Sources()[st.Imported.ID]; !ok || f.ID != sharedID || f.Version != 1 {
		t.Fatalf("the import doesn't remember where it came from: %+v", groupshare.Sources())
	}

	code, body = do("POST", "/api/groups/preview", map[string]any{"text": site.URL + "/g/" + goneID})
	var out struct{ Code, Said string }
	_ = json.Unmarshal([]byte(body), &out)
	if code == 200 || out.Code != "group_gone" || out.Said == "" {
		t.Fatalf("a link taken down: %d %s", code, body)
	}
	i18n, err := os.ReadFile("assets/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(i18n), `"`+out.Said+`":`) {
		t.Fatalf("%q is no key of i18n.js: the page would say it in English", out.Said)
	}
}

// magpie://group?id=… (its page's Open in magpie) opens the window on
// the Routing view with that group's preview over it: the view routing's
// client.js reads shared= on.
func TestGroupLinkOpensItsPreview(t *testing.T) {
	pluginLinks.Lock()
	pluginLinks.at = time.Time{}
	pluginLinks.Unlock()
	t.Cleanup(func() {
		pluginLinks.Lock()
		pluginLinks.at = time.Time{}
		pluginLinks.Unlock()
	})
	view, ok := groupView("magpie://group?id="+sharedID, time.Now())
	if !ok || view != "groups&shared="+sharedID {
		t.Fatalf("groupView = %q, %v", view, ok)
	}
	if !strings.HasPrefix(mainURL(view, ""), "/?view=groups&shared=") {
		t.Fatalf("the window opens at %s", mainURL(view, ""))
	}
	app, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `v === "routing" || v === "groups"`) {
		t.Fatal("app.js no longer shows Routing for view=groups")
	}
	// a bad id opens nothing: the import stash says it can't be read
	if v, ok := groupView("magpie://group?id=nope", time.Now().Add(time.Hour)); ok || v != "" {
		t.Fatalf("a group link with a bad id opens %q, %v", v, ok)
	}
}

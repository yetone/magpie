package groupshare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// site is usemagpie.ai's groups API as site/groups.js answers it, held
// in memory.
type site struct {
	*httptest.Server
	mu     sync.Mutex
	groups map[string]map[string]any // by id: title, public, version, token, group
	asked  []string                  // "METHOD path"
	bodies map[string][]byte         // last body by "METHOD path"
	next   int
}

func newSite(t *testing.T) *site {
	t.Helper()
	s := &site{groups: map[string]map[string]any{}, bodies: map[string][]byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	t.Setenv("MAGPIE_SHARE_SITE", s.URL)
	return s
}

func (s *site) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	s.asked = append(s.asked, key)
	s.bodies[key] = b
	send := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/groups/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/groups":
		var in map[string]any
		_ = json.Unmarshal(b, &in)
		s.next++
		id := fmt.Sprintf("abcdefghij%02d", s.next)
		id = strings.NewReplacer("0", "a", "1", "b", "8", "c", "9", "d").Replace(id)
		in["version"], in["token"] = 1, "tok-"+id
		s.groups[id] = in
		send(201, map[string]any{"id": id, "url": s.URL + "/g/" + id, "token": "tok-" + id, "version": 1})
	case r.Method == http.MethodPut:
		g, ok := s.groups[id]
		if !ok {
			send(404, map[string]any{"error": "no such shared group"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+g["token"].(string) {
			send(403, map[string]any{"error": "not yours"})
			return
		}
		var in map[string]any
		_ = json.Unmarshal(b, &in)
		in["version"], in["token"] = g["version"].(int)+1, g["token"]
		s.groups[id] = in
		send(200, map[string]any{"id": id, "url": s.URL + "/g/" + id, "version": in["version"]})
	case r.Method == http.MethodDelete:
		if _, ok := s.groups[id]; !ok {
			send(404, map[string]any{"error": "gone"})
			return
		}
		delete(s.groups, id)
		send(200, map[string]any{"deleted": true})
	case r.Method == http.MethodGet && r.URL.Path == "/api/groups" && r.URL.Query().Has("ids"):
		var out []map[string]any
		for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if g, ok := s.groups[id]; ok {
				out = append(out, map[string]any{"id": id, "version": g["version"], "title": g["title"]})
			}
		}
		send(200, map[string]any{"groups": out})
	case r.Method == http.MethodGet && r.URL.Path == "/api/groups":
		var out []map[string]any
		for id, g := range s.groups {
			if g["public"] == true {
				out = append(out, map[string]any{"id": id, "title": g["title"], "version": g["version"], "members": []string{"a/x"}, "uses": 3})
			}
		}
		send(200, map[string]any{"groups": out})
	case r.Method == http.MethodGet:
		g, ok := s.groups[id]
		if !ok {
			send(404, map[string]any{"error": "gone"})
			return
		}
		send(200, map[string]any{"id": id, "url": s.URL + "/g/" + id, "title": g["title"], "public": g["public"], "version": g["version"], "group": g["group"]})
	case r.Method == http.MethodPost && r.URL.Path == "/api/groups/like":
		send(200, map[string]any{"likes": 1})
	case r.Method == http.MethodPost && r.URL.Path == "/api/groups/report":
		send(200, map[string]any{"ok": true})
	default:
		send(404, map[string]any{"error": "no route"})
	}
}

func (s *site) count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.asked {
		if a == key {
			n++
		}
	}
	return n
}

func home(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	updates.Lock()
	updates.at, updates.got = updates.at.AddDate(-1, 0, 0), nil
	updates.Unlock()
	if err := provider.Save(provider.Provider{ID: "myrelay", Name: "My Relay", Key: "sk-relay-secret", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "y"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"myrelay/x", "myrelay/y"}}); err != nil {
		t.Fatal(err)
	}
}

// Sharing a group puts it at one link; sharing it again updates that
// link, as its next version, with the token only this magpie holds. What
// goes names no key, address or provider the user named themselves.
func TestPublishThenUpdate(t *testing.T) {
	home(t)
	s := newSite(t)
	ctx := context.Background()
	m, err := Publish(ctx, "mine", Meta{Title: "My group", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(m.ID) || m.Version != 1 || m.Token != "" || !strings.HasSuffix(m.URL, "/g/"+m.ID) {
		t.Fatalf("published %+v", m)
	}
	sent := string(s.bodies["POST /api/groups"])
	for _, leak := range []string{"sk-relay", "127.0.0.1", "myrelay", "My Relay"} {
		if strings.Contains(sent, leak) {
			t.Errorf("the share sent %q: %s", leak, sent)
		}
	}
	if !strings.Contains(sent, `"custom/x"`) {
		t.Errorf("the user's own provider goes as custom: %s", sent)
	}
	raw, _ := os.ReadFile(path())
	if !strings.Contains(string(raw), "tok-"+m.ID) {
		t.Fatalf("the token isn't kept here: %s", raw)
	}
	if info, _ := os.Stat(path()); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 { // Windows has no such bits
		t.Errorf("shared-groups.json is %v, want 0600: it holds the token", info.Mode().Perm())
	}
	again, err := Publish(ctx, "mine", Meta{Title: "My group, v2", Public: false})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != m.ID || again.Version != 2 || s.count("POST /api/groups") != 1 || s.count("PUT /api/groups/"+m.ID) != 1 {
		t.Fatalf("sharing again made another link: %+v, asked %v", again, s.asked)
	}
	if got := Shared()["mine"]; got.Version != 2 || got.Public || got.Token != "" {
		t.Errorf("Shared() = %+v", got)
	}
	if err := Unpublish(ctx, "mine"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Shared()["mine"]; ok || len(s.groups) != 0 {
		t.Errorf("unpublished, still shared: %v %v", Shared(), s.groups)
	}
}

// A link taken down on the site is never shared again behind the user's
// back: an update to it says it is gone and forgets it.
func TestPublishTakenDownIsGone(t *testing.T) {
	home(t)
	s := newSite(t)
	ctx := context.Background()
	m, err := Publish(ctx, "mine", Meta{Title: "My group", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	delete(s.groups, m.ID)
	s.mu.Unlock()
	if _, err := Publish(ctx, "mine", Meta{Title: "My group", Public: true}); err != ErrGone {
		t.Fatalf("err = %v, want ErrGone", err)
	}
	if s.count("POST /api/groups") != 1 {
		t.Errorf("a taken-down group was shared again at a new link: %v", s.asked)
	}
	if _, ok := Shared()["mine"]; ok {
		t.Error("the taken-down link is still recorded")
	}
}

// A group imported from a link hears of its author's newer version once,
// and not again after the user passes it over.
func TestUpdatesAfterImport(t *testing.T) {
	home(t)
	s := newSite(t)
	ctx := context.Background()
	m, err := Publish(ctx, "mine", Meta{Title: "Theirs", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Fetch(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := provider.ImportShared(p.Group, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Imported(res.ID, p); err != nil {
		t.Fatal(err)
	}
	if u, err := Updates(ctx); err != nil || len(u) != 0 {
		t.Fatalf("no newer version yet: %v %v", u, err)
	}
	if _, err := Publish(ctx, "mine", Meta{Title: "Theirs", Public: true}); err != nil {
		t.Fatal(err)
	}
	if u, _ := Updates(ctx); len(u) != 0 {
		t.Fatalf("asked again inside the hour: %v", u)
	}
	updates.Lock()
	updates.at = updates.at.AddDate(0, 0, -1)
	updates.Unlock()
	u, err := Updates(ctx)
	if err != nil || u[res.ID].Version != 2 {
		t.Fatalf("Updates = %v, %v; want %s at version 2", u, err, res.ID)
	}
	if err := PassOver(res.ID, 2); err != nil {
		t.Fatal(err)
	}
	if u, _ := Updates(ctx); len(u) != 0 {
		t.Errorf("passed over, still told: %v", u)
	}
	if err := Report(ctx, strings.Repeat("ab", 16)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s.bodies["POST /api/groups/report"]), m.ID) {
		t.Errorf("report %s", s.bodies["POST /api/groups/report"])
	}
	// the group removed, the next report says none is used here
	if err := provider.DeleteGroup(res.ID); err != nil {
		t.Fatal(err)
	}
	if err := Report(ctx, strings.Repeat("ab", 16)); err != nil {
		t.Fatal(err)
	}
	if got := string(s.bodies["POST /api/groups/report"]); !strings.Contains(got, `"groups":[]`) {
		t.Errorf("report after removing it: %s", got)
	}
}

// A computer that never imported a shared group says nothing each day.
func TestReportQuietWithNothingImported(t *testing.T) {
	home(t)
	s := newSite(t)
	if err := Report(context.Background(), strings.Repeat("ab", 16)); err != nil {
		t.Fatal(err)
	}
	if len(s.asked) != 0 {
		t.Errorf("asked %v", s.asked)
	}
}

// A test names no site, and with none nothing goes anywhere.
func TestNoSiteSharesNothing(t *testing.T) {
	home(t)
	t.Setenv("MAGPIE_SHARE_SITE", "")
	if _, err := Publish(context.Background(), "mine", Meta{Title: "x"}); err != ErrOff {
		t.Errorf("err = %v, want ErrOff", err)
	}
}

func TestLinkID(t *testing.T) {
	t.Setenv("MAGPIE_SHARE_SITE", "http://127.0.0.1:9")
	const id = "abcdefgh2345"
	for in, want := range map[string]string{
		id:                                         id,
		"  " + id + "\n":                           id,
		"https://usemagpie.ai/g/" + id:             id,
		"https://www.usemagpie.ai/g/" + id + "/":   id,
		"usemagpie.ai/g/" + id:                     id,
		"magpie://group?id=" + id:                  id,
		"http://127.0.0.1:9/g/" + id:               id,
		"https://evil.example/g/" + id:             "",
		"https://usemagpie.ai/a/" + id:             "",
		"magpie://import?id=" + id:                 "",
		"magpie://group?id=ABCDEFGH2345":           "",
		`{"magpie":"routing-group","group":{}}`:    "",
		"https://usemagpie.ai/g/" + id + " please": "",
	} {
		if got := LinkID(in); got != want {
			t.Errorf("LinkID(%q) = %q, want %q", in, got, want)
		}
	}
}

// A group removed and another made under its id is a new group: sharing
// it puts it at a link of its own, never at the removed one's as its next
// version, and the removed one's link can still be taken down (#7883).
func TestRemovedGroupForgetsItsLink(t *testing.T) {
	home(t)
	s := newSite(t)
	ctx := context.Background()
	old, err := Publish(ctx, "mine", Meta{Title: "Fast", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"myrelay/y"}}); err != nil {
		t.Fatal(err)
	}
	if got, ok := Shared()["mine"]; ok {
		t.Fatalf("the new group is taken as the removed one's share: %+v", got)
	}
	m, err := Publish(ctx, "mine", Meta{Title: "Fast", Public: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == old.ID || m.Version != 1 || s.count("PUT /api/groups/"+old.ID) != 0 || s.count("POST /api/groups") != 2 {
		t.Fatalf("shared at %+v after %+v; asked %v", m, old, s.asked)
	}
	if v := s.groups[old.ID]["version"]; v != 1 {
		t.Errorf("the removed group's link is at version %v", v)
	}
	r, ok := RetiredLink(old.URL)
	if !ok || r.ID != old.ID || r.Token != "" || r.Group != "mine" {
		t.Fatalf("RetiredLink(%s) = %+v, %v", old.URL, r, ok)
	}
	if err := Unpublish(ctx, old.URL); err != nil {
		t.Fatal(err)
	}
	if _, up := s.groups[old.ID]; up || s.count("DELETE /api/groups/"+old.ID) != 1 {
		t.Errorf("the removed group's link is still up: %v", s.asked)
	}
	if _, ok := s.groups[m.ID]; !ok || Shared()["mine"].ID != m.ID {
		t.Errorf("taking the old link down took the new one: %v %v", s.groups, Shared())
	}
	if _, ok := RetiredLink(old.ID); ok {
		t.Error("taken down, still kept")
	}
}

// A group imported from a link, removed, and another made under its id
// is not told of that link's newer versions (#7883); nor after a sync
// that removes it.
func TestRemovedGroupForgetsWhereItCameFrom(t *testing.T) {
	for _, how := range []string{"delete", "deletes", "mirror"} {
		t.Run(how, func(t *testing.T) {
			home(t)
			newSite(t)
			ctx := context.Background()
			m, err := Publish(ctx, "mine", Meta{Title: "Theirs", Public: true})
			if err != nil {
				t.Fatal(err)
			}
			p, err := Fetch(ctx, m.ID)
			if err != nil {
				t.Fatal(err)
			}
			res, err := provider.ImportShared(p.Group, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := Imported(res.ID, p); err != nil {
				t.Fatal(err)
			}
			switch how {
			case "delete":
				err = provider.DeleteGroup(res.ID)
			case "deletes":
				err = provider.DeleteGroups([]string{res.ID})
			case "mirror":
				ps, _ := provider.Stored()
				gs, _ := provider.StoredGroups()
				err = provider.Mirror(ps, slices.DeleteFunc(gs, func(g provider.Group) bool { return g.ID == res.ID }))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := Sources()[res.ID]; ok {
				t.Fatalf("removed by %s, still from %s", how, m.ID)
			}
			if err := provider.SaveGroup(provider.Group{ID: res.ID, Name: res.ID, Members: []string{"myrelay/x"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := Publish(ctx, "mine", Meta{Title: "Theirs", Public: true}); err != nil {
				t.Fatal(err)
			}
			updates.Lock()
			updates.at = updates.at.AddDate(0, 0, -1)
			updates.Unlock()
			if u, err := Updates(ctx); err != nil || len(u) != 0 {
				t.Errorf("the new %s is told of the removed one's link: %v %v", res.ID, u, err)
			}
			if how == "mirror" {
				if _, ok := Shared()["mine"]; !ok {
					t.Error("the mirror forgot a group it kept")
				}
			}
		})
	}
}

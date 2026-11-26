package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// laincatx on X: the Routing view's Copy as text answers the group as
// text with no key in it, and Import a group adds it back as a group of
// the user's, saying what it left out; text that is no shared group is
// refused in words.
func TestGroupShareAndImport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const key = "sk-gui-share-secret-42"
	if err := provider.Save(provider.Provider{ID: "a", Name: "a", Key: key, Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "duo", Name: "Duo", Members: []string{"a/m1", "a/m2"}, Routing: provider.Ordered,
		Rules: []provider.Rule{{Use: "a/m2", Compact: true}}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	post := func(action string, body any) (int, groupsJSON, string) {
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/groups/"+action, strings.NewReader(string(raw))))
		var st groupsJSON
		_ = json.Unmarshal(w.Body.Bytes(), &st)
		return w.Code, st, w.Body.String()
	}

	code, st, body := post("share", map[string]string{"id": "duo"})
	if code != 200 || !strings.Contains(st.Shared, `"routing-group"`) {
		t.Fatalf("share: %d %s", code, body)
	}
	if strings.Contains(st.Shared, key) {
		t.Fatalf("the shared text has the provider's key: %s", st.Shared)
	}

	// the same text with a member this magpie has no provider of
	shared := strings.Replace(st.Shared, `"members":["a/m1","a/m2"]`, `"members":["a/m1","a/m2","gone/m9"]`, 1)
	if shared == st.Shared {
		t.Fatalf("the members aren't spelt as expected: %s", st.Shared)
	}
	code, st, body = post("import", map[string]string{"text": shared})
	if code != 200 || st.Imported == nil || st.Imported.ID != "duo-2" || strings.Join(st.Imported.Missing, ",") != "gone/m9" {
		t.Fatalf("import: %d %s", code, body)
	}
	g, _, ok := provider.FindGroup("group/duo-2")
	if !ok || strings.Join(g.Members, ",") != "a/m1,a/m2" || len(g.Rules) != 1 {
		t.Fatalf("imported %+v", g)
	}
	if !strings.Contains(body, `"duo-2"`) {
		t.Fatalf("the answer doesn't list the group added: %s", body)
	}

	// why, for the page to say in the reader's language
	if code, _, body = post("import", map[string]string{"text": "a group, honest"}); code == 200 || !strings.Contains(body, "not a routing group shared from magpie") ||
		!strings.Contains(body, `"share":"not_shared"`) {
		t.Fatalf("bad text: %d %s", code, body)
	}
}

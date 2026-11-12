package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

func adminDo(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	serveAdmin(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

// ljayic on X: gateway keys are managed through the API by the app's own
// handlers, and what tells a secret is not served.
func TestAdminAPIGatewayKeys(t *testing.T) {
	sandboxHome(t)
	w := adminDo(t, "POST", "/gateway-keys/add-key", `{"name":"Laptop"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var added struct {
		Secret string
		Keys   []access.Key
	}
	if err := json.Unmarshal(w.Body.Bytes(), &added); err != nil || added.Secret == "" || len(added.Keys) != 1 {
		t.Fatalf("add-key: %v %s", err, w.Body)
	}
	id := added.Keys[0].ID
	if w := adminDo(t, "POST", "/gateway-keys/rename-key", `{"key":"`+id+`","name":"Main"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	list := adminDo(t, "GET", "/gateway-keys", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), "Main") {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	if strings.Contains(list.Body.String(), added.Secret) {
		t.Fatal("the list tells a key's secret")
	}
	// what is left out is a 404, whatever the app has
	for _, c := range []struct{ method, path string }{
		{"POST", "/gateway-keys/copy-key"},
		{"POST", "/gateway-keys/admin-key"},
		{"POST", "/provider/key"},
		{"POST", "/provider/show"},
		{"POST", "/provider/restartapp"},
		{"GET", "/usage"},
		{"GET", "/caller-keys"},
		{"POST", "/api/provider/save"},
	} {
		if w := adminDo(t, c.method, c.path, `{"key":"`+id+`","admin":"any"}`); w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", c.method, c.path, w.Code)
		}
	}
	// and a key can't be given the API through it
	if who, _ := access.Authenticate(added.Secret); who.Admin != "" {
		t.Errorf("the API gave a key Admin: %q", who.Admin)
	}
	if w := adminDo(t, "POST", "/gateway-keys/remove-key", `{"key":"`+id+`"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if keys, _ := access.List(); len(keys) != 0 {
		t.Errorf("remove-key left %d keys", len(keys))
	}
}

func TestAdminAPIProviders(t *testing.T) {
	sandboxHome(t)
	const canary = "sk-canary-0123456789abcdef"
	w := adminDo(t, "POST", "/provider/save", `{"id":"relay","name":"Relay","chat":"https://relay.example/v1","key":"`+canary+`"}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	list := adminDo(t, "GET", "/providers", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), "relay") {
		t.Fatalf("providers: %d %.300s", list.Code, list.Body)
	}
	if strings.Contains(list.Body.String(), canary) || strings.Contains(w.Body.String(), canary) {
		t.Fatal("a saved key is told")
	}
	if w := adminDo(t, "POST", "/provider/delete", `{"id":"relay"}`); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if list := adminDo(t, "GET", "/providers", ""); strings.Contains(list.Body.String(), `"id":"relay"`) {
		t.Error("delete left the provider")
	}
}

// Signing in returns the vendor's page and the code, opens nothing, and
// takes no password.
func TestAdminAPISignInOpensNothing(t *testing.T) {
	sandboxHome(t)
	w := adminDo(t, "POST", "/signin", `{"agent":"no-such-agent"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown agent: %d %s", w.Code, w.Body)
	}
	if w := adminDo(t, "POST", "/signin", `{`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", w.Code)
	}
}

// Every route the API serves is one the app has, so it can't list a
// route that went away.
func TestAdminRoutesEveryAPIRoute(t *testing.T) {
	sandboxHome(t)
	app := http.NewServeMux()
	providerRoutes(app, nil)
	callerKeyRoutes(app)
	for _, ar := range adminRoutes {
		method, path, _ := strings.Cut(ar.pattern, " ")
		to := path
		if ar.to != "" {
			to = ar.to
		}
		to = strings.NewReplacer("{action}", "x", "{id}", "x").Replace(to)
		r := httptest.NewRequest(method, "/api"+to, strings.NewReader("{}"))
		if _, pattern := app.Handler(r); pattern == "" {
			t.Errorf("%s: the app has no route /api%s", ar.pattern, to)
		}
	}
}

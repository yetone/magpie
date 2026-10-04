package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A group whose members are a pattern (#766) sends a request to a model the
// pattern matches, picks up one the vendor lists later without the group
// being saved again, and passes over a matched one switched off.
func TestPatternGroupRoutes(t *testing.T) {
	fresh(t)
	a, b := &keyed{}, &keyed{}
	serveOn(t, "or", "ka", []string{"x", "y:free"}, a)
	serveOn(t, "zen", "kb", []string{"z"}, b)
	if err := provider.SaveGroup(provider.Group{Name: "Free", Members: []string{"*:free"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	const req = `{"model":"group/free","messages":[{"role":"user","content":"hi"}]}`
	code, body := postAs(t, s, "", req)
	if code != 200 || !strings.Contains(body, "from ka") || len(b.tried) != 0 {
		t.Fatalf("to or/y:free: %d %s", code, body)
	}
	// another provider's free model, listed after the group was made
	serveOn(t, "zen", "kb", []string{"z", "w:free"}, b)
	g, ms, _ := provider.FindGroup("group/free")
	if len(ms) != 2 || ms[1].Provider.ID != "zen" || ms[1].Model != "w:free" {
		t.Fatalf("members: %+v", ms)
	}
	// the first switched off, the new one answers
	g.Off = []string{"or/y:free"}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	a.tried = nil
	code, body = postAs(t, s, "", req)
	if code != 200 || !strings.Contains(body, "from kb") || len(a.tried) != 0 {
		t.Fatalf("to zen/w:free: %d %s, or tried %v", code, body, a.tried)
	}
	// and /v1/models lists the group
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), `"group/free"`) {
		t.Fatal("group/free not listed")
	}
}

// A group whose patterns match nothing served now is named as such, not
// answered with every model magpie has.
func TestEmptyPatternGroupSaysSo(t *testing.T) {
	fresh(t)
	if err := provider.SaveGroup(provider.Group{ID: "none", Name: "None", Match: []string{"nobody/*"}}); err != nil {
		t.Fatal(err)
	}
	g, ok := emptyGroup("group/none")
	if !ok {
		t.Fatal("group/none not found as empty")
	}
	if msg := emptyGroupError(g); !strings.Contains(msg, "nobody/*") || !strings.Contains(msg, "match no model") {
		t.Fatal(msg)
	}
}

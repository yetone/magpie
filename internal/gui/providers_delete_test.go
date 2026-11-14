package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// providers/delete removes the providers picked together (eiyuuzaki2333 on
// X), each as the editor's Remove does: gone from the list and kept among
// the deleted, so their calls stay theirs. One not picked stays. One that
// can't be removed is named in unremoved while the rest go; none removed
// is refused.
func TestProvidersDeleteSeveral(t *testing.T) {
	sandboxHome(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := provider.Save(provider.Provider{ID: id, Name: "Relay " + id, Key: "k" + id, Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	del := func(body string) (int, providersJSON) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/providers/delete", strings.NewReader(body)))
		var st providersJSON
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, st
	}
	ids := func(st providersJSON) (out []string) {
		for _, p := range st.Providers {
			out = append(out, p.ID)
		}
		return out
	}
	have := func() (out []string) {
		for _, p := range provider.All() {
			out = append(out, p.ID)
		}
		return out
	}
	deleted := func() (out []string) {
		b, err := os.ReadFile(provider.Path())
		if err != nil {
			t.Fatal(err)
		}
		var f struct{ Deleted []struct{ ID string } }
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Deleted {
			out = append(out, d.ID)
		}
		return out
	}

	code, st := del(`{"ids":["a","c","a"]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got := ids(st); slices.Contains(got, "a") || slices.Contains(got, "c") || !slices.Contains(got, "b") || !slices.Contains(got, "d") {
		t.Fatalf("listed after: %v", got)
	}
	if got := have(); !slices.Equal(got, []string{"b", "d"}) {
		t.Fatalf("saved after: %v", got)
	}
	if got := deleted(); !slices.Contains(got, "a") || !slices.Contains(got, "c") {
		t.Fatalf("not kept among the deleted, as a single Remove keeps them: %v", got)
	}
	if len(st.Unremoved) != 0 {
		t.Fatalf("unremoved: %+v", st.Unremoved)
	}

	// one not there: the rest go, it is named
	code, st = del(`{"ids":["gone","b"]}`)
	if code != http.StatusOK || len(st.Unremoved) != 1 || st.Unremoved[0].ID != "gone" || st.Unremoved[0].Error == "" {
		t.Fatalf("status %d, unremoved %+v", code, st.Unremoved)
	}
	if got := have(); !slices.Equal(got, []string{"d"}) {
		t.Fatalf("saved after: %v", got)
	}

	// none removed, or none picked: refused, d stays
	for _, body := range []string{`{"ids":["gone"]}`, `{"ids":[]}`, `{}`} {
		if code, _ := del(body); code == http.StatusOK {
			t.Fatalf("%s: removed nothing and said ok", body)
		}
	}
	if got := have(); !slices.Equal(got, []string{"d"}) {
		t.Fatalf("saved after: %v", got)
	}
}

package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// #6582: an API whose model list names a model twice. A provider asked at
// the URL its list is at (ModelsURL), as one asked at its base, lists each
// model once: in Fetch's answer, the add form's List and Available, which
// the pickers, /v1/models and the agents' lists are made from.
func TestProviderListsEachModelOnce(t *testing.T) {
	isolateFetchHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5.6"},{"id":"glm-5.3"},{"id":"gpt-5.6"},{"id":"glm-5.3"}]}`)
	}))
	t.Cleanup(srv.Close)
	for _, p := range []Provider{
		{ID: "at-base", Name: "At base", Chat: srv.URL + "/v1", Key: "k"},
		{ID: "at-url", Name: "At URL", Chat: srv.URL + "/v1", ModelsURL: srv.URL + "/v1/models", Key: "k"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
		q, err := Find(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		once := func(what string, ms []catalog.Model) {
			t.Helper()
			if len(ms) != 2 || ms[0].ID != "gpt-5.6" || ms[1].ID != "glm-5.3" {
				ids := []string{}
				for _, m := range ms {
					ids = append(ids, m.ID)
				}
				t.Errorf("%s %s = %v, want [gpt-5.6 glm-5.3]", p.ID, what, ids)
			}
		}
		ms, err := q.Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		once("Fetch", ms)
		ls, err := q.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		once("List", ls)
		q, _ = Find(p.ID)
		once("Available", q.Available())
	}
}

package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// A vendor that lists its models away from the base URL requests go to
// (#67, Xiaomi MiMo) has them fetched from the models URL it was given,
// with its key; the base URLs are left as they were.
func TestFetchFromModelsURL(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	plan := httptest.NewServer(http.NotFoundHandler()) // serves chat, lists nothing
	defer plan.Close()
	var auth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"object":"list","data":[{"id":"mimo-v2.6-pro"},{"id":"mimo-v2.6-flash"}]}`))
	}))
	defer api.Close()

	p := Provider{ID: "mimo", Name: "MiMo", Chat: plan.URL + "/v1", Key: "sk-m"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("listed without a models URL")
	}
	p.ModelsURL = api.URL + "/v1/models"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	q, _ := Find("mimo")
	ms, err := q.Fetch(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "mimo-v2.6-pro" {
		t.Fatalf("%v %v", ms, err)
	}
	if auth != "Bearer sk-m" {
		t.Errorf("asked with %q", auth)
	}
	if got := q.Available(); len(got) != 2 {
		t.Errorf("available %v", got)
	}
	if q, _ = Find("mimo"); q.Chat != plan.URL+"/v1" || q.ModelsURL != api.URL+"/v1/models" {
		t.Errorf("chat %q, models %q", q.Chat, q.ModelsURL)
	}
}

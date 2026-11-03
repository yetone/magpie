package gateway

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// axelburks on Discord: a Volcengine Ark key's Seedance models are ticked
// in its editor as its Seedream ones are. One unticked there
// (HiddenVideomakers) leaves generate_video's list, another magpie's list
// and Automatic, and still makes its video when named; its image models
// are left as they were.
func TestHiddenVideomakerLeavesListsButStillFilms(t *testing.T) {
	fresh(t)
	was := isArk
	isArk = func(p provider.Provider) bool { return p.ID == "ark" }
	t.Cleanup(func() { isArk = was })
	if err := provider.Save(provider.Provider{ID: "ark", Name: "Volcengine Ark", Chat: "https://ark.example/api/v3", Key: "key",
		Models: []string{"doubao-seed-2-1-pro-260915"}}); err != nil {
		t.Fatal(err)
	}
	// the list as Ark gives it now: Seedance marked as video models, no
	// longer among the chat ones (catalog's TestFetchArkMarksVideoModels)
	if err := catalog.SaveLive("ark", "https://ark.example/api/v3", []catalog.Model{
		{ID: "doubao-seed-2-1-pro-260915"},
		{ID: "doubao-seedance-2-0-260128", Films: true},
		{ID: "doubao-seedance-2-0-fast-260128", Films: true},
		{ID: "doubao-seedream-5-0-pro-260628", Draws: true},
	}); err != nil {
		t.Fatal(err)
	}
	set := func(hidden []string) provider.Provider {
		t.Helper()
		found, err := provider.Find("ark")
		if err != nil {
			t.Fatal(err)
		}
		p := *found
		p.HiddenVideomakers = hidden
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		found, _ = provider.Find("ark")
		return *found
	}
	ids := func(ms []catalog.Model) (out []string) {
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return
	}
	p := set(nil)
	if got := ids(OfferedVideomakers(p)); !slices.Equal(got, []string{"doubao-seedance-2-0-260128", "doubao-seedance-2-0-fast-260128"}) {
		t.Fatalf("offered with none hidden %v", got)
	}

	p = set([]string{"doubao-seedance-2-0-260128"})
	if !slices.Equal(p.HiddenVideomakers, []string{"doubao-seedance-2-0-260128"}) {
		t.Fatalf("saved %v", p.HiddenVideomakers)
	}
	if got := ids(OfferedVideomakers(p)); !slices.Equal(got, []string{"doubao-seedance-2-0-fast-260128"}) {
		t.Fatalf("offered %v", got)
	}
	if got := ids(Videomakers(p)); !slices.Contains(got, "doubao-seedance-2-0-260128") {
		t.Fatalf("videomakers lost the hidden one: %v", got)
	}
	if got := ids(OfferedDrawers(p)); !slices.Equal(got, []string{"doubao-seedream-5-0-pro-260628"}) {
		t.Fatalf("a hidden video model changed the image models: %v", got)
	}
	var listed []string
	for _, o := range videomakerObjects(httptest.NewRequest(http.MethodGet, "/v1/models", nil)) {
		listed = append(listed, o["id"].(string))
	}
	if !slices.Equal(listed, []string{"ark/doubao-seedance-2-0-fast-260128"}) {
		t.Fatalf("video objects %v", listed)
	}
	if a := AutoVideomaker(); a != "ark/doubao-seedance-2-0-fast-260128" {
		t.Fatalf("automatic %q", a)
	}
	if all, def := mediaModels(true); !slices.Equal(all, []string{"ark/doubao-seedance-2-0-fast-260128"}) || def != all[0] {
		t.Fatalf("generate_video's list %v %q", all, def)
	}
	// named, it still makes its video, at Ark's tasks
	if q, id, ok := bareVideomaker("doubao-seedance-2-0-260128"); !ok || q.ID != "ark" || id != "doubao-seedance-2-0-260128" {
		t.Fatalf("bare hidden: %v %q %v", q.ID, id, ok)
	}
	if !arkFilms(p, "doubao-seedance-2-0-260128") {
		t.Fatal("a hidden Seedance isn't made at Ark's tasks")
	}

	p = set([]string{"doubao-seedance-2-0-260128", "doubao-seedance-2-0-fast-260128"})
	if a := AutoVideomaker(); a != "" {
		t.Fatalf("automatic with every model hidden: %q", a)
	}
}

package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
)

// Winnie7572 on X, #7496: OpenCode Go serves its free models with every
// window used up, and its own list says which they are: "monthly_usage":
// null under "go", where a model its plans count has each plan's monthly
// requests. Read from Go's reply of 2026-11-26 as it was sent, the free
// ones are Free and no other is; a "go" of another shape, or one leaving
// monthly_usage out, says nothing, and loses no model.
func TestFetchOpenCodeGoFree(t *testing.T) {
	list, err := os.ReadFile("testdata/opencode-go-models-2026-11-26.json")
	if err != nil {
		t.Fatal(err)
	}
	odd := `{"data":[{"id":"str","go":"free"},{"id":"empty","go":{}},{"id":"obj","go":{"monthly_usage":{}}},
	  {"id":"arr","go":[null]},{"id":"num","go":{"monthly_usage":0}},{"id":"none"},{"id":"nul","go":{"monthly_usage":null}}]}`
	for _, c := range []struct {
		body       string
		n          int
		free, paid []string
	}{
		{string(list), 34, []string{"beam-free", "hungerg-free", "hy4-flash-free", "iceman-contributor-free"},
			[]string{"deepseek-v4.1-flash", "glm-5.3", "hy4", "qwen3-embedding-8b"}},
		{odd, 7, []string{"nul"}, []string{"str", "empty", "obj", "arr", "num", "none"}},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte(c.body))
		}))
		ms, _, err := FetchAt(context.Background(), srv.URL+"/zen/go/v1", "k", false, nil)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) != c.n {
			t.Errorf("%d models, want %d", len(ms), c.n)
		}
		var free []string
		ids := map[string]bool{}
		for _, m := range ms {
			ids[m.ID] = true
			if m.Free {
				free = append(free, m.ID)
			}
		}
		slices.Sort(free)
		if !slices.Equal(free, c.free) {
			t.Errorf("free %v, want %v", free, c.free)
		}
		for _, id := range c.paid {
			if !ids[id] {
				t.Errorf("%s not listed", id)
			}
		}
	}
}

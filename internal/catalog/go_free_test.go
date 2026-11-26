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
		{string(list), 37, []string{"beam-free", "hungerg-free", "hy4-flash-free", "iceman-contributor-free"},
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

// OpenCode Go's list marks its System One models with "type": "decision".
// They stay in the list for routing groups, but none is offered as a chat
// model.
func TestFetchOpenCodeGoDecisions(t *testing.T) {
	list, err := os.ReadFile("testdata/opencode-go-models-2026-11-26.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write(list)
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/zen/go/v1", "k", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Model{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	for _, id := range []string{"jev-1.14", "gemini-decision-1", "gpt-6-luna-decision"} {
		m, ok := byID[id]
		if !ok || !m.Decides {
			t.Errorf("%s isn't marked as a decision model: %+v", id, m)
		}
		if !slices.Contains(m.OutputModalities, "decision") {
			t.Errorf("%s outputs %v", id, m.OutputModalities)
		}
	}
	if m := byID["deepseek-v4.1-flash"]; m.Decides {
		t.Error("a chat model was marked as a decision model")
	}
}

// Zen's list marks its decision models the same way, as it sent them on
// 11-26: the three Jevs, Gemini Decision 1 and GPT-6 Luna Decision, which
// it left out before since none answers with text.
func TestFetchOpenCodeZenDecisions(t *testing.T) {
	list, err := os.ReadFile("testdata/opencode-zen-models-2026-11-26.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Write(list)
	}))
	defer srv.Close()
	ms, _, err := FetchAt(context.Background(), srv.URL+"/zen/v1", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	var decides []string
	for _, m := range ms {
		if m.Decides {
			decides = append(decides, m.ID)
		}
	}
	slices.Sort(decides)
	if want := []string{"gemini-decision-1", "gpt-6-luna-decision", "jev-1.13", "jev-1.13-free", "jev-1.14"}; !slices.Equal(decides, want) {
		t.Errorf("decision models %v, want %v", decides, want)
	}
	if !slices.ContainsFunc(ms, func(m Model) bool { return m.ID == "deepseek-v4.1-flash" && !m.Decides }) {
		t.Error("a chat model is missing or marked as a decision model")
	}
}

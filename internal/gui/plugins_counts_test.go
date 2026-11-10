package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/stats"
)

// Discover's counts are asked once an hour; a like is taken only for a
// plugin installed here that Discover lists, is kept on this computer,
// and moves the count held at once.
func TestPluginCountsAndLikes(t *testing.T) {
	h := builtinHome(t)
	asked, likes := 0, map[string]bool{}
	wasAsk, wasLike, wasPublic := pluginCountsAsk, pluginLike, pluginPublic
	t.Cleanup(func() {
		pluginCountsAsk, pluginLike, pluginPublic = wasAsk, wasLike, wasPublic
		countsGot, countsAt = nil, countsAt.AddDate(-1, 0, 0)
	})
	countsGot = nil
	fail := false
	pluginCountsAsk = func(context.Context) (map[string]stats.Counts, error) {
		asked++
		if fail {
			return nil, errors.New("offline")
		}
		return map[string]stats.Counts{"opencode-a": {Users: 12, Likes: 3}}, nil
	}
	pluginLike = func(_ context.Context, name string, on bool) (int, error) {
		likes[name] = on
		if on {
			return 4, nil
		}
		return 3, nil
	}
	pluginPublic = func(context.Context) []string { return []string{"opencode-a"} }

	get := func() (out struct {
		Plugins map[string]stats.Counts `json:"plugins"`
		Liked   []string                `json:"liked"`
		Error   string                  `json:"error"`
	}) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/plugins/counts", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("counts: %d %s", rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return
	}
	like := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/plugins/like", strings.NewReader(body)))
		return rec
	}

	if c := get(); c.Plugins["opencode-a"] != (stats.Counts{Users: 12, Likes: 3}) || len(c.Liked) != 0 {
		t.Fatalf("first: %+v", c)
	}
	get()
	if asked != 1 {
		t.Fatalf("asked %d times within the hour", asked)
	}
	if rec := like(`{"name":"opencode-b","on":true}`); rec.Code == http.StatusOK || likes["opencode-b"] {
		t.Fatalf("a plugin not installed here was liked: %d %s", rec.Code, rec.Body)
	}
	if rec := like(`{"name":"opencode-a","on":true}`); rec.Code != http.StatusOK || !likes["opencode-a"] {
		t.Fatalf("like: %d %s", rec.Code, rec.Body)
	}
	if c := get(); c.Plugins["opencode-a"].Likes != 4 || !slices.Equal(c.Liked, []string{"opencode-a"}) {
		t.Fatalf("after a like: %+v", c)
	}
	// taking it back needs no install: a plugin removed since keeps no like
	pluginPublic = func(context.Context) []string { return nil }
	if rec := like(`{"name":"opencode-a","on":false}`); rec.Code != http.StatusOK || likes["opencode-a"] {
		t.Fatalf("unlike: %d %s", rec.Code, rec.Body)
	}
	if c := get(); c.Plugins["opencode-a"].Likes != 3 || len(c.Liked) != 0 {
		t.Fatalf("after unlike: %+v", c)
	}
	// an hour on, a failed ask keeps what was held
	countsAt = countsAt.Add(-2 * 60 * 60 * 1e9)
	fail = true
	if c := get(); c.Plugins["opencode-a"].Users != 12 || c.Error == "" {
		t.Fatalf("a failed ask: %+v", c)
	}
}

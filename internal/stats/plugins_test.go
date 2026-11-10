package stats

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// The day's plugins go to usemagpie.ai with the day's event, under its
// id, as plugin.Public has them; not when the user turned off what magpie
// is used with, nor when stats are off, nor with no plugin to name. A like
// goes under the same id.
func TestSendPlugins(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("MAGPIE_NO_STATS", "")
	usage = func() Usage { return Usage{} }
	plugins := []string{"@magpie-community/opencode-cursor-auth", "github:alice/tagged"}
	was := publicPlugins
	publicPlugins = func(context.Context) []string { return plugins }
	defer func() { usage, publicPlugins = readUsage, was }()
	type report struct {
		ID      string   `json:"id"`
		Plugins []string `json:"plugins"`
		Plugin  string   `json:"plugin"`
		On      bool     `json:"on"`
	}
	var reports, likes []report
	var posthogID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/batch/":
			var b struct {
				Batch []map[string]any `json:"batch"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			posthogID = b.Batch[0]["distinct_id"].(string)
		case "/api/plugins/report":
			var b report
			json.NewDecoder(r.Body).Decode(&b)
			reports = append(reports, b)
		case "/api/plugins/like":
			var b report
			json.NewDecoder(r.Body).Decode(&b)
			likes = append(likes, b)
			w.Write([]byte(`{"likes":7}`))
		default:
			t.Errorf("asked %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_STATS_HOST", srv.URL)
	t.Setenv("MAGPIE_SITE_HOST", srv.URL)
	day := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

	if err := Send(context.Background(), "0.1.300", "app", day); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].ID != posthogID || len(posthogID) != 32 || !slices.Equal(reports[0].Plugins, plugins) {
		t.Fatalf("reports %+v (event's id %q)", reports, posthogID)
	}
	// once a day
	Send(context.Background(), "0.1.300", "app", day.Add(time.Hour))
	if len(reports) != 1 {
		t.Fatalf("sent again the same day: %+v", reports)
	}

	n, err := Like(context.Background(), "github:alice/tagged", true)
	if err != nil || n != 7 || len(likes) != 1 || likes[0].ID != posthogID || likes[0].Plugin != "github:alice/tagged" || !likes[0].On {
		t.Fatalf("like: %d %v %+v", n, err, likes)
	}

	// none to name: nothing goes
	plugins = nil
	Send(context.Background(), "0.1.300", "app", day.Add(24*time.Hour))
	plugins = []string{"opencode-x"}
	settings.Save(settings.Settings{NoUsageStats: true})
	Send(context.Background(), "0.1.300", "app", day.Add(48*time.Hour))
	settings.Save(settings.Settings{NoStats: true})
	Send(context.Background(), "0.1.300", "app", day.Add(72*time.Hour))
	if len(reports) != 1 {
		t.Fatalf("sent with nothing to name, or turned off: %+v", reports)
	}
}

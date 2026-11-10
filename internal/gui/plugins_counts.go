package gui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/stats"
)

// Discover's counts, from usemagpie.ai (stats): each plugin's users and
// likes, asked at most every hour, and the plugins liked here, kept on
// this computer.

var (
	countsMu  sync.Mutex
	countsAt  time.Time
	countsGot map[string]stats.Counts
)

// pluginCountsAsk and pluginLike are stats', for tests to stand in for.
var (
	pluginCountsAsk = stats.PluginCounts
	pluginLike      = stats.Like
	pluginPublic    = plugin.Public
)

func likedPath() string { return filepath.Join(settings.Dir(), "plugin-likes.json") }

// liked is the plugins liked on this computer; none when the file can't be
// read, which only leaves the hearts empty.
func liked() []string {
	var l []string
	if b, err := os.ReadFile(likedPath()); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	if l == nil {
		l = []string{}
	}
	return l
}

func saveLiked(l []string) error {
	b, _ := json.Marshal(l)
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		return err
	}
	tmp := likedPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, likedPath())
}

// pluginCounts is the counts held, asked again when an hour old; a failed
// ask keeps what was held, tried again in a minute.
func pluginCounts(ctx context.Context) (map[string]stats.Counts, error) {
	countsMu.Lock()
	defer countsMu.Unlock()
	if countsGot != nil && time.Since(countsAt) < time.Hour {
		return countsGot, nil
	}
	got, err := pluginCountsAsk(ctx)
	if err != nil {
		countsAt = time.Now().Add(-time.Hour + time.Minute)
		return countsGot, err
	}
	countsGot, countsAt = got, time.Now()
	return got, nil
}

func pluginCountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plugins/counts", func(rw http.ResponseWriter, r *http.Request) {
		got, err := pluginCounts(r.Context())
		out := map[string]any{"plugins": got, "liked": liked()}
		if got == nil {
			out["plugins"] = map[string]stats.Counts{}
		}
		if err != nil {
			out["error"] = err.Error()
		}
		writeJSON(rw, out)
	})
	// a like, or taking one back: only a plugin installed here, which
	// Discover lists, can be liked
	mux.HandleFunc("POST /api/plugins/like", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string `json:"name"`
			On   bool   `json:"on"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil || in.Name == "" {
			fail(rw, errors.New("which plugin?"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if in.On && !slices.Contains(pluginPublic(ctx), in.Name) {
			fail(rw, errors.New("only a plugin installed here can be liked"))
			return
		}
		n, err := pluginLike(ctx, in.Name, in.On)
		if err != nil {
			fail(rw, err)
			return
		}
		l := slices.DeleteFunc(liked(), func(x string) bool { return x == in.Name })
		if in.On {
			l = append(l, in.Name)
		}
		if err := saveLiked(l); err != nil {
			fail(rw, err)
			return
		}
		countsMu.Lock()
		if countsGot != nil {
			c := countsGot[in.Name]
			c.Likes = n
			countsGot[in.Name] = c
		}
		countsMu.Unlock()
		writeJSON(rw, map[string]any{"likes": n, "liked": l})
	})
}

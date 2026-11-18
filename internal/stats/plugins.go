package stats

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/update"
)

// Discover's counts: how many computers use each plugin, and its likes.
// With the day's event, unless the user turned off what magpie is used
// with, usemagpie.ai is told which plugins Discover lists are installed
// and on here (plugin.Public: never a private repository, a folder or a
// package no list names), under the same random id; it counts the
// computers that said so in the last seven days. The Library's MCP
// servers go the same way, for the market's order and counts: those the
// market lists from what ships with magpie, by their market ids
// (library.PublicServers), never a server of the user's own. A like is
// the user's own act, sent when they make it.

// Site takes the day's plugins and the likes; MAGPIE_SITE_HOST points
// them elsewhere, for testing.
func Site() string {
	if h := os.Getenv("MAGPIE_SITE_HOST"); h != "" {
		return strings.TrimRight(h, "/")
	}
	return update.Site
}

// publicPlugins is plugin.Public, and publicServers
// library.PublicServers; tests put their own.
var (
	publicPlugins = plugin.Public
	publicServers = library.PublicServers
)

// sendPlugins tells usemagpie.ai the plugins installed here Discover
// lists, and the Library's servers the market lists; nothing when there
// are none.
func sendPlugins(ctx context.Context, id string) error {
	names, servers := publicPlugins(ctx), publicServers()
	if len(names) == 0 && len(servers) == 0 {
		return nil
	}
	if len(names) > most {
		names = names[:most]
	}
	if len(servers) > most {
		servers = servers[:most]
	}
	if names == nil {
		names = []string{}
	}
	body := map[string]any{"id": id, "plugins": names}
	if len(servers) > 0 {
		body["servers"] = servers
	}
	return post(ctx, "/api/plugins/report", body, nil)
}

// Counts is what usemagpie.ai says of each plugin: the computers that
// had it in the last seven days, and its likes.
type Counts struct {
	Users int `json:"users,omitempty"`
	Likes int `json:"likes,omitempty"`
}

// Board is what usemagpie.ai counts: each plugin's, by Discover's key,
// and each MCP server's, by its market id.
type Board struct {
	Plugins map[string]Counts `json:"plugins"`
	Servers map[string]Counts `json:"servers"`
}

// PluginCounts asks usemagpie.ai each plugin's and each server's counts.
func PluginCounts(ctx context.Context) (Board, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", Site()+"/api/plugins/stats", nil)
	if err != nil {
		return Board{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return Board{}, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return Board{}, fmt.Errorf("plugin counts: %s", res.Status)
	}
	var out Board
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 4<<20)).Decode(&out); err != nil {
		return Board{}, err
	}
	if out.Plugins == nil {
		out.Plugins = map[string]Counts{}
	}
	if out.Servers == nil {
		out.Servers = map[string]Counts{}
	}
	return out, nil
}

// Like likes the plugin Discover keys name, or takes the like back, under
// this computer's id; the answer is its likes now. The page lets only a
// plugin installed here be liked (gui), and the like says it is.
func Like(ctx context.Context, name string, on bool) (int, error) {
	id, err := installID()
	if err != nil {
		return 0, err
	}
	var out struct {
		Likes int `json:"likes"`
	}
	err = post(ctx, "/api/plugins/like", map[string]any{"id": id, "plugin": name, "on": on}, &out)
	return out.Likes, err
}

func post(ctx context.Context, path string, in any, out any) error {
	body, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", Site()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		var e struct{ Error string }
		_ = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&e)
		if e.Error != "" {
			return fmt.Errorf("usemagpie.ai: %s", e.Error)
		}
		return fmt.Errorf("usemagpie.ai: %s", res.Status)
	}
	if out != nil {
		return json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(out)
	}
	return nil
}

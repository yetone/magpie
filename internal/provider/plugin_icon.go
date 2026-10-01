package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// PluginIcon is the icon a plugin's provider shows: the picture its plugin
// gives it (the auth hook's icon, or package.json's magpie.icon), kept as
// a picture given by hand is, else the one the plugin market gives it.
func PluginIcon(pp plugin.Provider) string {
	if ic := pluginOwnIcon(pp.Icon); ic != "" {
		return ic
	}
	return plugin.Icon(pp.Spec, pp.ID)
}

// plugIcons are the pictures plugins gave, by their hash: the Icon value
// each is kept as, and when one was last tried (a URL being fetched or
// that failed, a data URI that isn't a picture).
var plugIcons struct {
	sync.Mutex
	from  string // the file they were read from
	kept  map[string]string
	tried map[string]time.Time
}

// fetchPluginIcon fetches an https picture as an import link's is: a
// public host only, at most MaxIcon. Tests point it elsewhere.
var fetchPluginIcon = FetchIcon

func plugIconsPath() string { return filepath.Join(filepath.Dir(Path()), "plugin-icons.json") }

func loadPlugIcons() {
	if plugIcons.from == plugIconsPath() {
		return
	}
	plugIcons.from = plugIconsPath()
	plugIcons.kept, plugIcons.tried = map[string]string{}, map[string]time.Time{}
	if b, err := os.ReadFile(plugIcons.from); err == nil {
		_ = json.Unmarshal(b, &plugIcons.kept)
	}
}

func keepPlugIcon(k, icon string) {
	plugIcons.kept[k] = icon
	delete(plugIcons.tried, k)
	if b, err := json.Marshal(plugIcons.kept); err == nil {
		_ = os.MkdirAll(filepath.Dir(plugIcons.from), 0o755)
		_ = os.WriteFile(plugIcons.from, b, 0o644)
	}
}

// pluginOwnIcon is the Icon value of the picture a plugin gave, "" while
// there is none to show. A data:image URI is kept at once; an https URL
// is fetched in the background, through FetchIcon's checks, and shows
// once it is in (a failure is tried again in an hour). Anything else is
// refused.
func pluginOwnIcon(said string) string {
	if said == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(said))
	k := hex.EncodeToString(sum[:16])
	plugIcons.Lock()
	defer plugIcons.Unlock()
	loadPlugIcons()
	if ic := plugIcons.kept[k]; ic != "" {
		if f := IconFile(strings.TrimPrefix(ic, iconPrefix)); f != "" {
			if _, err := os.Stat(f); err == nil {
				return ic
			}
		}
	}
	if t, ok := plugIcons.tried[k]; ok && time.Since(t) < time.Hour {
		return ""
	}
	plugIcons.tried[k] = time.Now()
	if len(said) > 11 && strings.EqualFold(said[:11], "data:image/") {
		if b := dataURI(said[len("data:"):]); b != nil {
			if ic, err := StoreIcon(b); err == nil {
				keepPlugIcon(k, ic)
				return ic
			}
		}
		return ""
	}
	u, err := iconURL(said)
	if err != nil {
		return ""
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ic, err := fetchPluginIcon(ctx, u)
		if err != nil {
			return
		}
		plugIcons.Lock()
		keepPlugIcon(k, ic)
		plugIcons.Unlock()
	}()
	return ""
}

// keptPluginIcons are the names of the stored pictures plugins gave, which
// pruneIcons leaves.
func keptPluginIcons() []string {
	plugIcons.Lock()
	defer plugIcons.Unlock()
	loadPlugIcons()
	var out []string
	for _, ic := range plugIcons.kept {
		if name, ok := strings.CutPrefix(ic, iconPrefix); ok {
			out = append(out, name)
		}
	}
	return out
}

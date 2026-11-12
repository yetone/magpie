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
	return MarketIcon(plugin.Icon(pp.Spec, pp.ID))
}

// MarketIcon is an icon the plugin market (registry.json) gives, as the
// page can show it. A bundled icon's name is given as it is. A picture
// (an https URL or a data:image URI) is checked, fetched and kept as a
// plugin's own picture is, and is "" until it is in: the page never
// loads a remote URL itself, and an icon("https://…") would look for a
// bundled file of that name.
func MarketIcon(said string) string {
	if MarketPicture(said) {
		return pluginOwnIcon(said)
	}
	return said
}

// MarketPicture is whether a market icon is a picture to fetch or decode
// (anything with a scheme or a path in it) rather than a bundled icon's
// name.
func MarketPicture(said string) bool { return strings.ContainsAny(said, ":/") }

// PluginOwnIcon is the Icon value of a picture a plugin gave (a data:image
// URI or an https URL), "" while there is none to show: an agent a
// plugin adds shows it as a provider's does.
func PluginOwnIcon(said string) string { return pluginOwnIcon(said) }

// plugIcons are the pictures plugins gave, by their hash: the Icon value
// each is kept as, and when one was last tried (a URL being fetched or
// that failed, a data URI that isn't a picture).
var plugIcons struct {
	sync.Mutex
	from  string // the file they were read from
	kept  map[string]string
	tried map[string]time.Time
	// seen is when each kept icon's file was last found on disk: within
	// iconSeenFor it is not looked for again. /api/providers asks for
	// every plugin provider's icon, and a stat each was a fifth of its
	// time on a real account's 26 providers
	seen map[string]time.Time
	// the URLs being fetched now, each closed once its fetch is over
	busy map[string]chan struct{}
}

// fetchPluginIcon fetches an https picture as an import link's is: a
// public host only, at most MaxIcon. Tests point it elsewhere.
var fetchPluginIcon = FetchIcon

// iconSeenFor is how long a kept icon found on disk is taken to be there
// still: one removed is fetched again within it.
const iconSeenFor = time.Minute

func plugIconsPath() string { return filepath.Join(filepath.Dir(Path()), "plugin-icons.json") }

func loadPlugIcons() {
	if plugIcons.from == plugIconsPath() {
		return
	}
	plugIcons.from = plugIconsPath()
	plugIcons.kept, plugIcons.tried, plugIcons.seen = map[string]string{}, map[string]time.Time{}, map[string]time.Time{}
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
		if t, ok := plugIcons.seen[k]; ok && time.Since(t) < iconSeenFor {
			return ic
		}
		if f := IconFile(strings.TrimPrefix(ic, iconPrefix)); f != "" {
			if _, err := os.Stat(f); err == nil {
				plugIcons.seen[k] = time.Now()
				return ic
			}
		}
		delete(plugIcons.seen, k)
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
	if plugIcons.busy == nil {
		plugIcons.busy = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	plugIcons.busy[k] = done
	fetch := fetchPluginIcon
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ic, err := fetch(ctx, u)
		plugIcons.Lock()
		if err == nil {
			keepPlugIcon(k, ic)
		}
		delete(plugIcons.busy, k)
		plugIcons.Unlock()
		close(done)
	}()
	return ""
}

// RepoIcons are the Icon values of the pictures plugins not installed yet
// give in their package.json (repositories tagged magpie-plugin), checked
// and kept as an installed plugin's are: "" where there is none to show.
// An https picture not fetched before is waited for up to wait, so the
// list's first answer has it (yetone: 非官方插件显示不出来 logo); one
// slower than that shows the next time the list is asked.
func RepoIcons(said []string, wait time.Duration) []string {
	out := make([]string, len(said))
	var busy []chan struct{}
	for i, s := range said {
		out[i] = pluginOwnIcon(s)
		if out[i] == "" && s != "" {
			sum := sha256.Sum256([]byte(s))
			plugIcons.Lock()
			if ch := plugIcons.busy[hex.EncodeToString(sum[:16])]; ch != nil {
				busy = append(busy, ch)
			}
			plugIcons.Unlock()
		}
	}
	if len(busy) == 0 {
		return out
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for _, ch := range busy {
		select {
		case <-ch:
		case <-t.C:
			return refill(out, said)
		}
	}
	return refill(out, said)
}

// refill asks again for the pictures not in yet, which those fetched since
// now have
func refill(out, said []string) []string {
	for i, s := range said {
		if out[i] == "" && s != "" {
			out[i] = pluginOwnIcon(s)
		}
	}
	return out
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

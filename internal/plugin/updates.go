package plugin

// Plugin updates, as magpie keeps itself up to date: the community's
// plugins (@magpie-community/*, the ones the built-in subscriptions move
// onto) update by themselves, a little after magpie starts and every few
// hours; anyone else's new version waits for the reader, who sees a dot
// on Plugins and updates it with a click. A plugin pinned to a version
// stays on it. Updating never cuts a reply streaming through a plugin:
// the host it runs on finishes it (see Restart).

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/update"
)

// Updates is what magpie's last look at npm found for the plugins.
type Updates struct {
	Checked time.Time `json:"checked"`
	// Waiting are the plugins with a newer version that magpie leaves to
	// the reader: someone else's, or one pinned to a version.
	Waiting []Waiting `json:"waiting"`
	// Updated are the plugins magpie updated by itself, the latest last.
	Updated []Updated `json:"updated"`
}

// Waiting is a plugin with a newer version on npm than the one installed.
type Waiting struct {
	Spec    string `json:"spec"`
	Package string `json:"package"`
	Version string `json:"version"`
	Latest  string `json:"latest"`
}

// Updated is a plugin magpie updated by itself.
type Updated struct {
	Package string    `json:"package"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	At      time.Time `json:"at"`
}

const (
	// updateEvery is how often magpie looks for plugin updates
	updateEvery = 6 * time.Hour
	// keepUpdated is how many updates magpie remembers making
	keepUpdated = 20
)

// Official is whether the package is the community's, magpie's own,
// which magpie keeps up to date by itself.
func Official(pkg string) bool { return strings.HasPrefix(pkg, "@magpie-community/") }

// Pinned is whether spec names a version (or a range or a tag other than
// latest): the user chose it, and it stays.
func Pinned(spec string) bool {
	if IsPath(spec) {
		return false
	}
	v := strings.TrimPrefix(spec, Name(spec))
	return v != "" && v != "@latest"
}

var (
	updatesMu sync.Mutex
	// latestOf is the version npm has of each package, "" for one it
	// didn't tell (tests stub it)
	latestOf = func(ctx context.Context, names []string) map[string]string {
		out := map[string]string{}
		for n, i := range Info(ctx, names) {
			out[n] = i.Version
		}
		return out
	}
	// installLatest installs the package's newest version (tests stub it)
	installLatest = func(ctx context.Context, pkg string) error { return install(ctx, pkg+"@latest") }
)

func updatesPath() string { return filepath.Join(settings.Dir(), "plugin-updates.json") }

func readUpdates() Updates {
	var u Updates
	if b, err := steady.ReadFile(updatesPath()); err == nil {
		_ = json.Unmarshal(b, &u)
	}
	return u
}

// PendingUpdates is what the last look found, less what has changed since:
// a plugin updated, removed or switched off since waits no more.
func PendingUpdates() Updates {
	updatesMu.Lock()
	defer updatesMu.Unlock()
	u := readUpdates()
	have := map[string]Entry{}
	for _, e := range Load().Plugins {
		have[Name(e.Spec)] = e
	}
	u.Waiting = slices.DeleteFunc(u.Waiting, func(w Waiting) bool {
		e, ok := have[w.Package]
		return !ok || e.Off || !update.Newer(w.Latest, Installed(e.Spec))
	})
	if u.Waiting == nil {
		u.Waiting = []Waiting{}
	}
	if u.Updated == nil {
		u.Updated = []Updated{}
	}
	return u
}

// LastUpdated is the update magpie made by itself to the package since
// the time given, if it made one.
func LastUpdated(pkg string, since time.Time) (Updated, bool) {
	u := PendingUpdates()
	for i := len(u.Updated) - 1; i >= 0; i-- {
		if x := u.Updated[i]; x.Package == pkg && x.At.After(since) {
			return x, true
		}
	}
	return Updated{}, false
}

// CheckUpdates asks npm for each plugin's newest version, updates the
// community's (unpinned, switched on) to it and notes the others' as
// waiting. The plugins updated are loaded again, a reply streaming
// through the old ones finishing first.
func CheckUpdates(ctx context.Context) (Updates, error) {
	var es []Entry
	var names []string
	for _, e := range Load().Plugins {
		if !IsPath(e.Spec) {
			es = append(es, e)
			names = append(names, Name(e.Spec))
		}
	}
	latest := map[string]string{}
	if len(names) > 0 {
		latest = latestOf(ctx, names)
	}
	var waiting []Waiting
	var made []Updated
	var errs []error
	for _, e := range es {
		pkg, have, now := Name(e.Spec), Installed(e.Spec), latest[Name(e.Spec)]
		if e.Off || have == "" || now == "" || !update.Newer(now, have) {
			continue
		}
		if !Official(pkg) || Pinned(e.Spec) {
			waiting = append(waiting, Waiting{Spec: e.Spec, Package: pkg, Version: have, Latest: now})
			continue
		}
		if err := installLatest(ctx, pkg); err != nil {
			errs = append(errs, err)
			log.Printf("updating the plugin %s: %s", pkg, err)
			waiting = append(waiting, Waiting{Spec: e.Spec, Package: pkg, Version: have, Latest: now})
			continue
		}
		v := Installed(e.Spec)
		if v == "" {
			v = now
		}
		made = append(made, Updated{Package: pkg, From: have, To: v, At: time.Now().UTC().Truncate(time.Second)})
		log.Printf("updated the plugin %s from %s to %s", pkg, have, v)
	}
	updatesMu.Lock()
	u := readUpdates()
	u.Checked = time.Now().UTC().Truncate(time.Second)
	u.Waiting = waiting
	u.Updated = append(u.Updated, made...)
	if n := len(u.Updated); n > keepUpdated {
		u.Updated = u.Updated[n-keepUpdated:]
	}
	if b, err := json.MarshalIndent(u, "", "  "); err == nil {
		if err := os.MkdirAll(settings.Dir(), 0o700); err == nil {
			_ = writeWhole(updatesPath(), b)
		}
	}
	updatesMu.Unlock()
	if len(made) > 0 {
		Restart()
	}
	if len(errs) > 0 {
		return u, errs[0]
	}
	return u, nil
}

// KeepUpdated looks for plugin updates a little after magpie starts, then
// every updateEvery, run by the magpie serving the gateway (one magpie,
// never two at once).
func KeepUpdated(ctx context.Context) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if len(Load().Plugins) > 0 {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			_, _ = CheckUpdates(cctx)
			cancel()
		}
		t.Reset(updateEvery)
	}
}

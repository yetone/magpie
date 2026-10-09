package plugin

// Plugin updates, as magpie keeps itself up to date: the community's
// plugins (@magpie-community/*, the ones the built-in subscriptions move
// onto) update by themselves, a little after magpie starts and every hour; anyone else's new version waits for the reader, who sees a dot
// on Plugins and updates it with a click. A plugin pinned to a version
// stays on it. Updating never cuts a reply streaming through a plugin:
// the host it runs on finishes it (see Restart).

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
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
	// updateEvery is how often magpie looks for plugin updates: a fix to a
	// community plugin reaches its users within the hour (one look at npm's
	// metadata for the plugins installed; six hours left someone told of a
	// fix installing it by hand, #681)
	updateEvery = time.Hour
	// bunEvery is how often magpie looks for a newer Bun
	bunEvery = 6 * time.Hour
	// keepUpdated is how many updates magpie remembers making
	keepUpdated = 20
)

// Official is whether the package is the community's, magpie's own,
// which magpie keeps up to date by itself.
func Official(pkg string) bool { return strings.HasPrefix(pkg, "@magpie-community/") }

// Pinned is whether spec names a version (or a range or a tag other than
// latest): the user chose it, and it stays. A git one's version is its
// repository's, not npm's: it is never pinned, and never asked of npm.
func Pinned(spec string) bool {
	if IsPath(spec) || IsGit(spec) {
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
	// installNewest installs the version of the package npm has as its
	// newest (installAt; tests stub it)
	installNewest = installAt
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
		// a git one's package may be on npm too, as someone else's
		if !IsPath(e.Spec) && !IsGit(e.Spec) {
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
		if err := installNewest(ctx, pkg, now); err != nil {
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

// VersionCheck is what asking npm now found of one installed plugin.
type VersionCheck struct {
	Spec    string `json:"spec"`
	Package string `json:"package"`
	Version string `json:"version"`          // installed
	Latest  string `json:"latest,omitempty"` // npm's newest, when it answered
	// Status is "update" (npm has a newer version), "current", "unknown"
	// (npm didn't say: Why and Error say why), or "git" or "folder" for
	// one npm has no versions of
	Status string `json:"status"`
	// Why is why npm didn't say: "offline" (not reached), "limited" (too
	// many requests), "registry" (it answered with an error) or "missing"
	// (it has no such package)
	Why   string `json:"why,omitempty"`
	Error string `json:"error,omitempty"`
	// Auto is whether magpie updates it by itself (the community's,
	// unpinned, switched on), so an update found would come anyway
	Auto bool `json:"auto"`
	Off  bool `json:"off"`
}

// VersionChecks is what a check asked now found.
type VersionChecks struct {
	At      time.Time      `json:"at"`
	Plugins []VersionCheck `json:"plugins"`
}

// checkEach is how long npm is waited for, for each package a check asks
var checkEach = 15 * time.Second

// CheckNow asks npm now, not what it said within the hour, for the newest
// version of each installed plugin, for the reader's Check for updates.
// Nothing is installed: what it finds is updated as any update is, with
// Upgrade. What npm says is kept, as Info keeps it, and the updates found
// for the reader (someone else's, or pinned) wait for them as the
// background look's do, putting the dot on Plugins.
func CheckNow(ctx context.Context) VersionChecks {
	es := Load().Plugins
	out := make([]VersionCheck, len(es))
	var wg sync.WaitGroup
	sem := make(chan struct{}, npmAtOnce)
	for i, e := range es {
		c := VersionCheck{Spec: e.Spec, Package: Name(e.Spec), Version: Installed(e.Spec), Off: e.Off}
		c.Auto = Official(c.Package) && !Pinned(e.Spec) && !e.Off
		switch {
		case IsPath(e.Spec):
			c.Status = "folder"
		case IsGit(e.Spec):
			c.Status = "git"
		}
		out[i] = c
		if c.Status != "" {
			continue
		}
		wg.Add(1)
		go func(c *VersionCheck) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, checkEach)
			info, err := npmAsk(cctx, c.Package)
			cancel()
			if err == nil || errors.Is(err, errNotFound) {
				npmMu.Lock()
				npmCached()[c.Package] = npmEntry{info, time.Now()}
				saveNPM()
				npmMu.Unlock()
			}
			if err == nil && info.Version == "" {
				err = errors.New("npm named no version")
			}
			if err != nil {
				c.Status, c.Why, c.Error = "unknown", whyNot(err), err.Error()
				return
			}
			c.Latest, c.Status = info.Version, "current"
			if c.Version != "" && update.Newer(info.Version, c.Version) {
				c.Status = "update"
			}
		}(&out[i])
	}
	wg.Wait()
	now := time.Now().UTC().Truncate(time.Second)

	// the updates found wait for the reader, as the background look's do;
	// one npm didn't answer for waits as it did
	updatesMu.Lock()
	u := readUpdates()
	asked := map[string]bool{}
	var waiting []Waiting
	for _, c := range out {
		if c.Status == "unknown" || c.Status == "git" || c.Status == "folder" {
			continue
		}
		asked[c.Package] = true
		if c.Status == "update" && !c.Off && !c.Auto {
			waiting = append(waiting, Waiting{Spec: c.Spec, Package: c.Package, Version: c.Version, Latest: c.Latest})
		}
	}
	for _, w := range u.Waiting {
		if !asked[w.Package] {
			waiting = append(waiting, w)
		}
	}
	u.Checked, u.Waiting = now, waiting
	if b, err := json.MarshalIndent(u, "", "  "); err == nil {
		if err := os.MkdirAll(settings.Dir(), 0o700); err == nil {
			_ = writeWhole(updatesPath(), b)
		}
	}
	updatesMu.Unlock()
	return VersionChecks{At: now, Plugins: out}
}

// whyNot is why npm said nothing of a package, in a word the page says in
// its own: not reached, too many requests, an error, or no such package.
func whyNot(err error) string {
	var se *statusError
	var ue *url.Error
	var ne net.Error
	switch {
	case errors.Is(err, errNotFound):
		return "missing"
	case errors.As(err, &se) && se.Code == http.StatusTooManyRequests:
		return "limited"
	case errors.As(err, &se):
		return "registry"
	case errors.As(err, &ue), errors.As(err, &ne), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "offline"
	}
	return "registry"
}

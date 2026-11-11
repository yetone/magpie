package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// The Discover list, built for magpie. Reading it takes a topic search and
// a package.json, an npm answer and an entry file for each of ~400
// repositories, all at GitHub (and npm), which a desktop that can't reach
// GitHub or is rate-limited there never gets through (River_WLan on X). The
// community repo's `tagged` workflow reads them every half hour instead,
// onto its dist branch (scripts/tagged.mjs there, the same reading as
// readTagged), and usemagpie.ai passes the file on. magpie asks there
// first, then the dist branch itself (through the 「国内镜像」 jsDelivr copy
// with the switch on), and reads GitHub only when neither answers.

// catalogURLs is where the list is asked, in order; moved by tests.
// MAGPIE_PLUGIN_CATALOG puts one address in front, and "off" keeps to
// GitHub.
var catalogURLs = []string{
	"https://usemagpie.ai/api/plugins/tagged",
	"https://raw.githubusercontent.com/magpie-community/plugins/dist/tagged.json",
}

// catalogMaxAge is how old the file may be: the workflow runs every half
// hour, so one this old means it stopped, and GitHub is asked after all.
const catalogMaxAge = 48 * time.Hour

type catalogFile struct {
	Version   int      `json:"version"`
	Generated string   `json:"generated"`
	Plugins   []Tagged `json:"plugins"`
}

// parseCatalog reads the file, keeping what the page could install: a
// repository's github: spec or its npm name, and a picture as ownIcon
// takes one. An empty list, an unknown version or a file past
// catalogMaxAge is an error, as is any that isn't JSON.
func parseCatalog(b []byte, now time.Time) ([]Tagged, error) {
	var f catalogFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("list version %d", f.Version)
	}
	at, err := time.Parse(time.RFC3339, f.Generated)
	if err != nil {
		return nil, fmt.Errorf("list has no date: %w", err)
	}
	if now.Sub(at) > catalogMaxAge {
		return nil, fmt.Errorf("list is from %s", f.Generated)
	}
	out := make([]Tagged, 0, len(f.Plugins))
	for _, t := range f.Plugins {
		if t.Repo == "" || strings.Count(t.Repo, "/") != 1 || !pkgName.MatchString(t.Package) {
			continue
		}
		// what Install adds is the repository or the package named from
		// it, never an address the file made up
		if t.Spec != "github:"+t.Repo && t.Spec != t.Package {
			continue
		}
		switch t.Kind {
		case "", "middleware", "agent", "page":
		default:
			continue
		}
		t.Icon = ownIcon(t.Icon)
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, errors.New("list has no plugins")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stars > out[j].Stars })
	return out, nil
}

// askCatalog is the list the workflow built, from the first address that
// answers.
func askCatalog(ctx context.Context) ([]Tagged, error) {
	urls := catalogURLs
	switch v := os.Getenv("MAGPIE_PLUGIN_CATALOG"); v {
	case "off":
		return nil, errors.New("catalog is off")
	case "":
	default:
		urls = append([]string{v}, urls...)
	}
	var last error
	for _, u := range urls {
		c, cancel := context.WithTimeout(ctx, 12*time.Second)
		b, err := fetchJSON(c, u, 16<<20)
		cancel()
		if err == nil {
			var l []Tagged
			if l, err = parseCatalog(b, time.Now()); err == nil {
				return l, nil
			}
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, last
}

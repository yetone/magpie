package usage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const summaryCacheSessions = 1024

type summaryEntry struct {
	version uint64
	meta    string
	value   Summary
}

var summaries struct {
	sync.Mutex
	entries map[Period]summaryEntry
}

func indexedSummary(p Period, now time.Time) Summary {
	if p != Today && p != Week && p != Month {
		p = All
	}
	snapshot := logSnapshotFor(true)
	_, offset := now.Zone()
	meta := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d", statKey(settings.Path()), statKey(provider.Path()), statKey(catalog.CachePath()), statKey(catalog.LivePath("antigravity")), now.Format("2006-01-02"), now.Location(), offset)
	// Sign-ins and plugin lists can change independently of providers.json.
	// Their identity and price feeds affect both grouping and historical costs.
	h := sha256.New()
	prices, _ := json.Marshal(settings.Load().ModelPrices)
	aliases, _ := json.Marshal(provider.Renamed())
	fmt.Fprint(h, string(prices), string(aliases))
	for _, current := range provider.All() {
		fmt.Fprintf(h, "%q:%q:%v:%t:%t:%t;", current.ID, current.Where(), current.Catalogs(), current.IsPlugin(), current.Account == nil, current.Key != "")
		fmt.Fprint(h, statKey(catalog.LivePath(current.ID)))
	}
	meta += fmt.Sprintf("|%x", h.Sum(nil))
	summaries.Lock()
	old, ok := summaries.entries[p]
	summaries.Unlock()
	if ok && old.version == snapshot.version && old.meta == meta {
		return cloneSummary(old.value)
	}
	if snapshot.uncached && len(snapshot.blocks) == 0 {
		snapshot = readLogSnapshot()
	}
	value := summarizeFrom(p, now, snapshot.first, snapshot.keyProviders, func(fn func(Record)) { snapshot.visit(p.Since(now), fn) })
	// Keep every session in the returned result; limit retained cache data only.
	if len(value.Sessions) > summaryCacheSessions {
		summaries.Lock()
		if previous := summaries.entries[p]; previous.version <= snapshot.version {
			delete(summaries.entries, p)
		}
		summaries.Unlock()
		return value
	}
	summaries.Lock()
	if summaries.entries == nil {
		summaries.entries = map[Period]summaryEntry{}
	}
	if previous := summaries.entries[p]; previous.version <= snapshot.version {
		summaries.entries[p] = summaryEntry{snapshot.version, meta, value}
	}
	summaries.Unlock()
	return cloneSummary(value)
}

// The exported result remains owned by its caller.
func cloneSummary(s Summary) Summary {
	s.Series = slices.Clone(s.Series)
	s.Agents = slices.Clone(s.Agents)
	s.Models = slices.Clone(s.Models)
	s.ProviderKeys = slices.Clone(s.ProviderKeys)
	s.Accounts = slices.Clone(s.Accounts)
	s.CallerKeys = slices.Clone(s.CallerKeys)
	s.Sessions = slices.Clone(s.Sessions)
	return s
}

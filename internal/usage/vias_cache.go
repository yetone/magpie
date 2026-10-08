package usage

import (
	"slices"
	"sync"
	"time"
)

var viasCache struct {
	sync.Mutex
	version uint64
	since   time.Time
	value   map[string][]Via
}

func cachedVias(since time.Time) map[string][]Via {
	snapshot := logSnapshotFor(true)
	viasCache.Lock()
	defer viasCache.Unlock()
	value := viasCache.value
	if value == nil || viasCache.version != snapshot.version || !viasCache.since.Equal(since) {
		if snapshot.uncached && len(snapshot.blocks) == 0 {
			snapshot = readLogSnapshot()
		}
		value = buildVias(snapshot, since)
		if snapshot.info != nil && snapshot.off == snapshot.info.Size() {
			viasCache.version, viasCache.since, viasCache.value = snapshot.version, since, value
		}
	}
	out := make(map[string][]Via, len(value))
	for k, vs := range value {
		out[k] = slices.Clone(vs)
	}
	return out
}

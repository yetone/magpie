package plugin

import (
	"strings"
	"sync"
	"time"
)

// LoadFresh is how long a model's load counts from the plugin's call that
// reported it (magpie.load in host.js). Past it the load is unknown: a
// plugin that stopped reporting says nothing of how busy the vendor is
// now, and routing never takes its silence for idle.
const LoadFresh = 5 * time.Minute

// LoadMost caps a reported load, as host.js's LOAD_MOST does: a vendor
// that queues reports past 100 (200 is twice what it serves at once,
// axelburks on Discord), and 100 times full is past any real queue.
const LoadMost = 10000

// loadModels caps the models one provider keeps a load for, as host.js
// caps one call's; a plugin reporting new ids every call can't grow it.
const loadModels = 1000

type modelLoad struct {
	pct float64
	at  time.Time
}

// loads are the models' loads the plugins reported, by plugin provider
// id and then by the model id lowercased.
var loads = struct {
	sync.Mutex
	m map[string]map[string]modelLoad
}{m: map[string]map[string]modelLoad{}}

// noteLoads keeps a "load" event's models: each a percent from 0 up to LoadMost,
// -1 clearing it. Models the event doesn't name keep theirs, each till
// its own LoadFresh runs out.
func noteLoads(provider string, got map[string]float64, now time.Time) {
	if provider == "" || len(got) == 0 {
		return
	}
	loads.Lock()
	defer loads.Unlock()
	m := loads.m[provider]
	if m == nil {
		m = map[string]modelLoad{}
		loads.m[provider] = m
	}
	for id, v := range got {
		k := strings.ToLower(id)
		if v < 0 || v != v {
			delete(m, k)
			continue
		}
		m[k] = modelLoad{pct: min(v, LoadMost), at: now}
	}
	if len(m) > loadModels {
		for k, l := range m {
			if now.Sub(l.at) > LoadFresh || len(m) > loadModels {
				delete(m, k)
			}
		}
	}
	if len(m) == 0 {
		delete(loads.m, provider)
	}
}

// ModelLoad is how busy the plugin provider's model is, a percent from
// 0 up to LoadMost as its plugin last reported it, and when (a model@size has its
// model's load unless reported itself); ok is false when the
// plugin never reported it, cleared it, or reported it more than
// LoadFresh before now.
func ModelLoad(provider, model string, now time.Time) (pct float64, at time.Time, ok bool) {
	if provider == "" || model == "" {
		return 0, time.Time{}, false
	}
	k := strings.ToLower(model)
	loads.Lock()
	l, ok := loads.m[provider][k]
	if i := strings.LastIndexByte(k, '@'); !ok && i > 0 {
		// grok-4.7@256k is the model grok-4.7 at a context size: as busy
		l, ok = loads.m[provider][k[:i]]
	}
	loads.Unlock()
	if !ok || now.Sub(l.at) > LoadFresh {
		return 0, time.Time{}, false
	}
	return l.pct, l.at, true
}

// FreshLoads is every load reported less than LoadFresh before now, by
// plugin provider id and then the model id lowercased, as ModelLoad
// answers each: nil when none is (a magpie with no plugin reporting).
func FreshLoads(now time.Time) map[string]map[string]float64 {
	loads.Lock()
	defer loads.Unlock()
	var out map[string]map[string]float64
	for provider, m := range loads.m {
		for k, l := range m {
			if now.Sub(l.at) > LoadFresh {
				continue
			}
			if out == nil {
				out = map[string]map[string]float64{}
			}
			if out[provider] == nil {
				out[provider] = map[string]float64{}
			}
			out[provider][k] = l.pct
		}
	}
	return out
}

// NoteLoadsForTest is a "load" event's loads reported at at, for a test.
func NoteLoadsForTest(provider string, got map[string]float64, at time.Time) {
	noteLoads(provider, got, at)
}

// ForgetLoads drops every load reported, for a test.
func ForgetLoads() {
	loads.Lock()
	loads.m = map[string]map[string]modelLoad{}
	loads.Unlock()
}

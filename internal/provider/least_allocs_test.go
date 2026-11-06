package provider

import (
	"runtime"
	"testing"
)

// leastAllocs is testing.AllocsPerRun's fewest over five rounds: it counts
// every goroutine's allocations, and another test's left running (a
// watcher, a poll) lands in one round now and then, never in all five.
func leastAllocs(runs int, f func()) float64 {
	least := testing.AllocsPerRun(runs, f)
	for range 4 {
		least = min(least, testing.AllocsPerRun(runs, f))
	}
	return least
}

// leastBytes is the fewest bytes a run of f allocated, over five rounds of
// runs, for the reason leastAllocs gives.
func leastBytes(runs int, f func()) uint64 {
	var least uint64
	for i := range 5 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range runs {
			f()
		}
		runtime.ReadMemStats(&after)
		if n := (after.TotalAlloc - before.TotalAlloc) / uint64(runs); i == 0 || n < least {
			least = n
		}
	}
	return least
}

package usage

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// Matching the gateway's calls to the session files' decodes only the
// files whose sessions or request ids the gateway names, and matches as
// reading every file did (#2278).
func TestMatchOpensOnlyNamedChunks(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		now := time.Now().Truncate(time.Second)
		gateway := &rowChunk{}
		var locals []*rowChunk
		named := map[*rowChunk]bool{}
		for s := range 12 {
			session := fmt.Sprint("session-", s)
			local := &rowChunk{Source: sessions.CallSource{Path: "/local/" + session}}
			for i := range 60 {
				r := Record{Time: now.Add(time.Duration(rng.Intn(20)) * time.Second), Agent: "claude", Provider: "relay", Session: session, Input: rng.Intn(3), Output: rng.Intn(2), Millis: 1000}
				if i%4 == 0 {
					r.RequestID = fmt.Sprint(session, "-", i)
				}
				if i%5 == 0 {
					r.Status = 500
				}
				if s < 4 { // the gateway has calls of four sessions
					gateway.add(Row{Record: r}, "", int64(len(gateway.Rows)), false)
				}
				if s == 0 && i == 0 { // its call is named in another session's file
					r.RequestID = ""
				}
				if s == 5 && i == 0 {
					r.RequestID = "session-0-0"
				}
				r.Time = r.Time.Add(time.Duration(rng.Intn(5)-2) * time.Second)
				local.add(Row{Record: r, Source: "log"}, "", int64(i), r.Failed())
			}
			local.serial = nextChunkSerial()
			locals = append(locals, local)
			if s < 4 || s == 5 {
				named[local] = true
			}
		}
		for _, since := range []time.Time{{}, now.Add(5 * time.Second)} {
			var opened []*rowChunk
			matchOpening = func(c *rowChunk) { opened = append(opened, c) }
			got := refMap(matchedBlocks([]*rowChunk{gateway}, locals, nil, nil, since, false))
			matchOpening = nil
			want := matchedLocalLegacy(gateway, locals, nil, since)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d: %d matches, want %d", seed, len(got), len(want))
			}
			for _, c := range opened {
				if !named[c] {
					t.Fatalf("seed %d: matching decoded %s, whose sessions and ids the gateway never named", seed, c.Source.Path)
				}
			}
			if since.IsZero() && len(opened) == 0 {
				t.Fatalf("seed %d: matching decoded no chunk", seed)
			}
		}
	}
	t.Cleanup(func() { matchOpening = nil })
}

// agy's gateway calls name no session, and its transcript's steps are
// matched by tokens and time (#2097): its file is one the gateway names.
func TestMatchOpensAgyChunks(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	gateway, agy, other := &rowChunk{}, &rowChunk{}, &rowChunk{}
	gateway.add(Row{Record: Record{Time: now, Agent: "agy", Provider: "relay", Input: 7, Output: 3, Millis: 1000}}, "", 0, false)
	agy.add(Row{Record: Record{Time: now.Add(-time.Minute), Agent: "agy", Session: "agy-transcript", Input: 7, Output: 3}, Source: "log"}, "", 0, false)
	other.add(Row{Record: Record{Time: now, Agent: "claude", Session: "s", Input: 7, Output: 3}, Source: "log"}, "", 0, false)
	agy.serial, other.serial = nextChunkSerial(), nextChunkSerial()
	var opened []*rowChunk
	matchOpening = func(c *rowChunk) { opened = append(opened, c) }
	t.Cleanup(func() { matchOpening = nil })
	got := refMap(matchedBlocks([]*rowChunk{gateway}, []*rowChunk{agy, other}, nil, nil, time.Time{}, false))
	if !got[rowRef{agy, 0}] || len(got) != 1 {
		t.Fatalf("matched %v; want the agy step alone", got)
	}
	if len(opened) != 1 || opened[0] != agy {
		t.Fatalf("decoded %d chunks; want the agy transcript alone", len(opened))
	}
}

// The Antigravity app's session-less gateway calls may be agy's, which
// follows the app's gateway when it isn't connected on its own row: the agy
// transcript is opened for them too.
func TestMatchOpensAgyChunksForTheApp(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	gateway, agy := &rowChunk{}, &rowChunk{}
	gateway.add(Row{Record: Record{Time: now, Agent: "antigravity", Provider: "relay", Input: 7, Output: 3, Millis: 1000}}, "", 0, false)
	agy.add(Row{Record: Record{Time: now.Add(-time.Minute), Agent: "agy", Session: "agy-transcript", Input: 7, Output: 3}, Source: "log"}, "", 0, false)
	agy.serial = nextChunkSerial()
	got := refMap(matchedBlocks([]*rowChunk{gateway}, []*rowChunk{agy}, nil, nil, time.Time{}, false))
	if !got[rowRef{agy, 0}] || len(got) != 1 {
		t.Fatalf("matched %v; want the agy step", got)
	}
}

// A request id's calls are a place in one slice, not a slice of their own
// in two maps: over a year's calls a poll's matching allocated ~1.5 GB in
// two minutes while an agent worked (#2278). It matches every call by its
// id as before.
func TestMatchByIDAllocatesNoSliceAnID(t *testing.T) {
	const n = 4000
	now := time.Now().Truncate(time.Second)
	gateway, local := &rowChunk{}, &rowChunk{Source: sessions.CallSource{Path: "/local/s"}}
	for i := range n {
		r := Record{Time: now.Add(-time.Duration(i) * time.Second), Agent: "claude", Provider: "relay", Session: "s", Input: 1 + i%7, Output: 1, Millis: 1000, RequestID: fmt.Sprint("req-", i)}
		gateway.add(Row{Record: r}, "", int64(i), false)
		if i%3 == 0 {
			r.RequestID = fmt.Sprint("req-", i/3) // a later call names an earlier id again
		}
		local.add(Row{Record: r, Source: "log"}, "", int64(i), false)
	}
	local.serial = nextChunkSerial()
	got := refMap(matchedBlocks([]*rowChunk{gateway}, []*rowChunk{local}, nil, nil, time.Time{}, false))
	if want := matchedLocalLegacy(gateway, []*rowChunk{local}, nil, time.Time{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("%d matches, want %d", len(got), len(want))
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	matchedBlocks([]*rowChunk{gateway}, []*rowChunk{local}, nil, nil, time.Time{}, false)
	runtime.ReadMemStats(&after)
	if mallocs := after.Mallocs - before.Mallocs; mallocs > n/10 {
		t.Fatalf("matching %d calls by id made %d allocations", n, mallocs)
	}
}

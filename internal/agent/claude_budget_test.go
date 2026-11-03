package agent

import (
	"reflect"
	"testing"
)

// TestClaudePlan (#2025): the largest window of the models is the one
// Claude Code is told, and each model whose own differs from the one it
// would run at without an entry gets one; a "[1m]" model counts as 1M, the
// user's own entries are theirs, and one past its model's window holds the
// largest to that window.
func TestClaudePlan(t *testing.T) {
	models := []claudeBudget{
		{id: "mythos-magpie-1", window: 500000, compact: 272000},
		{id: "mythos-magpie-2", window: 131000, compact: 272000},
		{id: "anthropic/magpie-3[1m]", window: 1000000, long: true, compact: 272000},
		{id: "anthropic/magpie-4", compact: -1},
		{id: "magpie/v/gpt-6.1-sol", window: 400000, compact: -1},
	}
	top, entries := claudePlan(models, 272000, nil)
	want := map[string]int{"mythos-magpie-2": 131000, "anthropic/magpie-4": 1000000, "magpie/v/gpt-6.1-sol": 400000}
	if top != 1000000 || !reflect.DeepEqual(entries, want) {
		t.Fatalf("magpie's default: %d %v", top, entries)
	}
	// no top-level window: every model runs at the largest, the [1m] one's 1M
	top, entries = claudePlan(models, 0, nil)
	want = map[string]int{"mythos-magpie-1": 272000, "mythos-magpie-2": 131000, "anthropic/magpie-3": 272000, "magpie/v/gpt-6.1-sol": 400000}
	if top != 1000000 || !reflect.DeepEqual(entries, want) {
		t.Fatalf("no default: %d %v", top, entries)
	}
	// the user's own entries are theirs; "auto" on the 131K one holds the
	// largest to its window
	top, entries = claudePlan(models, 0, map[string]string{"mythos-magpie-2": "auto", "magpie/v/gpt-6.1-sol": "100000"})
	want = map[string]int{"anthropic/magpie-3": 272000}
	if top != 131000 || !reflect.DeepEqual(entries, want) {
		t.Fatalf("the user's own: %d %v", top, entries)
	}
	// one within the model's window is kept as it is, and doesn't hold
	// the largest
	top, entries = claudePlan(models, 0, map[string]string{"mythos-magpie-2": "50000"})
	want = map[string]int{"mythos-magpie-1": 272000, "anthropic/magpie-3": 272000, "magpie/v/gpt-6.1-sol": 400000}
	if top != 1000000 || !reflect.DeepEqual(entries, want) {
		t.Fatalf("the user's 50000: %d %v", top, entries)
	}
	// the user's top-level window is the one a model without a threshold of
	// its own is held to; two ids Claude Code matches as one get the
	// smaller window
	top, entries = claudePlan([]claudeBudget{{id: "a", window: 1000000}, {id: "b", window: 200000}, {id: "B", window: 128000}}, 300000, nil)
	if top != 1000000 || !reflect.DeepEqual(entries, map[string]int{"b": 128000}) {
		t.Fatalf("the user's top-level: %d %v", top, entries)
	}
	// no window known: nothing to tell
	if top, entries = claudePlan([]claudeBudget{{id: "y"}}, 0, nil); top != 0 || entries != nil {
		t.Fatalf("nothing known: %d %v", top, entries)
	}
	// a [1m] one is told by its own 1M too, not the next one's (dumplings
	// on Discord), so it runs to it should Claude Code stop taking it for
	// 1M; the smaller one is held to its own
	top, entries = claudePlan([]claudeBudget{{id: "x[1m]", window: 1000000, long: true, compact: -1}, {id: "g", window: 272000, compact: -1}}, 0, nil)
	if top != 1000000 || !reflect.DeepEqual(entries, map[string]int{"g": 272000}) {
		t.Fatalf("[1m] beside 272K: %d %v", top, entries)
	}
	// a Desktop tier's pick Claude Code runs as a Claude model
	// (behavesAs, moer2831) would run at that model's window: it is told
	// its own, even where it is the largest
	top, entries = claudePlan([]claudeBudget{{id: "a", window: 1000000}, {id: "b", window: 1000000, behaves: true}, {id: "c", window: 400000, behaves: true}}, 0, nil)
	if top != 1000000 || !reflect.DeepEqual(entries, map[string]int{"b": 1000000, "c": 400000}) {
		t.Fatalf("behavesAs: %d %v", top, entries)
	}
	// an id magpie's editor can't put in a key path gets no entry
	if _, entries = claudePlan([]claudeBudget{{id: "a", window: 400000}, {id: "b*", window: 100000}}, 0, nil); len(entries) != 0 {
		t.Fatalf("b*: %v", entries)
	}
}

// TestBudgetPath: a dot in a model's id is part of its key, as Claude Code
// reads it, and the "[1m]" mark is not.
func TestBudgetPath(t *testing.T) {
	if p := budgetPath("magpie/v/gpt-6.1-sol[1m]"); p != `modelSettings.magpie/v/gpt-6\.1-sol.autoCompactWindow` {
		t.Fatal(p)
	}
	if got := budgetsRead(`{"a.b":{"autoCompactWindow":5},"c":{"autoCompactWindow":"auto"},"d":{"effortLevel":"high"}}`); !reflect.DeepEqual(got, map[string]string{"a.b": "5", "c": "auto"}) {
		t.Fatal(got)
	}
}

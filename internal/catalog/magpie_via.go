package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sync"
	"sync/atomic"
)

// Two magpies can each be the other's Remote magpie (cdredfox on Discord:
// two computers on one Tailscale network). Each one's list to the other
// had the other's own models in it, under its Remote magpie provider, and
// each fetch, every ten minutes (provider.RemoteMagpieEvery), took a turn
// more of the loop: rem/back/own/m1, then rem/back/rem/back/own/m1, … on
// both computers, without end. Every one of them joined its model's found
// group (auto/<model>), and a request to one went round the loop, masked
// again on each computer each time it came back.
//
// So a magpie's list to another magpie names, of each model, the magpies
// it passes through on its way to its vendor (magpie_via): the one
// listing it, and those its own list had from, each by its Self. A list
// that names this magpie and another along the way is of a model that
// leaves here and comes back: it isn't taken. A Remote magpie that is
// this magpie itself (sync brought one to the computer it names,
// hanslin1666 on Discord; the gateway's tests serve their remote from
// one process) has this magpie's own models, through this one alone,
// once: a model that went that way twice or more (rem/rem/own/m1) isn't
// taken either. A list from a magpie from before names nothing, and is
// taken as it was.

// MaxVia is the most magpies a model's way may pass through before a list
// is taken to loop through a magpie from before magpie_via, which
// stamps none of its own.
const MaxVia = 8

// Self is this run of magpie's name in magpie_via: random, made up once a
// run, and told only to a magpie that asks for its list.
func Self() string {
	if s := selfForTest.Load(); s != nil {
		return *s
	}
	return self()
}

var self = sync.OnceValue(func() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
})

// selfForTest stands for Self while a test plays another magpie
// (SetSelfForTest).
var selfForTest atomic.Pointer[string]

// SetSelfForTest has Self be s, for a test that plays several magpies
// in one process one after another, until the returned func puts it
// back.
func SetSelfForTest(s string) (restore func()) {
	was := selfForTest.Swap(&s)
	return func() { selfForTest.Store(was) }
}

// loops reports whether a model another magpie lists by way of via leaves
// this magpie and comes back to it.
func loops(via []string) bool {
	if len(via) > MaxVia {
		return true
	}
	if len(via) > 1 && slices.Contains(via, Self()) {
		return true
	}
	// a magpie twice on one way is a loop through magpies this one may
	// not know by name (this magpie restarted since, so its old name is
	// no longer Self)
	for i, v := range via {
		if slices.Contains(via[:i], v) {
			return true
		}
	}
	return false
}

// Loops reports whether what another magpie passes on by way of via (a
// model's magpie_via, a quota card's Via) leaves this magpie and comes
// back to it, or has passed through more magpies than MaxVia.
func Loops(via []string) bool { return loops(via) }

// ViaHere is the magpie_via a model goes out with in this magpie's list
// to another: the way it came here by, when it came from another magpie,
// then this one.
func ViaHere(came []string) []string {
	return append(slices.Clip(came), Self())
}

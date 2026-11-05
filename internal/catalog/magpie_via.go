package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sync"
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
// leaves here and comes back: it isn't taken. A magpie pointed at itself
// (the gateway's tests, which serve their remote from one process) is
// this magpie alone, and its list is taken as it was. A list from a
// magpie from before names nothing, and is taken as it was.

// MaxVia is the most magpies a model's way may pass through before a list
// is taken to loop through a magpie from before magpie_via, which
// stamps none of its own.
const MaxVia = 8

// Self is this run of magpie's name in magpie_via: random, made up once a
// run, and told only to a magpie that asks for its list.
var Self = sync.OnceValue(func() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
})

// loops reports whether a model another magpie lists by way of via leaves
// this magpie and comes back to it.
func loops(via []string) bool {
	if len(via) > MaxVia {
		return true
	}
	self := Self()
	return slices.Contains(via, self) && slices.ContainsFunc(via, func(v string) bool { return v != self })
}

// ViaHere is the magpie_via a model goes out with in this magpie's list
// to another: the way it came here by, when it came from another magpie,
// then this one.
func ViaHere(came []string) []string {
	return append(slices.Clip(came), Self())
}

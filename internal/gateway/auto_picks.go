package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
	"github.com/yetone/magpie/internal/usage"
)

// What magpie picks by itself for a slot Settings leaves on Automatic —
// Image recognition (AutoVision), Web search (autoSearcher), Image
// generation (AutoDrawer) and Video generation (AutoVideomaker) — is held
// to the provider it first picked, in auto-picks.json, while that provider
// can still serve it. A provider added later never takes a slot over:
// hxy920830 on X added a Claude account for account switching alone, and
// every agent's images and web searches went to it, as a Claude account
// ranks first in both picks. A default fills only a slot nothing serves:
// the held provider removed, turned off or left with no model for it, or
// nothing held yet. Settings › Models shows the pick as Automatic · <model>,
// and picking a model there is the user's own choice, which no hold
// touches. One with allowance left still goes before the held one out of
// it, as before (#1801): that is for the moment, and the hold stays.
const (
	slotVision   = "vision"
	slotSearcher = "searcher"
	slotDrawer   = "drawer"
	slotVideo    = "video"
)

// autoPicksFile is auto-picks.json: each slot's held provider, and whether
// the ledger was read for the picks made before holds were kept (Seeded).
// Nothing is held until it is: until then each pick is made as before.
type autoPicksFile struct {
	Vision   string `json:"vision,omitempty"`
	Searcher string `json:"searcher,omitempty"`
	Drawer   string `json:"drawer,omitempty"`
	Video    string `json:"video,omitempty"`
	Seeded   bool   `json:"seeded,omitempty"`
}

func (f *autoPicksFile) slot(s string) *string {
	switch s {
	case slotVision:
		return &f.Vision
	case slotSearcher:
		return &f.Searcher
	case slotDrawer:
		return &f.Drawer
	case slotVideo:
		return &f.Video
	}
	return nil
}

func autoPicksPath() string {
	return filepath.Join(filepath.Dir(provider.Path()), "auto-picks.json")
}

// heldPicks is auto-picks.json as last read, read again when the file
// changed: by another magpie, or a test's new HOME.
var heldPicks struct {
	sync.Mutex
	from string
	mod  time.Time
	f    autoPicksFile
}

// readAutoPicks is the file's picks. Called with heldPicks held. A file
// that can't be read or parsed holds nothing (a pick is made as before)
// and is not written over by a hold: what it held is unknown.
func readAutoPicks() (autoPicksFile, bool) {
	path := autoPicksPath()
	fi, err := os.Stat(path)
	if err != nil {
		heldPicks.from, heldPicks.mod, heldPicks.f = path, time.Time{}, autoPicksFile{}
		return autoPicksFile{}, os.IsNotExist(err)
	}
	if path == heldPicks.from && fi.ModTime().Equal(heldPicks.mod) {
		return heldPicks.f, true
	}
	b, err := os.ReadFile(path)
	var f autoPicksFile
	if err != nil || json.Unmarshal(b, &f) != nil {
		return autoPicksFile{}, false
	}
	heldPicks.from, heldPicks.mod, heldPicks.f = path, fi.ModTime(), f
	return f, true
}

// writeAutoPicks writes f through a temp file, only for this user to read.
// Called with heldPicks held.
func writeAutoPicks(f autoPicksFile) {
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	path := autoPicksPath()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	target, err := edit.Target(path) // a symlink stays, its target written
	if err != nil {
		return
	}
	tmp := target + ".magpie-tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if steady.Rename(tmp, target) != nil {
		os.Remove(tmp)
		return
	}
	if fi, err := os.Stat(path); err == nil {
		heldPicks.from, heldPicks.mod, heldPicks.f = path, fi.ModTime(), f
	}
}

// heldPick is the provider slot is held to, "" for none: nothing held
// yet, or holds not begun (the ledger not read yet, seedAutoPicks).
func heldPick(slot string) string {
	heldPicks.Lock()
	defer heldPicks.Unlock()
	f, _ := readAutoPicks()
	if !f.Seeded {
		return ""
	}
	return *f.slot(slot)
}

// settlePick holds slot to pid, magpie's pick, when the slot holds none or
// the one it holds can't serve it any more (held, as heldPick said, and
// heldServes whether it still can). A pick of "" holds nothing.
func settlePick(slot, held string, heldServes bool, pid string) {
	if pid == "" || held != "" && heldServes || pid == held {
		return
	}
	heldPicks.Lock()
	defer heldPicks.Unlock()
	f, ok := readAutoPicks()
	if !ok || !f.Seeded || *f.slot(slot) != held {
		// unreadable, not begun, or another pick held it since
		return
	}
	*f.slot(slot) = pid
	writeAutoPicks(f)
}

// providerOfRef is the provider of a "<provider>/<model>" ref.
func providerOfRef(ref string) string {
	id, _, _ := strings.Cut(ref, "/")
	return id
}

// pickedSince is how far back the ledger is read for a pick made before
// holds were kept.
const pickedSince = 30 * 24 * time.Hour

// pickBefore is the provider a slot was served by before the provider
// that serves it now (current) took it over, read from rows, the slot's
// successful calls in the order they were made: the one that served the
// call just before current's first. "" when current served the slot from
// the first row read (nothing was taken over that the ledger shows), or
// when no call was made: current is kept then. When current made no call,
// the one that served the last call.
func pickBefore(rows []string, current string) string {
	i := slices.Index(rows, current)
	switch {
	case len(rows) == 0 || i == 0:
		return ""
	case i < 0:
		return rows[len(rows)-1]
	}
	return rows[i-1]
}

// seedAutoPicks begins the holds, once: each Automatic slot is held to
// what served it before a provider added later took it over, when the
// ledger shows it (pickBefore) and that provider can still serve it, else
// to magpie's pick now. Image recognition and Web search are read from the
// ledger's Image description and Web search calls; Image and Video
// generation, which no call of the ledger names as Automatic's, are held
// to the pick now.
func seedAutoPicks() {
	heldPicks.Lock()
	f, ok := readAutoPicks()
	heldPicks.Unlock()
	if !ok || f.Seeded {
		return
	}
	var vision, search []string
	usage.Visit(time.Now().Add(-pickedSince), func(r usage.Record) {
		if r.Provider == "" || r.Status < 200 || r.Status > 299 || r.IsRejected() {
			return
		}
		switch {
		case r.Kind == "vision":
			vision = append(vision, r.Provider)
		case r.Kind == "web_search" && r.Source != usage.SourceSearch:
			// the searcher's model, not a search API or a provider's
			// own search for its own model (logNativeSearches)
			search = append(search, r.Provider)
		}
	})
	seed := autoPicksFile{Seeded: true}
	cur := providerOfRef(AutoVision())
	seed.Vision = cur
	if prev := pickBefore(vision, cur); prev != "" && seesFor(prev) {
		seed.Vision = prev
	}
	if p, _, ok := autoSearcher(); ok {
		seed.Searcher = p.ID
		if prev := pickBefore(search, p.ID); prev != "" && searcherFor(prev) {
			seed.Searcher = prev
		}
	}
	seed.Drawer = providerOfRef(AutoDrawer())
	seed.Video = providerOfRef(AutoVideomaker())
	heldPicks.Lock()
	defer heldPicks.Unlock()
	if f, ok := readAutoPicks(); ok && !f.Seeded {
		writeAutoPicks(seed)
	}
}

// seesFor is whether provider pid could be Image recognition's Automatic
// pick: it is on and has a model that sees (seersRanked).
func seesFor(pid string) bool {
	return slices.ContainsFunc(seersRanked(nil), func(r rankedSeer) bool { return providerOfRef(r.model) == pid })
}

// searcherFor is whether provider pid could be Web search's Automatic
// pick: it is on, magpie may search with it, and it has a model to.
func searcherFor(pid string) bool {
	p, err := provider.Find(pid)
	if err != nil || !p.On() || searchRank(*p) < 0 {
		return false
	}
	m, _ := searcherReady(*p)
	return m != ""
}

// KeepAutoPicksHeld begins the holds once the gateway serves.
func KeepAutoPicksHeld(context.Context) { seedAutoPicks() }

func init() { WhileServing = append(WhileServing, KeepAutoPicksHeld) }

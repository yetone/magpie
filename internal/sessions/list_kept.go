package sessions

import (
	"slices"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// keptList is the listing listSessions last made, and what it was made
// from. The Sessions page lists every session every 15 s, and between two
// polls the files mostly haven't changed, while each listing grouped,
// sorted and assembled every one of them again: over ~17k real sessions
// ~25 ms and ~12 MB of a poll, before the page's own copies. Guarded by mu.
var keptList struct {
	files []keptFile
	gen   uint64
	limit int
	at    time.Time
	out   []Session
	// the answers the listing got from the prices, Codex's names and
	// Claude Desktop's, asked again before it is given out
	prices  map[string]*catalog.Price
	titles  titleAnswers
	desktop map[string]string
	// the command an agy row resumes with (AgyStart), when one is listed:
	// it changes when agy is wired to magpie's gateway or let go of
	agy      bool
	agyStart string
	made     int // listings assembled: ListUnless's number for the one kept
}

// listKeptFor is how long a kept listing stands at most. What a row shows
// beside its files' parses, the prices and the names (whether an agent to
// carry it to is installed, a Claude Code subagent's meta, a Kimi wire
// file) is looked at again within it.
var listKeptFor = time.Minute

// keptListing is the kept listing when it was made of files as they are
// now, at limit, with the prices and names it got still the same. It is
// the kept slice itself: a caller copies it before writing rows of it
// (List puts the aliases in).
func keptListing(files []file, limit int, price func(string) *catalog.Price, titles func(string) string) ([]Session, bool) {
	k := &keptList
	if k.out == nil || k.gen != cacheGeneration || k.limit != limit || time.Since(k.at) >= listKeptFor ||
		!keptStill(k.files, files, k.prices, &k.titles, k.desktop, price, titles) || k.agy && agyStart() != k.agyStart {
		forgetKeptList()
		return nil, false
	}
	return k.out, true
}

// keepListing starts what keepList keeps: price and titles answer as they
// did, and note the answers.
func keepListing(files []file, limit int, price func(string) *catalog.Price, titles func(string) string) (func(string) *catalog.Price, func(string) string) {
	k := &keptList
	k.made++
	k.files, k.gen, k.limit, k.at = make([]keptFile, len(files)), cacheGeneration, limit, time.Now()
	for i, f := range files {
		k.files[i] = keptFileOf(f)
	}
	k.prices, k.titles, k.desktop = map[string]*catalog.Price{}, titleAnswers{}, map[string]string{}
	return func(model string) *catalog.Price {
			p := price(model)
			k.prices[model] = p
			return p
		}, func(id string) string {
			t := titles(id)
			k.titles.note(id, t)
			return t
		}
}

// keepList keeps out, the listing keepListing was started for, with the
// Claude Desktop names its sessions got.
func keepList(out []Session) {
	var ids []string
	for _, s := range out {
		if s.Agent == "claude-desktop" && s.WSL == "" {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) > 0 {
		names := DesktopTitles(ids)
		for _, id := range ids {
			keptList.desktop[id] = names[id]
		}
	}
	keptList.agy, keptList.agyStart = false, ""
	if i := slices.IndexFunc(out, func(s Session) bool { return s.Agent == "agy" && s.WSL == "" }); i >= 0 {
		keptList.agy, keptList.agyStart = true, agyStart()
	}
	keptList.titles.settle()
	keptList.out = slices.Clone(out)
}

// agyStart is what resumeCommand starts an agy row with, "" for agy's own.
func agyStart() string {
	if AgyStart == nil {
		return ""
	}
	return AgyStart()
}

func forgetKeptList() {
	keptList.files, keptList.out = nil, nil
	keptList.prices, keptList.titles, keptList.desktop = nil, titleAnswers{}, nil
}

package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/source"
)

// Partners are services that pay to be listed first in the add sheet,
// under a heading of their own that says so. Who they are changes without
// a release: the list is fetched from PartnersURL, as the plugin market's
// is, and kept on disk. A partner is a preset in all but where it comes
// from, so adding one, its editor and its key page work as a preset's do.

// PartnersURL is where the list is kept; MAGPIE_PARTNERS moves it, and
// MAGPIE_PARTNERS=off keeps to none.
const PartnersURL = "https://usemagpie.ai/api/partners"

// KindPartner is a partner's Kind: the add sheet lists them apart.
const KindPartner Kind = "partner"

// MaxPartners is the most partners listed; the rest of a longer list are
// left out.
const MaxPartners = 12

// Partner is one entry of the list: a preset, its tagline in each
// language, the languages it is shown in, and when it is shown.
type Partner struct {
	PresetDef
	// IconURL is the partner's logo, kept beside the user's own pictures
	// and shown as Icon.
	IconURL string `json:"iconUrl,omitempty"`
	// Notes is the tagline by language ("en", "zh", "zh-TW", "ja", ...);
	// "en" stands in for a language without one.
	Notes map[string]string `json:"notes,omitempty"`
	// Langs, when set, are the only languages the partner is listed in.
	Langs []string `json:"langs,omitempty"`
	// From and Until bound when it is listed; zero is unbounded.
	From  time.Time `json:"from,omitzero"`
	Until time.Time `json:"until,omitzero"`
}

// Live reports whether the partner is listed at now.
func (p Partner) Live(now time.Time) bool {
	return (p.From.IsZero() || !now.Before(p.From)) && (p.Until.IsZero() || now.Before(p.Until))
}

type partnerFeed struct {
	Partners []Partner `json:"partners"`
}

// partnerCache is what is kept on disk: the list fetched last, and every
// partner ever listed, so a provider added from one that has since left
// still finds its preset (its endpoints, regions and key page).
type partnerCache struct {
	At       time.Time `json:"at"`
	Partners []Partner `json:"partners"`
	// Tried is when the last fetch started, and Error how it ended, ""
	// once it got the list (At is then its time) or while it is still
	// out. So the file tells a fetch that failed (Error) from one that
	// never came back (Tried after At, no Error) and from none asked
	// (Tried old): an app that kept an old list for days left nothing to
	// tell them apart (#3362).
	Tried time.Time          `json:"tried,omitzero"`
	Error string             `json:"error,omitempty"`
	Seen  map[string]Partner `json:"seen,omitempty"`
	// Noticed are the partners the add sheet has listed to the user; one
	// listed since is new, and the add button says so (NoticePartners).
	Noticed []string `json:"noticed,omitempty"`
}

var (
	partnerMu      sync.Mutex
	partnerState   *partnerCache // nil until read from disk
	partnerNext    time.Time     // the next fetch, when none is running
	partnerFetches bool
	partnerLogged  string // the failure logged last, so a long one is logged once
)

// A list is fetched again after partnerEvery, so a partner added reaches
// an app left running within the hour; the list is a couple of KB and its
// icons aren't fetched again (partnerIcon). A var, so a test can shorten it.
var partnerEvery = time.Hour

const partnerRetry = 10 * time.Minute

var partnerID = lazyre.New(`^[a-z0-9][a-z0-9-]{1,40}$`)

func partnerCacheFile() string { return filepath.Join(appdir.Cache(), "partners.json") }

func partnersSource() string {
	src := os.Getenv("MAGPIE_PARTNERS")
	if src == "" {
		if testing.Testing() {
			// a test never asks the real site; one that wants a list
			// names its own
			return "off"
		}
		src = PartnersURL
	}
	return src
}

// Partners are the partners listed now, in the list's order: what was
// fetched last, never waited for. A list this process hasn't fetched, or
// fetched an hour ago, is fetched again behind the call, and the page
// asked again shows it.
func Partners() []Partner {
	if partnersSource() == "off" {
		return nil
	}
	partnerMu.Lock()
	defer partnerMu.Unlock()
	loadPartners()
	if !partnerFetches && time.Now().After(partnerNext) {
		partnerFetches = true
		go refreshPartners()
	}
	now := time.Now()
	var out []Partner
	for _, p := range partnerState.Partners {
		if p.Live(now) {
			p.Sponsored = true
			out = append(out, p)
		}
	}
	return out
}

// PartnersNow is Partners for a command that ends at once, before a fetch
// behind it could: a list due is fetched first, waited for at most wait.
func PartnersNow(wait time.Duration) []Partner {
	if partnersSource() == "off" {
		return nil
	}
	partnerMu.Lock()
	loadPartners()
	due := !partnerFetches && time.Now().After(partnerNext)
	if due {
		partnerFetches = true
	}
	partnerMu.Unlock()
	if due {
		done := make(chan struct{})
		go func() { refreshPartners(); close(done) }()
		select {
		case <-done:
		case <-time.After(wait):
		}
	}
	return Partners()
}

// KeepPartnersFresh fetches the partner list when the magpie serving the
// gateway starts, and again whenever it is due (every hour, ten minutes
// after a failed fetch), whether or not a page asks for it, so a partner
// added reaches an app left running within the hour, and the list kept on
// disk is fresh for the next start.
func KeepPartnersFresh(ctx context.Context) {
	if partnersSource() == "off" {
		return
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		partnerMu.Lock()
		loadPartners()
		due := !partnerFetches && !time.Now().Before(partnerNext)
		if due {
			partnerFetches = true
		}
		partnerMu.Unlock()
		if due {
			refreshPartners()
		}
		partnerMu.Lock()
		wait := time.Until(partnerNext)
		partnerMu.Unlock()
		// a fetch Partners started is still running when partnerNext
		// has passed: look again in a second
		t.Reset(max(wait, time.Second))
	}
}

// loadPartners reads the list kept on disk the first time it's asked for;
// partnerMu held.
func loadPartners() {
	if partnerState != nil {
		return
	}
	partnerState = &partnerCache{Seen: map[string]Partner{}}
	b, err := os.ReadFile(partnerCacheFile())
	if err != nil {
		return
	}
	var c partnerCache
	if json.Unmarshal(b, &c) != nil {
		return
	}
	if c.Seen == nil {
		c.Seen = map[string]Partner{}
	}
	// what was written is checked again: the rules may be stricter now
	c.Partners = validPartners(c.Partners)
	for id, p := range c.Seen {
		if v := validPartners([]Partner{p}); len(v) == 1 && v[0].ID == id {
			c.Seen[id] = v[0]
		} else {
			delete(c.Seen, id)
		}
	}
	// partnerNext stays zero: a process started fetches on its first ask,
	// whatever the kept list's age. Waiting out the kept list's six hours
	// held back a partner added minutes after it was fetched, restart or not.
	partnerState = &c
}

// refreshPartners fetches the list and keeps it; on a failure the list
// held stays, and it is tried again in ten minutes. When it started and
// how it ended are kept with the list (Tried, Error), and a failure is
// logged once until the list is fetched again.
func refreshPartners() {
	partnerMu.Lock()
	loadPartners()
	partnerState.Tried, partnerState.Error = time.Now(), ""
	savePartners()
	partnerMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	src := partnersSource()
	list, err := fetchPartners(ctx, src)
	if err == nil {
		for i := range list {
			list[i].Icon = partnerIcon(ctx, list[i])
		}
	}
	partnerMu.Lock()
	defer partnerMu.Unlock()
	partnerFetches = false
	loadPartners()
	if err != nil {
		partnerNext = time.Now().Add(partnerRetry)
		partnerState.Error = partnerError(err)
		savePartners()
		if partnerState.Error != partnerLogged {
			partnerLogged = partnerState.Error
			log.Printf("partners: the list couldn't be fetched from %s, asked again in %s: %s", src, partnerRetry, partnerLogged)
		}
		return
	}
	if partnerLogged != "" {
		partnerLogged = ""
		log.Printf("partners: the list is fetched again from %s", src)
	}
	now := time.Now()
	partnerState.At, partnerState.Partners = now, list
	for _, p := range list {
		partnerState.Seen[p.ID] = p
	}
	partnerNext = now.Add(partnerEvery)
	savePartners()
}

// partnerError is err as the list keeps it: its text, cut to a size
// that stays readable in the file.
func partnerError(err error) string {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500]) + "…"
	}
	return msg
}

// savePartners writes what is held to disk; partnerMu held.
func savePartners() {
	b, err := json.Marshal(partnerState)
	if err != nil {
		return
	}
	// loadPartners reads a file that doesn't parse as no list and no Seen:
	// it is kept aside before it is written over, and not written over
	// when it can't be read. Every try writes the file (Tried, #3362), so
	// without this the first one, offline too, lost the presets of the
	// partners no longer listed that providers added from them still use.
	if lastgood.KeepUnparsed(partnerCacheFile(), lastgood.Into[partnerCache]) != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(partnerCacheFile()), 0o755)
	tmp := partnerCacheFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, partnerCacheFile())
	}
}

// NoticePartners keeps that the user has been shown the partners ids, so
// they are no longer new. It is kept whether or not they are counted.
func NoticePartners(ids ...string) {
	if partnersSource() == "off" {
		return
	}
	partnerMu.Lock()
	defer partnerMu.Unlock()
	loadPartners()
	changed := false
	for _, id := range ids {
		if _, ok := partnerState.Seen[id]; ok && !slices.Contains(partnerState.Noticed, id) {
			partnerState.Noticed = append(partnerState.Noticed, id)
			changed = true
		}
	}
	if changed {
		savePartners()
	}
}

// PartnerNoticed reports whether the user has been shown the partner id.
func PartnerNoticed(id string) bool {
	partnerMu.Lock()
	defer partnerMu.Unlock()
	loadPartners()
	return slices.Contains(partnerState.Noticed, id)
}

func fetchPartners(ctx context.Context, src string) ([]Partner, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "magpie")
	req.Header.Set("Accept", "application/json")
	res, err := source.Do(http.DefaultClient, req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("partners: %s", res.Status)
	}
	var f partnerFeed
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&f); err != nil {
		return nil, fmt.Errorf("partners: %w", err)
	}
	return validPartners(f.Partners), nil
}

// validPartners keeps the entries magpie can list safely, at most
// MaxPartners: an id of its own that no built-in preset has, a name, and
// https addresses only. What a partner may not set (a decision API, no
// key, an endpoint of the user's own) is cleared.
func validPartners(in []Partner) []Partner {
	var out []Partner
	ids := map[string]bool{}
	for _, p := range in {
		if len(out) == MaxPartners {
			break
		}
		if err := checkPartner(p); err != nil || ids[p.ID] {
			continue
		}
		ids[p.ID] = true
		p.Kind, p.Sponsored = KindPartner, false
		p.Decide, p.NoKey, p.Hosts = "", false, false
		p.Endpoint, p.EndpointHint, p.EndpointNeeded = "", "", ""
		for i := range p.Regions {
			p.Regions[i].Decide = ""
		}
		p.SplitAccounts = slices.ContainsFunc(p.Regions, func(r Region) bool { return placeOf(r) == "china" }) &&
			slices.ContainsFunc(p.Regions, func(r Region) bool { return placeOf(r) == "global" })
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" {
			// the first region is the default, as a built-in preset's is,
			// unless it is China's or Global's and the user is in the
			// other (homeRegion): its key page, site and catalog with it
			r := p.Regions[homeRegion(p.Regions, inMainlandChina())]
			p.Chat, p.Responses, p.Anthropic = r.Chat, r.Responses, r.Anthropic
			p.KeysURL, p.Website, p.Catalog = cmp.Or(r.KeysURL, p.KeysURL), cmp.Or(r.Website, p.Website), cmp.Or(r.Catalog, p.Catalog)
			if r.Website != "" {
				p.SiteHost = r.SiteHost
			}
		}
		p.Note = p.Notes["en"]
		if !strings.HasPrefix(p.Icon, "file:") || IconFile(strings.TrimPrefix(p.Icon, "file:")) == "" {
			p.Icon = "generic"
		}
		out = append(out, p)
	}
	return out
}

// homeRegion is the region a new provider of a partner starts at: the
// first, unless the first is China's or Global's and the other is listed
// too, when it is the one where the user is (inChina, from this
// computer's time zone). Unknown (0) keeps the first. Kimi Code lists
// China first, and a user abroad started at a plan that isn't theirs.
// Only a region named for China or Global counts: "Pay as you go ·
// China" is another plan, and "Auto" or "Direct" no place.
func homeRegion(rs []Region, inChina int) int {
	if len(rs) < 2 || inChina == 0 {
		return 0
	}
	first := placeOf(rs[0])
	if first == "" {
		return 0
	}
	want := "global"
	if inChina > 0 {
		want = "china"
	}
	if first == want {
		return 0
	}
	if i := slices.IndexFunc(rs, func(r Region) bool { return placeOf(r) == want }); i > 0 {
		return i
	}
	return 0
}

// placeOf says whether a region is China's ("china"), Global's ("global")
// or neither (""), by its id or its name.
func placeOf(r Region) string {
	id, name := strings.ToLower(r.ID), strings.ToLower(strings.TrimSpace(r.Name))
	switch {
	case id == "cn" || id == "china" || name == "china" || name == "china mainland" || name == "mainland china":
		return "china"
	case id == "global" || id == "intl" || name == "global" || name == "international":
		return "global"
	}
	return ""
}

// inMainlandChina says whether this computer's time zone is mainland
// China's: 1 it is, -1 it isn't, 0 unknown (no zone could be read). A
// var, so a test sets where it is.
var inMainlandChina = func() int { return chinaZone(cmp.Or(LocalZone(), systemZoneName())) }

// chinaZone reads a time zone's name: an IANA name, or Windows' own,
// whose China Standard Time is Beijing's (Hong Kong's too, which Windows
// doesn't tell apart). Hong Kong, Macau and Taipei have zones of their
// own, so they are abroad.
func chinaZone(name string) int {
	switch name {
	case "":
		return 0
	case "Asia/Shanghai", "Asia/Urumqi", "Asia/Chongqing", "Asia/Chungking", "Asia/Harbin", "Asia/Kashgar", "PRC", "China Standard Time":
		return 1
	}
	return -1
}

func checkPartner(p Partner) error {
	if !partnerID.MatchString(p.ID) {
		return errors.New("bad id")
	}
	if builtinPreset(p.ID) != nil {
		return errors.New("a built-in preset's id")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("no name")
	}
	if p.Chat == "" && p.Responses == "" && p.Anthropic == "" && len(p.Regions) == 0 {
		return errors.New("no endpoint")
	}
	urls := []string{p.Chat, p.Responses, p.Anthropic, p.Website, p.KeysURL, p.IconURL}
	for _, r := range p.Regions {
		if r.Chat == "" && r.Responses == "" && r.Anthropic == "" {
			return errors.New("a region with no endpoint")
		}
		urls = append(urls, r.Chat, r.Responses, r.Anthropic, r.KeysURL, r.Website)
	}
	for _, u := range urls {
		if u != "" && !httpsURL(u) {
			return fmt.Errorf("not https: %s", u)
		}
	}
	return nil
}

func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// partnerIcon keeps the partner's logo, through FetchIcon's checks, and
// returns the Icon that shows it: the one kept before when it can't be
// fetched, else the generic one.
func partnerIcon(ctx context.Context, p Partner) string {
	if p.IconURL == "" {
		return "generic"
	}
	// an icon kept from the same URL is used as it is, so an hourly fetch
	// doesn't download it again; a partner's new logo goes at a new URL
	partnerMu.Lock()
	loadPartners()
	s, ok := partnerState.Seen[p.ID]
	partnerMu.Unlock()
	kept := ok && s.IconURL == p.IconURL && s.Icon != "" && s.Icon != "generic"
	if kept {
		if f := IconFile(strings.TrimPrefix(s.Icon, iconPrefix)); f != "" {
			if _, err := os.Stat(f); err == nil {
				return s.Icon
			}
		}
	}
	if icon, err := fetchPartnerIcon(ctx, p.IconURL); err == nil {
		return icon
	}
	if kept {
		return s.Icon
	}
	return "generic"
}

var fetchPartnerIcon = FetchIcon

// keptPartnerIcons are the pictures the partners listed now show, which
// pruneIcons leaves (#3424): no provider points at a partner's logo until
// one is added from it, so the prune after any save of providers.json
// took it once it was an hour old, and the add sheet showed the partner
// with the generic icon until the next fetch got the logo again.
func keptPartnerIcons() []string {
	if partnersSource() == "off" {
		return nil
	}
	partnerMu.Lock()
	defer partnerMu.Unlock()
	loadPartners()
	var out []string
	for _, p := range partnerState.Partners {
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			out = append(out, name)
		}
	}
	return out
}

// partnerPreset is the preset of a partner listed now or before, for a
// provider added from it: one no longer listed isn't Sponsored.
func partnerPreset(id string) *PresetDef {
	if partnersSource() == "off" {
		return nil
	}
	partnerMu.Lock()
	defer partnerMu.Unlock()
	loadPartners()
	p, ok := partnerState.Seen[id]
	if !ok {
		return nil
	}
	d := p.PresetDef
	d.Sponsored = false
	for _, l := range partnerState.Partners {
		if l.ID == id && l.Live(time.Now()) {
			d.Sponsored = true
		}
	}
	return &d
}

// resetPartners forgets the list held in memory, for tests. A fetch
// still running would store its list over the one held after the reset,
// so it is waited for first.
func resetPartners() {
	claimPartnerFetch()
	partnerState, partnerNext, partnerFetches, partnerLogged = nil, time.Time{}, false, ""
	partnerMu.Unlock()
}

// claimPartnerFetch waits for the fetch running, if any, to end and returns
// with partnerMu held and no fetch running, for tests.
func claimPartnerFetch() {
	for {
		partnerMu.Lock()
		if !partnerFetches {
			return
		}
		partnerMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
}

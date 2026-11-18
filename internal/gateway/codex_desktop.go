package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
)

// The Codex app's picker shows only the models of Codex's list that a
// flag of OpenAI's names, for Codex on its built-in provider, unless Codex
// reads its list from a file (model_catalog_json): beside the ChatGPT
// sign-in it showed GPT alone, magpie's models in the list it was handed
// all the same (laddieyao on Discord). The CLI shows the whole list. So
// where the Codex app is installed, magpie writes the list it hands Codex
// into a file for Codex to read (agent.CodexDesktopList), and Codex asks
// for no list then: the backend's part of it is asked for here, with
// Codex's sign-in, and kept.

// CodexDesktopChanged is called when the backend's list for the Codex app
// changed, for the file to be written again (agent.SyncCatalogSoon).
var CodexDesktopChanged func()

// codexDeskFetch asks the backend for its list (a test's stands in).
var codexDeskFetch = provider.CodexLiveList

// codexDeskWho names the sign-in Codex is on now, whose list the backend
// gives (a test's stands in).
var codexDeskWho = provider.CodexSignInWho

// codexDeskVersion is the Codex version the backend's list is asked for
// now, and codexDeskBehind whether a list asked for one is behind it; the
// backend lists a model only to a client new enough for it (tests' stand
// in).
var (
	codexDeskVersion = provider.CodexClientVersion
	codexDeskBehind  = provider.CodexVersionBehind
)

// codexDeskListed is what the sign-in's own list, as magpie keeps it for
// the codex provider, has and when it was asked for (a test's stands in).
var codexDeskListed = provider.CodexSignInListed

const (
	// codexDeskFor is how long a list the backend gave stands before it
	// is asked for again; codexDeskRetry how long after one it didn't
	codexDeskFor   = time.Hour
	codexDeskRetry = 5 * time.Minute
	codexDeskWait  = 30 * time.Second
)

// codexDeskLook is how often the magpie serving the gateway looks whether
// the list is due (KeepCodexDeskFresh); a var so tests can look sooner.
var codexDeskLook = time.Minute

var codexDesk struct {
	sync.Mutex
	body       []byte
	got, tried time.Time
	asking     bool
	// revoked: the last ask had the backend call the sign-in revoked
	// (provider.CodexListRevoked)
	revoked bool
	// ver is the Codex version body was asked for, and slugs its models
	ver   string
	slugs map[string]bool
	// failed: the last ask got no list (the backend not reachable from
	// here, or not answering 200), and none is known for who
	failed bool
	// who is the sign-in body and revoked are of (codexDeskWho); asked
	// the one the last ask was for; want the one a caller last asked about
	who, asked, want string
	done             chan struct{} // closed as the ask under way ends
}

// codexDeskDue says the list kept for who is to be asked for again: none
// kept, an hour old, asked for an older Codex than magpie asks as now, or
// older than the sign-in's own list magpie keeps for the codex provider
// while that lists a model it lacks. The backend's list is the same for
// both, asked apart: OpenAI listed GPT-6.2 Astra on 11-17, magpie's
// pickers had it, and the Codex app's file didn't (ANAGABRIELRLki on X).
// Called with codexDesk held; behind and listed are read before it is.
func codexDeskDue(who string, behind func(string) bool, listed []string, listedAt time.Time) bool {
	c := &codexDesk
	if c.who != who || c.body == nil || time.Since(c.got) >= codexDeskFor || behind(c.ver) {
		return true
	}
	if listedAt.After(c.got) {
		for _, id := range listed {
			if !c.slugs[id] {
				return true
			}
		}
	}
	return false
}

// codexDeskAsk starts an ask for who's list when it is due and none is
// under way, not again within codexDeskRetry of the last one for who.
// Called with codexDesk held.
func codexDeskAsk(who string, due bool) {
	c := &codexDesk
	if due && !c.asking && (c.asked != who || time.Since(c.tried) >= codexDeskRetry) {
		c.asking, c.tried, c.asked = true, time.Now(), who
		c.done = make(chan struct{})
		go codexDeskRefresh(c.done, who)
	}
}

// codexDeskInputs reads, outside codexDesk's lock, what codexDeskDue weighs.
func codexDeskInputs() (behind func(string) bool, listed []string, listedAt time.Time) {
	now := codexDeskVersion()
	behind = func(v string) bool { return codexDeskBehind(v, now) }
	listed, listedAt, _ = codexDeskListed()
	return behind, listed, listedAt
}

// KeepCodexDeskFresh asks for the Codex app's list again whenever it is
// due (codexDeskDue), once the list was wanted, run by the magpie serving
// the gateway. The list was asked for only as magpie wrote the agents'
// files (CodexDesktopCatalog, from agent.SyncCatalog), which nothing does
// on a quiet magpie: a model OpenAI listed since stayed out of the file,
// and with model_catalog_json named Codex asks for no list of its own.
// A list that changed is told (CodexDesktopChanged), and the file is
// written again.
func KeepCodexDeskFresh(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.Next(codexDeskLook):
		}
		codexDesk.Lock()
		wanted := codexDesk.want != ""
		codexDesk.Unlock()
		if !wanted {
			continue
		}
		who := codexDeskWho()
		behind, listed, listedAt := codexDeskInputs()
		codexDesk.Lock()
		if codexDesk.want != "" {
			codexDesk.want = who
			codexDeskAsk(who, codexDeskDue(who, behind, listed, listedAt))
		}
		codexDesk.Unlock()
	}
}

func init() { WhileServing = append(WhileServing, KeepCodexDeskFresh) }

// CodexDesktopCatalog is the list a Codex beside its ChatGPT sign-in is
// handed (/backend-api/codex/models), as a file Codex reads with
// model_catalog_json: the backend's entries as it sent them, narrowed and
// set as the codex provider has them, magpie's after them. ok is false
// while the backend's list is not known: none asked for yet, or none it
// gave. That is unknown, never an empty list, and the file as it was
// stands. The backend is asked again in the background when its list is
// old, CodexDesktopChanged told when it changed.
//
// A sign-in the backend calls revoked, with no list of its from before,
// is handed Codex's last list of its own and magpie's models, as
// codexModels answers Codex then (#4308): the backend has said all it
// will for that sign-in, and without it the app's picker had none of
// magpie's models until the user signed in again.
//
// The list is the list of the account Codex is signed in to, and a Pro
// account lists models a Plus one doesn't. Once Codex is on another
// account, the list from before is not known for it: the backend is asked
// at once, and the file stands as it was until it answers (chzbz on X: a
// 20x account added and switched to, GPT-6.1 Sol not in the app's picker,
// the file kept the other account's list up to an hour).
func CodexDesktopCatalog() (b []byte, ok bool) {
	who := codexDeskWho()
	behind, listed, listedAt := codexDeskInputs()
	codexDesk.Lock()
	codexDesk.want = who
	body, revoked, failed := codexDesk.body, codexDesk.revoked, codexDesk.failed
	mine := codexDesk.who == who
	if !mine {
		body, revoked, failed = nil, false, false
	}
	codexDeskAsk(who, codexDeskDue(who, behind, listed, listedAt))
	codexDesk.Unlock()
	if body == nil && (revoked || failed) {
		var own []any
		for _, e := range codexcat.CacheEntries() {
			own = append(own, e)
		}
		// the backend can't be asked (not reachable from here: a Windows
		// box behind a firewall): Codex's own last list, from its
		// models_cache.json, stands in. With none of it, the file stays
		// as it was rather than hold magpie's models alone.
		if !revoked && len(own) == 0 {
			return nil, false
		}
		b, err := json.MarshalIndent(map[string]any{"models": codexMerged(own, true)}, "", " ")
		return b, err == nil
	}
	if body == nil {
		return nil, false
	}
	// each caller's own copy: codexMerged writes into the entries
	var list struct {
		Models []any `json:"models"`
	}
	if json.Unmarshal(body, &list) != nil || len(list.Models) == 0 {
		return nil, false
	}
	b, err := json.MarshalIndent(map[string]any{"models": codexMerged(list.Models, false)}, "", " ")
	if err != nil {
		return nil, false
	}
	return b, true
}

// codexDeskRefresh asks the backend for who's list.
func codexDeskRefresh(done chan struct{}, who string) {
	ctx, cancel := context.WithTimeout(context.Background(), codexDeskWait)
	defer cancel()
	ver := codexDeskVersion()
	b, err := codexDeskFetch(ctx)
	codexDesk.Lock()
	changed := false
	wasRevoked := codexDesk.revoked && codexDesk.who == who
	if codexDesk.who != who {
		// another sign-in's list, and what was said of it, is not who's
		codexDesk.body, codexDesk.revoked, codexDesk.failed, codexDesk.who = nil, false, false, who
	}
	if err == nil {
		changed = !bytes.Equal(b, codexDesk.body) || codexDesk.revoked
		codexDesk.body, codexDesk.got, codexDesk.revoked, codexDesk.failed = b, time.Now(), false, false
		codexDesk.ver, codexDesk.slugs = ver, codexDeskSlugs(b)
	} else if revoked := provider.CodexListRevoked(err); revoked != codexDesk.revoked {
		// told only where it changes what is handed out: with no list
		// from before (CodexDesktopCatalog)
		changed = codexDesk.body == nil
		codexDesk.revoked = revoked
	}
	// no list for who from before, and none now: the cached one stands in
	// (CodexDesktopCatalog), told once
	// (a 502 after the backend called the sign-in revoked is still no
	// answer, as before)
	if err != nil && codexDesk.body == nil && !codexDesk.failed && !wasRevoked && !codexDesk.revoked {
		codexDesk.failed = true
		changed = true
	}
	// Codex went on to another sign-in while this one was asked about:
	// told, for the next look to ask about that one
	if codexDesk.want != who {
		changed = true
	}
	codexDesk.asking = false
	codexDesk.Unlock()
	if err != nil {
		log.Println("codex: the ChatGPT backend's model list for the Codex app:", err)
	}
	if changed && CodexDesktopChanged != nil {
		CodexDesktopChanged()
	}
	close(done)
}

// codexDeskSlugs is the slugs a backend list has.
func codexDeskSlugs(b []byte) map[string]bool {
	var list struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	_ = json.Unmarshal(b, &list)
	out := make(map[string]bool, len(list.Models))
	for _, m := range list.Models {
		out[m.Slug] = true
	}
	return out
}

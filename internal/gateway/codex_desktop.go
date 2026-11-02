package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

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

const (
	// codexDeskFor is how long a list the backend gave stands before it
	// is asked for again; codexDeskRetry how long after one it didn't
	codexDeskFor   = time.Hour
	codexDeskRetry = 5 * time.Minute
	codexDeskWait  = 30 * time.Second
)

var codexDesk struct {
	sync.Mutex
	body       []byte
	got, tried time.Time
	asking     bool
	// revoked: the last ask had the backend call the sign-in revoked
	// (provider.CodexListRevoked)
	revoked bool
	// who is the sign-in body and revoked are of (codexDeskWho); asked
	// the one the last ask was for; want the one a caller last asked about
	who, asked, want string
	done             chan struct{} // closed as the ask under way ends
}

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
	codexDesk.Lock()
	codexDesk.want = who
	body, revoked := codexDesk.body, codexDesk.revoked
	mine := codexDesk.who == who
	if !mine {
		body, revoked = nil, false
	}
	stale := body == nil || time.Since(codexDesk.got) >= codexDeskFor
	if stale && !codexDesk.asking && (codexDesk.asked != who || time.Since(codexDesk.tried) >= codexDeskRetry) {
		codexDesk.asking, codexDesk.tried, codexDesk.asked = true, time.Now(), who
		codexDesk.done = make(chan struct{})
		go codexDeskRefresh(codexDesk.done, who)
	}
	codexDesk.Unlock()
	if body == nil && revoked {
		var own []any
		for _, e := range codexcat.CacheEntries() {
			own = append(own, e)
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
	b, err := codexDeskFetch(ctx)
	codexDesk.Lock()
	changed := false
	if codexDesk.who != who {
		// another sign-in's list, and what was said of it, is not who's
		codexDesk.body, codexDesk.revoked, codexDesk.who = nil, false, who
	}
	if err == nil {
		changed = !bytes.Equal(b, codexDesk.body) || codexDesk.revoked
		codexDesk.body, codexDesk.got, codexDesk.revoked = b, time.Now(), false
	} else if revoked := provider.CodexListRevoked(err); revoked != codexDesk.revoked {
		// told only where it changes what is handed out: with no list
		// from before (CodexDesktopCatalog)
		changed = codexDesk.body == nil
		codexDesk.revoked = revoked
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

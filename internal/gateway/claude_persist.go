package gateway

import (
	"encoding/json"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/steady"
)

// The idle runs and the shelf survive the gateway's end (#6292, recih on
// X): a magpie that stops (an update, a restart) keeps the saved session
// of each Claude Code waiting for its conversation's next turn, and the
// gateway that starts next resumes it (--resume), told the turn alone,
// where a new Claude Code told the whole conversation wrote ~100k tokens
// to the cache again.
//
// The file is claude-shelf.json beside the settings. It is written once,
// as the gateway ends (abortAll), and read by the next gateway the first
// time a turn finds nothing on its shelf (the magpie a restart hands over
// to may start before the old one has written it); a read takes the file
// away, so a session that went on in a run is not resumed from a record of
// where it stood. A record is a hint: the file is unknown to the shelf
// until it parses whole, each session must still be on disk (unshelve's
// start checks the folder and the file again), and one that waited longer
// than a run would have is let go. Anything else falls back to the whole
// conversation told to a new Claude Code, as before.

const shelfFileVersion = 1

type shelfFile struct {
	Version int          `json:"version"`
	Saved   time.Time    `json:"saved"`
	Entries []shelfEntry `json:"entries"`
}

// shelfEntry is a savedSession as the file has it. The conversation's
// fingerprint is Key (turnKey, which holds the account, the model, the
// settings and the messages), the folders are Claude Code's project
// folders under its config folder (HOME or CLAUDE_CONFIG_DIR) for the work
// folder, Owner the account.
type shelfEntry struct {
	Session    string          `json:"session"`
	Sessions   []string        `json:"sessions"`
	Owner      string          `json:"owner"`
	Conv       string          `json:"conv"`
	Key        string          `json:"key"`
	Behind     string          `json:"behind,omitempty"`
	BehindTurn string          `json:"behindTurn,omitempty"`
	BehindLine string          `json:"behindLine,omitempty"`
	BehindMsg  string          `json:"behindMsg,omitempty"`
	Tools      []string        `json:"tools,omitempty"`
	At         time.Time       `json:"at"`
	Until      time.Time       `json:"until,omitzero"` // set for a session kept till a limit resets (keepForReset)
	TUI        bool            `json:"tui,omitempty"`
	Appended   bool            `json:"appended,omitempty"`
	Cache      shelfEntryCache `json:"cache"`
}

type shelfEntryCache struct {
	Count int    `json:"count"`
	Floor string `json:"floor"`
	TTL   string `json:"ttl,omitempty"`
}

func shelfPath() string { return filepath.Join(filepath.Dir(provider.Path()), "claude-shelf.json") }

// shelfRead is when the file was last read (its mtime), per path, so a
// turn that finds nothing looks at the file again only when it changed.
var shelfRead sync.Map

// keepShelf shelves the runs waiting for their next turn, and writes the
// shelf to the file for the next gateway (b.mu not held). Called as the
// gateway ends, before its runs are ended: a shelved run's session is
// left on disk when it ends.
func (b *subscriptionBridge) keepShelf() {
	b.mu.Lock()
	for key, run := range b.idle {
		if run.idleKey == key {
			b.shelve(run)
		}
	}
	var entries []shelfEntry
	for _, s := range b.shelf {
		s.mu.Lock()
		gone := s.gone
		s.mu.Unlock()
		if gone || !s.saved() || !time.Now().Before(s.expires()) {
			continue
		}
		e := shelfEntry{Session: s.session, Sessions: s.sessions, Owner: s.owner, Conv: s.conv, Key: s.key,
			Behind: s.behind, BehindTurn: s.behindTurn, BehindLine: s.behindLine, BehindMsg: s.behindMsg,
			At: s.at, Until: s.until, TUI: s.tui, Appended: s.appended,
			Cache: shelfEntryCache{s.cachePrefix.count, s.cachePrefix.floor, s.cachePrefix.ttl}}
		for name := range s.tools {
			e.Tools = append(e.Tools, name)
		}
		entries = append(entries, e)
	}
	b.mu.Unlock()
	if len(entries) == 0 {
		return
	}
	path := shelfPath()
	data, err := json.Marshal(shelfFile{Version: shelfFileVersion, Saved: time.Now(), Entries: entries})
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	if path, err = edit.Target(path); err != nil {
		return
	}
	tmp := path + ".magpie-tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	if steady.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return
	}
	log.Printf("claude: %d saved session(s) kept for the next start", len(entries))
}

// loadShelf puts the saved sessions an earlier gateway left on the shelf,
// when the file changed since this one looked (b.mu not held). A file that
// can't be read whole is unknown: nothing is taken from it.
func (b *subscriptionBridge) loadShelf() {
	path := shelfPath()
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if last, ok := shelfRead.Load(path); ok && last.(time.Time).Equal(fi.ModTime()) {
		return
	}
	shelfRead.Store(path, fi.ModTime())
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f shelfFile
	if json.Unmarshal(data, &f) != nil || f.Version != shelfFileVersion {
		return
	}
	// taken away: what a run goes on with from here is not what it says
	if os.Remove(path) != nil {
		return
	}
	for _, e := range f.Entries {
		s := e.saved()
		if s == nil {
			continue
		}
		b.mu.Lock()
		if b.shelf[s.key] != nil {
			b.mu.Unlock()
			continue
		}
		b.shelf[s.key] = s
		b.shelfTimer(s)
		b.mu.Unlock()
	}
}

// saved is the entry as a savedSession, nil where it is not one to trust:
// a session that isn't a plain name, a folder that isn't a project folder
// of Claude Code's, no session file there, or one waiting longer than a run
// would have.
func (e shelfEntry) saved() *savedSession {
	// a limit's reset further off than quotaShelfLongest is a misreading
	if e.Session == "" || e.Key == "" || len(e.Sessions) == 0 || strings.ContainsAny(e.Session, `/\`) || strings.Contains(e.Session, "..") ||
		e.Until.After(e.At.Add(quotaShelfLongest)) {
		return nil
	}
	for _, dir := range e.Sessions {
		if !filepath.IsAbs(dir) || filepath.Base(filepath.Dir(dir)) != "projects" {
			return nil
		}
	}
	s := &savedSession{session: e.Session, sessions: e.Sessions, owner: e.Owner, conv: e.Conv, key: e.Key,
		behind: e.Behind, behindTurn: e.BehindTurn, behindLine: e.BehindLine, behindMsg: e.BehindMsg,
		at: e.At, until: e.Until, tui: e.TUI, appended: e.Appended, cachePrefix: cachePrefix{e.Cache.Count, e.Cache.Floor, e.Cache.TTL}}
	if len(e.Tools) > 0 {
		s.tools = map[string]bool{}
		for _, name := range e.Tools {
			s.tools[name] = true
		}
	}
	if !time.Now().Before(s.expires()) || !s.saved() {
		return nil
	}
	return s
}

// A turn Anthropic refuses for the account's limit (a 429 before any of
// the reply: "You've hit your 5-hour limit · resets 9pm") ends its run,
// and the conversation's session went with it: the turn asked again once
// the limit reset, hours later, found no run and no saved session, and a
// new Claude Code was told the whole conversation, 354,900 tokens written
// to the cache again (#6292, recih). The session is kept on the shelf
// instead, as it stood before the refused turn, until the limit resets
// and quotaShelfMargin after, quotaShelfLongest at most.
const (
	// quotaShelfMargin is how long past the reset the session waits: a
	// client asks again when its user comes back, not on the minute
	quotaShelfMargin = time.Hour
	// quotaShelfUnsaid is the reset taken when neither the refusal nor
	// the account's kept limits say when: Claude's shortest window
	quotaShelfUnsaid = 5 * time.Hour
	// quotaShelfLongest bounds the wait: a week's window, and a day over,
	// as long as an account out of quota sits out (longestQuota)
	quotaShelfLongest = longestQuota
)

// quotaShelfFor is how long a session kept past a turn refused for the
// limit of owner's account waits, msg the refusal's words: until the
// reset msg names, else the latest reset of the account's windows that
// are used up, else quotaShelfUnsaid; quotaShelfMargin over, and never
// less than a run would have waited (idleLongest).
func quotaShelfFor(owner, msg string, now time.Time) time.Duration {
	d := resetsAt([]byte(msg), now)
	if d <= 0 {
		if user, _ := ownerAccount(owner); user != "" {
			for _, l := range provider.KeptClaudeLimits(user) {
				if w := time.Unix(l.ResetsAt, 0).Sub(now); l.Used >= 1 && w > d {
					d = w
				}
			}
		}
	}
	if d <= 0 {
		d = quotaShelfUnsaid
	}
	return min(max(d+quotaShelfMargin, idleLongest), quotaShelfLongest)
}

// keepForReset shelves the session of run, whose turn of req was refused
// for the limit of owner's account before any of its reply (relay, msg
// the refusal's words), as the conversation stood before that turn: a
// copy cut where the turn began, so the refused turn, its user message
// and Claude Code's error line, is in none of it, and the turn asked
// again resumes it told the turn alone, as it would have been. Where
// that is the run knows when it was started from a saved session and
// has answered no turn since (fromLen, the file's length then), or by
// the line and the reply it had heard as the turn began (backLine,
// backMsg). Called before the run ends, which removes its own file.
// Nothing is kept for a run started for the turn (there is no
// conversation before it in its session), a conversation taken back
// (rewound) and not gone on with since, or one with the client's own
// cache marks, as no branch is cut of one.
func (b *subscriptionBridge) keepForReset(run *subscriptionRun, req *Request, owner, msg string) {
	key, since := nextTurn(req, owner)
	if key == "" {
		return
	}
	run.mu.Lock()
	sid, sessions, tools := run.sessionID, slices.Clone(run.sessions), maps.Clone(run.tools)
	skip := run.closed || run.rewound && !run.replied || run.clientMarks || run.rewinding != nil
	fromLen, backLine, backMsg := run.fromLen, run.backLine, run.backMsg
	tui, appended, prefix := run.tui, run.appended, run.cachePrefix
	run.mu.Unlock()
	if skip || sid == "" || len(sessions) == 0 {
		return
	}
	var cut string
	var err error
	switch {
	case fromLen > 0:
		// the lines it was started from, whole: what it wrote since is
		// the refused turn
		cut, err = cutSessionWhere(sessions, sid, "the saved session's end", func(b []byte, _ bool) int {
			if int64(len(b)) < fromLen || b[fromLen-1] != '\n' {
				return -1
			}
			return int(fromLen)
		})
	case backLine != "" || backMsg != "":
		cut, err = cutReply(sessions, sid, backLine, backMsg)
	default:
		return
	}
	if err != nil {
		log.Printf("claude: the session of a turn refused for the account's limit couldn't be cut before the turn (%v); the turn asked again is told the whole conversation", err)
		return
	}
	conv := ""
	if keys := conversationKeys(owner, req.Messages[:len(req.Messages)-len(since)]); len(keys) > 0 {
		conv = keys[len(keys)-1]
	}
	now := time.Now()
	s := &savedSession{session: cut, sessions: sessions, owner: owner, conv: conv, key: key, tools: tools,
		at: now, until: now.Add(quotaShelfFor(owner, msg, now)), tui: tui, appended: appended, cachePrefix: prefix}
	b.mu.Lock()
	if old := b.shelf[key]; old != nil {
		go old.discard()
	}
	b.shelf[key] = s
	b.shelfTimer(s)
	b.trimShelf()
	b.mu.Unlock()
	log.Printf("claude: a turn refused for the account's limit leaves its conversation's session, cut before the turn, for the turn asked again until %s", heldClock(s.until, now))
}

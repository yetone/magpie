package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Which agents' session files magpie may read (RustCats on X: a new
// install showed every agent's history before anyone agreed to it). Reads
// is the one gate: every lister asks it before it lists an agent's
// folder, the listings are filtered by it again, and so are the readers
// that take a session or a call already listed (a cache made before a
// switch was turned off). A fresh install reads nothing until the
// Sessions page's consent, or Settings, allows an agent; an install that
// already read sessions keeps reading them all.

// ErrNotRead: the session's agent is one magpie may not read.
var ErrNotRead = errors.New("magpie is not allowed to read this agent's sessions (Settings, Sessions)")

// settled is the decision SettleReads made this run, so a fresh install
// that couldn't save it (settings.json unreadable) still reads nothing,
// and the listings don't write settings.json again and again.
var settled struct {
	sync.Mutex
	r    *settings.SessionReads
	done bool
}

// readsAllInTests: in a test binary an install nothing settled reads every
// agent, as before, and SettleReads writes nothing to settings.json; the
// tests of the gate turn it off.
var readsAllInTests = testing.Testing()

// Reads is whether magpie may read agent's own session files.
func Reads(agent string) bool {
	return currentReads().Reads(agent)
}

// readsNone is the answer while OwnOff holds: no agent's files are read.
var readsNone = &settings.SessionReads{Asked: true}

// OwnOff, when set and true, is gateway mode on magpie web's page
// (gui.gatewayMode; paultian on Discord): the page lists what came
// through this gateway from other computers, never this computer's own
// agents' session files, so no agent's files are read, whatever
// SessionReads says. Gateway-recorded sessions (the usage ledger and the
// gateway's own store) are not behind this gate and still show. It is
// asked on every Reads, so it must be cheap.
var OwnOff atomic.Pointer[func() bool]

// ownOff is whether OwnOff is set and says so.
func ownOff() bool {
	f := OwnOff.Load()
	return f != nil && (*f)()
}

func currentReads() *settings.SessionReads {
	if ownOff() {
		return readsNone
	}
	if r := settings.Shared().SessionReads; r != nil {
		return r
	}
	if readsAllInTests {
		return &settings.SessionReads{Asked: true, All: true}
	}
	return SettleReads()
}

// SettleReads decides, once, what an install with no SessionReads reads:
// one that already read sessions (its sessions cache or call cache is
// there) keeps reading every agent; a fresh one reads none until asked.
// The decision is saved, so a cache written later can't turn a fresh
// install into one that reads everything. A value already saved is kept.
func SettleReads() *settings.SessionReads {
	if r := settings.Shared().SessionReads; r != nil {
		return r
	}
	if readsAllInTests {
		return &settings.SessionReads{Asked: true, All: true}
	}
	settled.Lock()
	defer settled.Unlock()
	if settled.done {
		return settled.r
	}
	r := &settings.SessionReads{}
	if readBefore() {
		r = &settings.SessionReads{Asked: true, All: true}
	}
	var saved *settings.SessionReads
	err := settings.SetSessionReads(func(cur *settings.SessionReads) *settings.SessionReads {
		if cur != nil {
			saved = cur
			return cur
		}
		return r
	})
	if saved != nil {
		r = saved
	}
	if err != nil {
		// not saved: decided for this run only; an unreadable
		// settings.json is not a reason to read more
		r = &settings.SessionReads{}
	}
	settled.r, settled.done = r, true
	return r
}

// forgetSettled is for tests that settle again in a new HOME.
func forgetSettled() {
	settled.Lock()
	settled.r, settled.done = nil, false
	settled.Unlock()
}

// readBefore is whether this install read sessions before SessionReads
// existed: it kept a sessions cache, or a call cache of any version.
func readBefore() bool {
	if _, err := os.Stat(CachePath()); err == nil {
		return true
	}
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(CachePath()), "calls-v*"))
	return len(m) > 0
}

// SetReads records the user's choice: all, when given, is whether an
// agent nobody named is read (allow all, Settings' "agents found later");
// each agent in agents is read or not as given; the others stay as they
// were. The lists are read again.
func SetReads(all *bool, agents map[string]bool) error {
	err := settings.SetSessionReads(func(cur *settings.SessionReads) *settings.SessionReads {
		r := &settings.SessionReads{Asked: true}
		if cur != nil {
			r.All = cur.All
			for a, on := range cur.Agents {
				if r.Agents == nil {
					r.Agents = map[string]bool{}
				}
				r.Agents[a] = on
			}
		}
		if all != nil {
			r.All = *all
		}
		for a, on := range agents {
			if r.Agents == nil {
				r.Agents = map[string]bool{}
			}
			r.Agents[a] = on
		}
		return r
	})
	if err != nil {
		return err
	}
	settled.Lock()
	settled.r, settled.done = nil, false
	settled.Unlock()
	Reset()
	return nil
}

// allowed is fs without the files of agents magpie may not read.
func allowed(fs []file) []file {
	r := currentReads()
	// most polls read every agent: fs as it is then, not a copy of each
	// file listed (#2278)
	k := slices.IndexFunc(fs, func(f file) bool { return !r.Reads(f.agent) })
	if k < 0 {
		return fs
	}
	out := slices.Clone(fs[:k])
	for _, f := range fs[k+1:] {
		if r.Reads(f.agent) {
			out = append(out, f)
		}
	}
	return out
}

// Found is the agents with a session store on this computer, by a look at
// their folders only (nothing in them is read): those the consent lists.
// WSL distros are not looked into; allow all covers them.
func Found() []string {
	// gateway mode reads none of them, so it asks about none (OwnOff)
	if ownOff() {
		return nil
	}
	exists := func(p string) bool {
		if p == "" {
			return false
		}
		_, err := os.Stat(p)
		return err == nil
	}
	var out []string
	add := func(agent string, paths ...string) {
		for _, p := range paths {
			if exists(p) {
				out = append(out, agent)
				return
			}
		}
	}
	add("claude", filepath.Join(ClaudeDir(), "projects"))
	var desktop []string
	for _, d := range callDesktopDirs() {
		desktop = append(desktop, filepath.Join(d, "local-agent-mode-sessions"), filepath.Join(d, "claude-code-sessions"))
	}
	add("claude-desktop", desktop...)
	var codex []string
	for _, h := range CodexDirs() {
		codex = append(codex, filepath.Join(h, "sessions"), filepath.Join(h, "archived_sessions"))
	}
	add("codex", codex...)
	add("opencode", OpenCodeDir())
	add("pi", PiDir())
	add("raft", filepath.Join(SlockDir(), "agents"))
	add("cindy", cindySessionDirs()...)
	add("zcode", zcodeDB())
	add("dsh", filepath.Join(DshDir(), "sessions"))
	add("cline", ClineSessionDir())
	add("qoder", filepath.Join(QoderDir("qoder"), "projects"))
	add("qoder-cn", filepath.Join(QoderDir("qoder-cn"), "projects"))
	add("grok", filepath.Join(GrokDir(), "sessions"))
	add("workbuddy", filepath.Join(WorkBuddyDir(), "projects"))
	add("workbuddy-ai", filepath.Join(WorkBuddyAIDir(), "projects"))
	add("codebuddy", filepath.Join(CodeBuddyDir(), "projects"))
	add("omp", filepath.Join(OmpDir(), "sessions"))
	add("droid", filepath.Join(FactoryDir(), "sessions"))
	add("cursor", filepath.Join(CursorDir(), "chats"), filepath.Join(cursorDataDir(), "projects"), cursorEditorDB())
	add("hermes", HermesDirs()...)
	add("alma", almaDB())
	add("minimax-code", minimaxSessions())
	add("kimi", kimiSessions())
	add("agy", filepath.Join(AgyDir(), "brain"))
	add("gemini", geminiTmpDir())
	return out
}

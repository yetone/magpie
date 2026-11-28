package gateway

// GET /v1/magpie/claude-runs: the Claude Code processes the Claude
// subscription's bridge has running, for `magpie claude-code runs`.
//
// Each conversation on a Claude account is answered by a real Claude Code
// (`claude -p`), with magpie's MCP helper (`magpie claude-mcp-helper`)
// beside it for the client's tools, and a run is kept for the
// conversation's next turn (idleMost, idleLongest) or while it waits on
// the client's tool results (parkLongest, its 30-minute timer). Claude
// Code's parallel subagents are conversations of their own, so a session
// that starts many has many pairs running. In Activity Monitor or Task
// Manager they show as Claude Code's version ("2.1.311": its binary is
// named so) and "magpie", with nothing to say they are magpie's or why
// they stay (@Candydidi3 on X: "magpie里拉起很多子进程"). This lists each
// with its pid, model, account, what it waits for and since when.
//
// With ?screen=1 each run of Claude Code's interactive mode (claudetui)
// has its screen as it stands too, masked as the logs are (screenSafe),
// for `magpie claude-code screen` and the Claude editor's look at it: a
// run that seems stuck shows where (FremontDango on Discord, #8075). It
// is read from the helper, which only reads it: nothing is typed.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/redact"
)

// ClaudeRunsPath is where the gateway lists its Claude Code runs.
const ClaudeRunsPath = "/v1/magpie/claude-runs"

// ClaudeRun is one Claude Code process a conversation runs on.
type ClaudeRun struct {
	PID     int    `json:"pid"`
	Model   string `json:"model"`
	Account string `json:"account,omitempty"`
	// State is "answering" (a turn is under way), "idle" (kept for the
	// conversation's next turn) or "tools" (its reply called the client's
	// tools, whose results it waits for); Since is when it began to.
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	Started time.Time `json:"started"`
	// Ends is when magpie lets it go if nothing comes for it (its timer).
	Ends time.Time `json:"ends,omitzero"`
	// RSS is Claude Code's resident memory in bytes, 0 when not read.
	RSS int64 `json:"rss,omitempty"`
	// Screen is, asked for (?screen=1), the interactive Claude Code's
	// screen as it stands, row by row, masked (screenSafe); ScreenWhy
	// says why there is none.
	Screen    []string `json:"screen,omitempty"`
	ScreenWhy string   `json:"screenWhy,omitempty"`
}

// ClaudeRuns is what ClaudeRunsPath answers.
type ClaudeRuns struct {
	Object string      `json:"object"` // "claude-runs"
	Runs   []ClaudeRun `json:"runs"`
	// Live is every Claude Code magpie has running, the runs and its
	// one-off asks (a test, a warm-up, a /usage read) together.
	Live int `json:"live"`
	// Most is how many may run at once (0: no bound), MostWhy what says so.
	Most    int    `json:"most"`
	MostWhy string `json:"mostWhy,omitempty"`
	// IdleMost runs are kept for a next turn, IdleFor each at most.
	IdleMost int           `json:"idleMost"`
	IdleFor  time.Duration `json:"idleFor"`
}

// runTimeout is how long a run is kept with nothing coming for it while
// a turn is under way or its client's tools run: its timer (endIn).
const runTimeout = 30 * time.Minute

// claudeRunList is the bridge's runs, oldest first, and claudeRoom's count;
// with screens, each one's screen as it stands (screen).
func (b *subscriptionBridge) claudeRunList(ctx context.Context, screens bool) ClaudeRuns {
	type seen struct {
		run  *subscriptionRun
		idle time.Time
	}
	var all []seen
	b.mu.Lock()
	for _, r := range b.runs {
		s := seen{run: r}
		if r.idleKey != "" && b.idle[r.idleKey] == r {
			s.idle = r.idleAt
		}
		all = append(all, s)
	}
	b.mu.Unlock()
	out := ClaudeRuns{Object: "claude-runs", Runs: []ClaudeRun{}, IdleMost: idleMost, IdleFor: idleLongest}
	var listed []*subscriptionRun // out.Runs' runs, in their order
	for _, s := range all {
		r := s.run
		r.mu.Lock()
		closed, parked, tree, active, ends := r.closed, r.parkedAt, r.tree, r.active, r.ends
		r.mu.Unlock()
		if closed {
			continue
		}
		user, _ := ownerAccount(r.owned())
		c := ClaudeRun{PID: r.pid(), Model: r.model, Account: user, Started: r.started, State: "answering", Since: active, Ends: ends}
		switch {
		case !s.idle.IsZero():
			c.State, c.Since = "idle", s.idle
		case !parked.IsZero():
			c.State, c.Since = "tools", parked
		}
		if c.Since.IsZero() {
			c.Since = r.started
		}
		if tree != nil {
			if rss, ok := claudeRunRSS(tree); ok {
				c.RSS = rss
			}
		}
		out.Runs = append(out.Runs, c)
		listed = append(listed, r)
	}
	if screens {
		// each asked at once: a helper that doesn't answer holds up
		// screenWait, not the others' looks
		var looks sync.WaitGroup
		for i, r := range listed {
			looks.Go(func() {
				out.Runs[i].Screen, out.Runs[i].ScreenWhy = r.screen(ctx)
			})
		}
		looks.Wait()
	}
	slices.SortFunc(out.Runs, func(a, b ClaudeRun) int { return a.Started.Compare(b.Started) })
	claudeRoom.mu.Lock()
	out.Live = claudeRoom.live
	claudeRoom.mu.Unlock()
	out.Most, out.MostWhy = claudeRunsMost()
	return out
}

// claudeRuns answers ClaudeRunsPath, to a program on this computer only:
// it names the accounts and models in use.
func (s *Server) claudeRuns(w http.ResponseWriter, r *http.Request) {
	if why := profileRefused(r, "list of Claude Code processes", "magpie claude-code runs"); why != "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"message": why}})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.subscription.claudeRunList(r.Context(), r.URL.Query().Get("screen") == "1"))
}

// screenWait is how long a helper may take to answer a look at its screen:
// it answers at once (claudetui's reading), so one that doesn't is stuck
// or gone.
var screenWait = 2 * time.Second

// screenPrefix begins a look's request id: its answer is told apart from
// the others' in readOutput, as it answers no message.
const screenPrefix = "screen-"

// screen is the interactive Claude Code's screen as it stands, masked
// (screenSafe), asked of its helper (claudetui.ScreenRequest); or why
// there is none.
func (r *subscriptionRun) screen(ctx context.Context) ([]string, string) {
	r.mu.Lock()
	tui, in := r.tui, r.stdin != nil
	r.mu.Unlock()
	if !tui || !in {
		return nil, "Claude Code runs as claude -p here, which draws no screen"
	}
	reply, err := r.control(ctx, screenPrefix+randomToken(), map[string]any{"subtype": claudetui.ScreenRequest}, screenWait)
	switch {
	case err != nil:
		return nil, "the screen couldn't be read: " + logSafe(err.Error())
	case reply.Subtype != "success":
		return nil, "the screen couldn't be read: " + logSafe(reply.Error)
	case len(reply.Response.Rows) == 0:
		return nil, "Claude Code hasn't drawn anything yet"
	}
	return screenSafe(reply.Response.Rows), ""
}

// screenSafe is a screen's rows masked as logSafe masks a log line, the
// layout kept: secrets out (redact.Scrub), this user's home folder and any
// other written ~.
func screenSafe(rows []string) []string {
	s := strings.Join(rows, "\n")
	s = homeSafe(s)
	s = redact.Scrub(s)
	return strings.Split(s, "\n")
}

// homeSafe writes ~ for this user's home folder in s, in each of its
// spellings, and for any other home folder (homePath).
func homeSafe(s string) string {
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		for _, h := range []string{home, filepath.ToSlash(home), strings.ReplaceAll(home, "/", `\`)} {
			s = strings.ReplaceAll(s, h, "~")
		}
	}
	return homePath.ReplaceAllString(s, "~")
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// `magpie sessions search` and `magpie sessions show` (yansheng1003 on
// X: a command an agent can call to search its sessions): the Sessions
// page's search over every agent's sessions (sessions.SearchSaid, the
// index of what was said) and the sessions MCP server's text of a hit
// and of a session (sessions.SaidText, SessionText), read here without
// the app running.

const sessionsSearchUsage = "usage: magpie sessions search <words…> [--agent <agent>] [--days N|today|all] [--limit N] [--wait <duration>] [--json]"

const sessionsShowUsage = "usage: magpie sessions show <agent:id> [--find <words>] [--from N] [--max-chars N] [--full] [--json]"

// searchWait is how long `magpie sessions search` waits for the index to
// be brought up to date: the first time on a long history it is made,
// and what was made is kept for the next run.
const searchWait = 60 * time.Second

// sessionsTextHow is how the command's texts say to ask for more.
var sessionsTextHow = sessions.TextHow{
	More: "ask with a higher --limit",
	Read: "Read one with: magpie sessions show <agent:id> --find <words>",
	// From and Whole: showHow, which keeps show's own flags
}

// errNotAsked: nothing is read until the user allows it.
var errNotAsked = errors.New(notAskedLine)

type searchOpts struct {
	words []string
	agent string
	days  int // -1: every day
	limit int
	wait  time.Duration
	json  bool
	find  string
	from  int
	most  int
	full  bool // show: each message whole, never cut at 8000 characters
}

// parseSearchArgs reads the arguments of search (or show): words, and
// the flags each takes. A flag it doesn't know is an error, never words
// to look for; after -- every argument is words.
func parseSearchArgs(args []string, show bool) (searchOpts, error) {
	o := searchOpts{days: -1, limit: 10, wait: searchWait}
	use := sessionsSearchUsage
	if show {
		use = sessionsShowUsage
	}
	words := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !words || !strings.HasPrefix(a, "-") || a == "-" {
			o.words = append(o.words, a)
			continue
		}
		if a == "--" {
			words = false
			continue
		}
		name, v, has := strings.Cut(a, "=")
		takes := map[string]bool{"--agent": !show, "-a": !show, "--days": !show, "-d": !show, "--limit": !show, "-n": !show, "--wait": !show,
			"--find": show, "--from": show, "--max-chars": show}
		switch {
		case name == "--json":
			if has {
				return o, fmt.Errorf("--json takes no value\n%s", use)
			}
			o.json = true
			continue
		case name == "--full" && show:
			if has {
				return o, fmt.Errorf("--full takes no value\n%s", use)
			}
			o.full = true
			continue
		case name == "-h" || name == "--help":
			return o, fmt.Errorf("%s", use)
		case !takes[name]:
			return o, fmt.Errorf("unknown flag %q\n%s", a, use)
		}
		if !has {
			if i+1 >= len(args) || (strings.HasPrefix(args[i+1], "-") && name != "--find") {
				return o, fmt.Errorf("%s takes a value\n%s", name, use)
			}
			i++
			v = args[i]
		}
		num := func() (int, error) {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("%s takes a number\n%s", name, use)
			}
			return n, nil
		}
		var err error
		switch name {
		case "--agent", "-a":
			o.agent = v
		case "--days", "-d":
			switch strings.ToLower(v) {
			case "today", "1d":
				o.days = 1
			case "all", "0":
				o.days = -1
			default:
				n, e := strconv.Atoi(strings.TrimSuffix(strings.ToLower(v), "d"))
				if e != nil || n < 0 {
					return o, fmt.Errorf("--days takes a number of days, today or all\n%s", use)
				}
				o.days = n
			}
		case "--limit", "-n":
			if o.limit, err = num(); err == nil && (o.limit < 1 || o.limit > 100) {
				err = fmt.Errorf("--limit takes a number from 1 to 100\n%s", use)
			}
		case "--wait":
			if o.wait, err = time.ParseDuration(v); err != nil || o.wait < 0 {
				err = fmt.Errorf("--wait takes a duration (30s, 2m)\n%s", use)
			}
		case "--find":
			o.find = v
		case "--from":
			o.from, err = num()
		case "--max-chars":
			o.most, err = num()
		}
		if err != nil {
			return o, err
		}
	}
	if len(o.words) == 0 {
		return o, fmt.Errorf("%s", use)
	}
	if show && len(o.words) != 1 {
		return o, fmt.Errorf("one session at a time\n%s", use)
	}
	return o, nil
}

// signalContext ends on Ctrl+C, so a long first index stops cleanly
// (what was indexed is kept).
func signalContext() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// sessionsSearchTo: `magpie sessions search <words…>`.
func sessionsSearchTo(w io.Writer, args []string) error {
	o, err := parseSearchArgs(args, false)
	if err != nil {
		return err
	}
	if !sessions.SettleReads().Asked {
		return errNotAsked
	}
	needle := sessions.NeedleOf(strings.Join(o.words, " "))
	if o.agent != "" {
		if o.agent, err = searchAgent(o.agent); err != nil {
			return err
		}
		if !sessions.Reads(o.agent) {
			return fmt.Errorf("%w: magpie sessions read on %s allows it", sessions.ErrNotRead, o.agent)
		}
	}
	ctx, stop := signalContext()
	defer stop()
	var progress func(int, int)
	if isTerminal(os.Stderr) && !o.json {
		shown := false
		progress = func(n, of int) {
			if n < of {
				fmt.Fprintf(os.Stderr, "\rIndexing %d of %d sessions…", n, of)
				shown = true
			}
		}
		defer func() {
			if shown {
				fmt.Fprint(os.Stderr, "\r\033[K")
			}
		}()
	}
	if err := sessions.SaidReady(ctx, o.wait, progress); err != nil {
		return err
	}
	var since time.Time
	if o.days > 0 {
		loc := sessions.Local() // Settings' time zone, else the system's
		y, m, d := time.Now().In(loc).Date()
		since = time.Date(y, m, d-(o.days-1), 0, 0, 0, 0, loc)
	}
	found, err := sessions.SearchSaidSince(ctx, needle, o.agent, since, o.limit)
	if errors.Is(err, sessions.ErrNoWords) {
		return fmt.Errorf("nothing to search for: the words need a letter or digit\n%s", sessionsSearchUsage)
	}
	if err != nil {
		return err
	}
	if o.json {
		for i := range found.Hits {
			found.Hits[i].Session.Path = tildePath(found.Hits[i].Session.Path)
		}
		return writeJSON(w, found)
	}
	fmt.Fprintln(w, sessions.SaidText(found, needle, sessionsTextHow))
	return nil
}

// sessionsShowTo: `magpie sessions show <agent:id>`.
func sessionsShowTo(w io.Writer, args []string) error {
	o, err := parseSearchArgs(args, true)
	if err != nil {
		return err
	}
	if !sessions.SettleReads().Asked {
		return errNotAsked
	}
	s, err := findSession(o.words[0])
	if err != nil {
		return err
	}
	ask := sessions.ReadAsk{Find: o.find, From: o.from, MaxChars: o.most, Whole: o.full}
	how := showHow(o)
	if o.json {
		// the parts the text would show, from where it would: --json
		// read from the start whatever --from said (hdhfhf_hdh219 on X)
		tr, err := sessions.ReadTranscript(s, ask)
		if err != nil {
			return err
		}
		var notes []string
		cut, first := 0, -1
		for i, p := range tr.Parts {
			if p.Cut > 0 && (p.Kind == "text" || p.Kind == "context") {
				if cut++; first < 0 {
					first = tr.From + i
				}
			}
		}
		if cut > 0 {
			notes = append(notes, fmt.Sprintf("%d message(s) cut short, the first at %d (each part's cut is the characters left off); --full gives every message whole: %s", cut, first, how.Whole(s, first)+" --json"))
		}
		next := tr.Next()
		if next > 0 {
			notes = append(notes, "more from message "+strconv.Itoa(next)+": "+how.From(s, next)+" --json")
		}
		s.Path = tildePath(s.Path)
		return writeJSON(w, struct {
			Session    sessions.Session    `json:"session"`
			Transcript sessions.Transcript `json:"transcript"`
			// Next is the --from of the rest, when there is more
			Next  int      `json:"next,omitempty"`
			Notes []string `json:"notes,omitempty"`
		}{s, tr, next, notes})
	}
	text, err := sessions.SessionText(s, ask, how)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, text)
	return nil
}

// showHow is how show's text says to read on: the next read keeps the
// flags this one was given (--max-chars, --full), and a message cut
// short names --full.
func showHow(o searchOpts) sessions.TextHow {
	how := sessionsTextHow
	var keep string
	if o.most > 0 {
		keep += " --max-chars " + strconv.Itoa(o.most)
	}
	if o.full {
		keep += " --full"
	}
	how.From = func(s sessions.Session, n int) string {
		return fmt.Sprintf("magpie sessions show %s --from %d%s", sessions.SessionRef(s), n, keep)
	}
	how.Whole = func(s sessions.Session, n int) string {
		return fmt.Sprintf("magpie sessions show %s --from %d --full", sessions.SessionRef(s), n)
	}
	return how
}

// findSession is the session ref names: agent:id as search prints it,
// or an id alone when one agent has it.
func findSession(ref string) (sessions.Session, error) {
	agent, id, ok := strings.Cut(ref, ":")
	if ok && agent != "" && id != "" {
		if !sessions.Reads(agent) {
			return sessions.Session{}, fmt.Errorf("%w: magpie sessions read on %s allows it", sessions.ErrNotRead, agent)
		}
		if s, found := sessions.Find(agent, id); found {
			return s, nil
		}
		return sessions.Session{}, fmt.Errorf("no session %s (magpie sessions search finds them)", ref)
	}
	var have []sessions.Session
	for _, s := range sessions.List(sessions.All) {
		if s.ID == ref && !slices.ContainsFunc(have, func(h sessions.Session) bool { return h.Agent == s.Agent }) {
			have = append(have, s)
		}
	}
	switch len(have) {
	case 0:
		return sessions.Session{}, fmt.Errorf("no session %s (magpie sessions search finds them; name one as agent:id)", ref)
	case 1:
		if !sessions.Reads(have[0].Agent) {
			return sessions.Session{}, sessions.ErrNotRead
		}
		if s, found := sessions.Find(have[0].Agent, have[0].ID); found {
			return s, nil
		}
		return have[0], nil
	}
	var refs []string
	for _, s := range have {
		refs = append(refs, sessions.SessionRef(s))
	}
	return sessions.Session{}, fmt.Errorf("%s is more than one session: %s", ref, strings.Join(refs, ", "))
}

// searchAgent is the agent typed, by id or name.
func searchAgent(typed string) (string, error) {
	names := agentNames()
	if _, ok := names[typed]; ok || slices.Contains(sessions.Found(), typed) {
		return typed, nil
	}
	for id, name := range names {
		if strings.EqualFold(id, typed) || strings.EqualFold(name, typed) {
			return id, nil
		}
	}
	return "", fmt.Errorf("unknown agent %q (magpie sessions read lists them)\n%s", typed, sessionsSearchUsage)
}

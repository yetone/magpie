// Package claudetui runs Claude Code's interactive mode for magpie's Claude
// subscription bridge, as `magpie claude-tui-helper`: the bridge talks to
// it as it talks to `claude -p --input-format stream-json --output-format
// stream-json`, and it runs the real interactive Claude Code in a
// pseudo-terminal of its own. A user message is pasted into Claude Code's
// prompt and sent with Enter, as a person does; what Claude Code answers is
// read from the session's transcript, which Claude Code writes as it goes,
// and told back as stream-json; a reply as Anthropic streams it, where
// Claude Code loads the helper's tap (tap.go), and one whole message at a
// time where it doesn't.
//
// Anthropic counts a request by its entrypoint: the interactive mode's
// "cli", where -p's is "sdk-cli", and an sdk-cli request costs about
// twice as much of the plan's allowance (anthropics/claude-code#97074).
// The run is the real interactive Claude Code: nothing sets its
// entrypoint.
package claudetui

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/wslrun"
)

// ReadyWait is how long Claude Code may take to show its prompt before
// the helper ends, saying why. The bridge's wait for the helper's answer
// to its initialize request is longer (#4504): a wait shorter than this
// one ended the helper as it waited, and the turn was answered 502 with
// no reason.
const ReadyWait = 45 * time.Second

// how long Claude Code may take to show its prompt, a pasted message to
// show in it, and a sent one to reach its transcript
var (
	readyWait = ReadyWait
	pasteWait = 10 * time.Second
	sentWait  = 3 * time.Second
	// rows of one message are written together: a message is whole once
	// none of its rows came for this long, or anything else did
	messageQuiet = 150 * time.Millisecond
)

type shim struct {
	outMu sync.Mutex
	out   io.Writer

	// pty is Claude Code's terminal: what it draws is read from it, and
	// what is typed written to it
	pty io.ReadWriter
	child
	sid string
	// projects is Claude Code's projects folder, where it keeps the
	// session's transcript (<projects>/<folder>/<sid>.jsonl)
	projects string

	screenMu sync.Mutex
	screen   []byte
	changed  chan struct{}

	// rows ticks when a user message (or one queued mid-turn) reaches the
	// transcript: a pasted message was sent
	rowsMu sync.Mutex
	rows   int
	rowsCh chan struct{}
	// a turn is going: from a message's row to the turn's end
	busy bool

	exited chan struct{}
	exit   error

	// images is where a message's images are written for their paths to
	// be pasted, made with the first; images counts them
	images  string
	nimages int

	// wsl is a Claude Code in a WSL distro (WSLEnv); nil for this
	// machine's. wslr reads its transcript inside the distro, when
	// Windows can't (wslread.go)
	wsl  *WSL
	wslr *wslRead

	// tap copies the replies' streams (tap.go), nil with none; env is
	// what Claude Code is given for it
	tap *tap
	env []string

	// listed is the file magpie's tool server writes once Claude Code has
	// listed its tools (mcpListed), "" for a run without it
	listed string

	// doing is what Claude Code is being waited on for, as late says it
	// once its caller stops waiting (DeadlineEnv)
	doing atomic.Value
}

// Run runs Claude Code at path, args being the bridge's `claude -p` ones,
// with stream-json on standard input and output: interactive, with the
// same model, tools and settings (interactiveArgs). One that doesn't come
// up ends, saying why: it never becomes the -p run it stands for, as a -p
// request counts against the plan as another client's would, and starts
// without the interactive mode's cache.
func Run(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: magpie " + Command + " <claude> [claude -p's arguments]")
	}
	path, pargs := args[0], args[1:]
	var wsl *WSL
	if v := os.Getenv(WSLEnv); v != "" {
		wsl = &WSL{}
		if err := json.Unmarshal([]byte(v), wsl); err != nil || len(wsl.Args) == 0 || wsl.Projects == "" && wsl.Distro == "" {
			return fmt.Errorf("%s: %q", WSLEnv, v)
		}
		// wsl.exe's own arguments, then Claude Code's
		pargs = append(slices.Clone(wsl.Args), pargs...)
	}
	fail := func(why string) error {
		fmt.Fprintf(os.Stderr, "claude-tui: %s\n", why)
		return fmt.Errorf("Claude Code's interactive mode didn't run: %s", why)
	}
	args = interactiveArgs(pargs[len(wslArgs(wsl)):])
	s := &shim{out: os.Stdout, changed: make(chan struct{}, 1), rowsCh: make(chan struct{}, 1), exited: make(chan struct{}), wsl: wsl, listed: mcpListed(args)}
	resume := ""
	for i, a := range args {
		if a == "--resume" && i+1 < len(args) {
			resume = args[i+1]
		}
	}
	if resume != "" {
		s.sid = resume
	} else {
		s.sid = newUUID()
		args = append(args, "--session-id", s.sid)
	}
	s.projects = filepath.Join(configDir(), "projects")
	if wsl != nil && wsl.Projects != "" {
		s.projects = wsl.Projects
	} else if wsl != nil {
		dir, err := os.MkdirTemp("", "magpie-claude-wsl-")
		if err != nil {
			return fail(err.Error())
		}
		defer os.RemoveAll(dir)
		if s.wslr, err = startWSLRead(wsl.Distro, s.sid, dir); err != nil {
			return fail(err.Error())
		}
		defer s.wslr.stop()
		s.projects = dir
	}
	// a resumed session's file has its earlier turns: only what is
	// written after the start is read
	var from int64
	if p := s.transcript(); p != "" {
		if fi, err := os.Stat(p); err == nil {
			from = fi.Size()
		}
	}
	if s.tap, s.env = newTap(s.path); s.tap != nil {
		s.tap.out, s.tap.sid = s, s.sid
	}
	defer s.tap.drop()
	if err := s.start(path, append(slices.Clone(wslArgs(wsl)), args...)); err != nil {
		s.tap.drop()
		return fail(fmt.Sprintf("Claude Code's interactive mode could not start (%v)", err))
	}
	defer s.stop()
	defer s.dropImages()
	go dropLeft()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, endSignals...)
	go func() {
		<-sigs
		s.stop()
		s.dropImages()
		s.tap.drop()
		os.Exit(1)
	}()
	if by, ok := deadline(os.Getenv(DeadlineEnv)); ok {
		started := time.Now()
		t := time.AfterFunc(time.Until(by), func() {
			fmt.Fprintf(os.Stderr, "claude-tui: %s\n", s.late(time.Since(started)))
			s.stop()
			s.dropImages()
			s.tap.drop()
			os.Exit(1)
		})
		defer t.Stop()
	}
	s.doing.Store("showed no prompt")
	if err := s.ready(); err != nil {
		signal.Reset()
		s.stop()
		s.tap.drop()
		return fail(err.Error())
	}
	s.emit(map[string]any{"type": "system", "subtype": "init", "session_id": s.sid})
	go s.tail(from)
	return s.input(os.Stdin)
}

// wslArgs is wsl.exe's arguments before Claude Code's, none for this
// machine's Claude Code.
func wslArgs(wsl *WSL) []string {
	if wsl == nil {
		return nil
	}
	return wsl.Args
}

// configDir is the folder Claude Code keeps its state in.
func configDir() string {
	if d := appdir.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// childEnv is the environment for Claude Code: the shim's, without what a
// Claude Code above it marks its own children with (one started from a
// Claude Code session keeps no transcript, "inherited
// CLAUDE_CODE_CHILD_SESSION marker"), in a terminal it can draw in, with
// extra (the tap's) in place of the shim's own.
func childEnv(extra ...string) []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch {
		case k == "CLAUDECODE", k == "CLAUDE_CODE_ENTRYPOINT", k == "CLAUDE_CODE_CHILD_SESSION",
			k == "CLAUDE_CODE_SESSION_ID", k == "CLAUDE_CODE_SESSION_ATTENDED", k == "CLAUDE_CODE_EXECPATH",
			k == "CLAUDE_PID", k == "CLAUDE_EFFORT", k == "TERM", k == "COLUMNS", k == "LINES",
			strings.HasPrefix(k, "CLAUDE_CODE_MESSAGING_"), k == WSLEnv:
			continue
		case slices.ContainsFunc(extra, func(x string) bool { return strings.HasPrefix(x, k+"=") }):
			continue
		case strings.EqualFold(k, "WSLENV") && os.Getenv(WSLEnv) != "":
			// a Claude Code in WSL is given only what WSLENV names
			continue
		}
		env = append(env, e)
	}
	if os.Getenv(WSLEnv) != "" {
		pass := os.Getenv("WSLENV") + ":TERM"
		for _, x := range extra {
			k, _, _ := strings.Cut(x, "=")
			pass += ":" + k
		}
		env = append(env, "WSLENV="+strings.Trim(pass, ":"))
	}
	return append(append(env, extra...), "TERM=xterm-256color")
}

// read keeps the tail of what Claude Code draws, and answers what it asks
// of the terminal: where the cursor is, and what terminal it is.
func (s *shim) read() {
	buf := make([]byte, 64<<10)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			d := buf[:n]
			if bytes.Contains(d, []byte("\x1b[6n")) {
				_, _ = io.WriteString(s.pty, "\x1b[1;1R")
			}
			if bytes.Contains(d, []byte("\x1b[c")) || bytes.Contains(d, []byte("\x1b[0c")) {
				_, _ = io.WriteString(s.pty, "\x1b[?62;c")
			}
			s.screenMu.Lock()
			s.screen = append(s.screen, d...)
			if len(s.screen) > 256<<10 {
				s.screen = append([]byte(nil), s.screen[len(s.screen)-128<<10:]...)
			}
			s.screenMu.Unlock()
			select {
			case s.changed <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[P^_][^\x1b]*\x1b\\|\x1b.`)

// plain is what Claude Code drew since the screen was last cleared
// (clearScreen), its escapes taken out.
func (s *shim) plain() string {
	s.screenMu.Lock()
	b := string(s.screen)
	s.screenMu.Unlock()
	return ansi.ReplaceAllString(b, " ")
}

func (s *shim) clearScreen() {
	s.screenMu.Lock()
	s.screen = s.screen[:0]
	s.screenMu.Unlock()
}

// lastScreen is the end of what Claude Code drew, for an error to say.
func (s *shim) lastScreen() string {
	p := strings.Join(strings.Fields(s.plain()), " ")
	if len(p) > 600 {
		p = p[len(p)-600:]
	}
	return p
}

var (
	trustNo  = regexp.MustCompile(`❯\s*No,\s*exit`)
	trustYes = regexp.MustCompile(`❯\s*Yes,\s*I trust`)
	// how long a Yes stays drawn before Enter is pressed on it
	trustSettle = 300 * time.Millisecond
)

// lastAt is where re's last match in p starts, -1 with none.
func lastAt(re *regexp.Regexp, p string) int {
	m := re.FindAllStringIndex(p, -1)
	if len(m) == 0 {
		return -1
	}
	return m[len(m)-1][0]
}

// deadline reads DeadlineEnv's value: when the caller stops waiting.
func deadline(v string) (time.Time, bool) {
	ms, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

// late is why the run ends at its caller's deadline (DeadlineEnv), d
// after it started: what Claude Code hadn't done by then, and what it
// last drew.
func (s *shim) late(d time.Duration) string {
	doing, _ := s.doing.Load().(string)
	if doing == "" {
		doing = "showed no prompt"
	}
	return fmt.Sprintf("Claude Code %s in the %s its caller waits for it: %s", doing, d.Round(100*time.Millisecond), s.lastScreen())
}

var errUntrusted = errors.New(Untrusted + ", which only its user answers")

// signIn is what Claude Code's interactive mode shows where it is not
// signed in, as -p may be (CLAUDE_CODE_OAUTH_TOKEN, a container whose
// ~/.claude.json has no onboarding done): nothing the helper may answer.
var signIn = regexp.MustCompile(`Select login method|Log in with your Claude account|claude\.ai/oauth/authorize`)

// The screens Claude Code's interactive mode shows where its onboarding
// isn't done (a config folder only -p was run in, or one signed in where
// `claude` was never run, #4817), after its text style (theme): the
// security notes, which Enter goes past as it would for a person, and,
// in a terminal it knows how to set up (TERM_PROGRAM Apple_Terminal,
// vscode, …), whether to set it up, which Esc skips: magpie's terminal is
// no one's. Once past them, Claude Code records the onboarding done
// itself (2.1.316: hasCompletedOnboarding, lastOnboardingVersion), and
// the next run shows none of them. They are matched in the screen's
// words, any space between them one: Claude Code draws some by moving the
// cursor.
var (
	firstRun      = regexp.MustCompile(`Security notes:.*Press Enter to continue`)
	terminalSetup = regexp.MustCompile(`terminal setup\?.*Esc to skip`)
)

// onboardTries is how often one onboarding screen is answered before the
// run ends.
const onboardTries = 3

// ready waits for Claude Code's prompt. Asked whether to trust the folder
// it runs in, it is told yes, as a person would, when TrustEnv says so.
func (s *shim) ready() error {
	deadline := time.After(readyWait)
	answered := 0
	prompted := false
	var yesAt, downAt time.Time
	// an onboarding screen is answered once it has stayed drawn
	// trustSettle: a key sent as it is first drawn can be lost to its
	// being drawn again, and nothing then draws it once more
	shownAt := map[string]time.Time{}
	pressed := map[string]int{}
	// the folder the trust question is about, for the reason a run that
	// ends on it gives: "showed no prompt" said nothing of the question
	// a saved account's reads waited out on (#4830, mydosu)
	folder, _ := os.Getwd()
	if folder == "" {
		folder = "the folder it runs in"
	}
	asking := "asked whether to trust " + folder + ", not answered"
	onboard := func(screen, key string) error {
		if shownAt[screen].IsZero() {
			shownAt[screen] = time.Now()
		}
		if time.Since(shownAt[screen]) < trustSettle {
			return nil
		}
		if pressed[screen] >= onboardTries {
			return fmt.Errorf("Claude Code's interactive mode stays on its first-run screens, its onboarding not done; run Claude Code once in a terminal and go through them: %s", s.lastScreen())
		}
		pressed[screen]++
		delete(shownAt, screen)
		s.clearScreen()
		_, _ = io.WriteString(s.pty, key)
		return nil
	}
	for {
		p := s.plain()
		words := strings.Join(strings.Fields(p), " ")
		doing := "showed no prompt"
		if strings.Contains(p, "trust this folder") {
			doing = asking
		}
		s.doing.Store(doing)
		switch {
		case strings.Contains(p, "trust this folder") && os.Getenv(TrustEnv) != "1":
			return errUntrusted
		case signIn.MatchString(p):
			return fmt.Errorf("Claude Code's interactive mode asks to sign in: %s", s.lastScreen())
		case firstRun.MatchString(words):
			if err := onboard("notes", "\r"); err != nil {
				return err
			}
		case terminalSetup.MatchString(words):
			if err := onboard("terminal", "\x1b"); err != nil {
				return err
			}
		case strings.Contains(p, "trust this folder") && answered < 3:
			// the choice is the last one drawn after ❯, "No, exit" at
			// first. A key sent as the question is first drawn can be lost
			// to its being drawn again on No, so Enter goes only to a Yes
			// drawn trustSettle ago and not drawn over since.
			no, yes := lastAt(trustNo, p), lastAt(trustYes, p)
			switch {
			case yes > no:
				if yesAt.IsZero() {
					yesAt = time.Now()
				}
				if time.Since(yesAt) < trustSettle {
					break
				}
				answered++
				yesAt = time.Time{}
				s.clearScreen()
				_, _ = io.WriteString(s.pty, "\r")
			case no >= 0:
				yesAt = time.Time{}
				if time.Since(downAt) >= time.Second {
					downAt = time.Now()
					_, _ = io.WriteString(s.pty, "\x1b[B")
				}
			}
		case strings.Contains(p, "keep browser tools off"):
			// Claude in Chrome found: kept off, as the bridge runs it
			// --no-chrome
			s.clearScreen()
			_, _ = io.WriteString(s.pty, "\x1b")
		case strings.Contains(words, "Choose the text style"):
			if err := onboard("theme", "\r"); err != nil {
				return err
			}
		case strings.Contains(p, "❯ "):
			if !s.toolsListed() {
				// drawn before Claude Code has its MCP servers' tools: it
				// connects them as the prompt shows, and a message sent
				// now is answered without the agent's tools, which
				// magpie's tool server carries (MinhuWang on X)
				prompted = true
				break
			}
			// drawn: a moment for its input to be listened to, and for
			// the tools just listed to be taken up
			time.Sleep(300 * time.Millisecond)
			return nil
		}
		select {
		case <-s.changed:
		case <-time.After(200 * time.Millisecond):
		case <-s.exited:
			return fmt.Errorf("Claude Code ended before it showed its prompt (%v): %s", s.exit, s.lastScreen())
		case <-deadline:
			if prompted {
				return fmt.Errorf("Claude Code showed its prompt, but hadn't listed the tools of magpie's tool server in %s", readyWait)
			}
			doing, _ := s.doing.Load().(string)
			return fmt.Errorf("Claude Code %s in %s: %s", doing, readyWait, s.lastScreen())
		}
	}
}

// toolsListed is whether Claude Code has listed magpie's tool server's
// tools, or the run has no such server.
func (s *shim) toolsListed() bool {
	if s.listed == "" {
		return true
	}
	_, err := os.Stat(s.listed)
	return err == nil
}

// mcpListed is the file magpie's tool server (MCPCommand) is told, in the
// --mcp-config of args, to write once Claude Code has listed its tools;
// "" when it isn't told one.
func mcpListed(args []string) string {
	for i, a := range args {
		if a != "--mcp-config" || i+1 >= len(args) {
			continue
		}
		var c struct {
			Servers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal([]byte(args[i+1]), &c) != nil {
			continue
		}
		for _, srv := range c.Servers {
			if len(srv.Args) == 4 && srv.Args[0] == MCPCommand {
				return srv.Args[3]
			}
		}
	}
	return ""
}

func (s *shim) emit(v any) {
	b, _ := json.Marshal(v)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = s.out.Write(append(b, '\n'))
}

// input reads the bridge's stream-json: user messages, sent to Claude Code,
// and control requests, of which interactive mode takes none but initialize.
func (s *shim) input(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var m struct {
			Type      string `json:"type"`
			UUID      string `json:"uuid"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Command string `json:"command"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "control_request":
			resp := map[string]any{"subtype": "success", "request_id": m.RequestID, "response": map[string]any{}}
			if m.Request.Subtype != "initialize" {
				resp = map[string]any{"subtype": "error", "request_id": m.RequestID,
					"error": "Claude Code's interactive mode takes no " + m.Request.Subtype + " request"}
			}
			s.emit(map[string]any{"type": "control_response", "response": resp})
		case "command":
			if err := s.command(m.Command); err != nil {
				return err
			}
		case "user":
			s.doing.Store("hadn't answered")
			pieces, err := s.userPieces(m.Message.Content)
			if err != nil {
				s.emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": s.sid,
					"result": "this message can't be pasted into Claude Code's interactive mode: " + err.Error()})
				continue
			}
			err = s.send(pieces)
			s.dropImages()
			if err != nil {
				return err
			}
			if m.UUID != "" {
				s.emit(map[string]any{"type": "command_lifecycle", "command_uuid": m.UUID, "state": "queued"})
			}
		}
	}
	return sc.Err()
}

// how long a command's panel may take to be drawn, and how long it stays
// as it is before it is taken as drawn whole
var (
	commandWait   = 20 * time.Second
	commandSettle = time.Second
)

// command runs /usage, which Claude Code answers itself, asking no model:
// it is typed, and what Claude Code shows for it is told as the result -p's
// /usage gives (usageText), its local_command named. The panel is closed
// after. It is the one command a line {"type":"command"} runs: the plan's
// allowance read in the interactive mode, where -p's /usage tells no
// window of it (Claude Code 2.1.316).
func (s *shim) command(name string) error {
	s.doing.Store("hadn't drawn " + name + "'s panel")
	if name != "/usage" {
		s.emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": s.sid,
			"result": "the helper types no " + name + " in Claude Code's interactive mode"})
		return nil
	}
	for _, c := range name {
		if _, err := io.WriteString(s.pty, string(c)); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	// the command menu drawn as it was typed settles, and is left out of
	// what is read
	time.Sleep(commandSettle)
	s.clearScreen()
	if _, err := io.WriteString(s.pty, "\r"); err != nil {
		return err
	}
	by := time.After(commandWait)
	last, quiet := "", time.Now()
	for waiting := true; waiting; {
		p := s.plain()
		if p != last {
			last, quiet = p, time.Now()
		}
		// drawn: its windows, and the panel still a moment; or anything
		// else (an error, a plan with no allowance) still for longer
		still := time.Since(quiet)
		if strings.TrimSpace(p) != "" && (usageUsed.MatchString(p) && still >= commandSettle || still >= 3*commandSettle) {
			break
		}
		select {
		case <-s.changed:
		case <-time.After(100 * time.Millisecond):
		case <-by:
			waiting = false
		case <-s.exited:
			return fmt.Errorf("Claude Code ended (%v): %s", s.exit, s.lastScreen())
		}
	}
	_, _ = io.WriteString(s.pty, "\x1b")
	s.emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": s.sid,
		"local_command": "usage", "result": usageText(last)})
	return nil
}

var (
	usageName  = regexp.MustCompile(`^(session|week(?: \([^)]*\))?)`)
	usageUsed  = regexp.MustCompile(`([0-9.]+)%\s+used`)
	usageReset = regexp.MustCompile(`Resets (.*?\))`)
)

// usageText is the plan's allowance as /usage's panel shows it, each
// window a bar ("Current session ███▌ 99% used", then "Resets 7pm
// (Asia/Shanghai)" below), said as -p's lines say it: "Current session:
// 99% used · resets 7pm (Asia/Shanghai)", a window drawn again told as
// drawn last. Overages are said as /usage says them. A panel with no
// window is told as drawn, for its words to say why.
func usageText(screen string) string {
	t := strings.Join(strings.Fields(screen), " ")
	var names []string
	said := map[string]string{}
	for _, chunk := range strings.Split(t, "Current ")[1:] {
		name, used := usageName.FindString(chunk), usageUsed.FindStringSubmatch(chunk)
		if name == "" || used == nil {
			continue
		}
		line := "Current " + name + ": " + used[1] + "% used"
		if r := usageReset.FindStringSubmatch(chunk[strings.Index(chunk, used[0]):]); r != nil {
			line += " · resets " + r[1]
		}
		if _, ok := said[name]; !ok {
			names = append(names, name)
		}
		said[name] = line
	}
	var lines []string
	for _, n := range names {
		lines = append(lines, said[n])
	}
	if strings.Contains(t, "using your overages") {
		lines = append(lines, "You are currently using your overages to power your Claude Code usage.")
	}
	if len(lines) == 0 {
		return t
	}
	return strings.Join(lines, "\n")
}

// piece is a part of a user message as it is pasted: words, or the path
// of an image file, which Claude Code's prompt takes as that image
// ("[Image #1]") when it is pasted alone.
type piece struct{ text, image string }

// imageTypes are the images Claude Code's prompt takes, by their file's
// extension.
var imageTypes = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// userPieces is a user message as it is pasted: its text blocks one after
// another, an image block written to a file of its own.
func (s *shim) userPieces(raw json.RawMessage) ([]piece, error) {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return []piece{{text: str}}, nil
	}
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	var out []piece
	var b strings.Builder
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			if bl.Text == "" {
				continue
			}
			if b.Len() > 0 || len(out) > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(bl.Text)
		case "image":
			path, err := s.writeImage(bl.Source.Type, bl.Source.MediaType, bl.Source.Data)
			if err != nil {
				return nil, err
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
				out = append(out, piece{text: b.String()})
				b.Reset()
			}
			out = append(out, piece{image: path})
		default:
			return nil, fmt.Errorf("a %s block", bl.Type)
		}
	}
	if b.Len() > 0 || len(out) == 0 {
		out = append(out, piece{text: b.String()})
	}
	return out, nil
}

// writeImage writes an image block's bytes to a file of its own, named
// for its type, and returns its path.
func (s *shim) writeImage(source, mediaType, data string) (string, error) {
	if source != "base64" {
		return "", fmt.Errorf("an image by %s", source)
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("an image's data: %w", err)
	}
	if mediaType == "" {
		mediaType = http.DetectContentType(b)
	}
	ext, ok := imageTypes[mediaType]
	if !ok {
		return "", fmt.Errorf("a %s image", mediaType)
	}
	if s.images == "" {
		if s.images, err = os.MkdirTemp("", imagesPrefix+strconv.Itoa(os.Getpid())+"-"); err != nil {
			return "", err
		}
	}
	s.nimages++
	path := filepath.Join(s.images, fmt.Sprintf("image-%d%s", s.nimages, ext))
	return path, os.WriteFile(path, b, 0o600)
}

// dropImages removes the images pasted: once their message is in the
// transcript, Claude Code has them, and when the helper ends.
func (s *shim) dropImages() {
	if s.images != "" {
		_ = os.RemoveAll(s.images)
		s.images = ""
	}
}

// imagesPrefix names the folders images are written to, the helper's pid
// after it.
const imagesPrefix = "magpie-claude-images-"

// imagesLeft is how long an image or tap folder is left before another
// helper removes it: one killed, as Windows ends a run, can't remove its
// own. A tap's files are open by then, in a run that goes on longer:
// removed, they are read and written as before.
const imagesLeft = time.Hour

// dropLeft removes the image and tap folders helpers left, made over
// imagesLeft ago.
func dropLeft() {
	m, _ := filepath.Glob(filepath.Join(os.TempDir(), imagesPrefix+"*"))
	t, _ := filepath.Glob(filepath.Join(os.TempDir(), tapPrefix+"*"))
	for _, d := range append(m, t...) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && time.Since(fi.ModTime()) > imagesLeft {
			_ = os.RemoveAll(d)
		}
	}
}

// pasteable is text as a paste can carry it: no escape that would end the
// paste early, nor other control characters, its lines ended by \n; and
// not opening with what Claude Code's prompt takes as a command (/) or a
// shell line (!), a memory (#) or a background task (&). busy is for a
// message sent while a turn is going, which Claude Code queues.
func pasteable(text string, busy bool) string {
	text = pasteClean(text)
	// After a line break the prompt takes it as words: a space before it
	// is trimmed, and a zero-width one taken out of the paste. Claude Code
	// trims a queued message, though, and runs one opening with / as a
	// command even so, or drops it: that one is marked as the user's
	// words, as the bridge marks them after a tool's result.
	if t := strings.TrimLeft(text, " \t\n"); t != "" && strings.ContainsRune("/!#&", rune(t[0])) {
		if busy {
			text = userMark + t
		} else {
			text = "\n" + t
		}
	}
	return text
}

// pasteClean is text without an escape that would end a paste early, nor
// other control characters, its lines ended by \n.
func pasteClean(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r':
			return '\n'
		case r == '\n', r == '\t':
			return r
		case r < 0x20, r == 0x7f:
			return -1
		}
		return r
	}, text)
}

// send pastes a message's pieces into Claude Code's prompt, each image
// once the prompt shows the one before (its file is read as it is pasted,
// and words pasted meanwhile would come before it), and presses Enter,
// again while the message hasn't reached the transcript: an Enter that
// lands while the paste is still being taken in is part of it.
func (s *shim) send(pieces []piece) error {
	s.rowsMu.Lock()
	before, busy := s.rows, s.busy
	s.rowsMu.Unlock()
	text, image := "", 0
	for i, p := range pieces {
		if p.image != "" {
			var err error
			if image, err = s.pasteImage(s.path(p.image), image); err != nil {
				return err
			}
			continue
		}
		if i == 0 {
			p.text = pasteable(p.text, busy)
		} else {
			p.text = pasteClean(p.text)
		}
		if i == 0 && len(pieces) == 1 && strings.TrimSpace(p.text) == "" {
			p.text = "."
		}
		s.clearScreen()
		if _, err := io.WriteString(s.pty, "\x1b[200~"+p.text+"\x1b[201~"); err != nil {
			return err
		}
		text = p.text
	}
	if err := s.shown(text); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	for try := 0; try < 4; try++ {
		if _, err := io.WriteString(s.pty, "\r"); err != nil {
			return err
		}
		by := time.After(sentWait)
		for {
			s.rowsMu.Lock()
			sent := s.rows > before
			s.rowsMu.Unlock()
			if sent {
				return nil
			}
			select {
			case <-s.rowsCh:
				continue
			case <-by:
			case <-s.exited:
				return fmt.Errorf("Claude Code ended (%v): %s", s.exit, s.lastScreen())
			}
			break
		}
	}
	return fmt.Errorf("Claude Code did not take the message: %s", s.lastScreen())
}

var imageShown = regexp.MustCompile(`\[Image #(\d+)\]`)

// path is where Claude Code reads a file of the helper's at path: one in
// WSL under the distro's mount of the drive.
func (s *shim) path(path string) string {
	if s.wsl == nil {
		return path
	}
	return wslrun.Tool{Mount: s.wsl.Mount}.Linux(path)
}

// pasteImage pastes an image file's path, and waits for the prompt to show
// it taken as an image: one numbered past after, the message's image
// before it. It returns the number shown.
func (s *shim) pasteImage(path string, after int) (int, error) {
	s.clearScreen()
	if _, err := io.WriteString(s.pty, "\x1b[200~"+path+"\x1b[201~"); err != nil {
		return 0, err
	}
	by := time.After(pasteWait)
	for {
		for _, m := range imageShown.FindAllStringSubmatch(s.plain(), -1) {
			if n, _ := strconv.Atoi(m[1]); n > after {
				return n, nil
			}
		}
		select {
		case <-s.changed:
		case <-time.After(100 * time.Millisecond):
		case <-by:
			return 0, fmt.Errorf("Claude Code did not take an image pasted as its path: %s", s.lastScreen())
		case <-s.exited:
			return 0, fmt.Errorf("Claude Code ended (%v): %s", s.exit, s.lastScreen())
		}
	}
}

// shownWait is how long a paste of n bytes may take to show: Claude Code
// takes a long one in at about 100KB a second (a real 3MB paste showed
// "[Pasted text" after 35s), so pasteWait and a second for every 64KB.
func shownWait(n int) time.Duration {
	return pasteWait + time.Duration(n/(64<<10))*time.Second
}

// shown waits for the prompt to show pasted text: its last words, or
// Claude Code's "[Pasted text" for a long one. Text "" waits for nothing.
func (s *shim) shown(text string) error {
	if text == "" {
		return nil
	}
	tail := strings.TrimSpace(text)
	if i := strings.LastIndexByte(tail, '\n'); i >= 0 {
		tail = strings.TrimSpace(tail[i+1:])
	}
	if r := []rune(tail); len(r) > 16 {
		tail = string(r[len(r)-16:])
	}
	shownBy := time.After(shownWait(len(text)))
shown:
	for {
		p := s.plain()
		if strings.Contains(p, "Pasted text") || tail != "" && strings.Contains(strings.Join(strings.Fields(p), " "), strings.Join(strings.Fields(tail), " ")) {
			break
		}
		select {
		case <-s.changed:
		case <-time.After(100 * time.Millisecond):
		case <-shownBy:
			break shown
		case <-s.exited:
			return fmt.Errorf("Claude Code ended (%v): %s", s.exit, s.lastScreen())
		}
	}
	return nil
}

// transcript is the session's transcript, "" before Claude Code writes it.
func (s *shim) transcript() string {
	m, _ := filepath.Glob(filepath.Join(s.projects, "*", s.sid+".jsonl"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

// userMark is what the bridge marks the user's words with after a tool's
// result (renderParts).
const userMark = "[user message]\n"

func (s *shim) sawRow() {
	s.rowsMu.Lock()
	s.rows++
	s.busy = true
	s.rowsMu.Unlock()
	select {
	case s.rowsCh <- struct{}{}:
	default:
	}
}

// tail reads the transcript from offset from on, as Claude Code writes it,
// and tells the bridge what it says.
func (s *shim) tail(from int64) {
	var path string
	for path == "" {
		if path = s.transcript(); path == "" {
			select {
			case <-s.exited:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return
	}
	t := newTranslator(s)
	t.tap = s.tap
	rd := bufio.NewReaderSize(f, 1<<20)
	var part []byte
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			part = append(part, line...)
			if part[len(part)-1] == '\n' {
				t.row(part)
				part = nil
			}
			continue
		}
		if err != nil && err != io.EOF {
			return
		}
		t.quiet()
		s.tap.read()
		wait := 50 * time.Millisecond
		if s.tap != nil && s.tap.going(false) {
			wait = tapPoll
		}
		select {
		case <-s.exited:
			// what it wrote last
			s.wslr.settle()
			if line, _ := rd.ReadBytes('\n'); len(line) > 0 {
				t.row(append(part, line...))
			}
			t.flush()
			s.tap.settle()
			return
		case <-time.After(wait):
		}
	}
}

func (s *shim) turnOver() {
	s.rowsMu.Lock()
	s.busy = false
	s.rowsMu.Unlock()
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// interactiveArgs are `claude -p` arguments made the interactive mode's:
// the same model, tools, MCP servers, settings, effort, system prompt and
// session, without what only -p takes; its permission prompts kept, and
// magpie's MCP tools (and the web search -p was given) allowed without
// one, where -p skipped them all.
func interactiveArgs(pargs []string) []string {
	var out []string
	allowed := []string{"mcp__magpie"}
	for i := 0; i < len(pargs); i++ {
		a := pargs[i]
		switch a {
		case "-p", "--print", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions",
			"--no-session-persistence", "--replay-user-messages":
			continue
		case "--output-format", "--input-format", "--thinking-display", "--json-schema", "--permission-mode":
			i++
			continue
		case "--tools":
			if i+1 < len(pargs) {
				for _, t := range strings.FieldsFunc(pargs[i+1], func(r rune) bool { return r == ',' || r == ' ' }) {
					allowed = append(allowed, t)
				}
			}
		}
		out = append(out, a)
	}
	return append(out, "--permission-mode", "default", "--allowedTools", strings.Join(allowed, ","), "--no-chrome")
}

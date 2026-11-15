package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

const claudeCodeUsage = "usage: magpie claude-code                  which Claude Code the Claude subscription runs\n" +
	"       magpie claude-code install [--yes]  download Anthropic's own build of Claude Code for magpie (a server or container without one)\n" +
	"       magpie claude-code remove           delete the Claude Code magpie downloaded\n" +
	"       magpie claude-code runs [N|auto]    the Claude Code processes running for Claude subscriptions, and how many may run at once (auto: what this machine's memory holds)"

// claudeCodeCmd: magpie claude-code [install [--yes]|remove]. A Claude
// subscription is answered by Claude Code, and signed in to through it; a
// server or a container has none (Jorben on Discord), so magpie downloads
// Anthropic's build when asked, as Anthropic's installer does.
func claudeCodeCmd(args []string) error {
	if len(args) == 0 {
		return claudeCodeStatus()
	}
	switch args[0] {
	case "install":
		yes := false
		for _, a := range args[1:] {
			switch a {
			case "--yes", "-y":
				yes = true
			default:
				return fmt.Errorf("magpie claude-code install: unknown %q\n%s", a, claudeCodeUsage)
			}
		}
		return installClaudeCode(yes)
	case "remove", "rm":
		path, v := claudeCode.downloaded()
		if path == "" {
			fmt.Println(muted.Render("magpie hasn't downloaded Claude Code"))
			return nil
		}
		if err := claudecode.Remove(); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "removed Claude Code", v, muted.Render("("+claudecode.Root()+")"))
		return nil
	case "runs":
		return claudeCodeRuns(args[1:])
	case "-h", "--help", "help":
		fmt.Println(claudeCodeUsage)
		return nil
	}
	return fmt.Errorf("magpie claude-code: unknown %q\n%s", args[0], claudeCodeUsage)
}

// claudeCodeRuns: magpie claude-code runs [N|auto]. Each Claude
// subscription conversation, test and /usage read is a Claude Code process
// of about 100 MiB; a 512 MiB service ran 16 and the kernel killed them
// (#2956). magpie holds them to what the machine's memory holds unless the
// user says how many (claudeRunsMost).
func claudeCodeRuns(args []string) error {
	s := settings.Load()
	switch {
	case len(args) > 1:
		return fmt.Errorf("magpie claude-code runs takes one value\n%s", claudeCodeUsage)
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help"):
		fmt.Println(claudeCodeUsage)
		return nil
	case len(args) == 1:
		n := 0
		if args[0] != "auto" {
			v, err := strconv.Atoi(args[0])
			if err != nil || v < 1 {
				return fmt.Errorf("magpie claude-code runs takes a number from 1, or auto, not %q", args[0])
			}
			n = v
		}
		s.ClaudeRunsMost = n
		if err := settings.Save(s); err != nil {
			return err
		}
	}
	auto, why := gateway.ClaudeMemRuns()
	autoSaid := "no bound: this machine's memory isn't known to magpie"
	if auto > 0 {
		autoSaid = why
	}
	switch {
	case s.ClaudeRunsMost > 0:
		fmt.Println(green.Render("●"), s.ClaudeRunsMost, "Claude Code processes may run at once", muted.Render("· your setting"))
		fmt.Println(muted.Render("  auto would be " + autoSaid + " · magpie claude-code runs auto"))
	case auto > 0:
		fmt.Println(green.Render("●"), auto, "Claude Code processes may run at once", muted.Render("· "+why))
		fmt.Println(muted.Render("  magpie claude-code runs N sets how many"))
	default:
		fmt.Println(green.Render("●"), "any number of Claude Code processes may run", muted.Render("· "+autoSaid))
		fmt.Println(muted.Render("  magpie claude-code runs N sets how many"))
	}
	printClaudeRuns()
	return nil
}

// printClaudeRuns lists the Claude Code processes the running magpie has
// for Claude subscriptions (gateway.ClaudeRunsPath), and what they are:
// each conversation on a Claude account, a subagent's too, is answered by
// a real Claude Code with magpie's MCP helper beside it, kept for its next
// turn. A task manager shows them as Claude Code's version and "magpie"
// (@Candydidi3 on X asked why magpie started so many).
func printClaudeRuns() {
	base := claudeCode.gateway()
	runs, err := fetchClaudeRuns(base)
	fmt.Println()
	if err != nil {
		fmt.Println(muted.Render("  " + err.Error()))
		return
	}
	n := len(runs.Runs)
	others := max(runs.Live-n, 0)
	head := fmt.Sprintf("%d Claude Code running for Claude subscription conversations", n)
	if n == 0 {
		head = "no Claude Code running for Claude subscription conversations"
	}
	if others > 0 {
		head += fmt.Sprintf(", %d more for a test, warm-up or usage read", others)
	}
	fmt.Println(head)
	now := time.Now()
	for _, r := range runs.Runs {
		state := map[string]string{"answering": "answering", "idle": "kept for its next turn", "tools": "waiting on your agent's tools"}[r.State]
		if state == "" {
			state = r.State
		}
		line := fmt.Sprintf("  pid %-7d %-22s %s %s", r.PID, r.Model, state, runSpan(now.Sub(r.Since)))
		var tail []string
		if r.State != "answering" && !r.Ends.IsZero() && r.Ends.After(now) {
			tail = append(tail, "ends in "+runSpan(r.Ends.Sub(now)))
		}
		if r.RSS > 0 {
			tail = append(tail, bytesSize(uint64(r.RSS)))
		}
		if r.Account != "" {
			tail = append(tail, r.Account)
		}
		if len(tail) > 0 {
			line += muted.Render(" · " + strings.Join(tail, " · "))
		}
		fmt.Println(line)
	}
	for _, l := range []string{
		"Each conversation on a Claude account (each subagent too) is answered by Claude Code, which a task",
		"manager names by its version (2.1.x), with a \"magpie claude-mcp-helper\" beside it for your agent's tools.",
		fmt.Sprintf("One whose turn is over is kept up to %s for its next (%d at most), so that turn reads the prompt", runSpan(runs.IdleFor), runs.IdleMost),
		"cache instead of writing it again. magpie ends each by itself; quitting magpie ends them all.",
	} {
		fmt.Println(muted.Render("  " + l))
	}
}

// runSpan says a duration the way the run list does: 45s, 12m, 1h5m.
func runSpan(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d/time.Second), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// fetchClaudeRuns asks the gateway at base for its Claude Code runs. The
// error says why there is no list: nothing answers (no magpie running, so
// no Claude Code of magpie's either), or a magpie too old to tell.
func fetchClaudeRuns(base string) (gateway.ClaudeRuns, error) {
	var out gateway.ClaudeRuns
	c := &http.Client{Timeout: 10 * time.Second}
	res, err := c.Get(base + gateway.ClaudeRunsPath)
	if err != nil {
		return out, fmt.Errorf("no magpie answers at %s, so none of its Claude Code is running", base)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	switch {
	case err != nil:
		return out, fmt.Errorf("the list of Claude Code processes from %s was cut off: %v", base, err)
	case res.StatusCode == http.StatusNotFound:
		return out, fmt.Errorf("the magpie at %s is older than this one and doesn't list its Claude Code processes: update it", base)
	case res.StatusCode != http.StatusOK:
		return out, fmt.Errorf("the magpie at %s answered %s: %s", base, res.Status, strings.TrimSpace(string(body)))
	}
	if json.Unmarshal(body, &out) != nil || out.Object != "claude-runs" {
		return out, fmt.Errorf("what %s answered is not a list of Claude Code processes: is it magpie?", base)
	}
	return out, nil
}

// claudeCode is what the command reads of the machine; tests stand in for it.
var claudeCode = struct {
	own        func() string
	downloaded func() (string, string)
	latest     func(context.Context) (claudecode.Release, error)
	install    func(context.Context, claudecode.Release, func(int64)) (string, error)
	gateway    func() string // the running magpie's gateway, asked for its runs
}{
	own:        claudecode.Own,
	downloaded: claudecode.Downloaded,
	latest:     claudecode.Latest,
	install:    claudecode.Install,
	gateway:    gateway.URL,
}

func claudeCodeStatus() error {
	own := claudeCode.own()
	path, v := claudeCode.downloaded()
	switch {
	case own != "":
		fmt.Println(green.Render("●"), "Claude Code", muted.Render(own), muted.Render("· this machine's own, which the Claude subscription runs"))
		if path != "" {
			fmt.Println(muted.Render("  magpie's download (" + v + ", " + path + ") isn't used while it is here · magpie claude-code remove deletes it"))
		}
	case path != "":
		fmt.Println(green.Render("●"), "Claude Code", v, muted.Render(path), muted.Render("· downloaded by magpie"))
		fmt.Println(muted.Render("  magpie keeps it at the newest release while it runs (DISABLE_AUTOUPDATER=1 holds it) · magpie claude-code remove deletes it"))
	default:
		fmt.Println(amber.Render("○"), "no Claude Code here: a Claude subscription can't be signed in to or answered")
		fmt.Println(muted.Render("  install it (https://code.claude.com/docs), or magpie claude-code install downloads Anthropic's own build for magpie"))
	}
	return nil
}

// installClaudeCode downloads the newest Claude Code after saying what,
// from where and to where, and asking (unless yes).
func installClaudeCode(yes bool) error {
	ctx, stop := interruptContext()
	defer stop()
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	r, err := claudeCode.latest(lctx)
	cancel()
	if err != nil {
		return fmt.Errorf("asking for Claude Code's newest release: %w", err)
	}
	if path, v := claudeCode.downloaded(); v == r.Version {
		fmt.Println(green.Render("✓"), "Claude Code", v, "is downloaded already", muted.Render("("+path+")"))
		return nil
	}
	fmt.Println("Claude Code", bold.Render(r.Version), muted.Render("("+r.Platform+", "+mb(r.Size)+")"))
	fmt.Println(muted.Render("  from  " + r.URL))
	fmt.Println(muted.Render("  to    " + claudecode.Root()))
	fmt.Println(muted.Render("  Anthropic's own build, as its installer downloads it; its SHA-256 is checked against Anthropic's manifest"))
	fmt.Println(muted.Render("  it is Anthropic's software, under Anthropic's terms; magpie runs it for the Claude subscription only"))
	if own := claudeCode.own(); own != "" {
		fmt.Println(amber.Render("!"), "this machine has a Claude Code of its own,", own+": it is the one run while it is there")
	}
	if !yes {
		fmt.Print("Download it? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return fmt.Errorf("download canceled")
		}
	}
	last := time.Time{}
	tty := isTerminal(os.Stdout)
	path, err := claudeCode.install(ctx, r, func(done int64) {
		if !tty || time.Since(last) < 200*time.Millisecond && done < r.Size {
			return
		}
		last = time.Now()
		fmt.Printf("\r  %s of %s", mb(done), mb(r.Size))
	})
	if tty {
		fmt.Println()
	}
	if err != nil {
		return fmt.Errorf("downloading Claude Code %s: %w", r.Version, err)
	}
	fmt.Println(green.Render("✓"), "Claude Code", r.Version, "downloaded and checked", muted.Render("("+path+")"))
	fmt.Println(muted.Render("  add a Claude account: magpie accounts add claude"))
	return nil
}

func mb(n int64) string { return fmt.Sprintf("%.0f MB", float64(n)/(1<<20)) }

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

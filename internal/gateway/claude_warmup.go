package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/claudetui"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/redact"
	"golang.org/x/text/unicode/norm"
)

// claudeProxy is the proxy a Claude Code run on an account's behalf goes
// through: the one ctx names (the account's own, or its subscription's),
// else the subscription's.
func claudeProxy(ctx context.Context) string {
	if c := netproxy.Choice(ctx); c != "" {
		return c
	}
	return provider.ProxyOf("claude")
}

// warmClaude sends a Claude account one "hi" through Claude Code, as the
// bridge runs it (claudeCLIArgs): none of its tools, settings or MCP
// servers, nothing kept on disk, at Haiku, the least an account's window
// is started by. configDir is a saved account's config directory, "" for
// the one Claude Code is signed in to.
func warmClaude(ctx context.Context, configDir string) error {
	return askClaude(ctx, configDir, "haiku")
}

// askClaude has Claude Code answer one "hi" at model, as warmClaude does;
// it is how a Claude account's model test runs (provider.ProbeClaudeVia),
// so the test asks Anthropic as Claude Code does, through Claude Code.
func askClaude(ctx context.Context, configDir, model string) error {
	binary, err := claudeBinary()
	if err != nil {
		return err
	}
	if claudeTUIOn() {
		return askClaudeTUI(ctx, binary, configDir, model)
	}
	tmp, err := os.MkdirTemp("", "magpie-claude-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cmd := binary.command(ctx, claudeWarmArgs(model)...)
	cmd.Dir = tmp
	cmd.Stdin = strings.NewReader("hi")
	env, err := binary.proxied(ctx, cleanClaudeEnv(os.Environ()))
	if err != nil {
		return err
	}
	cmd.Env = binary.env(env, configDir)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	runErr := runClaudeTree(ctx, cmd, "a Claude Code test on "+model)
	if res, ok := claudeResult(out.Bytes()); ok && res.IsError {
		return errors.New("Claude Code: " + clip(res.Result))
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			return runErr
		}
		return errors.New("Claude Code: " + clip(msg))
	}
	return nil
}

// askClaudeTUI is askClaude with the interactive mode on (claudeTUIOn),
// whose model requests are never -p: the "hi" is said in it,
// through its helper as the bridge runs it, and the run ends at its
// result.
func askClaudeTUI(ctx context.Context, binary claudeCLI, configDir, model string) error {
	if _, why := claudeMachineWhy(binary); why != "" {
		return claudeTUIErr(why)
	}
	work, done, err := oneOffDir()
	if err != nil {
		return err
	}
	defer done()
	trustWork(binary, configDir, work)
	cmd, err := binary.tuiCommand(ctx, claudeTUIWarmArgs(model)...)
	if err != nil {
		return err
	}
	cmd.Dir = work
	env, err := binary.proxied(ctx, cleanClaudeEnv(os.Environ()))
	if err != nil {
		return err
	}
	// the folder is magpie's own: asked whether to trust it, say yes
	env = append(env, claudetui.TrustEnv+"=1")
	env = append(env, helperDeadline(ctx)...)
	cmd.Env = binary.tuiEnv(binary.env(binary.asRoot(env), configDir), configDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}}})
	go func() { _, _ = stdin.Write(append(b, '\n')) }()
	// the run ends once it has its result, its input closed
	out := &resultLines{done: func() { _ = stdin.Close() }}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, &stderr
	runErr := runClaudeTree(ctx, cmd, "a Claude Code test on "+model)
	dropOneOff(binary, configDir, work, out.sid)
	if out.res != nil {
		if out.res.IsError {
			return errors.New("Claude Code: " + clip(out.res.Result))
		}
		return nil
	}
	err = oneOffErr(ctx, stderr.String(), runErr)
	logOneOff("a test on "+model, configDir, stderr.String(), err)
	return err
}

// resultLines reads the helper's stream-json for its first result, and
// calls done once it has it.
type resultLines struct {
	buf  []byte
	res  *claudeRun
	done func()
	// sid is the session the helper said it runs (its init's session_id)
	sid string
}

func (w *resultLines) Write(p []byte) (int, error) {
	if w.res != nil {
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	for w.res == nil {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		var r claudeRun
		if json.Unmarshal(w.buf[:i], &r) == nil {
			if r.SessionID != "" {
				w.sid = r.SessionID
			}
			if r.Type == "result" {
				w.res = &r
				w.done()
			}
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func init() {
	provider.ProbeClaudeVia(askClaude)
	// Warm up now sends what the scheduled warm-up sends
	provider.WarmClaudeVia(warmClaude)
	provider.UsageClaudeVia(claudeUsage)
}

// claudeUsage is what Claude Code's /usage prints for the account in
// configDir (empty for its own): a command asking no model, run as
// askClaude runs it, with nothing of the user's settings and nothing kept.
func claudeUsage(ctx context.Context, configDir string) (string, error) {
	binary, err := claudeBinary()
	if err != nil {
		return "", err
	}
	if claudeTUIOn() {
		return claudeUsageTUI(ctx, binary, configDir)
	}
	tmp, err := os.MkdirTemp("", "magpie-claude-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	cmd := binary.command(ctx, claudeUsageArgs()...)
	cmd.Dir = tmp
	cmd.Stdin = strings.NewReader("")
	env, err := binary.proxied(ctx, claudeUsageEnv())
	if err != nil {
		return "", err
	}
	cmd.Env = binary.env(env, configDir)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	runErr := runClaudeTree(ctx, cmd, "a Claude Code /usage read")
	var full *claudeFull
	if errors.As(runErr, &full) {
		return "", runErr
	}
	if res, ok := claudeResult(out.Bytes()); ok {
		if res.IsError {
			return "", errors.New("Claude Code: " + clip(res.Result))
		}
		return res.Result, nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = strings.TrimSpace(out.String())
	}
	if msg == "" && runErr != nil {
		return "", runErr
	}
	return "", errors.New("Claude Code: " + clip(msg))
}

// claudeUsageEnv is the environment a /usage read runs Claude Code in:
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC makes /usage skip its request
// and show only a reading another run left in the last hour, so it stays
// only when the user set it.
func claudeUsageEnv() []string {
	env := cleanClaudeEnv(os.Environ())
	if os.Getenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC") == "" {
		env = slices.DeleteFunc(env, func(e string) bool {
			return strings.HasPrefix(e, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=")
		})
	}
	return append(env, "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "DISABLE_AUTOUPDATER=1")
}

// claudeUsageTUI is claudeUsage with the interactive mode on
// (claudeTUIOn): /usage is typed in it, through its helper, which tells
// the plan's windows its panel shows as -p's /usage lines say them
// (claudetui's usageText). -p's /usage tells only the session's cost
// there (Claude Code 2.1.316), so the plan's allowance is read only so.
func claudeUsageTUI(ctx context.Context, binary claudeCLI, configDir string) (string, error) {
	if _, why := claudeMachineWhy(binary); why != "" {
		return "", claudeTUIErr(why)
	}
	work, done, err := oneOffDir()
	if err != nil {
		return "", err
	}
	defer done()
	trustWork(binary, configDir, work)
	cmd, err := binary.tuiCommand(ctx, claudeTUIUsageArgs()...)
	if err != nil {
		return "", err
	}
	cmd.Dir = work
	env, err := binary.proxied(ctx, claudeUsageEnv())
	if err != nil {
		return "", err
	}
	// the folder is magpie's own: asked whether to trust it, say yes
	env = append(env, claudetui.TrustEnv+"=1")
	env = append(env, helperDeadline(ctx)...)
	cmd.Env = binary.tuiEnv(binary.env(binary.asRoot(env), configDir), configDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	go func() { _, _ = io.WriteString(stdin, `{"type":"command","command":"/usage"}`+"\n") }()
	out := &resultLines{done: func() { _ = stdin.Close() }}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, &stderr
	runErr := runClaudeTree(ctx, cmd, "a Claude Code /usage read")
	dropOneOff(binary, configDir, work, out.sid)
	var full *claudeFull
	if errors.As(runErr, &full) {
		return "", runErr
	}
	if out.res != nil {
		if out.res.IsError {
			return "", errors.New("Claude Code: " + clip(out.res.Result))
		}
		return out.res.Result, nil
	}
	err = oneOffErr(ctx, stderr.String(), runErr)
	logOneOff("a /usage read", configDir, stderr.String(), err)
	return "", err
}

// oneOffDir is the folder a one-off interactive run (a test, a /usage)
// works in: magpie's own, the one the bridge's runs work in
// (claudeWorkDir). Claude Code asks whether to trust a folder once for
// each of its state files, and keeps the answer there: a folder of each
// run's own was asked again every time, and left one more trusted
// project in the state file each time (#4830, mydosu: 67 of them). done
// removes a folder of the run's own, made only when magpie's can't be
// used.
func oneOffDir() (dir string, done func(), err error) {
	if dir, err := claudeWorkDir(); err == nil {
		return dir, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "magpie-claude-")
	if err != nil {
		return "", nil, err
	}
	return tmp, func() { _ = os.RemoveAll(tmp) }, nil
}

// trustWork records, in a saved account's config directory (configDir,
// "" for the account Claude Code is signed in to, whose ~/.claude.json
// magpie never writes), that Claude Code may work in magpie's own folder
// work, before a run there: asked whether to trust it, a run of that
// account waited out its caller's time on the question (#4830, mydosu).
// A folder of the run's own (oneOffDir's fallback) is not recorded, so
// entries don't pile up.
func trustWork(binary claudeCLI, configDir, work string) {
	if configDir == "" {
		return
	}
	if filepath.Clean(work) != filepath.Join(os.TempDir(), claudeWorkName()) {
		return
	}
	if err := provider.ClaudeTrustFolder(configDir, claudeTrustKeys(binary, work)...); err != nil {
		log.Printf("claude: magpie's work folder not recorded as trusted for %s: %v", configDir, err)
	}
}

// trustGOOS is the system claudeTrustKeys keys for; a var for tests.
var trustGOOS = runtime.GOOS

// claudeTrustKeys are the projects keys Claude Code looks a trusted folder
// up by when it runs in work (2.1.316, its trust check): the folder's path
// in NFC, on Windows with forward slashes ("C:/Users/<me>/AppData/Local/
// Temp/magpie-claude-work"), and the path its links lead to, which is the
// one a Mac's getcwd says (/private/var/… for /var/…). One in WSL runs in
// the distro's mount of it.
func claudeTrustKeys(binary claudeCLI, work string) []string {
	if binary.wsl != nil {
		return []string{binary.file(work)}
	}
	var keys []string
	for _, w := range []string{work, evalSymlinks(work)} {
		k := norm.NFC.String(filepath.Clean(w))
		if trustGOOS == "windows" {
			k = strings.ReplaceAll(k, `\`, "/")
		}
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// dropOneOff removes the session a one-off interactive run left in
// magpie's folder, by its id (sid, "" when it told none): a test's "hi"
// keeps its transcript there. A folder of the run's own goes whole.
func dropOneOff(binary claudeCLI, configDir, work, sid string) {
	if binary.wsl != nil || sid == "" {
		return
	}
	removeSession(claudeSessions(configDir, work), sid)
}

// oneOffMargin is how long before its caller's deadline a one-off
// interactive run's helper ends on its own (claudetui.DeadlineEnv), for
// what it says to be read before the run is killed.
const oneOffMargin = 3 * time.Second

// helperDeadline tells the helper when ctx ends, oneOffMargin early (a
// quarter of what is left, when that is less), nothing when it doesn't.
func helperDeadline(ctx context.Context) []string {
	dl, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	margin := min(oneOffMargin, time.Until(dl)/4)
	return []string{claudetui.DeadlineEnv + "=" + strconv.FormatInt(dl.Add(-margin).UnixMilli(), 10)}
}

// oneOffErr is why a one-off interactive run ended with no result: what
// its helper said, else that its caller stopped waiting, never only the
// exit status its kill left (on Windows "exit status 1", #4830).
func oneOffErr(ctx context.Context, stderr string, runErr error) error {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return errors.New("Claude Code: " + clip(msg))
	}
	if ctx.Err() != nil {
		return fmt.Errorf("Claude Code's interactive mode said nothing before magpie stopped waiting for it (%v)", ctx.Err())
	}
	if runErr != nil {
		return runErr
	}
	return errors.New("Claude Code ended with no answer")
}

// logOneOff writes to app.log why a one-off interactive run (what) of
// the account in configDir failed (err): the reason the helper gave on
// stderr and the end of Claude Code's last screen, which err, the card's
// text, clips to its first 300 bytes. mydosu (#4830) found nothing in
// app.log to say why every read of a saved account failed. Secrets and
// the user's folders are taken out (logSafe).
func logOneOff(what, configDir, stderr string, err error) {
	if err == nil {
		return
	}
	why := strings.TrimSpace(stderr)
	if why == "" {
		why = err.Error()
	}
	who := "the account Claude Code is signed in to"
	if configDir != "" {
		who = "saved account " + filepath.Base(configDir)
	}
	log.Printf("claude: %s of %s failed: %s", what, who, logSafe(why))
}

// homePath is a user's home folder in a path, on Windows, a Mac or
// Linux, with either slash: what follows it is kept, the user's name not.
var homePath = regexp.MustCompile(`(?i)(?:\b[a-z]:)?[\\/]+(?:users|home)[\\/]+[^\\/\s"'│:]+`)

// logSafe is s for app.log: secrets out (redact.Scrub), this user's home
// folder and any other home folder in it written ~, its spaces
// collapsed, and only its start and end kept when it is long.
func logSafe(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		for _, h := range []string{home, filepath.ToSlash(home), strings.ReplaceAll(home, "/", `\`)} {
			s = strings.ReplaceAll(s, h, "~")
		}
	}
	s = homePath.ReplaceAllString(s, "~")
	s = redact.Scrub(s)
	const head, tail = 300, 500
	if len(s) > head+tail {
		i := head
		for i > 0 && !utf8.RuneStart(s[i]) {
			i--
		}
		s = s[:i] + " " + clipTail(s, tail)
	}
	return s
}

// runClaudeTree runs a one-off Claude Code (a test, a /usage) and ends,
// with it, whatever it started: its context ending kills only the
// process magpie started, and on Windows that is cmd.exe running npm's
// claude.cmd, whose node went on running with nobody waiting for it
// (#1894). In a tree, a job on Windows and a session elsewhere, what is
// left once the head is gone is ended with it, and on Windows when magpie
// exits mid-run.
func runClaudeTree(ctx context.Context, cmd *exec.Cmd, what string) error {
	room, err := claudeRoom.take(ctx, what)
	if err != nil {
		return err
	}
	defer room(false)
	t, err := proc.StartTree(cmd)
	if err != nil {
		return err
	}
	return t.Wait()
}

// claudeRun is the result Claude Code prints for a -p run with
// --output-format json.
type claudeRun struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	// SessionID is the session the run said it is (stream-json's lines)
	SessionID string `json:"session_id"`
}

// claudeResult reads the result from what claude -p --output-format json
// printed: one object, or, when the user turned on Claude Code's verbose
// output (its own config, which --setting-sources leaves on), an array of
// every message, the result last.
func claudeResult(b []byte) (claudeRun, bool) {
	var res claudeRun
	if json.Unmarshal(b, &res) == nil {
		return res, true
	}
	var all []claudeRun
	if json.Unmarshal(b, &all) != nil {
		return claudeRun{}, false
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Type == "result" {
			return all[i], true
		}
	}
	return claudeRun{}, false
}

// disableDeepLinkSettings keeps every spawned Claude Code from registering
// the claude-cli:// OS URL handler (#4928): --setting-sources "" keeps the
// user's own disableDeepLinkRegistration from loading, and a headless run
// has no use for the handler. --settings is not one of the sources
// --setting-sources controls, so this applies however the user's settings
// read.
const disableDeepLinkSettings = `{"disableDeepLinkRegistration":"disable"}`

func claudeUsageArgs() []string {
	return []string{"-p", "/usage", "--output-format", "json",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--settings", disableDeepLinkSettings, "--no-session-persistence"}
}

// claudeTUIUsageArgs are claudeUsageArgs for the interactive mode's
// helper, /usage typed in it rather than given.
func claudeTUIUsageArgs() []string {
	return []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--settings", disableDeepLinkSettings}
}

func claudeWarmArgs(model string) []string {
	return []string{"-p", "--output-format", "json", "--model", model,
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--settings", disableDeepLinkSettings, "--no-session-persistence"}
}

// claudeTUIWarmArgs are claudeWarmArgs for the interactive mode's
// helper, which takes the bridge's stream-json.
func claudeTUIWarmArgs(model string) []string {
	return []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--model", model,
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--settings", disableDeepLinkSettings}
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

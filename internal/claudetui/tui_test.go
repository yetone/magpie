package claudetui

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary is also the shim (MAGPIE_TUI_TEST=shim) and a fake
// Claude Code (MAGPIE_TUI_TEST=claude): interactive, it shows a prompt,
// writes each line typed into its transcript as a turn answered "echo:
// <line>"; with -p it answers each stream-json line with a -p result.
func TestMain(m *testing.M) {
	switch os.Getenv("MAGPIE_TUI_TEST") {
	case "shim":
		if err := Run(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "claude":
		fakeClaude(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeTrust asks whether to trust the folder as Claude Code 2.1.314 does:
// its words placed by cursor moves, "No, exit" chosen, and drawn again on
// No once it listens, the keys before that lost. Enter on No exits.
// With always, it never listens, as a question whose answer never goes
// through.
func fakeTrust(r *bufio.Reader, always bool) {
	var mu sync.Mutex
	yes, deaf := false, true
	draw := func() {
		marks := map[bool]string{true: "\x1b[38;2;177;185;249m❯\x1b[39m", false: " "}
		fmt.Printf("\x1b[2GDo\x1b[5Gyou\x1b[9Gtrust\x1b[15Gthis\x1b[20Gfolder?\r\n\x1b[2G%s\x1b[4GNo,\x1b[8Gexit\r\n\x1b[2G%s\x1b[4GYes,\x1b[9GI\x1b[11Gtrust\x1b[17Gthis\x1b[22Gfolder\r\n", marks[!yes], marks[yes])
	}
	draw()
	time.AfterFunc(150*time.Millisecond, func() {
		if always {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		deaf, yes = false, false
		draw()
	})
	for {
		c, err := r.ReadByte()
		if err != nil {
			os.Exit(0)
		}
		if c == 0x1b {
			seq := make([]byte, 2)
			if _, err := io.ReadFull(r, seq); err != nil {
				os.Exit(0)
			}
			c = seq[1]
		}
		mu.Lock()
		switch {
		case deaf:
		case c == 'B' || c == 'A':
			yes = c == 'B'
			draw()
		case c == '\r' && !yes:
			os.Exit(1)
		case c == '\r':
			mu.Unlock()
			return
		}
		mu.Unlock()
	}
}

// fakeOnboard shows Claude Code 2.1.316's onboarding where its config
// folder has none done (FAKE_ONBOARD set): the text style, the security
// notes, and with "terminal" whether to set up the terminal. Each is
// drawn again 150ms after it is first, the keys before that lost; Enter
// goes on, and Esc skips the terminal setup, whose Enter (Yes) is written
// down as "terminal-set-up". Gone through, it writes "onboarded" in the
// config folder, and shows none of it again. With "stuck" the notes come
// back after each Enter.
func fakeOnboard(r *bufio.Reader, how string) {
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if _, err := os.Stat(filepath.Join(cfg, "onboarded")); err == nil {
		return
	}
	screens := []string{
		"\r\n Let's get started.\r\n\r\n Choose\x1b[1Cthe text style that looks best with your terminal\r\n ❯ 1. Dark mode ✔\r\n   2. Light mode\r\n",
		// a space a move of the cursor or a no-break space, as 2.1.316
		// draws them
		"\r\n Security\x1b[1Cnotes:\r\n\r\n 1. Claude can make mistakes\r\n    You should always review Claude's responses\r\n\r\n 2. Due to prompt injection risks, only use it with code you trust\r\n\r\n Press\u00a0Enter\x1b[1Cto\x1b[1Ccontinue…\r\n",
	}
	if how == "terminal" {
		screens = append(screens, "\r\n Use Claude Code's terminal\x1b[1Csetup?\r\n\r\n For the optimal coding experience, enable the recommended settings\r\n for your terminal: Option+Enter for newlines and no audible bell\r\n\r\n ❯ 1. Yes, use recommended settings\r\n   2. No, maybe later with /terminal-setup\r\n\r\n Enter to confirm · Esc to skip\r\n")
	}
	for i := 0; i < len(screens); i++ {
		fmt.Print("\x1b[2J\x1b[H" + screens[i])
		deaf := make(chan struct{})
		time.AfterFunc(150*time.Millisecond, func() {
			fmt.Print("\x1b[2J\x1b[H" + screens[i])
			close(deaf)
		})
		for {
			c, err := r.ReadByte()
			if err != nil {
				os.Exit(0)
			}
			select {
			case <-deaf:
			default:
				continue
			}
			terminal := strings.Contains(screens[i], "terminal\x1b[1Csetup")
			if c == '\r' && terminal {
				_ = os.WriteFile(filepath.Join(cfg, "terminal-set-up"), nil, 0o600)
			}
			if c == '\r' && how == "stuck" && i == 1 {
				i--
				break
			}
			if c == '\r' || c == 0x1b && terminal {
				break
			}
		}
	}
	_ = os.MkdirAll(cfg, 0o700)
	_ = os.WriteFile(filepath.Join(cfg, "onboarded"), nil, 0o600)
}

func fakeClaude(args []string) {
	if slices.Contains(args, "-p") {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"result":"print mode"}` + "\n")
		}
		return
	}
	if os.Getenv("FAKE_FAIL") == "1" {
		fmt.Print("something went wrong\n")
		os.Exit(3)
	}
	if os.Getenv("FAKE_STALL") == "1" {
		// drawn, but no prompt ever comes
		fmt.Print("\r\nStarting up…\r\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if os.Getenv("FAKE_LOGIN") == "1" {
		// signed in for -p alone: the interactive mode asks how to sign in
		fmt.Print("\r\nSelect login method:\r\n\r\n ❯ 1. Claude account with subscription\r\n   2. Anthropic Console account\r\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	sid := ""
	if i := slices.Index(args, "--session-id"); i >= 0 {
		sid = args[i+1]
	}
	dir := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "work")
	_ = os.MkdirAll(dir, 0o700)
	// raw, as Claude Code's terminal is: a paste is taken whole, and Enter
	// outside one sends it
	fakeRaw()
	r := bufio.NewReader(os.Stdin)
	if v := os.Getenv("FAKE_ONBOARD"); v != "" {
		fakeOnboard(r, v)
	}
	if v := os.Getenv("FAKE_TRUST"); v != "" {
		fakeTrust(r, v == "deaf")
	}
	// what it was started with, for the test to read
	_ = os.WriteFile(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "args"), []byte(strings.Join(args, "\n")), 0o600)
	// FAKE_TAP: Bun loads the tap BUN_OPTIONS names, which says so, and
	// each reply is streamed to it before its message is written
	var tapped *os.File
	if os.Getenv("FAKE_TAP") == "1" {
		_, js, _ := strings.Cut(os.Getenv("BUN_OPTIONS"), "--preload ")
		if b, err := os.ReadFile(js); err == nil && string(b) == string(tapJS) {
			tapped, _ = os.OpenFile(os.Getenv(tapEnv), os.O_APPEND|os.O_WRONLY, 0)
		}
		if tapped != nil {
			_, _ = tapped.WriteString(`{"tap":"on"}` + "\n")
		}
	}
	// FAKE_MCP_DELAY: magpie's tool server is listed that long after the
	// prompt shows, as Claude Code connects its MCP servers beside it; a
	// message sent before then is answered without the tools
	listed := mcpListed(args)
	if d, err := time.ParseDuration(os.Getenv("FAKE_MCP_DELAY")); err == nil && listed != "" {
		time.AfterFunc(d, func() { _ = os.WriteFile(listed, nil, 0o600) })
	}
	streams := 0
	fmt.Print("\r\n❯ ")
	// the prompt's words; an image file's path pasted alone is read a
	// moment later and becomes "[Image #<n>:<its bytes>]" where the
	// prompt's words end then
	var mu sync.Mutex
	var typed, paste strings.Builder
	pasting, images := false, 0
	// retry is what an "r" draws while /usage's panel says it couldn't
	// load the figures, nil when it says nothing of the kind
	var retry []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			return
		}
		switch {
		case c == 0x1b:
			seq := make([]byte, 5)
			if _, err := io.ReadFull(r, seq); err != nil {
				return
			}
			pasting = string(seq) == "[200~"
			if pasting {
				paste.Reset()
				continue
			}
			p := paste.String()
			if b, err := os.ReadFile(p); err == nil && strings.HasPrefix(filepath.Base(p), "image-") {
				images++
				n := images
				go func() {
					time.Sleep(200 * time.Millisecond)
					mu.Lock()
					defer mu.Unlock()
					typed.WriteString(fmt.Sprintf("[Image #%d:%s]", n, b))
					fmt.Printf("[Image #%d]", n)
				}()
				continue
			}
			mu.Lock()
			typed.WriteString(p)
			mu.Unlock()
			fmt.Print(p) // drawn in the prompt
			continue
		case pasting:
			paste.WriteByte(c)
			continue
		case c == 'r' && retry != nil:
			fakeFrames(retry)
			retry = nil
			continue
		case c != '\r':
			mu.Lock()
			typed.WriteByte(c)
			mu.Unlock()
			fmt.Printf("%c", c)
			continue
		}
		mu.Lock()
		line := typed.String()
		typed.Reset()
		mu.Unlock()
		if listed != "" {
			if _, err := os.Stat(listed); err != nil {
				line += " (without magpie's tools)"
			}
		}
		if tapped != nil {
			streams++
			n := streams
			for _, e := range []string{`{"type":"message_start","message":{"id":"msg_1","model":"claude-haiku","usage":{"input_tokens":3}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"echo: "}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + strconvQuote(line) + `}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`} {
				_, _ = fmt.Fprintf(tapped, `{"s":%d,"e":%s}`+"\n", n, e)
			}
			_, _ = fmt.Fprintf(tapped, `{"s":%d,"end":"done"}`+"\n", n)
		}
		if line == "/usage" {
			// Claude Code drawn again before a slow /usage's panel: its
			// banner and folder, as ikunbots' read told them (v0.1.2738)
			if after := os.Getenv("FAKE_USAGE_BANNER"); after != "" {
				fmt.Print("\x1b[2J\x1b[H ▐▛███▜▌   Claude Code v2.1.316\r\n▝▜█████▛▘  Opus 4.8 · Claude Pro\r\n  ▘▘ ▝▝    /private/var/folders/3d/qhyx6qps/T/magpie-claude-work-501\r\n\r\n" +
					strings.Repeat("─", 80) + "\r\n❯ \r\n" + strings.Repeat("─", 80) + "\r\n")
				ms, err := strconv.Atoi(after)
				if err != nil {
					continue
				}
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
			// its panel, as Claude Code 2.1.316 drew it
			b, _ := os.ReadFile(os.Getenv("FAKE_USAGE"))
			// FAKE_USAGE_FIRST: drawn before it, as Windows' pseudo
			// console passed on 2.1.316's panel loading its figures, or
			// failing to; FAKE_USAGE_THEN says what comes after: the
			// figures 4s later ("slow"), or on an "r" ("retry"), or the
			// failure again on an "r" ("retry-fails")
			if first := os.Getenv("FAKE_USAGE_FIRST"); first != "" {
				f, _ := os.ReadFile(first)
				_, _ = os.Stdout.Write(f)
				switch os.Getenv("FAKE_USAGE_THEN") {
				case "slow":
					time.Sleep(4 * time.Second)
				case "retry":
					retry = b
					continue
				case "retry-fails":
					retry = f
					continue
				}
			}
			fakeFrames(b)
			continue
		}
		f, _ := os.OpenFile(filepath.Join(dir, sid+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		rows := []any{
			map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": line}},
			map[string]any{"type": "assistant", "requestId": "req_1", "message": map[string]any{"id": "msg_1", "model": "claude-haiku", "role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "echo: " + line}}}},
			map[string]any{"type": "system", "subtype": "turn_duration"},
		}
		for _, row := range rows {
			b, _ := json.Marshal(row)
			_, _ = f.Write(append(b, '\n'))
		}
		f.Close()
		fmt.Print("\r\n❯ ")
	}
}

// fakeFrames draws b a frame at a time, as Claude Code does: each frame
// ends parking the cursor at the screen's foot (ESC[24;1H in the 80 by 24
// the captures were made in, ESC[50;1H in the helper's 200 by 50), and the
// next comes a moment later. A terminal that renders what it is given
// (Windows' pseudo console) passes on each frame, not only how the last
// one left the screen.
func fakeFrames(b []byte) {
	parks := [][]byte{[]byte("\x1b[24;1H"), []byte("\x1b[50;1H")}
	for len(b) > 0 {
		i, n := -1, 0
		for _, park := range parks {
			if j := bytes.Index(b, park); j >= 0 && (i < 0 || j < i) {
				i, n = j, len(park)
			}
		}
		if i < 0 {
			_, _ = os.Stdout.Write(b)
			return
		}
		_, _ = os.Stdout.Write(b[:i+n])
		b = b[i+n:]
		time.Sleep(50 * time.Millisecond)
	}
}

// shim runs the shim on the fake Claude Code with pargs, says lines on its
// input and returns what it wrote, once it wrote want lines.
func runShim(t *testing.T, env []string, pargs []string, lines []string, want int) []map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cfg := filepath.Join(home, "claude")
	cmd := exec.Command(exe, pargs...)
	tmp := filepath.Join(home, "tmp")
	_ = os.Mkdir(tmp, 0o700)
	cmd.Env = append(os.Environ(), "MAGPIE_TUI_TEST=shim", "HOME="+home, "XDG_CACHE_HOME="+filepath.Join(home, "cache"), "LOCALAPPDATA="+filepath.Join(home, "cache"),
		"TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp, "CLAUDE_CONFIG_DIR="+cfg)
	cmd.Env = append(cmd.Env, env...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	t.Setenv("MAGPIE_TUI_TEST_CFG", cfg)
	for _, l := range lines {
		// a line after "raw:" is said as it is
		if raw, ok := strings.CutPrefix(l, "raw:"); ok {
			_, _ = io.WriteString(stdin, raw+"\n")
			continue
		}
		// a line that is JSON is the message's content blocks
		var content any = l
		if json.Valid([]byte(l)) {
			content = json.RawMessage(l)
		}
		b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
		_, _ = stdin.Write(append(b, '\n'))
	}
	got := make(chan map[string]any)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			var m map[string]any
			_ = json.Unmarshal(sc.Bytes(), &m)
			got <- m
		}
	}()
	var out []map[string]any
	by := time.After(30 * time.Second)
	for len(out) < want {
		select {
		case m := <-got:
			out = append(out, m)
		case <-by:
			t.Fatalf("the shim wrote %d lines of %d in 30s: %v", len(out), want, out)
		}
	}
	return out
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func fakePath(t *testing.T) string {
	t.Helper()
	exe, _ := os.Executable()
	// the fake is the test binary, told so by its environment: a script
	// beside it starts it so
	p, script := fakeScript(t.TempDir(), exe)
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

var pArgs = []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--include-partial-messages", "--verbose",
	"--model", "haiku", "--tools", "WebSearch", "--strict-mcp-config", "--mcp-config", "{}", "--setting-sources", "",
	"--dangerously-skip-permissions", "--effort", "high", "--thinking-display", "summarized"}

func TestShimTurnsInteractive(t *testing.T) {
	out := runShim(t, nil, append([]string{fakePath(t)}, pArgs...), []string{"hello", "/help"}, 5)
	if out[0]["type"] != "system" || out[0]["subtype"] != "init" || out[0]["session_id"] == "" {
		t.Fatalf("first line %v, want the init", out[0])
	}
	var texts []string
	results := 0
	for _, m := range out[1:] {
		switch m["type"] {
		case "assistant":
			b, _ := json.Marshal(m["message"])
			texts = append(texts, string(b))
		case "result":
			results++
		}
	}
	if len(texts) != 2 || !strings.Contains(texts[0], "echo: hello") || results != 2 {
		t.Fatalf("got %v", out)
	}
	// a slash opening the message is pasted after a line break: words,
	// not a command
	if !strings.Contains(texts[1], `\n/help"`) {
		t.Fatalf("the second turn said %s", texts[1])
	}
	args, _ := os.ReadFile(filepath.Join(os.Getenv("MAGPIE_TUI_TEST_CFG"), "args"))
	got := strings.Split(string(args), "\n")
	for _, no := range []string{"-p", "--dangerously-skip-permissions", "--output-format", "--verbose", "--thinking-display"} {
		if slices.Contains(got, no) {
			t.Errorf("the interactive Claude Code was given %s: %q", no, got)
		}
	}
	for _, yes := range []string{"--no-chrome", "mcp__magpie,WebSearch", "--session-id", "high"} {
		if !slices.Contains(got, yes) {
			t.Errorf("the interactive Claude Code wasn't given %s: %q", yes, got)
		}
	}
}

// A message's images are pasted as their files' paths, each alone and
// after the prompt shows the one before: the words and images reach
// Claude Code in their order. One it can't take as a file is an error.
func TestShimPastesImages(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	msg := `[{"type":"text","text":"look:"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64("red") + `"}},` +
		`{"type":"text","text":"and"},{"type":"image","source":{"type":"base64","media_type":"image/gif","data":"` + b64("blue") + `"}},{"type":"text","text":"now"}]`
	byURL := `[{"type":"text","text":"see"},{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]`
	out := runShim(t, nil, append([]string{fakePath(t)}, pArgs...), []string{msg, byURL}, 4)
	var said string
	var failed bool
	for _, m := range out[1:] {
		switch m["type"] {
		case "assistant":
			b, _ := json.Marshal(m["message"])
			said = string(b)
		case "result":
			failed = failed || m["is_error"] == true && strings.Contains(fmt.Sprint(m["result"]), "an image by url")
		}
	}
	if want := `echo: look:\n\n[Image #1:red]\n\nand\n\n[Image #2:blue]\n\nnow"`; !strings.Contains(said, want) {
		t.Fatalf("Claude Code was sent %s, want %s", said, want)
	}
	if !failed {
		t.Fatalf("an image by its URL wasn't an error: %v", out)
	}
	// Claude Code has them once their message is in the transcript: the
	// files are gone while the helper goes on
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(os.Getenv("MAGPIE_TUI_TEST_CFG")), "tmp", imagesPrefix+"*"))
	if len(left) > 0 {
		t.Fatalf("the images are left after their message: %v", left)
	}
}

// A folder of images or a tap's a killed helper left is removed by a
// later one after imagesLeft, and one in use isn't.
func TestDropLeft(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	for _, prefix := range []string{imagesPrefix, tapPrefix} {
		old, now := filepath.Join(tmp, prefix+"1-a"), filepath.Join(tmp, prefix+"2-b")
		for _, d := range []string{old, now} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(filepath.Join(d, "image-1.png"), []byte("x"), 0o600)
		}
		then := time.Now().Add(-imagesLeft - time.Minute)
		if err := os.Chtimes(old, then, then); err != nil {
			t.Fatal(err)
		}
		dropLeft()
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Errorf("the %s folder left %s ago is still there (%v)", prefix, imagesLeft, err)
		}
		if _, err := os.Stat(now); err != nil {
			t.Errorf("the %s folder in use was removed: %v", prefix, err)
		}
	}
}

// runEnds runs the shim in home, told "hello", until it
// exits or answers: what it wrote, its stderr, and how it ended.
func runEnds(t *testing.T, home string, env []string) (out, stderr string, err error) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hello"}})
	return runEndsTold(t, home, env, string(b))
}

// runEndsTold is runEnds told line, a stream-json line, for "hello".
func runEndsTold(t *testing.T, home string, env []string, line string) (out, stderr string, err error) {
	t.Helper()
	exe, _ := os.Executable()
	cfg := filepath.Join(home, "claude")
	tmp := filepath.Join(home, "tmp")
	_ = os.Mkdir(tmp, 0o700)
	cmd := exec.Command(exe, append([]string{fakePath(t)}, pArgs...)...)
	cmd.Env = append(os.Environ(), "MAGPIE_TUI_TEST=shim", "HOME="+home, "XDG_CACHE_HOME="+filepath.Join(home, "cache"), "LOCALAPPDATA="+filepath.Join(home, "cache"),
		"TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp, "CLAUDE_CONFIG_DIR="+cfg)
	cmd.Env = append(cmd.Env, env...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	_, _ = io.WriteString(stdin, line+"\n")
	done := make(chan string, 1)
	go func() {
		var all strings.Builder
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			all.WriteString(sc.Text() + "\n")
			if strings.Contains(sc.Text(), `"type":"result"`) {
				break
			}
		}
		done <- all.String()
	}()
	select {
	case out = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the shim neither answered nor ended in 30s")
	}
	if !strings.Contains(out, `"type":"result"`) {
		err = cmd.Wait()
	}
	return out, errb.String(), err
}

// An interactive mode that doesn't come up ends the run, saying why, and
// never becomes -p: a -p request counts against the plan as another
// client's would, and starts without the interactive mode's cache. Nothing
// is kept for the next run, which tries the interactive mode again.
func TestShimNeverPrints(t *testing.T) {
	for _, c := range []struct {
		name, why string
		env       []string
	}{
		{"failed start", "something went wrong", []string{"FAKE_FAIL=1"}},
		{"untrusted folder", "trust the folder", []string{"FAKE_TRUST=1"}},
		// at once, not after readyWait: runEnds waits 30s (#4504)
		{"asks to sign in", "sign", []string{"FAKE_LOGIN=1"}},
		{"first-run screens it stays on", "first-run screens", []string{"FAKE_ONBOARD=stuck"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			out, stderr, err := runEnds(t, home, c.env)
			if err == nil || strings.Contains(out, "print mode") {
				t.Fatalf("ended %v, wrote %q", err, out)
			}
			if !strings.Contains(stderr, "claude-tui: ") || !strings.Contains(strings.ToLower(stderr), c.why) || strings.Contains(stderr, "-p") {
				t.Fatalf("stderr %q", stderr)
			}
			// the next run, Claude Code fixed, is the interactive mode
			out, stderr, err = runEnds(t, home, nil)
			if err != nil || strings.Contains(out, "print mode") || !strings.Contains(out, "echo: hello") {
				t.Fatalf("the next run ended %v, wrote %q, stderr %q", err, out, stderr)
			}
		})
	}
}

// The first message waits for Claude Code to have listed the tools of
// magpie's tool server, which it connects as its prompt shows: sent at
// the prompt, it was answered without the agent's tools (MinhuWang on X,
// a pi agent, the MCP server slow to load).
func TestShimFirstMessageWaitsForTools(t *testing.T) {
	listed := filepath.Join(t.TempDir(), "listed")
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"magpie": map[string]any{"command": "magpie", "args": []string{MCPCommand, "callback", "tools.json", listed}},
	}})
	args := slices.Clone(pArgs)
	args[slices.Index(args, "--mcp-config")+1] = string(cfg)
	start := time.Now()
	out := runShim(t, []string{"FAKE_MCP_DELAY=2s"}, append([]string{fakePath(t)}, args...), []string{"hello"}, 3)
	b, _ := json.Marshal(out[1]["message"])
	if out[1]["type"] != "assistant" || !strings.Contains(string(b), "echo: hello") || strings.Contains(string(b), "without magpie's tools") {
		t.Fatalf("the first turn said %s", b)
	}
	if d := time.Since(start); d < 2*time.Second {
		t.Fatalf("answered in %s, before the tools were listed", d)
	}
}

// A config folder whose onboarding isn't done (the account Claude Code
// is signed in to where `claude` was never run, #4817) is gone through
// as a person would: the text style kept, the security notes read on,
// the terminal setup skipped. Claude Code records it done, and the next
// run is shown none of it.
func TestShimGoesThroughOnboarding(t *testing.T) {
	for _, how := range []string{"1", "terminal"} {
		t.Run(how, func(t *testing.T) {
			home := t.TempDir()
			for run := range 2 {
				out, stderr, err := runEnds(t, home, []string{"FAKE_ONBOARD=" + how})
				if err != nil || !strings.Contains(out, "echo: hello") || strings.Contains(out, "print mode") {
					t.Fatalf("run %d ended %v, wrote %q, stderr %q", run, err, out, stderr)
				}
			}
			cfg := filepath.Join(home, "claude")
			if _, err := os.Stat(filepath.Join(cfg, "onboarded")); err != nil {
				t.Fatal("the onboarding wasn't recorded done")
			}
			if _, err := os.Stat(filepath.Join(cfg, "terminal-set-up")); err == nil {
				t.Fatal("the user's terminal was set up")
			}
		})
	}
}

// A one-off run's caller (a /usage read, a test's "hi") tells the helper
// when it stops waiting (DeadlineEnv), and the helper ends then on its
// own, saying what Claude Code hadn't done and what it last drew. Killed
// at the caller's deadline instead, it said nothing, and the reading's
// error was "exit status 1" (#4830, mydosu, Windows).
func TestShimEndsAtItsCallersDeadline(t *testing.T) {
	wd, _ := os.Getwd()
	for _, c := range []struct {
		name, line, why, screen string
		env                     []string
	}{
		{"no prompt", `{"type":"user","message":{"role":"user","content":"hello"}}`, "showed no prompt in the", "Starting up", []string{"FAKE_STALL=1"}},
		// the question a saved account's reads waited out on (#4830,
		// mydosu): said, with the folder, never "showed no prompt"
		{"trust unanswered", `{"type":"user","message":{"role":"user","content":"hello"}}`, "asked whether to trust " + wd + ", not answered in the", "trust this folder", []string{"FAKE_TRUST=deaf", TrustEnv + "=1"}},
		// FAKE_USAGE names no panel: nothing is drawn for /usage
		{"no /usage panel", `{"type":"command","command":"/usage"}`, "hadn't drawn /usage's panel in the", "", []string{"FAKE_USAGE=" + filepath.Join(t.TempDir(), "none")}},
		// the banner drawn again and no panel after it: never told as one
		{"banner, no /usage panel", `{"type":"command","command":"/usage"}`, "hadn't drawn /usage's panel in the", "magpie-claude-work-501", []string{"FAKE_USAGE_BANNER=never"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			by := start.Add(3 * time.Second).UnixMilli()
			out, stderr, err := runEndsTold(t, t.TempDir(), append(c.env, DeadlineEnv+"="+strconv.FormatInt(by, 10)), c.line)
			d := time.Since(start)
			if err == nil || strings.Contains(out, `"type":"result"`) {
				t.Fatalf("ended %v, wrote %q", err, out)
			}
			if !strings.Contains(stderr, "claude-tui: Claude Code "+c.why) || !strings.Contains(stderr, c.screen) {
				t.Fatalf("stderr %q", stderr)
			}
			// at the deadline, with room for a slow machine to start it,
			// and well before the helper's own waits (20s, 45s)
			if d < 2500*time.Millisecond || d > 15*time.Second {
				t.Fatalf("ended after %s, the deadline 3s", d)
			}
		})
	}
}

func TestShimTrustsWhenTold(t *testing.T) {
	out := runShim(t, []string{"FAKE_TRUST=1", TrustEnv + "=1"}, append([]string{fakePath(t)}, pArgs...), []string{"hello"}, 3)
	if out[0]["subtype"] != "init" || out[1]["type"] != "assistant" {
		t.Fatalf("got %v, want an interactive turn", out)
	}
}

func TestPasteable(t *testing.T) {
	for _, c := range []struct {
		in   string
		busy bool
		want string
	}{
		{"hello", false, "hello"},
		{"/help", false, "\n/help"},
		{"  !ls", false, "\n!ls"},
		{"# note", true, userMark + "# note"},
		{"a\r\nb\x1b[201~c", false, "a\nb[201~c"},
		{"x / y", true, "x / y"},
	} {
		if got := pasteable(c.in, c.busy); got != c.want {
			t.Errorf("pasteable(%q, %v) = %q, want %q", c.in, c.busy, got, c.want)
		}
	}
}

type lines struct {
	out          []map[string]any
	rows, overed int
}

func (l *lines) emit(v any) {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	l.out = append(l.out, m)
}
func (l *lines) sawRow()   { l.rows++ }
func (l *lines) turnOver() { l.overed++ }

func TestTranslatorAPIError(t *testing.T) {
	l := &lines{}
	tr := newTranslator(l)
	for _, row := range []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":429,"apiErrorParams":{"rate_limit_info":{"status":"rejected"}},"message":{"id":"x","model":"<synthetic>","content":[{"type":"text","text":"You've hit your limit"}]}}`,
		`{"type":"system","subtype":"turn_duration"}`,
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"agent"}}`,
	} {
		tr.row([]byte(row))
	}
	kinds := []string{}
	for _, m := range l.out {
		kinds = append(kinds, m["type"].(string))
	}
	if !slices.Equal(kinds, []string{"assistant", "rate_limit_event", "result"}) || l.rows != 1 || l.overed != 2 {
		t.Fatalf("got %v (rows %d, over %d)", l.out, l.rows, l.overed)
	}
	if r := l.out[2]; r["is_error"] != true || r["result"] != "You've hit your limit" || r["api_error_status"] != float64(429) {
		t.Fatalf("result %v", r)
	}
}

// An assistant row is told with its uuid, its place in the session, as
// -p tells it: the bridge cuts a branch of the session there (bbfatx on
// X). Told without it, no branch could be made of an interactive run's
// session.
func TestTranslatorTellsTheRowsUUID(t *testing.T) {
	l := &lines{}
	tr := newTranslator(l)
	tr.row([]byte(`{"type":"assistant","uuid":"row-1","message":{"id":"m","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"mcp__magpie__Read","input":{}}]}}`))
	if len(l.out) != 1 || l.out[0]["uuid"] != "row-1" {
		t.Fatalf("got %v", l.out)
	}
}

// A Claude Code in WSL is pasted an image's path as the distro reads it,
// and is handed TERM through WSLENV, the gateway's own names kept.
func TestShimInWSL(t *testing.T) {
	s := &shim{wsl: &WSL{Args: []string{"-d", "Ubuntu"}, Mount: "/mnt/", Projects: `\\wsl$\Ubuntu\home\me\.claude\projects`}}
	if got := s.path(`C:\Users\me\AppData\Local\Temp\magpie-images-1\1.png`); got != "/mnt/c/Users/me/AppData/Local/Temp/magpie-images-1/1.png" {
		t.Fatalf("path %q", got)
	}
	if got := (&shim{}).path("/tmp/a.png"); got != "/tmp/a.png" {
		t.Fatalf("this machine's path %q", got)
	}
	t.Setenv(WSLEnv, `{"args":["-d","Ubuntu"],"projects":"x"}`)
	t.Setenv("WSLENV", "CLAUDE_CONFIG_DIR/p")
	env := childEnv()
	var wslenv []string
	for _, e := range env {
		if strings.HasPrefix(e, WSLEnv+"=") {
			t.Fatalf("the child was handed %s", e)
		}
		if strings.HasPrefix(strings.ToUpper(e), "WSLENV=") {
			wslenv = append(wslenv, e)
		}
	}
	if len(wslenv) != 1 || wslenv[0] != "WSLENV=CLAUDE_CONFIG_DIR/p:TERM" {
		t.Fatalf("WSLENV %q", wslenv)
	}
	// the tap's go through WSLENV too, in place of the shim's own
	t.Setenv("BUN_OPTIONS", "--smol")
	env = childEnv("BUN_OPTIONS=--preload /mnt/c/t/tap.js", tapEnv+"=/mnt/c/t/tap.jsonl")
	var bun []string
	for _, e := range env {
		if strings.HasPrefix(e, "BUN_OPTIONS=") || strings.HasPrefix(e, tapEnv+"=") || strings.HasPrefix(e, "WSLENV=") {
			bun = append(bun, e)
		}
	}
	if want := []string{"WSLENV=CLAUDE_CONFIG_DIR/p:TERM:BUN_OPTIONS:" + tapEnv, "BUN_OPTIONS=--preload /mnt/c/t/tap.js", tapEnv + "=/mnt/c/t/tap.jsonl"}; !slices.Equal(bun, want) {
		t.Fatalf("env %q, want %q", bun, want)
	}
}

// /usage is typed, and the windows its panel shows are told as -p's
// /usage lines say them, in a result naming its local command; the
// panel is the bytes Claude Code 2.1.316 drew, a bar, then "81%", then
// "used", each placed by a move of the cursor.
func TestShimReadsUsage(t *testing.T) {
	panel, _ := filepath.Abs(filepath.Join("testdata", "usage-2.1.316.bin"))
	out := runShim(t, []string{"FAKE_USAGE=" + panel}, append([]string{fakePath(t)}, pArgs...), []string{`raw:{"type":"command","command":"/usage"}`}, 2)
	want := "Current session: 0% used · resets 12am (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"
	if r := out[1]; r["type"] != "result" || r["is_error"] != false || r["local_command"] != "usage" || r["result"] != want {
		t.Fatalf("told %v", r)
	}
}

// Claude Code draws in the terminal the helper gives it, 200 by 50 on
// every system, so a panel with three windows sits below the 24th row.
// Each is read: read on an 80 by 24 grid, the rows under the 24th were
// drawn over one another, and only the last window drawn was told
// (#4988, "7 days · Fable" alone on a Team plan; shinnqy on X).
func TestShimReadsUsageBelowTheTwentyFourthRow(t *testing.T) {
	// as 2.1.316 draws: from the top, down by rows (CR, ESC[nB), each
	// figure placed at its column (ESC[55G)
	var b strings.Builder
	b.WriteString("\x1b[H\r\x1b[29B   Settings  Status   Config   Usage   Stats\x1b[K")
	for _, w := range []struct{ name, used, reset string }{
		{"Current session", "29%", "Resets 2pm (Asia/Shanghai)"},
		{"Current week (all models)", "17%", "Resets Nov 8 at 4am (Asia/Shanghai)"},
		{"Current week (Fable)", "0%", "Resets Nov 8 at 4am (Asia/Shanghai)"},
	} {
		b.WriteString("\r\x1b[2B   " + w.name + "\x1b[K")
		b.WriteString("\r\x1b[1B   " + strings.Repeat("█", 10) + "\x1b[55G" + w.used + "\x1b[59Gused\x1b[K")
		b.WriteString("\r\x1b[1B   " + w.reset + "\x1b[K")
	}
	b.WriteString("\r\x1b[2B   Esc to cancel\x1b[K\x1b[50;1H")
	// a frame later the session's figure is drawn again, only the cells
	// that changed, as Claude Code's renderer does
	b.WriteString("\x1b[H\r\x1b[32B\x1b[55G30%\x1b[50;1H")
	panel := filepath.Join(t.TempDir(), "usage-200x50.bin")
	if err := os.WriteFile(panel, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runShim(t, []string{"FAKE_USAGE=" + panel}, append([]string{fakePath(t)}, pArgs...), []string{`raw:{"type":"command","command":"/usage"}`}, 2)
	want := "Current session: 30% used · resets 2pm (Asia/Shanghai)\nCurrent week (all models): 17% used · resets Nov 8 at 4am (Asia/Shanghai)\nCurrent week (Fable): 0% used · resets Nov 8 at 4am (Asia/Shanghai)"
	if r := out[1]; r["type"] != "result" || r["is_error"] != false || r["result"] != want {
		t.Fatalf("told %v", r)
	}
}

// Claude Code draws its banner again before /usage's panel, and a slow
// /usage leaves it still for seconds. It is waited past, never told as the
// panel: read as one, the banner was an allowance's error (ikunbots on X,
// v0.1.2738, "/usage told no allowance: ▘▘ ▝▝ /private/var/folders/…").
func TestShimWaitsPastTheBanner(t *testing.T) {
	panel, _ := filepath.Abs(filepath.Join("testdata", "usage-2.1.316.bin"))
	out := runShim(t, []string{"FAKE_USAGE=" + panel, "FAKE_USAGE_BANNER=4500"}, append([]string{fakePath(t)}, pArgs...), []string{`raw:{"type":"command","command":"/usage"}`}, 2)
	want := "Current session: 0% used · resets 12am (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"
	if r := out[1]; r["type"] != "result" || r["is_error"] != false || r["result"] != want {
		t.Fatalf("told %v", r)
	}
}

func TestUsageDrawn(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "usage-2.1.316.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !usageDrawn(ansi.ReplaceAllString(string(b), " ")) {
		t.Fatal("2.1.316's panel isn't taken as drawn")
	}
	for screen, want := range map[string]bool{
		"▐▛███▜▌ Claude Code v2.1.316 ▘▘ ▝▝ /private/var/folders/3d/x/T/magpie-claude-work-501 ──── ❯ ────": false,
		"Settings Status Config Usage Stats Loading usage…":               false,
		"Settings Usage /usage is only available for subscription plans.": true,
	} {
		if got := usageDrawn(screen); got != want {
			t.Errorf("usageDrawn(%q) = %v, want %v", screen, got, want)
		}
	}
}

// Windows' pseudo console (Seagull fries, Claude Code 2.1.316 on Windows
// 11): /usage's panel is drawn "Loading usage data…" while Claude Code
// asks for the figures, and an answer slower than 3s had the panel's
// frame told as the figures ("❯ /usage ─── Settin s Status Config Usage
// …"), which the card read as an account with no allowance. A panel that
// failed to load ("Error: Failed to load usage data", "r to retry") was
// told the same way. The figures are waited for, a failure retried once,
// and a panel still with no figures told as an error. The first frames
// are the bytes the pseudo console passed on for a real 2.1.316 in a
// sandbox (testdata/usage-*-win-2.1.316.bin).
func TestShimWaitsForUsageFigures(t *testing.T) {
	panel, _ := filepath.Abs(filepath.Join("testdata", "usage-2.1.316.bin"))
	loading, _ := filepath.Abs(filepath.Join("testdata", "usage-loading-win-2.1.316.bin"))
	failed, _ := filepath.Abs(filepath.Join("testdata", "usage-failed-win-2.1.316.bin"))
	want := "Current session: 0% used · resets 12am (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"
	for _, c := range []struct{ name, first, then, err string }{
		{"slow", loading, "slow", ""},
		{"retried", failed, "retry", ""},
		{"failed again", failed, "retry-fails", "/usage couldn't load the plan's usage: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := runShim(t, []string{"FAKE_USAGE=" + panel, "FAKE_USAGE_FIRST=" + c.first, "FAKE_USAGE_THEN=" + c.then},
				append([]string{fakePath(t)}, pArgs...), []string{`raw:{"type":"command","command":"/usage"}`}, 2)
			r := out[1]
			if c.err == "" {
				if r["type"] != "result" || r["is_error"] != false || r["result"] != want {
					t.Fatalf("told %v", r)
				}
				return
			}
			got, _ := r["result"].(string)
			if r["type"] != "result" || r["is_error"] != true || !strings.HasPrefix(got, c.err) || !strings.Contains(got, "Failed to load usage data") {
				t.Fatalf("told %v", r)
			}
		})
	}
}

// /usage's panel draws the figures Claude Code saved on its last read
// (cachedUsageUtilization, kept an hour) at once, "Refreshing…" under
// them, while it asks for today's, and "Showing last-known usage as of
// 20m ago (rate limited until 12:11am)" once it couldn't. Both were told
// as the account's figures, read now: the card showed the read before,
// up to an hour old (jsc9495: 92% left where Claude's usage page said
// 53%). The first frames are the bytes a real Claude Code 2.1.316 drew in
// a sandbox whose saved figures were 20 minutes old, its sign-in refused
// (testdata/usage-refreshing-2.1.316.bin, usage-last-known-2.1.316.bin).
// The refreshed panel is the earlier capture, drawn on a cleared screen.
func TestShimWaitsPastSavedUsage(t *testing.T) {
	refreshing, _ := filepath.Abs(filepath.Join("testdata", "usage-refreshing-2.1.316.bin"))
	lastKnown, _ := filepath.Abs(filepath.Join("testdata", "usage-last-known-2.1.316.bin"))
	b, err := os.ReadFile(filepath.Join("testdata", "usage-2.1.316.bin"))
	if err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(t.TempDir(), "usage-refreshed.bin")
	if err := os.WriteFile(fresh, append([]byte("\x1b[2J\x1b[H"), b...), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("refreshed", func(t *testing.T) {
		out := runShim(t, []string{"FAKE_USAGE=" + fresh, "FAKE_USAGE_FIRST=" + refreshing, "FAKE_USAGE_THEN=slow"},
			append([]string{fakePath(t)}, pArgs...), []string{`raw:{"type":"command","command":"/usage"}`}, 2)
		want := "Current session: 0% used · resets 12am (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"
		if r := out[1]; r["type"] != "result" || r["is_error"] != false || r["result"] != want {
			t.Fatalf("told %v", r)
		}
	})
	t.Run("last known", func(t *testing.T) {
		out := runShim(t, []string{"FAKE_USAGE=" + lastKnown},
			append([]string{fakePath(t)}, pArgs...), []string{`raw:{"type":"command","command":"/usage"}`}, 2)
		r := out[1]
		want := "/usage couldn't load the plan's usage: Showing last-known usage as of 20m ago (rate limited until 12:11am)"
		if r["type"] != "result" || r["is_error"] != true || r["result"] != want {
			t.Fatalf("told %v", r)
		}
	})
}

// A command other than /usage is told as an error, and none is typed.
func TestShimRunsNoOtherCommand(t *testing.T) {
	out := runShim(t, nil, append([]string{fakePath(t)}, pArgs...), []string{`raw:{"type":"command","command":"/login"}`}, 2)
	if r := out[1]; r["type"] != "result" || r["is_error"] != true || !strings.Contains(r["result"].(string), "/login") {
		t.Fatalf("told %v", r)
	}
}

func TestUsageText(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "usage-2.1.316.bin"))
	if err != nil {
		t.Fatal(err)
	}
	want := "Current session: 0% used · resets 12am (Asia/Shanghai)\nCurrent week (all models): 81% used · resets Nov 4 at 6am (Asia/Shanghai)"
	if got := usageText(ansi.ReplaceAllString(string(b), " ")); got != want {
		t.Fatalf("usageText = %q, want %q", got, want)
	}
	for screen, want := range map[string]string{
		// a window drawn again is told as drawn last, the Sonnet week its own
		"Current session ██ 10% used Resets 7pm (Asia/Shanghai) Current session ███ 12% used Resets 7pm (Asia/Shanghai) Current week (Sonnet only) █ 4% used Resets Nov 4 at 6am (UTC)": "Current session: 12% used · resets 7pm (Asia/Shanghai)\nCurrent week (Sonnet only): 4% used · resets Nov 4 at 6am (UTC)",
		// with no reset drawn
		"Current session  0% used": "Current session: 0% used",
		"Current session ████ 100% used Resets 7pm (UTC) You are using your overages": "Current session: 100% used · resets 7pm (UTC)\nYou are currently using your overages to power your Claude Code usage.",
		// no window: told as drawn, for its words to say why
		"Settings  Usage   /usage is only available for subscription plans.": "Settings Usage /usage is only available for subscription plans.",
	} {
		if got := usageText(screen); got != want {
			t.Errorf("usageText(%q) = %q, want %q", screen, got, want)
		}
	}
}

// A long paste is waited for as long as Claude Code takes it in: a real
// 3MB one showed "[Pasted text" after 35s, where pasteWait alone would
// press Enter while the paste was still being taken in.
func TestShownWaitGrowsWithThePaste(t *testing.T) {
	if got := shownWait(100); got != pasteWait {
		t.Fatalf("a short paste: %v", got)
	}
	if got := shownWait(3 << 20); got < 40*time.Second {
		t.Fatalf("a 3MB paste waits %v", got)
	}
}

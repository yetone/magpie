package appdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// what the reporter of #7812 found holding .config\magpie: an agent's MCP
// servers from mcp-npm, the free-model runner's node, the plugin host
func TestHolding(t *testing.T) {
	cfg := `C:\Users\u\.config\magpie`
	ps := []Proc{
		{PID: 1, PPID: 0, Args: `C:\Windows\explorer.exe`},
		{PID: 10, PPID: 1, Args: `"C:\Users\u\AppData\Local\Programs\magpie\magpie.exe"`}, // self
		{PID: 20, PPID: 1, Args: `C:\Users\u\.local\bin\claude.exe`},
		{PID: 21, PPID: 20, Args: `node C:\Users\u\.config\magpie\mcp-npm\ab12\node_modules\figma-developer-mcp\dist\cli.js --stdio`},
		{PID: 22, PPID: 20, Args: `node c:/users/u/.config/magpie/mcp-npm/cd34/node_modules/@scope/devtools-mcp/index.js`},
		{PID: 30, PPID: 10, Args: `"C:\Users\u\.config\magpie\our-free-model\runtime\node-v24.21.0-win32-x64\node.exe" runner.js`},
		{PID: 40, PPID: 1, Args: `node C:\Users\u\.config\magpie-old\x.js`}, // another folder
		{PID: 41, PPID: 1, Args: `notepad.exe`},
		{PID: 42, PPID: 1, Args: ``},
	}
	hs := Holding(ps, []string{cfg + `\`}, 10)
	var got []string
	for _, h := range hs {
		got = append(got, h.String())
		if h.Ours != (h.PID == 30) {
			t.Errorf("%d: Ours = %v", h.PID, h.Ours)
		}
	}
	want := []string{
		"figma-developer-mcp (started by claude.exe), pid 21",
		"@scope/devtools-mcp (started by claude.exe), pid 22",
		"node.exe (started by magpie.exe), pid 30",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("holders:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if o := notOurs(hs); len(o) != 2 || o[0].PID != 21 || o[1].PID != 22 {
		t.Fatalf("notOurs = %+v", o)
	}
}

// heldBy has HeldBy list one MCP server an agent started from magpie's
// folder, under a parent that isn't this magpie unless ours.
func heldBy(t *testing.T, cfg string, ours bool) {
	t.Helper()
	parent := 999999
	if ours {
		parent = os.Getpid()
	}
	old, oldWait := HeldBy, heldWait
	HeldBy = func() ([]Proc, error) {
		return []Proc{
			{PID: 999999, PPID: 1, Args: "claude"},
			{PID: 1000000, PPID: parent, Args: "node " + filepath.Join(cfg, "mcp-npm", "ab", "node_modules", "figma-developer-mcp", "cli.js")},
		}, nil
	}
	heldWait = 0
	t.Cleanup(func() { HeldBy, heldWait = old, oldWait })
}

// Windows refuses to rename a folder something has a file in open: the
// move stops before a byte is copied, names what holds it, and leaves
// everything where it was (#7812: the copy was made, then the rename at
// its end refused, and a half copy was left on D:).
func TestMoveHeldCopiesNothing(t *testing.T) {
	cfg, cache, disk := moveHome(t)
	oldR, oldP := rename, probeRename
	rename = func(string, string) error { return errors.New("cross-device link") }
	denied := errors.New("Access is denied.")
	probeRename = func(from, to string) error {
		if from == cfg {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: denied}
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { rename, probeRename = oldR, oldP })
	root, err := Target(disk)
	if err != nil {
		t.Fatal(err)
	}
	heldBy(t, cfg, false)
	err = Move(root)
	var he *HeldError
	if !errors.As(err, &he) || !errors.Is(err, denied) {
		t.Fatalf("Move = %v, want a HeldError", err)
	}
	if !strings.Contains(err.Error(), "figma-developer-mcp (started by claude), pid 1000000") {
		t.Fatalf("the error doesn't name what holds the folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "config")); err == nil {
		t.Fatal("files were copied to the other disk before the folder was known to move")
	}
	if _, ok := linkOf(cfg); ok {
		t.Fatal("magpie's folder is a link after a refused move")
	}
	checkFiles(t, cfg, cache)
	if _, err := os.Stat(cfg + checkSuffix); err == nil {
		t.Fatal("the check's folder is left")
	}

	// and from MovePending, the error is kept for Settings
	if _, err := AskMove(disk); err == nil {
		t.Fatal("asked to move while an agent's server runs from the folder")
	}
	HeldBy = nil
	if _, err := AskMove(disk); err != nil {
		t.Fatal(err)
	}
	if moved, err := MovePending(); moved || !errors.As(err, &he) {
		t.Fatalf("MovePending = %v, %v", moved, err)
	}
	if e := Where().Error; !strings.Contains(e, "can't be moved while something has a file in it open") {
		t.Fatalf("Settings reads %q", e)
	}
	checkFiles(t, cfg, cache)
}

// Asking for a move refuses at once what won't end when magpie quits (an
// agent's MCP server), so the user hears it before magpie restarts; what
// runs under this magpie ends with it, and doesn't stop the ask.
func TestAskMoveRefusesWhatOutlivesMagpie(t *testing.T) {
	cfg, _, disk := moveHome(t)
	heldBy(t, cfg, false)
	_, err := AskMove(disk)
	var he *HeldError
	if !errors.As(err, &he) || !strings.Contains(err.Error(), "figma-developer-mcp") {
		t.Fatalf("AskMove = %v", err)
	}
	if MoveAsked() {
		t.Fatal("a move was asked for anyway")
	}
	heldBy(t, cfg, true)
	if _, err := AskMove(disk); err != nil {
		t.Fatalf("refused for a process of magpie's own: %v", err)
	}
	if !MoveAsked() {
		t.Fatal("no move asked for")
	}
}

// A magpie that died between the check's two renames finds its folder
// again at the next start, never an empty one.
func TestCheckLeftoverPutBack(t *testing.T) {
	cfg, cache, _ := moveHome(t)
	if err := os.Rename(cfg, cfg+checkSuffix); err != nil {
		t.Fatal(err)
	}
	if err := Unreachable(); err != nil {
		t.Fatal(err)
	}
	checkFiles(t, cfg, cache)
}

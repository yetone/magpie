package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/codexrelay"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// magpie data: where magpie keeps its files, and moving them to another
// disk (CHENGJIAHEGOXIS and buhaodeshiyehao on X; appdir/move.go). A move
// is made at once when no magpie serves the gateway, and otherwise at the
// next start of the app, which Settings › Sync's Data folder restarts
// it for.
func dataCmd(args []string) error {
	if len(args) == 0 || args[0] == "where" {
		p := appdir.Where()
		fmt.Println("files:", p.Config)
		if p.ConfigAt != "" {
			fmt.Println("       →", p.ConfigAt)
		}
		fmt.Println("cache:", p.Cache)
		if p.CacheAt != "" {
			fmt.Println("       →", p.CacheAt)
		}
		if p.Portable {
			fmt.Println("portable: they move with magpie's data folder")
		}
		switch p.Pending {
		case "":
		case "home":
			fmt.Println("moving back to the default folders when magpie next starts")
		default:
			fmt.Println("moving to", p.Pending, "when magpie next starts")
		}
		if p.Error != "" {
			fmt.Println("the last move wasn't made:", p.Error)
		}
		return appdir.Unreachable()
	}
	switch args[0] {
	case "move":
		if len(args) != 2 || strings.HasPrefix(args[1], "-") && args[1] != "--home" {
			return errors.New("usage: magpie data move <folder> | --home")
		}
		dir := args[1]
		if dir == "--home" {
			dir = ""
		} else if a, err := filepath.Abs(dir); err == nil {
			dir = a
		}
		if gatewayUp() {
			root, err := appdir.AskMove(dir)
			if err != nil {
				return err
			}
			fmt.Printf("magpie is running: its files move to %s when it next starts (quit it and open it again)\n", homeOr(root))
			return nil
		}
		root, err := appdir.Target(dir)
		if err != nil {
			return err
		}
		appdir.CancelMove()
		if err := appdir.Move(root); err != nil {
			return err
		}
		fmt.Println(green.Render("●"), "magpie's files are in", homeOr(root), "now")
		return nil
	case "cancel":
		return appdir.CancelMove()
	case "-h", "--help", "help":
		fmt.Print(dataUsage)
		return nil
	}
	return fmt.Errorf("unknown data command %q\n%s", args[0], dataUsage)
}

const dataUsage = `magpie data                     where magpie keeps its files and its cache
magpie data move <folder>       move them into <folder>/magpie (another disk), leaving a link where they were
magpie data move --home         move them back
magpie data cancel              forget a move asked for while magpie was running
`

func homeOr(root string) string {
	if root == "" {
		return appdir.Where().Config
	}
	return root
}

// gatewayUp says whether a magpie serves the gateway: one running holds
// files in the folders a move would take away.
var gatewayUp = func() bool {
	codexrelay.Leave() // what a quitting magpie left at the port lets go of it
	return gateway.ServedBy().Running
}

// movePending makes the move Settings asked for, as the app starts again
// for it: after the magpie it replaces has exited, and before anything is
// read or written. With another magpie still serving the gateway (magpie
// serve, a launch agent) the move waits for a start without one.
func movePending() {
	if !appdir.MoveAsked() {
		return
	}
	update.AwaitPredecessor()
	if gatewayUp() {
		fmt.Fprintln(os.Stderr, "magpie: its folders move when no other magpie is running")
		return
	}
	if _, err := appdir.MovePending(); err != nil {
		fmt.Fprintln(os.Stderr, "magpie: its folders weren't moved:", err)
	}
}

// unreachableStop ends magpie when its folders were moved to a disk that
// isn't connected, saying so in a box for the app, which has no terminal.
// magpie data still answers, to say where they are.
func unreachableStop(args []string) {
	err := appdir.Unreachable()
	if err == nil || len(args) > 0 && args[0] == "data" {
		return
	}
	fmt.Fprintln(os.Stderr, "magpie:", err)
	if hasGUI && (len(args) == 0 || servesPages(args) && args[0] != "serve" && args[0] != "tui" && args[0] != "web") {
		alert("magpie", err.Error())
	}
	os.Exit(1)
}

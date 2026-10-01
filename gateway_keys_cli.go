package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/yetone/magpie/internal/access"
)

func gatewayKeys(args []string) error { return gatewayKeysTo(os.Stdout, args) }

func gatewayKeysTo(out io.Writer, args []string) error {
	args = args[1:]
	if len(args) == 0 {
		args = []string{"list"}
	}
	action := args[0]
	if (action == "list" && len(args) != 1) || (action != "list" && len(args) != 2) {
		return fmt.Errorf("usage: magpie gateway-key list | add <name> | rotate <id> | remove <id>")
	}
	switch action {
	case "list", "add", "rotate", "remove":
	default:
		return fmt.Errorf("unknown gateway-key command %q", action)
	}
	access.MigrateLegacyLANKeyBestEffort()
	if action == "list" {
		keys, err := access.List()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATE\tGATEWAY KEY")
		for _, k := range keys {
			state := "enabled"
			if k.Off {
				state = "disabled"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.ID, strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return ' '
				}
				return r
			}, k.Name), state, k.Masked)
		}
		return w.Flush()
	}
	in := access.Change{Key: args[1]}
	if action == "add" {
		in = access.Change{Name: args[1]}
	}
	secret, err := access.Update(action+"-key", in)
	if err != nil {
		return err
	}
	if secret != "" {
		_, err = fmt.Fprintln(out, secret)
	} else {
		_, err = fmt.Fprintln(out, "Gateway key removed")
	}
	return err
}

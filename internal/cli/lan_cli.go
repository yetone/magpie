package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

const lanUsage = `usage: magpie lan [on|off]
  Settings › Network and sharing › Share on local network, from a terminal (rael_tao
  on X: a server's gateway listened on [::] after sharing was turned off).
    magpie lan          whether the gateway is shared, and where it listens
    magpie lan on       other computers on this network reach it, with a gateway key
    magpie lan off      only this computer reaches it: it listens on 127.0.0.1
  The magpie serving the gateway moves at once. MAGPIE_ADDR, when set, is where it
  listens whatever is set here; in Docker, publish the port as -p 127.0.0.1:3425:3425
  to keep it on the host.`

func lanCmd(args []string) error {
	if len(args) > 1 {
		return errors.New(lanUsage)
	}
	if len(args) == 1 {
		switch args[0] {
		case "on", "off":
			if err := access.ConfigureLAN(args[0] == "on", false); err != nil {
				return err
			}
		case "status":
		default:
			return fmt.Errorf("unknown %q\n%s", args[0], lanUsage)
		}
	}
	return lanStatus()
}

func lanStatus() error {
	on := settings.Load().LAN
	if on {
		fmt.Println("Share on local network: on")
	} else {
		fmt.Println("Share on local network: off")
	}
	env := os.Getenv("MAGPIE_ADDR")
	if env != "" && gateway.OnNetwork() {
		fmt.Printf("MAGPIE_ADDR=%s has the gateway listen on the network whatever is set here: set it to 127.0.0.1:%s, or unset it, and restart magpie to keep the gateway on this computer\n", env, gateway.Port())
	}
	if l, ok := gateway.ReadListening(); ok {
		fmt.Printf("the gateway listens on %s (%s, pid %d)\n", l.Addr, l.Serves, l.Pid)
	} else {
		fmt.Println("no magpie serves the gateway now")
	}
	return nil
}

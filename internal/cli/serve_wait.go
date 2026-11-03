package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/codexrelay"
	"github.com/yetone/magpie/internal/gateway"
)

// gatewayFreeEvery is how often magpie serve, waiting for the gateway's
// port (awaitGatewayFree), looks again: as often as the app does
// (gui.gatewayWatch). A var for tests.
var gatewayFreeEvery = 15 * time.Second

// plainServe says args are magpie serve's for the gateway alone: no
// --web, whose page shows the gateway another magpie serves and so starts
// beside it (gui.StartWeb), and nothing serveCmd refuses, which it says
// at once.
func plainServe(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--tls-cert" || a == "--tls-key":
			i++
		case strings.HasPrefix(a, "--tls-cert="), strings.HasPrefix(a, "--tls-key="):
		default:
			return false
		}
	}
	return true
}

// awaitGatewayFree returns once nothing holds the gateway's port, looking
// every so often while another magpie serves it (the app, a second
// service, the copy an update left finishing its requests) or another
// program holds it. magpie serve waits here before it moves or starts
// anything: started beside the app by the service (launchd's KeepAlive,
// systemd's Restart), it used to run the start-up moves, start the plugin
// host and fetch every account's models, fail to bind and exit 1, and be
// started again 5s later, 6,035 times in two days in #4938: serve.log at
// 279MB, and the logins lock held against the app's requests till they
// went 504. Waiting, it does nothing until the port is free, and then
// serves, as the app does beside a magpie serving (gui.watchGateway).
func awaitGatewayFree(every time.Duration) {
	said := false
	for {
		// the Codex relay a magpie quitting left at the port lets go of
		// it for this one, as for the app (codexrelay)
		codexrelay.Leave()
		o := gateway.ServedBy()
		if !o.Running && !gateway.Taken() {
			if said {
				fmt.Println(green.Render("●"), "the gateway's port is free: this magpie serves it now")
			}
			return
		}
		if !said {
			said = true
			fmt.Println(amber.Render("!"), standbyLine(o))
		}
		time.Sleep(every)
	}
}

// standbyLine says, once, why magpie serve waits: who has the port.
func standbyLine(o gateway.Served) string {
	who := "another program has " + gateway.URL()
	if o.Running {
		who = "another magpie serves the gateway on " + gateway.URL()
		var about []string
		if o.Serves != "" {
			about = append(about, o.Serves)
		}
		if o.Version != "" {
			about = append(about, o.Version)
		}
		if o.Pid != 0 {
			about = append(about, fmt.Sprintf("pid %d", o.Pid))
		}
		if len(about) > 0 {
			who += " (" + strings.Join(about, ", ") + ")"
		}
	}
	return who + ": this one serves it once that one stops, and does nothing till then"
}

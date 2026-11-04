package gui

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
)

// portJSON is what setting the gateway's port did: the port it is on, the
// agents whose configs were moved with it, and what one couldn't be.
type portJSON struct {
	Port  int      `json:"port"`
	Moved []string `json:"moved,omitempty"`
	Error string   `json:"error,omitempty"`
}

// portFree says whether the gateway could listen at addr: nothing else
// does. A var so tests can say a port is taken.
var portFree = func(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

// magpieOnPort says whether a magpie program of this user's listens on
// port: the one serving the gateway is then this computer's, whose port is
// set there. Not when the program listening is another one answering for
// a magpie inside a container (OrbStack Helper, docker-proxy) or none this
// user can see; when the processes can't be listed at all, it is taken to
// be one.
func magpieOnPort(port string) bool {
	n, _ := strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pids, err := proc.ListeningOn(ctx, n)
	if err != nil {
		return true
	}
	for _, pid := range pids {
		if pid == os.Getpid() {
			continue
		}
		if path, err := proc.Executable(pid); err == nil && proc.RunsMagpie(pid, path) {
			return true
		}
	}
	return false
}

// errPortTaken is a port another program listens on.
func errPortTaken(p int) error {
	return fmt.Errorf("port %d is in use by another program: pick another one", p)
}

// setPort moves the gateway to port p (0 for the default) — the port in
// Settings' Gateway address (Magic_zero on Discord: 3425 was taken, and a port of one's
// own is easier to tell apart): it checks nothing listens there, saves it,
// moves the gateway this magpie serves (or starts it, when the port it was
// on was taken), and then every agent connected to magpie, whose configs
// have the gateway's URL in them. A magpie of this computer's serving the
// gateway has it moved there; one out of this one's reach — in a container
// (leslie_luo on Discord: an old magpie in OrbStack held 3425, so the port
// could be changed neither there nor here), another user's — is left on
// its port, and this one serves on the new one.
func setPort(p int) (portJSON, error) {
	if err := settings.CheckPort(p); err != nil {
		return portJSON{}, err
	}
	if a := os.Getenv("MAGPIE_ADDR"); a != "" {
		return portJSON{}, fmt.Errorf("MAGPIE_ADDR=%s sets the gateway's address, which comes before Settings: unset it to set the port here", a)
	}
	want := p
	if want == 0 {
		want = settings.DefaultPort
	}
	cur := settings.Load()
	was := cur.Port
	if strconv.Itoa(want) == gateway.Port() {
		if was != p {
			cur.Port = p
			if err := settings.Save(cur); err != nil {
				return portJSON{}, err
			}
		}
		return portJSON{Port: want}, nil
	}
	gw := served.Load()
	if gw == nil && gateway.Running() && magpieOnPort(gateway.Port()) {
		return portJSON{}, fmt.Errorf("another magpie serves the gateway at %s: set its port there, or quit it first", gateway.URL())
	}
	host := "127.0.0.1"
	if cur.LAN {
		host = "0.0.0.0"
	}
	if err := portFree(net.JoinHostPort(host, strconv.Itoa(want))); err != nil {
		return portJSON{}, errPortTaken(want)
	}
	// the agents reaching the gateway where it is now, to move with it
	on := agent.OnGateway()
	cur.Port = p
	if err := settings.Save(cur); err != nil {
		return portJSON{}, err
	}
	if gw != nil {
		if err := gw.Relisten(); err != nil {
			back := settings.Load()
			back.Port = was
			_ = settings.Save(back)
			return portJSON{}, errPortTaken(want)
		}
	} else {
		// the port it was on was taken by something else: it serves now
		serveGateway()
	}
	out := portJSON{Port: want}
	moved, err := agent.Rewire(on)
	out.Moved = moved
	if err != nil {
		out.Error = err.Error()
	}
	return out, nil
}

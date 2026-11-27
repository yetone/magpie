package gateway

import (
	"context"
	"log"
	"net"
	"strconv"

	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/filememo"
)

// listensAt is whether a listener at a is where to says: the same host and
// port, 0.0.0.0 and [::] alike (a dual-stack socket on every interface
// reads back as [::]), and any port when to asks for port 0.
func listensAt(a net.Addr, to string) bool {
	if ta, ok := a.(*net.TCPAddr); ok {
		if h, p, err := net.SplitHostPort(to); err == nil && p == "0" {
			to = net.JoinHostPort(h, strconv.Itoa(ta.Port))
		}
	}
	return sameAddr(a, to)
}

// followSavedAddr moves the gateway to where settings say it listens when
// they change outside this process, until ctx ends: Share on local network
// turned off in another magpie (the app while a service serves, the
// `magpie web` page, the tray's window) or in settings.json by hand left
// it on every interface until it was restarted (rael_tao on X). The
// process that changes the setting moves it at once when it serves
// (gui's settings/lan); this one follows on the change, or on the next
// look when changes can't be watched.
func (s *Server) followSavedAddr(ctx context.Context) {
	written := filememo.Written()
	failed := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.Next(filememo.OthersEvery()):
		case <-written:
		}
		// subscribed before reading, so a write during the look wakes it again
		written = filememo.Written()
		// handing over (handover.go): the socket is the next magpie's
		if s.notAccepting.Load() {
			continue
		}
		s.lnMu.Lock()
		ln := s.ln
		s.lnMu.Unlock()
		to := listenAddr()
		if ln == nil || listensAt(ln.Addr(), to) {
			failed = ""
			continue
		}
		if err := s.Relisten(); err != nil {
			// said once a place, not on every look while it stays taken
			if failed != to {
				log.Printf("gateway: can't move to %s as settings say: %v", to, err)
			}
			failed = to
			continue
		}
		failed = ""
		log.Printf("gateway: listening on %s, as settings say now", to)
	}
}

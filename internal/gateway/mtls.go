package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/mtls"
	"github.com/yetone/magpie/internal/settings"
)

// mTLS (hhdaxia on X, #5596): with Settings → Network and sharing →
// Require a client certificate on (settings MTLS, `magpie mtls on`), other
// computers reach the gateway and magpie web's page only over TLS, showing
// a client certificate magpie's own CA issued (`magpie mtls issue`) and
// hasn't revoked:
//
//   - the TLS listener (tlsListener) asks every peer that isn't this
//     computer, and isn't a WSL distro's, for one, and ends the handshake
//     of a peer without a good one before any HTTP is read;
//   - the guards (lanGuard, ClientCertGuard) refuse 403 every request from
//     another machine that came without a verified one: a proxy's or
//     tunnel's on loopback (proxied), magpie web's page port, the WSL
//     listener's plain HTTP from a peer that isn't WSL's, a connection
//     made before the switch was turned on, or a certificate revoked
//     since its connection's handshake.
//
// This computer — loopback, and this computer calling its own address —
// and a WSL distro's agent with the WSL key (fromWSL) are answered as
// before, over plain HTTP too. Without --tls-cert the server certificate
// is the CA's own, made in memory for the names the client dials
// (ownServerCert); with --tls-cert it is that one, and the CA is used for
// the client certificates alone.

// MTLSOn: other computers need a client certificate magpie issued.
func MTLSOn() bool { return settings.Shared().MTLS }

// MTLSStore is where magpie's CA and the client certificates it issued are
// kept.
func MTLSStore() mtls.Store { return mtls.Store{Dir: filepath.Join(settings.Dir(), "mtls")} }

// clientCertAdmitted: r came over TLS with a client certificate magpie's
// CA verified at the handshake, which is still on the issued list.
func clientCertAdmitted(r *http.Request) bool {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	return MTLSStore().Admits(r.TLS.PeerCertificates[0]) == nil
}

// ownAddress: r is this computer calling one of its own addresses (the
// kernel answers it without the network), not through a proxy.
func ownAddress(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	tcp, ok := la.(*net.TCPAddr)
	return ip != nil && ok && tcp.IP.Equal(ip) && !proxied(r) && !viaPagePort(r)
}

// mtlsRefuses says why r, with mTLS on, is refused: "" when it is let
// through (mTLS off, a good certificate, or from this computer or WSL).
// Whether r would be let through is asked first, so this computer's own
// requests never wait on settings.json for the switch.
func mtlsRefuses(r *http.Request) string {
	why := NeedsClientCert(r)
	if why == "" || !MTLSOn() {
		return ""
	}
	return why
}

// NeedsClientCert is why r would be refused with mTLS on, "" when it
// would be let through: the page turning it on is asked, so it never
// shuts out the computer it is open on.
func NeedsClientCert(r *http.Request) string {
	if clientCertAdmitted(r) || local(r) || ownAddress(r) || fromWSL(takeWSLPathKey(r)) {
		return ""
	}
	why := "magpie asks other computers for a client certificate it issued (Settings → Network and sharing → Require a client certificate), and this request came without a good one"
	switch {
	case r.TLS == nil && proxied(r):
		why += ": it came through a proxy or tunnel, which can't pass a certificate on; reach magpie directly over https://, or have the proxy check client certificates itself and set MAGPIE_TRUST_PROXY=1"
	case r.TLS == nil:
		why += ": it came over plain HTTP; call magpie at https://"
	case len(r.TLS.PeerCertificates) > 0:
		why += ": the certificate shown was revoked or isn't one this magpie issued"
	default:
		why += ": issue one with `magpie mtls issue <device>` and give it to the client"
	}
	return why
}

// refuseWithoutClientCert answers a gateway request mtlsRefuses, in the
// error shape agents show; true when it did.
func refuseWithoutClientCert(w http.ResponseWriter, r *http.Request) bool {
	why := mtlsRefuses(r)
	if why == "" {
		return false
	}
	log.Printf("refused %s %s from %s: no good client certificate", r.Method, r.URL.Path, r.RemoteAddr)
	http.Error(w, why, http.StatusForbidden)
	return true
}

// ClientCertGuard refuses a request mtlsRefuses, for a server other than
// the gateway's own (magpie web's page).
func ClientCertGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuseWithoutClientCert(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tlsServer is what a listener serves TLS with: the files --tls-cert
// names, or with mTLS on and none named, a certificate magpie's CA signs.
// It is asked for each connection, so turning mTLS on or off takes effect
// for the next one without listening again.
type tlsServer struct {
	files *certFiles // nil without --tls-cert
}

// config is the TLS config for a connection, nil to serve it plain HTTP
// (no --tls-cert and mTLS off). strict asks for a client certificate and
// ends a handshake without one, for a peer that isn't this computer or a
// WSL distro.
func (t *tlsServer) config(strict bool) *tls.Config {
	on := MTLSOn()
	if t == nil || (t.files == nil && !on) {
		return nil
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// HTTP/1.1 alone, as over plain HTTP: Codex's websocket and the
		// streams' handling hijack the connection
		NextProtos: []string{"http/1.1"},
	}
	if t.files != nil {
		cfg.GetCertificate = t.files.get
	} else {
		cfg.GetCertificate = ownServerCert
	}
	if !on {
		return cfg
	}
	st := MTLSStore()
	pool, err := mtlsPool(st)
	if err != nil {
		log.Printf("mTLS: magpie's CA can't be read (%v): no client certificate is taken", err)
		pool = x509.NewCertPool()
	}
	cfg.ClientCAs = pool
	cfg.ClientAuth = tls.VerifyClientCertIfGiven
	if strict {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return nil // judged by the guards, for a lenient peer
		}
		return st.Admits(cs.PeerCertificates[0])
	}
	return cfg
}

var poolCache struct {
	sync.Mutex
	path  string
	stamp fileStamp
	pool  *x509.CertPool
}

// mtlsPool is the store's CA as a pool, read again when ca.pem changes.
func mtlsPool(st mtls.Store) (*x509.CertPool, error) {
	poolCache.Lock()
	defer poolCache.Unlock()
	now := stampOf(st.CAFile())
	if poolCache.pool != nil && poolCache.path == st.CAFile() && poolCache.stamp == now && now != (fileStamp{}) {
		return poolCache.pool, nil
	}
	p, err := st.Pool()
	if err != nil {
		return nil, err
	}
	poolCache.path, poolCache.stamp, poolCache.pool = st.CAFile(), stampOf(st.CAFile()), p
	return p, nil
}

var ownCerts struct {
	sync.Mutex
	key   string
	stamp fileStamp
	made  time.Time
	cert  *tls.Certificate
}

// ownServerCert is a server certificate magpie's CA signs for this
// computer's names and addresses and the name the client dialled (a DDNS
// or Tailscale name): only a client given the CA trusts it, so the name a
// client asks for is the one it gets. Made in memory, and again when the
// names, the addresses or the CA change, or after a day.
func ownServerCert(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	names := []string{"localhost"}
	if h, err := os.Hostname(); err == nil && h != "" {
		h = strings.TrimSuffix(strings.ToLower(h), ".")
		names = append(names, h)
		if !strings.Contains(h, ".") {
			names = append(names, h+".local")
		}
	}
	if hello != nil && hello.ServerName != "" && net.ParseIP(hello.ServerName) == nil {
		names = append(names, strings.ToLower(hello.ServerName))
	}
	slices.Sort(names)
	names = slices.Compact(names)
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
				ips = append(ips, n.IP)
			}
		}
	}
	var key strings.Builder
	key.WriteString(strings.Join(names, ","))
	for _, ip := range ips {
		key.WriteString("|" + ip.String())
	}
	st := MTLSStore()
	ownCerts.Lock()
	defer ownCerts.Unlock()
	stamp := stampOf(st.CAFile())
	if ownCerts.cert != nil && ownCerts.key == key.String() && ownCerts.stamp == stamp && time.Since(ownCerts.made) < 24*time.Hour {
		return ownCerts.cert, nil
	}
	c, err := st.ServerCert(names, ips)
	if err != nil {
		return nil, errors.New("mTLS: magpie's CA can't sign a server certificate: " + err.Error())
	}
	ownCerts.key, ownCerts.stamp, ownCerts.made, ownCerts.cert = key.String(), stampOf(st.CAFile()), time.Now(), &c
	return &c, nil
}

// fromWSLNet: c's peer is on one of WSL's networks, at the address
// Windows has there (a distro's agent, judged by its key in fromWSL).
func fromWSLNet(c net.Conn) bool {
	r, ok := c.RemoteAddr().(*net.TCPAddr)
	l, ok2 := c.LocalAddr().(*net.TCPAddr)
	if !ok || !ok2 {
		return false
	}
	return slices.ContainsFunc(WSLNets(), func(n *net.IPNet) bool { return n.IP.Equal(l.IP) && n.Contains(r.IP) })
}

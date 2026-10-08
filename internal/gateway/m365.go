package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	M365Origin      = "https://pivot.claude.ai"
	M365DefaultPort = 8787
)

// m365ListenAddr is a var so tests take an ephemeral port. The product's
// endpoint is fixed and predictable for the Office add-in.
var m365ListenAddr = fmt.Sprintf("127.0.0.1:%d", M365DefaultPort)

// M365Status is the second, loopback-only HTTPS endpoint Claude for
// Microsoft 365 calls. It serves the same Handler as the ordinary gateway.
type M365Status struct {
	Running     bool                  `json:"running"`
	URL         string                `json:"url"`
	Error       string                `json:"error,omitempty"`
	Certificate M365CertificateStatus `json:"certificate"`
}

func M365URL() string { return "https://" + m365ListenAddr }

// M365Server owns only the TLS listener. Provider state and routing remain
// on Server, whose Handler is shared by both listeners.
type M365Server struct {
	mu     sync.Mutex
	server *http.Server
	ln     net.Listener
	err    string
}

func NewM365Server() *M365Server { return &M365Server{} }

func (m *M365Server) failed(err error) error {
	m.mu.Lock()
	m.err = err.Error()
	m.mu.Unlock()
	return err
}

func (m *M365Server) Status() M365Status {
	m.mu.Lock()
	running, last := m.ln != nil, m.err
	m.mu.Unlock()
	return M365Status{Running: running, URL: M365URL(), Error: last, Certificate: M365Certificate()}
}

// Start prepares the local certificate and serves handler over TLS. The CA
// isn't trusted here: that system change is a separate, explicit action.
func (m *M365Server) Start(ctx context.Context, handler http.Handler) error {
	f := m365CertificatePaths()
	if err := ensureM365Certificate(f); err != nil {
		return m.failed(err)
	}
	pair, err := tls.LoadX509KeyPair(f.serverCert, f.serverKey)
	if err != nil {
		return m.failed(fmt.Errorf("load Microsoft 365 certificate: %w", err))
	}
	ln, err := Listen(m365ListenAddr)
	if err != nil {
		return m.failed(fmt.Errorf("listen for Microsoft 365 at %s: %w", M365URL(), err))
	}
	srv := &http.Server{
		Handler: lanGuard(m365Only(handler)), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 5 * time.Minute,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
	}
	tlsLn := tls.NewListener(ln, srv.TLSConfig)
	m.mu.Lock()
	if m.ln != nil {
		m.mu.Unlock()
		ln.Close()
		return m.failed(errors.New("Microsoft 365 HTTPS gateway is already running"))
	}
	m.server, m.ln, m.err = srv, tlsLn, ""
	m.mu.Unlock()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.stop(c, srv)
	}()
	go func() {
		err := srv.Serve(tlsLn)
		m.mu.Lock()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.err = err.Error()
		}
		if m.server == srv {
			m.server, m.ln = nil, nil
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *M365Server) Stop(ctx context.Context) error {
	m.mu.Lock()
	srv := m.server
	m.mu.Unlock()
	return m.stop(ctx, srv)
}

// stop closes only the generation it was handed. A gateway restart can have
// started a replacement by the time the old context ends; the old one must
// never stop the new listener.
func (m *M365Server) stop(ctx context.Context, srv *http.Server) error {
	if srv == nil {
		return nil
	}
	m.mu.Lock()
	if m.server == srv {
		m.server, m.ln = nil, nil
	}
	m.mu.Unlock()
	return srv.Shutdown(ctx)
}

// m365Only keeps the Office-facing listener to the two Anthropic routes the
// add-in needs. OPTIONS reaches corsGuard inside handler for its preflight.
func m365Only(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := r.Method == http.MethodOptions && (r.URL.Path == "/v1/models" || r.URL.Path == "/v1/messages") ||
			r.Method == http.MethodGet && r.URL.Path == "/v1/models" ||
			r.Method == http.MethodPost && r.URL.Path == "/v1/messages"
		if !allowed {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

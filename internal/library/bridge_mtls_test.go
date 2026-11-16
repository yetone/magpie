package library

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mtls"
	"github.com/yetone/magpie/internal/provider"
)

// hhdaxia on X: a Remote magpie behind mTLS took the provider's client
// certificate for its model calls alone. The Library's server for its MCP
// router is checked, and used, through the same transport: with the
// certificate on the provider it answers; with none it is refused at the
// handshake, as before.
func TestRemoteMagpieRouterShowsClientCert(t *testing.T) {
	h := sandbox(t)
	st := mtls.Store{Dir: t.TempDir()}
	cert, err := st.ServerCert([]string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := st.Pool()
	var seen http.Header
	srv := httptest.NewUnstartedServer(mcpHandler(t, false, &seen))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	iss, err := st.Issue("office")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := mtls.Export(iss, h)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Name: "office", Transport: "http", URL: srv.URL + gateway.HubMCPAll, Bridge: "office", Headers: bridgeHeaders("remote-key")}
	// the server's CA alone: trusted, but no certificate to show
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "remote-key", Chat: srv.URL,
		ServerCA: filepath.Join(dir, "ca.pem")}); err != nil {
		t.Fatal(err)
	}
	if got := CheckServer(context.Background(), s); got.State == "ok" {
		t.Fatalf("without a client certificate: %+v", got)
	}
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "remote-key", Chat: srv.URL,
		ClientCert: filepath.Join(dir, "client.pem"), ClientKey: filepath.Join(dir, "client-key.pem"), ServerCA: filepath.Join(dir, "ca.pem")}); err != nil {
		t.Fatal(err)
	}
	if got := CheckServer(context.Background(), s); got.State != "ok" || got.Tools != 2 {
		t.Fatalf("with the provider's client certificate: %+v", got)
	}
}

package netproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/netproxy/sshtest"
	"github.com/yetone/magpie/internal/settings"
)

// sshVia gets target with the proxy choice, as a vendor request takes it.
func sshVia(t *testing.T, choice, target string) (string, error) {
	t.Helper()
	base := &http.Transport{Proxy: Func}
	defer base.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(With(context.Background(), choice), http.MethodGet, target, nil)
	res, err := Dispatch(base).RoundTrip(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b), nil
}

func saveProxies(t *testing.T, ps ...settings.NamedProxy) {
	t.Helper()
	if err := settings.Save(settings.Settings{Proxies: ps}); err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		id := p.ID
		t.Cleanup(func() { CloseSSH(id) })
	}
}

func savedProxy(t *testing.T, id string) settings.NamedProxy {
	t.Helper()
	for _, p := range settings.Load().Proxies {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no saved proxy %s", id)
	return settings.NamedProxy{}
}

// A saved SSH server carries a request as ssh -D would, with no ssh
// command: the user's host, user and password are all it takes
// (@laiuuui on X, whose VPS gives only SSH). The host key is trusted on
// first use and kept; the request reaches the vendor's name through the
// server, which resolves it (socks5h), and the login happens once.
func TestSSHProxyCarriesRequests(t *testing.T) {
	clearProxyEnv(t)
	srv := sshtest.Start(t, sshtest.Options{User: "root", Pass: "pw:@/ #1"})
	saveProxies(t, settings.NamedProxy{ID: "pxssh", Name: "VPS", URL: srv.URL, User: "root", Pass: "pw:@/ #1"})
	for range 2 {
		got, err := sshVia(t, "@pxssh", "http://vendor.example/v1/models")
		if err != nil || got != "carried vendor.example/v1/models" {
			t.Fatalf("through the SSH server: %q, %v", got, err)
		}
	}
	if got := srv.Targets(); len(got) == 0 || got[0] != "vendor.example:80" {
		t.Fatalf("the server was asked for %v, want vendor.example:80 by name", got)
	}
	if n := srv.Logins(); n != 1 {
		t.Fatalf("%d logins for two requests, want 1", n)
	}
	if hk := savedProxy(t, "pxssh").HostKey; hk != srv.HostKey {
		t.Fatalf("host key kept %q, want %q", hk, srv.HostKey)
	}
	// the address a request is shown with is the server, never the
	// tunnel's own login
	if got := Redact("@pxssh"); got != srv.URL {
		t.Fatalf("Redact(@pxssh) = %q, want %q", got, srv.URL)
	}
}

// A private key logs in as a password does, and one with a passphrase
// takes it from the password field.
func TestSSHProxyKeyLogin(t *testing.T) {
	clearProxyEnv(t)
	signer, pemKey := sshtest.NewKey(t)
	srv := sshtest.Start(t, sshtest.Options{User: "deploy", Key: signer.PublicKey()})
	saveProxies(t, settings.NamedProxy{ID: "pxkey", Name: "VPS", URL: srv.URL, User: "deploy", Key: pemKey})
	if got, err := sshVia(t, "@pxkey", "http://vendor.example/k"); err != nil || got != "carried vendor.example/k" {
		t.Fatalf("with a key: %q, %v", got, err)
	}
}

// A server whose host key isn't the trusted one is refused, and the
// failure says what changed; nothing goes through it.
func TestSSHProxyRefusesAChangedHostKey(t *testing.T) {
	clearProxyEnv(t)
	srv := sshtest.Start(t, sshtest.Options{User: "root", Pass: "pw"})
	other := sshtest.Start(t, sshtest.Options{User: "root", Pass: "pw"})
	p := settings.NamedProxy{ID: "pxmitm", Name: "VPS", URL: srv.URL, User: "root", Pass: "pw", HostKey: other.HostKey}
	saveProxies(t, p)
	if got, err := sshVia(t, "@pxmitm", "http://vendor.example/x"); err == nil {
		t.Fatalf("a changed host key carried the request: %q", got)
	}
	err := SSHReady(context.Background(), p)
	var changed *HostKeyChangedError
	var why *Why
	if !errors.As(err, &changed) || !errors.As(err, &why) || !strings.Contains(why.Why, "host key changed") || why.Now == "" || why.Was == why.Now {
		t.Fatalf("SSHReady = %v, want a host key change said", err)
	}
	if len(srv.Targets()) != 0 {
		t.Fatalf("a channel was opened through the server: %v", srv.Targets())
	}
	if hk := savedProxy(t, "pxmitm").HostKey; hk != other.HostKey {
		t.Fatalf("the trusted key was written over: %q", hk)
	}
}

// A refused login, a port with no server and a server that isn't SSH each
// say why, in words the page translates.
func TestSSHProxySaysWhyItFailed(t *testing.T) {
	clearProxyEnv(t)
	srv := sshtest.Start(t, sshtest.Options{User: "root", Pass: "pw"})
	pam := sshtest.Start(t, sshtest.Options{User: "root", Pass: "pw", Interactive: true})
	notSSH := fakeHTTPProxy(t, "u", "p")
	for _, c := range []struct {
		name string
		p    settings.NamedProxy
		want string
	}{
		{"wrong password", settings.NamedProxy{URL: srv.URL, User: "root", Pass: "nope"}, "turned the login away"},
		// a real Ubuntu sshd with UsePAM no answers keyboard-interactive
		// with 51 where the library waits for 60
		{"wrong password, keyboard-interactive refused", settings.NamedProxy{URL: pam.URL, User: "root", Pass: "nope"}, "turned the login away"},
		// port 1: a freed test port can be handed to anything next
		{"nothing listens", settings.NamedProxy{URL: "ssh://127.0.0.1:1", User: "root", Pass: "pw"}, "nothing answers SSH"},
		{"not SSH", settings.NamedProxy{URL: "ssh://" + strings.TrimPrefix(notSSH.URL, "http://"), User: "root", Pass: "pw"}, "doesn't speak SSH"},
		{"no login", settings.NamedProxy{URL: srv.URL, User: "root"}, "needs a password or a private key"},
		{"bad key", settings.NamedProxy{URL: srv.URL, User: "root", Key: "-----BEGIN OPENSSH PRIVATE KEY-----\nnope\n"}, "can't be read"},
	} {
		_, err := CheckSSH(context.Background(), c.p)
		var why *Why
		if !errors.As(err, &why) || !strings.Contains(why.Why, c.want) {
			t.Errorf("%s: %v, want a Why with %q", c.name, err, c.want)
		}
	}
}

// The tunnel's SOCKS end takes only magpie's own login: another program
// on this computer can't send through the user's server.
func TestSSHProxyLocalEndNeedsItsLogin(t *testing.T) {
	clearProxyEnv(t)
	srv := sshtest.Start(t, sshtest.Options{User: "root", Pass: "pw"})
	saveProxies(t, settings.NamedProxy{ID: "pxlocal", Name: "VPS", URL: srv.URL, User: "root", Pass: "pw"})
	addr, ok := settings.ResolveProxy("@pxlocal")
	u, err := url.Parse(addr)
	if !ok || err != nil || u.Scheme != "socks5h" || u.User == nil {
		t.Fatalf("resolved to %q (%v)", addr, err)
	}
	if c, err := DialSOCKS(context.Background(), u, "vendor.example:80"); err != nil {
		t.Fatalf("with its login: %v", err)
	} else {
		c.Close()
	}
	for _, who := range []*url.Userinfo{nil, url.UserPassword("magpie", "guess")} {
		bad := *u
		bad.User = who
		if c, err := DialSOCKS(context.Background(), &bad, "vendor.example:80"); err == nil {
			c.Close()
			t.Fatalf("the SOCKS end let %v in", who)
		}
	}
}

// An SSH server typed where an HTTP or SOCKS proxy goes says so, from
// the bytes a real check reads off it. A real OpenSSH's banner has a
// space, which Go reads as an HTTP status line with a bad code; the
// check then reads the server's first bytes.
func TestProxyFailureNamesAnSSHServer(t *testing.T) {
	clearProxyEnv(t)
	for _, version := range []string{"", "SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.20"} {
		srv := sshtest.Start(t, sshtest.Options{User: "root", Pass: "pw", Version: version})
		for _, scheme := range []string{"http", "socks5"} {
			addr := scheme + "://" + srv.Addr
			_, err := sshVia(t, addr, "http://vendor.example/x")
			if err == nil {
				t.Fatalf("%s: an SSH server carried a request", scheme)
			}
			w := ExplainServer(context.Background(), addr, err.Error())
			if w == nil || !strings.Contains(w.Why, "is an SSH server") || w.Addr != srv.Addr {
				t.Errorf("%s, banner %q: %q explained as %+v", scheme, version, err, w)
			}
		}
	}
	// an HTTP server that answers nonsense isn't called SSH
	junk := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		c, buf, _ := rw.(http.Hijacker).Hijack()
		buf.WriteString("HTTP/1.1 abc\r\n\r\n")
		buf.Flush()
		c.Close()
	}))
	t.Cleanup(junk.Close)
	addr := "http://" + strings.TrimPrefix(junk.URL, "http://")
	_, err := sshVia(t, addr, "http://vendor.example/x")
	if err == nil {
		t.Fatal("junk carried a request")
	}
	if w := ExplainServer(context.Background(), addr, err.Error()); w == nil || strings.Contains(w.Why, "SSH server") {
		t.Errorf("an HTTP server answering junk: %+v", w)
	}
}

// An ssh:// address can't be a choice by itself: its login goes with a
// saved proxy.
func TestInlineSSHAddressRefused(t *testing.T) {
	if err := settings.CheckProxy("ssh://203.0.113.5:22"); err == nil || !strings.Contains(err.Error(), "saved proxy") {
		t.Fatalf("CheckProxy(ssh://) = %v", err)
	}
}

// A backup without keys leaves an SSH proxy's password and private key
// out; a sync without keys keeps this computer's.
func TestSSHSecretsStayHome(t *testing.T) {
	p := settings.NamedProxy{ID: "px1", Name: "VPS", URL: "ssh://203.0.113.5:22", User: "root", Pass: "pw", Key: "KEY", HostKey: "ssh-ed25519 AAAA"}
	out := settings.WithoutProxyPasswords([]settings.NamedProxy{p})[0]
	if out.Pass != "" || out.Key != "" || out.HostKey != p.HostKey {
		t.Fatalf("stripped: %+v", out)
	}
	merged := settings.MergeProxies([]settings.NamedProxy{p}, []settings.NamedProxy{out}, false)[0]
	if merged.Pass != "pw" || merged.Key != "KEY" {
		t.Fatalf("a sync without keys lost them: %+v", merged)
	}
}

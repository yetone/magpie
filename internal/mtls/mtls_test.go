package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func leafOf(t *testing.T, b []byte) *x509.Certificate {
	t.Helper()
	p, _ := pem.Decode(b)
	if p == nil {
		t.Fatal("not PEM")
	}
	c, err := x509.ParseCertificate(p.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func verifies(t *testing.T, st Store, c *x509.Certificate) error {
	t.Helper()
	pool, err := st.Pool()
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}

// TestIssueAdmitsRevoke: a certificate issued verifies by the CA and is
// admitted; revoked, it is refused; issuing the name again revokes the one
// before and admits the new one alone.
func TestIssueAdmitsRevoke(t *testing.T) {
	st := Store{Dir: filepath.Join(t.TempDir(), "mtls")}
	a, err := st.Issue("laptop")
	if err != nil {
		t.Fatal(err)
	}
	ca := leafOf(t, a.CA)
	if !ca.IsCA {
		t.Fatal("ca.pem isn't a CA")
	}
	la := leafOf(t, a.Cert)
	if err := verifies(t, st, la); err != nil {
		t.Fatalf("issued certificate doesn't verify: %v", err)
	}
	if _, err := tls.X509KeyPair(a.Cert, a.Key); err != nil {
		t.Fatalf("cert and key don't pair: %v", err)
	}
	if err := st.Admits(la); err != nil {
		t.Fatalf("issued certificate refused: %v", err)
	}
	b, err := st.Issue("LAPTOP")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Admits(la); err == nil {
		t.Fatal("a name issued again still admits the certificate before")
	}
	lb := leafOf(t, b.Cert)
	if err := st.Admits(lb); err != nil {
		t.Fatalf("new certificate refused: %v", err)
	}
	if n, err := st.Revoke("laptop"); err != nil || n != 1 {
		t.Fatalf("revoke = %d, %v", n, err)
	}
	if err := st.Admits(lb); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked certificate: %v", err)
	}
	if _, err := st.Revoke("laptop"); err == nil {
		t.Fatal("revoking nothing in use is no error")
	}
	l, err := st.List()
	if err != nil || len(l) != 2 {
		t.Fatalf("list = %v, %v", l, err)
	}
	for _, c := range l {
		if c.Good(c.Issued) {
			t.Errorf("%s still good", c.Serial)
		}
	}
}

// TestRevokeBySerial: a serial names one certificate.
func TestRevokeBySerial(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	a, _ := st.Issue("a")
	b, _ := st.Issue("b")
	if n, err := st.Revoke(strings.ToUpper(a.Serial)); err != nil || n != 1 {
		t.Fatalf("revoke = %d, %v", n, err)
	}
	if st.Admits(leafOf(t, a.Cert)) == nil || st.Admits(leafOf(t, b.Cert)) != nil {
		t.Fatal("the serial revoked the wrong certificate")
	}
}

// TestAdmitsRefusesForeign: a certificate another CA signed, or this CA's
// own not on the list (the list lost or unreadable), is refused.
func TestAdmitsRefusesForeign(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	other := Store{Dir: t.TempDir()}
	st.Issue("mine")
	o, _ := other.Issue("theirs")
	lo := leafOf(t, o.Cert)
	if verifies(t, st, lo) == nil {
		t.Fatal("another CA's certificate verifies")
	}
	if st.Admits(lo) == nil {
		t.Fatal("another CA's certificate admitted")
	}
	m, _ := st.Issue("phone")
	lm := leafOf(t, m.Cert)
	if err := os.WriteFile(st.clientsFile(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Admits(lm); err == nil {
		t.Fatal("an unreadable list admits")
	}
	if _, err := st.Issue("x"); err == nil {
		t.Fatal("issue over an unreadable list wrote over it")
	}
	os.Remove(st.clientsFile())
	if err := st.Admits(lm); err == nil {
		t.Fatal("a lost list admits")
	}
}

// TestCAHalfMissing: a CA with its key or its certificate gone is an
// error, never made over (which would orphan every certificate issued).
func TestCAHalfMissing(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	if _, _, err := st.CA(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(st.caKeyFile()); fi.Mode().Perm() != 0o600 {
			t.Errorf("ca-key.pem mode %v", fi.Mode().Perm())
		}
	}
	os.Remove(st.caFile())
	if _, _, err := st.CA(); err == nil {
		t.Fatal("a CA without its certificate was made over")
	}
	if _, err := os.Stat(st.caFile()); err == nil {
		t.Fatal("ca.pem written again")
	}
}

func TestIssueName(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	for _, n := range []string{"", "  ", "a/b", `a\b`, "a:b", "a\nb"} {
		if _, err := st.Issue(n); err == nil {
			t.Errorf("%q issued", n)
		}
	}
}

// TestServerCert: signed by the CA for the names and addresses given.
func TestServerCert(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	c, err := st.ServerCert([]string{"localhost", "box.local"}, []net.IP{net.IPv4(192, 168, 1, 5)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := st.Pool()
	for _, name := range []string{"box.local", "192.168.1.5"} {
		if _, err := c.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := c.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "evil.example"}); err == nil {
		t.Error("signed for a name not given")
	}
}

// TestExport: three files, the key 0600, a second export beside the first.
func TestExport(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	out := t.TempDir()
	iss, _ := st.Issue("my phone")
	d1, err := Export(iss, out)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(d1) != "magpie-my-phone" {
		t.Fatalf("folder %s", d1)
	}
	d2, _ := Export(iss, out)
	if d2 == d1 {
		t.Fatal("second export over the first")
	}
	for _, f := range []string{"client.pem", "client-key.pem", "ca.pem"} {
		if _, err := os.Stat(filepath.Join(d1, f)); err != nil {
			t.Error(err)
		}
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(d1, "client.pem"), filepath.Join(d1, "client-key.pem")); err != nil {
		t.Error(err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(d1, "client-key.pem")); fi.Mode().Perm() != 0o600 {
			t.Errorf("key mode %v", fi.Mode().Perm())
		}
	}
}

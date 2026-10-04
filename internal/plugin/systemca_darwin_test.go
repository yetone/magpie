package plugin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func trustPlist(entries map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>trustList</key><dict>`)
	for h, settings := range entries {
		fmt.Fprintf(&b, `<key>%s</key><dict><key>issuerName</key><data>MBQxEjAQBgNVBAMMCVRlc3QgUm9vdA==</data><key>modDate</key><date>2026-10-04T09:00:00Z</date><key>trustSettings</key>%s</dict>`, h, settings)
	}
	b.WriteString(`</dict><key>trustVersion</key><integer>1</integer></dict></plist>`)
	return b.String()
}

const (
	sslTrust = `<array><dict><key>kSecTrustSettingsPolicy</key><data>KoZIhvdjZAED</data><key>kSecTrustSettingsPolicyName</key><string>sslServer</string><key>kSecTrustSettingsResult</key><integer>1</integer></dict></array>`
	deny     = `<array><dict><key>kSecTrustSettingsResult</key><integer>3</integer></dict></array>`
	unset    = `<array><dict><key>kSecTrustSettingsResult</key><integer>4</integer></dict></array>`
	noResult = `<array><dict><key>kSecTrustSettingsPolicyName</key><string>sslServer</string></dict></array>`
	allTrust = `<array/>`
)

func TestTrustedIn(t *testing.T) {
	got := trustedIn([]byte(trustPlist(map[string]string{
		"aa": allTrust, "BB": sslTrust, "CC": deny, "DD": unset, "EE": noResult,
	})))
	want := map[string]bool{"AA": true, "BB": true, "CC": false, "DD": false, "EE": true}
	for h, w := range want {
		if got[h] != w {
			t.Errorf("%s trusted = %v, want %v (all: %v)", h, got[h], w, got)
		}
	}
}

func testRoot(t *testing.T, name string) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func sha1Hex(der []byte) string {
	s := sha1.Sum(der)
	return strings.ToUpper(hex.EncodeToString(s[:]))
}

// A proxy's root the user trusts is given to Bun; one an admin trusted and
// the user then denied, one only kept in a keychain, and Apple's (trusted
// in the system domain, which isn't read) are not.
func TestSystemRootsAreTheTrustedOnes(t *testing.T) {
	proxy, denied, kept := testRoot(t, "Proxy CA"), testRoot(t, "Denied CA"), testRoot(t, "Kept CA")
	user := trustPlist(map[string]string{sha1Hex(proxy): sslTrust, sha1Hex(denied): deny})
	admin := trustPlist(map[string]string{sha1Hex(denied): allTrust})
	var pems []byte
	for _, der := range [][]byte{kept, proxy, denied, proxy} { // the proxy's in both keychains
		pems = append(pems, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}

	old := security
	t.Cleanup(func() { security = old })
	security = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "trust-settings-export":
			body := user
			if slices.Contains(args, "-d") {
				body = admin
			}
			return nil, os.WriteFile(args[len(args)-1], []byte(body), 0o600)
		case "find-certificate":
			return pems, nil
		}
		return nil, fmt.Errorf("unexpected security %v", args)
	}

	got := systemRoots()
	if len(got) != 1 || !slices.Equal(got[0], proxy) {
		var names []string
		for _, der := range got {
			c, _ := x509.ParseCertificate(der)
			names = append(names, c.Subject.CommonName)
		}
		t.Fatalf("roots given to Bun = %v, want only Proxy CA", names)
	}
}

package plugin

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

func systemCABundle() string { return "" }

// security runs macOS's security tool (tests stub it).
var security = func(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return proc.CommandContext(ctx, "/usr/bin/security", args...).Output()
}

// systemRoots are the certificates in the System and login keychains that
// the user or an admin marked as trusted: where a proxy's or a company's
// root goes when it is installed. Apple's own roots are trusted in the
// system domain and left out; Bun ships the same public ones. A
// certificate only kept in a keychain, or marked as not trusted, stays out.
func systemRoots() [][]byte {
	trusted := map[string]bool{}
	// the user's settings win over the admin's, so they are read last
	for _, domain := range [][]string{{"-d"}, {}} {
		for h, ok := range trustSettings(domain...) {
			trusted[h] = ok
		}
	}
	if len(trusted) == 0 {
		return nil
	}
	keychains := []string{"/Library/Keychains/System.keychain"}
	if home, err := os.UserHomeDir(); err == nil {
		keychains = append(keychains, filepath.Join(home, "Library", "Keychains", "login.keychain-db"))
	}
	out, _ := security(append([]string{"find-certificate", "-a", "-p"}, keychains...)...)
	var certs [][]byte
	for rest := out; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		sum := sha1.Sum(b.Bytes)
		h := strings.ToUpper(hex.EncodeToString(sum[:]))
		if trusted[h] {
			certs = append(certs, b.Bytes)
			delete(trusted, h) // once, though it is in both keychains
		}
	}
	return certs
}

// trustSettings reads one domain's trust settings (the user's, or with -d
// the admin's): each certificate's SHA-1 and whether it is trusted.
func trustSettings(domain ...string) map[string]bool {
	dir, err := os.MkdirTemp("", "magpie-trust-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "trust.plist")
	if _, err := security(append(append([]string{"trust-settings-export"}, domain...), f)...); err != nil {
		return nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	return trustedIn(b)
}

// trustedIn reads an exported trust settings plist. A certificate is
// trusted when its settings are empty (trust it as a root, for all) or
// one says to trust it, and none says to deny it; a result left out
// means trust, as Apple's documentation has it.
func trustedIn(plist []byte) map[string]bool {
	v, err := decodePlist(plist)
	if err != nil {
		return nil
	}
	top, _ := v.(map[string]any)
	list, _ := top["trustList"].(map[string]any)
	out := map[string]bool{}
	for h, e := range list {
		entry, _ := e.(map[string]any)
		settings, _ := entry["trustSettings"].([]any)
		trust, deny := len(settings) == 0, false
		for _, s := range settings {
			m, _ := s.(map[string]any)
			r, has := m["kSecTrustSettingsResult"].(int64)
			switch {
			case !has, r == 1, r == 2: // TrustRoot, TrustAsRoot
				trust = true
			case r == 3: // Deny
				deny = true
			}
		}
		out[strings.ToUpper(h)] = trust && !deny
	}
	return out
}

// decodePlist reads an XML property list into maps, slices, strings,
// int64s and bools; data and dates come back as their text.
func decodePlist(b []byte) (any, error) {
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.Strict = false
	for {
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		if s, ok := t.(xml.StartElement); ok && s.Name.Local != "plist" {
			return plistValue(d, s)
		}
	}
}

func plistValue(d *xml.Decoder, s xml.StartElement) (any, error) {
	switch s.Name.Local {
	case "dict":
		m := map[string]any{}
		key := ""
		for {
			t, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := t.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					var k string
					if err := d.DecodeElement(&k, &t); err != nil {
						return nil, err
					}
					key = k
					continue
				}
				v, err := plistValue(d, t)
				if err != nil {
					return nil, err
				}
				m[key] = v
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		var a []any
		for {
			t, err := d.Token()
			if err != nil {
				return nil, err
			}
			switch t := t.(type) {
			case xml.StartElement:
				v, err := plistValue(d, t)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			case xml.EndElement:
				return a, nil
			}
		}
	case "true", "false":
		if err := d.Skip(); err != nil {
			return nil, err
		}
		return s.Name.Local == "true", nil
	default:
		var text string
		if err := d.DecodeElement(&text, &s); err != nil && err != io.EOF {
			return nil, err
		}
		if s.Name.Local == "integer" {
			n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err != nil {
				return nil, err
			}
			return n, nil
		}
		return strings.TrimSpace(text), nil
	}
}

package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/catalog"
)

// caEnv has Bun trust what this machine trusts. Bun checks TLS against
// the roots it ships, not the system's, so behind a box that re-signs
// HTTPS (a company proxy, a VPN, Surge's or Proxyman's MITM) whose root
// the user installed, magpie gets through and Bun doesn't: `bun add`
// fails with "UNABLE_TO_VERIFY_LEAF_SIGNATURE downloading package
// manifest", and a plugin's requests fail the same way. NODE_EXTRA_CA_CERTS
// adds the system's roots to Bun's, for installs and for the host alike.
// One the environment sets already is the user's and is left as it is.
func caEnv(env []string) []string {
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if strings.ToUpper(k) == "NODE_EXTRA_CA_CERTS" && strings.TrimSpace(v) != "" {
			return nil
		}
	}
	if p := systemCAFile(); p != "" {
		return []string{"NODE_EXTRA_CA_CERTS=" + p}
	}
	return nil
}

var (
	systemCAOnce sync.Once
	systemCAPath string
)

// systemCAFile is a PEM file of the roots the system trusts beyond Bun's
// own, "" when there are none to add; it is made once a run.
var systemCAFile = func() string {
	systemCAOnce.Do(func() {
		if p := systemCABundle(); p != "" {
			systemCAPath = p
			return
		}
		certs := systemRoots()
		if len(certs) == 0 {
			return
		}
		var b bytes.Buffer
		for _, der := range certs {
			_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: der})
		}
		systemCAPath = writeCAFile(b.Bytes())
	})
	return systemCAPath
}

// writeCAFile keeps pem beside Bun's install cache, named by its content so
// a Bun that is running keeps reading the file it was given.
func writeCAFile(b []byte) string {
	sum := sha256.Sum256(b)
	dir := filepath.Join(filepath.Dir(catalog.CachePath()), "bun")
	p := filepath.Join(dir, "system-ca-"+hex.EncodeToString(sum[:6])+".pem")
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
		return p
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	tmp, err := os.CreateTemp(dir, ".system-ca-*")
	if err != nil {
		return ""
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil {
		os.Remove(tmp.Name())
		return ""
	}
	return p
}

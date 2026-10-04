package plugin

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func systemCABundle() string { return "" }

// systemRoots are the certificates in Windows' ROOT store, where a proxy's
// or a company's root is installed (the user's view, which holds the
// machine's too).
func systemRoots() [][]byte {
	name, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		return nil
	}
	store, err := windows.CertOpenSystemStore(0, name)
	if err != nil {
		return nil
	}
	defer windows.CertCloseStore(store, 0)
	var certs [][]byte
	var c *windows.CertContext
	for {
		c, err = windows.CertEnumCertificatesInStore(store, c)
		if err != nil || c == nil {
			break
		}
		der := unsafe.Slice(c.EncodedCert, c.Length)
		certs = append(certs, append([]byte(nil), der...))
	}
	return certs
}

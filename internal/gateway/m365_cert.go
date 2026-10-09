package gateway

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// M365CertificateStatus is the local certificate Claude for Microsoft 365
// sees, and whether this computer trusts the CA that issued it.
type M365CertificateStatus struct {
	Ready       bool   `json:"ready"`
	Trusted     bool   `json:"trusted"`
	Message     string `json:"message,omitempty"`
	Certificate string `json:"certificate,omitempty"`
}

type m365CertificateFiles struct {
	caCert, serverCert, serverKey string
}

func m365CertificatePaths() m365CertificateFiles {
	dir := filepath.Join(settings.Dir(), "m365")
	return m365CertificateFiles{
		caCert: filepath.Join(dir, "local-ca.pem"), serverCert: filepath.Join(dir, "local-server.pem"), serverKey: filepath.Join(dir, "local-server-key.pem"),
	}
}

// M365Certificate returns the certificate's state without creating files.
func M365Certificate() M365CertificateStatus {
	f := m365CertificatePaths()
	s := M365CertificateStatus{Certificate: f.caCert}
	if !m365CertificateFilesValid(f) {
		s.Message = "The local HTTPS certificate has not been prepared"
		return s
	}
	s.Ready = true
	s.Trusted = m365CertificateTrusted(f.caCert)
	if s.Trusted {
		s.Message = "The local HTTPS certificate is trusted"
	} else {
		s.Message = "Trust the local certificate before connecting from Microsoft 365"
	}
	return s
}

// InstallM365Certificate adds magpie's local CA to this user's trust store.
// Generating it has no system effect; only this explicit action installs it.
func InstallM365Certificate(ctx context.Context) (M365CertificateStatus, error) {
	f := m365CertificatePaths()
	if err := ensureM365Certificate(f); err != nil {
		return M365CertificateStatus{}, err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		keychain, err := defaultMacKeychain(ctx)
		if err != nil {
			return M365CertificateStatus{}, err
		}
		cmd = exec.CommandContext(ctx, "security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", keychain, f.caCert)
	case "windows":
		cmd = exec.CommandContext(ctx, "certutil", "-user", "-addstore", "Root", f.caCert)
	default:
		return M365Certificate(), errors.New("automatic certificate trust is supported on macOS and Windows")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return M365Certificate(), commandError("install local certificate", err, out)
	}
	s := M365Certificate()
	if !s.Trusted {
		return s, errors.New("certificate was installed but is not trusted yet; restart magpie and Microsoft 365, then try again")
	}
	return s, nil
}

// RemoveM365Certificate removes magpie's local CA from this user's trust store.
func RemoveM365Certificate(ctx context.Context) (M365CertificateStatus, error) {
	f := m365CertificatePaths()
	fingerprint, err := m365CertificateFingerprint(f.caCert)
	if err != nil {
		return M365CertificateStatus{}, err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		keychain, err := defaultMacKeychain(ctx)
		if err != nil {
			return M365CertificateStatus{}, err
		}
		cmd = exec.CommandContext(ctx, "security", "delete-certificate", "-Z", fingerprint, keychain)
	case "windows":
		cmd = exec.CommandContext(ctx, "certutil", "-user", "-delstore", "Root", fingerprint)
	default:
		return M365Certificate(), errors.New("automatic certificate removal is supported on macOS and Windows")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return M365Certificate(), commandError("remove local certificate", err, out)
	}
	return M365Certificate(), nil
}

func ensureM365Certificate(f m365CertificateFiles) error {
	if m365CertificateFilesValid(f) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.caCert), 0o700); err != nil {
		return err
	}
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return err
	}
	caSerial, err := m365Serial()
	if err != nil {
		return err
	}
	caTpl := &x509.Certificate{
		SerialNumber: caSerial, Subject: pkix.Name{CommonName: "Magpie M365 Local CA", Organization: []string{"Magpie"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true, IsCA: true, MaxPathLen: 0,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serverSerial, err := m365Serial()
	if err != nil {
		return err
	}
	serverTpl := &x509.Certificate{
		SerialNumber: serverSerial, Subject: pkix.Name{CommonName: "localhost", Organization: []string{"Magpie"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(2, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTpl, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err := writeM365PEM(f.caCert, "CERTIFICATE", caDER, 0o644); err != nil {
		return err
	}
	if err := writeM365PEM(f.serverCert, "CERTIFICATE", serverDER, 0o644); err != nil {
		return err
	}
	return writeM365PEM(f.serverKey, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverKey), 0o600)
}

func m365CertificateFilesValid(f m365CertificateFiles) bool {
	pair, err := tls.LoadX509KeyPair(f.serverCert, f.serverKey)
	if err != nil || len(pair.Certificate) == 0 {
		return false
	}
	server, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || time.Now().Add(30*24*time.Hour).After(server.NotAfter) {
		return false
	}
	b, err := os.ReadFile(f.caCert)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return false
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	_, err = server.Verify(x509.VerifyOptions{DNSName: "localhost", Roots: pool})
	return err == nil
}

func m365Serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func writeM365PEM(path, typ string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: data}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func m365CertificateTrusted(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return false
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err == nil
}

func m365CertificateFingerprint(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return "", errors.New("invalid local CA certificate")
	}
	digest := sha1.Sum(block.Bytes)
	return strings.ToUpper(hex.EncodeToString(digest[:])), nil
}

func defaultMacKeychain(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "security", "default-keychain", "-d", "user").Output()
	if err != nil {
		return "", fmt.Errorf("find the default macOS keychain: %w", err)
	}
	path := strings.Trim(strings.TrimSpace(string(out)), `"`)
	if path == "" {
		return "", errors.New("the default macOS keychain was not found")
	}
	return path, nil
}

func commandError(action string, err error, out []byte) error {
	if detail := strings.TrimSpace(string(out)); detail != "" {
		return fmt.Errorf("%s: %s", action, detail)
	}
	return fmt.Errorf("%s: %w", action, err)
}

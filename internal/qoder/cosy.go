package qoder

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// RSAPublicKeyPEM is Qoder's embedded 1024-bit RSA public key (from the
// client's cosy source), used to encrypt the per-request AES key that wraps
// the user blob in the COSY Authorization header.
const RSAPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// User is the signed-in Qoder account a COSY envelope is built for.
type User struct {
	UID       string // the Qoder user id (x-gw-user-id / Cosy-User value)
	Name      string // display name, may be empty
	Email     string // account email, may be empty
	Token     string // the jt- Bearer token (security_oauth_token)
	MachineID string // generated once at sign-in, saved with the account
}

var qoderRSAKey *rsa.PublicKey

func init() {
	block, _ := pem.Decode([]byte(RSAPublicKeyPEM))
	if block == nil {
		panic("qoder: failed to decode embedded RSA public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic("qoder: parse RSA public key: " + err.Error())
	}
	rsaKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		panic("qoder: embedded key is not an RSA public key")
	}
	qoderRSAKey = rsaKey
}

// aes128CBCEncrypt encrypts with AES-128-CBC and PKCS#7 padding.
func aes128CBCEncrypt(key, iv, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padLen := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

// rsaPKCS1Encrypt wraps an AES key with Qoder's embedded public key.
func rsaPKCS1Encrypt(key []byte) ([]byte, error) {
	return rsa.EncryptPKCS1v15(rand.Reader, qoderRSAKey, key)
}

// generateUserBlob builds the AES-encrypted user info the COSY payload
// carries, returning (info, key) both base64'd. The AES key doubles as the IV.
func generateUserBlob(user *User) (string, string, error) {
	blob := map[string]string{
		"uid":                  user.UID,
		"aid":                  "",
		"name":                 user.Name,
		"email":                user.Email,
		"security_oauth_token": user.Token,
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		return "", "", fmt.Errorf("qoder: marshal user blob: %w", err)
	}
	key := []byte(strings.ReplaceAll(newUUID(), "-", "")[:16])
	iv := key[:16]
	infoEnc, err := aes128CBCEncrypt(key, iv, raw)
	if err != nil {
		return "", "", fmt.Errorf("qoder: aes encrypt user blob: %w", err)
	}
	keyEnc, err := rsaPKCS1Encrypt(key)
	if err != nil {
		return "", "", fmt.Errorf("qoder: rsa encrypt key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(infoEnc), base64.StdEncoding.EncodeToString(keyEnc), nil
}

// urlPathname is the request's path without its /algo prefix or query, which
// is what goes into the COSY signature.
func urlPathname(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	path := u.Path
	if strings.HasPrefix(path, "/algo") {
		path = path[len("/algo"):]
	}
	return path
}

// BuildCosyHeaders builds the full set of COSY request headers for a Qoder
// call to rawURL with body already in its wire (encoded) form.
func BuildCosyHeaders(rawURL string, user *User, body string, timestamp int64) (map[string]string, error) {
	if user == nil || user.MachineID == "" {
		return nil, fmt.Errorf("qoder: missing account machine id")
	}
	infoB64, keyB64, err := generateUserBlob(user)
	if err != nil {
		return nil, err
	}
	requestID := strings.ReplaceAll(newUUID(), "-", "")
	if timestamp == 0 {
		timestamp = time.Now().Unix()
	}
	payloadObj := map[string]any{
		"version":     "v1",
		"requestId":   requestID,
		"info":        infoB64,
		"cosyVersion": "1.1.49",
		"ideVersion":  "",
	}
	payloadJSON, err := json.Marshal(payloadObj)
	if err != nil {
		return nil, fmt.Errorf("qoder: marshal cosy payload: %w", err)
	}
	payload := base64.StdEncoding.EncodeToString(payloadJSON)

	path := urlPathname(rawURL)
	sig := cosySignature(payload, keyB64, timestamp, body, path)

	auth := "Bearer COSY." + payload + "." + sig
	machineID := user.MachineID

	return map[string]string{
		"Accept":                "application/json",
		"Accept-Encoding":       "identity",
		"Content-Type":          "application/json",
		"Authorization":         auth,
		"Cosy-Business-Product": "app",
		"Cosy-Business-Type":    "agent",
		"Cosy-ClientIp":         machineID,
		"Cosy-ClientType":       "10",
		"Cosy-Data-Policy":      "disagree",
		"Cosy-Date":             fmt.Sprintf("%d", timestamp),
		"Cosy-Key":              keyB64,
		"Cosy-MachineId":        machineID,
		"Cosy-MachineToken":     machineID,
		"Cosy-MachineType":      "5",
		"Cosy-MachineOS":        MachineOS(),
		"Cosy-Scene":            "app",
		"Cosy-User":             user.UID,
		"Cosy-Version":          "1.1.49",
		"Login-Version":         "v2",
	}, nil
}

func cosySignature(payload, key string, timestamp int64, body, path string) string {
	sum := md5.Sum([]byte(payload + "\n" + key + "\n" + fmt.Sprintf("%d", timestamp) + "\n" + body + "\n" + path))
	return hex.EncodeToString(sum[:])
}

// MachineOS uses the architecture and platform names used by desktop clients.
func MachineOS() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "386":
		arch = "x86"
	case "arm64":
		arch = "aarch64"
	}
	os := runtime.GOOS
	if os == "windows" {
		os = "win32"
	}
	return arch + "_" + os
}

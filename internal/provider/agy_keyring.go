package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// agy keeps its own Google sign-in where go-keyring puts it: one entry,
// service "gemini" and user "antigravity", in the system's keyring. The
// Antigravity app (2.x) reads the same entry, so whichever account is in
// it is the account both are signed in to. The value is JSON
// ({"token":{access_token,refresh_token,expiry,…},"auth_method":"consumer",
// "id_token_email":…}); on macOS go-keyring writes it behind
// "go-keyring-base64:" (an older one hex-encoded it behind
// "go-keyring-encoded:"), on Windows as the credential's bytes, on Linux
// to the Secret Service as it is. Where agy has no keyring to use (WSL, a
// Linux with no D-Bus session) it keeps the same JSON in a file instead,
// .gemini/antigravity-cli/antigravity-oauth-token under its home.
//
// magpie writes it only to switch agy to another of its accounts
// (agy_switch.go, Provider.SwitchOnly), and only in place: macOS' item is
// updated (add-generic-password -U, which keeps its access list, so agy
// still reads it without asking), never deleted and added again;
// Windows' credential is written over by CredWriteW; Linux' item is
// stored over by secret-tool, the file replaced by a rename.
const (
	agyKeyService = "gemini"
	agyKeyUser    = "antigravity"
)

// agyKeyring is agy's own sign-in where this computer keeps it. Read
// answers the JSON, errAgyNoEntry when there is none; any other error
// means it couldn't be read, which says nothing of what is there.
type agyKeyring interface {
	Read() (string, error)
	Write(value string) error
	// Where names it for a message: "macOS' keychain", a file's path, …
	Where() string
}

var errAgyNoEntry = errors.New("agy has no sign-in stored")

// agyEnvSignIns are what agy signs in with in place of its stored
// sign-in: a Gemini or Google API key, Application Default Credentials,
// Vertex AI. Set, the keyring isn't what agy runs on.
var agyEnvSignIns = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "AGY_ADC_AUTH", "GOOGLE_GENAI_USE_VERTEXAI"}

// agyKeyringHere is agy's sign-in store on this computer; a var so tests
// put a stand-in in its place and never reach the real one.
var agyKeyringHere = agySystemKeyring

func agySystemKeyring() (agyKeyring, error) {
	for _, k := range agyEnvSignIns {
		if os.Getenv(k) != "" {
			return nil, fmt.Errorf("agy signs in with %s here, not with an account in the keyring", k)
		}
	}
	switch runtime.GOOS {
	case "darwin":
		return agyMacKeychain{}, nil
	case "windows":
		return agyWinCredential{}, nil
	case "linux":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		if agyLinuxUsesFile() {
			return agyFileStore{path: agyTokenFile(home)}, nil
		}
		p, err := exec.LookPath("secret-tool")
		if err != nil {
			return nil, errors.New("magpie needs secret-tool (libsecret-tools) to switch agy's account here")
		}
		return agySecretTool{path: p}, nil
	}
	return nil, fmt.Errorf("magpie doesn't switch agy's account on %s", runtime.GOOS)
}

// agyLinuxUsesFile says agy keeps its sign-in in its file rather than the
// Secret Service on this Linux: under WSL, or with no D-Bus session.
var agyLinuxUsesFile = func() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
		return true
	}
	return os.Getenv("DBUS_SESSION_BUS_ADDRESS") == ""
}

// agyTokenFile is where an agy whose home is home keeps its sign-in when
// it uses no keyring: agy's own under the user's home on such a computer,
// and every account the agy-cli plugin signed in to under that account's
// home (agyAccountHome).
func agyTokenFile(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
}

// agyDecode is a stored value's JSON as go-keyring wrote it.
func agyDecode(raw string) string {
	v := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(v, "go-keyring-base64:"):
		if b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "go-keyring-base64:")); err == nil {
			return string(b)
		}
	case strings.HasPrefix(v, "go-keyring-encoded:"):
		if b, err := hex.DecodeString(strings.TrimPrefix(v, "go-keyring-encoded:")); err == nil {
			return string(b)
		}
	}
	return v
}

// agyKeyWait bounds one run of the tool that reads or writes the keyring.
var agyKeyWait = 15 * time.Second

// agyMacKeychain is the login keychain's generic password, through
// `security` as go-keyring runs it: the item's access list names
// `security`, so agy, which reads it through `security` too, still reads
// it without a prompt after magpie wrote it.
type agyMacKeychain struct{}

func (agyMacKeychain) Where() string { return "macOS' keychain" }

func (agyMacKeychain) Read() (string, error) {
	b, err := securityKeychain{}.Find(agyKeyService, agyKeyUser)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errAgyNoEntry
	}
	if err != nil {
		return "", err
	}
	return agyDecode(string(b)), nil
}

// Write updates the item in place (-U), its secret given on `security
// -i`'s standard input, as go-keyring does, so it is never on a command
// line another process can read. The value goes base64-encoded behind
// go-keyring's prefix, which agy decodes.
func (agyMacKeychain) Write(value string) error {
	enc := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(value))
	ctx, cancel := context.WithTimeout(context.Background(), agyKeyWait)
	defer cancel()
	cmd := proc.CommandContext(ctx, "security", "-i")
	// service, user and the base64 text hold no quote: quoted whole, each
	// is one word to security's reader
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s '%s' -a '%s' -w '%s'\n", agyKeyService, agyKeyUser, enc))
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil || strings.TrimSpace(errOut.String()) != "" {
		return errors.New("the keychain didn't take agy's sign-in")
	}
	return nil
}

// agyFileStore is agy's file sign-in, replaced whole by a rename.
type agyFileStore struct{ path string }

func (f agyFileStore) Where() string { return f.path }

func (f agyFileStore) Read() (string, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errAgyNoEntry
	}
	if err != nil {
		return "", err
	}
	return agyDecode(string(b)), nil
}

func (f agyFileStore) Write(value string) error {
	return writePrivateQuiet(f.path, []byte(value))
}

// agySecretTool is the Secret Service's item go-keyring keeps, by its
// attributes service and username.
type agySecretTool struct{ path string }

func (agySecretTool) Where() string { return "the Secret Service" }

func (s agySecretTool) Read() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agyKeyWait)
	defer cancel()
	cmd := proc.CommandContext(ctx, s.path, "lookup", "service", agyKeyService, "username", agyKeyUser)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil && len(bytes.TrimSpace(out)) > 0:
		return agyDecode(string(out)), nil
	// secret-tool exits 1 saying nothing when no item matches; a keyring
	// it couldn't reach it says so
	case errors.As(err, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(errOut.String()) == "" && ctx.Err() == nil:
		return "", errAgyNoEntry
	}
	return "", errors.New("the Secret Service didn't give agy's sign-in to magpie")
}

func (s agySecretTool) Write(value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), agyKeyWait)
	defer cancel()
	cmd := proc.CommandContext(ctx, s.path, "store", "--label=Password for '"+agyKeyUser+"' on '"+agyKeyService+"'",
		"service", agyKeyService, "username", agyKeyUser)
	cmd.Stdin = strings.NewReader(value)
	if err := cmd.Run(); err != nil {
		return errors.New("the Secret Service didn't take agy's sign-in")
	}
	return nil
}

// agyWinCredential is Credential Manager's generic credential
// "gemini:antigravity", its blob the value's bytes as they are.
type agyWinCredential struct{}

func (agyWinCredential) Where() string { return "Windows' Credential Manager" }

func (agyWinCredential) Read() (string, error) {
	b, err := agyCredRead(agyKeyService + ":" + agyKeyUser)
	if err != nil {
		return "", err
	}
	return agyDecode(string(b)), nil
}

func (agyWinCredential) Write(value string) error {
	return agyCredWrite(agyKeyService+":"+agyKeyUser, agyKeyUser, []byte(value))
}

package davsync

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/backup"
)

// SetupPrefix begins a sync setup copied as text (WJaunita45604 on X: one
// copy, pasted on another computer, sets sync up there the same).
const SetupPrefix = "magpie-sync:"

// Setup is what a setup copied as text holds: the server, its password or
// token, and what is synced. Never the passphrase: the text is sealed with
// it, so it is typed on the other computer to open the text, as it is
// typed there to open the server's file.
type Setup struct {
	URL       string `json:"url"`
	User      string `json:"user,omitempty"`
	Password  string `json:"password,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	PathStyle bool   `json:"pathStyle,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Keys      bool   `json:"keys"`
	Agents    bool   `json:"agents"`
	Library   bool   `json:"library"`
	Memory    bool   `json:"memory"`
	Usage     bool   `json:"usage,omitempty"`
	SignIns   bool   `json:"signIns,omitempty"`
}

// ErrSetupOff is SetupText with sync off: there is nothing to copy.
var ErrSetupOff = errors.New("sync isn't set up on this computer")

// ErrNotSetup is OpenSetupText given text that isn't a copied setup.
var ErrNotSetup = errors.New("this isn't a sync setup copied from magpie")

// ErrSetupPassphrase is OpenSetupText with a passphrase that doesn't open it.
var ErrSetupPassphrase = errors.New("the passphrase doesn't open this setup: type the one sync uses on the other computer")

// SetupText is this computer's sync setup as one line of text, sealed with
// its passphrase (AES-GCM under a PBKDF2 key, as the server's file is), so
// the text alone gives no one the server's password.
func SetupText() (string, error) {
	c, ok := Load()
	if !ok {
		return "", ErrSetupOff
	}
	if c.Passphrase == "" {
		return "", ErrSetupOff
	}
	b, err := json.Marshal(Setup{URL: c.URL, User: c.User, Password: c.Password, Endpoint: c.Endpoint, Region: c.Region,
		PathStyle: c.PathStyle, Branch: c.Branch, Keys: c.Keys, Agents: c.Agents, Library: c.library(), Memory: c.memoryOn(),
		Usage: c.Usage, SignIns: c.SignIns})
	if err != nil {
		return "", err
	}
	sealed, err := backup.SealData(b, c.Passphrase)
	if err != nil {
		return "", err
	}
	return SetupPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// setupBody is the setup's text after its prefix, to the first character
// that can't be in it: line breaks and spaces a chat or a mail wrapped it
// with are in it, and taken out after.
var setupBody = regexp.MustCompile(`^[A-Za-z0-9_\-+/=\s]*`)

// OpenSetupText opens what SetupText gave, with the passphrase it was sealed
// with. Text before the setup, and line breaks a chat put in it, are skipped.
func OpenSetupText(text, pass string) (Setup, error) {
	i := strings.Index(text, SetupPrefix)
	if i < 0 {
		return Setup{}, ErrNotSetup
	}
	body := text[i+len(SetupPrefix):]
	body = strings.Join(strings.Fields(setupBody.FindString(body)), "")
	body = strings.NewReplacer("+", "-", "/", "_", "=", "").Replace(body)
	sealed, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Setup{}, ErrNotSetup
	}
	plain, err := backup.OpenData(sealed, pass)
	if errors.Is(err, backup.ErrPassphrase) {
		return Setup{}, ErrSetupPassphrase
	}
	if err != nil {
		return Setup{}, ErrNotSetup
	}
	var s Setup
	if json.Unmarshal(plain, &s) != nil || s.URL == "" {
		return Setup{}, ErrNotSetup
	}
	return s, nil
}

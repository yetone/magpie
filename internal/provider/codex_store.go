package provider

// Codex's own sign-in, where Codex keeps it (#2367, #2312, Dony2050).
//
// Codex 0.162's config.toml says where (codex-rs/login/src/auth/storage.rs,
// codex-rs/config/src/types.rs): cli_auth_credentials_store "file" (the
// default) is auth.json in CODEX_HOME; "keyring" is the system keychain
// alone; "auto" is the keychain, else auth.json, read and saved in that
// order; "ephemeral" is Codex's memory. With [features]
// secret_auth_storage on (the default on Windows), keyring and auto mean
// Codex's encrypted auth storage instead of the keychain item.
//
// magpie reads and writes the keychain item on macOS only, through
// `security` as it does Claude Code's (readClaudeKeychainItem): the
// generic password of service "Codex Auth" and account
// "cli|<first 16 hex of SHA-256 of the canonical CODEX_HOME>", holding
// auth.json's JSON on one line. Elsewhere, and for the encrypted
// storage, the store is one magpie doesn't read: keyring reads as
// unknown, auto as auth.json, which is what Codex falls back to.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/proc"
)

const codexKeychainService = "Codex Auth"

// keychain is the system keychain as magpie reaches an item in it. Find
// gives fs.ErrNotExist for no item; Remove, nil. Stat gives the item's
// modification date, or fs.ErrNotExist: it reads only attributes, which
// macOS gives without asking the user (only the secret is behind the
// item's access list), so a kept read is checked with it instead of read
// again. Tests put a fake in codexKeychain: no test reaches the user's own
// keychain.
type keychain interface {
	Find(service, account string) ([]byte, error)
	Stat(service, account string) (string, error)
	Put(service, account string, secret []byte) error
	Remove(service, account string) error
}

var (
	codexKeychain keychain = securityKeychain{}
	// codexKeychainHere: the keychain item is magpie's to read on this
	// system (macOS); a var so tests run the macOS path everywhere
	codexKeychainHere = runtime.GOOS == "darwin"
	// secretAuthStorageDefault is Codex's default for [features]
	// secret_auth_storage: cfg!(windows) (codex-rs/features/src/lib.rs)
	secretAuthStorageDefault = runtime.GOOS == "windows"
)

// keychainWait bounds a `security` run that puts up no prompt: a Stat, a
// write, a removal.
var keychainWait = 10 * time.Second

// keychainAsk bounds reading the secret, which may put macOS' prompts in
// front of the user, one after the other: the login keychain's password,
// then whether `security` may have the item. The user reads and types in
// that time; under 10s an Allow that took 11–16s failed (#2367,
// Dony2050). Nobody waits on it all that time (findCodexKeychain).
var keychainAsk = 2 * time.Minute

var (
	// errKeychainRefused: `security` exits with errSecAuthFailed (-25293,
	// exit 51) for Deny or a wrong password, errSecUserCanceled (-128,
	// exit 128) for Cancel (Security.framework's SecBase.h)
	errKeychainRefused  = errors.New("macOS's keychain prompt was denied or cancelled")
	errKeychainNoAnswer = errors.New("macOS's keychain prompt got no answer in time")
	errKeychainWaiting  = errors.New("magpie is waiting for an answer to macOS's keychain prompt")
	errKeychainUnread   = errors.New("the keychain didn't give it to magpie")
)

// securityKeychain is macOS' `security`, as readClaudeKeychainItem and
// writeClaudeCredential run it. Neither its output nor its arguments go
// into an error: both can hold the credential.
type securityKeychain struct{}

func (securityKeychain) Find(service, account string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainAsk)
	defer cancel()
	out, err := proc.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		return nil, keychainError(ctx, err)
	}
	b, _ := keychainText(bytes.TrimSpace(out))
	return b, nil
}

// Stat reads the item without -w: its attributes, never its secret, so
// macOS doesn't ask. Its "mdat" line is the modification date, to the
// second.
func (securityKeychain) Stat(service, account string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainWait)
	defer cancel()
	out, err := proc.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", account).Output()
	if err != nil {
		return "", keychainError(ctx, err)
	}
	return keychainMdat(out)
}

// keychainMdat is the "mdat" attribute of `security`'s listing of an item.
func keychainMdat(out []byte) (string, error) {
	for line := range bytes.Lines(out) {
		if _, v, ok := bytes.Cut(line, []byte(`"mdat"<timedate>=`)); ok {
			if v = bytes.TrimSpace(v); len(v) > 0 {
				return string(v), nil
			}
		}
	}
	return "", errors.New("the keychain item has no modification date")
}

func (securityKeychain) Put(service, account string, secret []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainWait)
	defer cancel()
	if err := proc.CommandContext(ctx, "security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", string(secret)).Run(); err != nil {
		return errors.New("the keychain didn't take the sign-in")
	}
	return nil
}

func (securityKeychain) Remove(service, account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainWait)
	defer cancel()
	if err := proc.CommandContext(ctx, "security", "delete-generic-password", "-s", service, "-a", account).Run(); err != nil && !keychainItemMissing(err) {
		return errors.New("the keychain didn't remove the sign-in")
	}
	return nil
}

// keychainError says what a failed `security` read was: no item, the
// prompt refused, no answer in time, or something else.
func keychainError(ctx context.Context, err error) error {
	var exit *exec.ExitError
	switch {
	case keychainItemMissing(err):
		return fs.ErrNotExist
	case ctx.Err() != nil:
		return errKeychainNoAnswer
	case errors.As(err, &exit) && (exit.ExitCode() == 51 || exit.ExitCode() == 128):
		return errKeychainRefused
	}
	return errKeychainUnread
}

// keychainItemMissing: `security` exits 44 for errSecItemNotFound
// (-25300; it exits with the status' low byte).
func keychainItemMissing(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 44
}

// codexKeychainAccount is Codex's compute_store_key: the home made
// canonical where it can be (else as given), SHA-256, its first 16 hex
// digits after "cli|".
func codexKeychainAccount(home string) string {
	p := home
	if abs, err := filepath.Abs(home); err == nil {
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			p = real
		}
	}
	sum := sha256.Sum256([]byte(p))
	return "cli|" + hex.EncodeToString(sum[:])[:16]
}

// codexStoreSetting is what config.toml in a Codex home says of where
// the sign-in is kept.
type codexStoreSetting struct {
	Mode    string // file, keyring, auto or ephemeral
	Secrets bool   // [features] secret_auth_storage
}

// readCodexStoreSetting reads config.toml as Codex does: no file or no
// key is "file". A file that doesn't parse, or a value Codex doesn't
// know (it would refuse to start), is an error: where the sign-in is
// isn't known.
func readCodexStoreSetting(home string) (codexStoreSetting, error) {
	path := filepath.Join(home, "config.toml")
	s, err := filememo.Read("codex credential store", path, func(b []byte) (codexStoreSetting, error) {
		var c struct {
			Store    string         `toml:"cli_auth_credentials_store"`
			Features map[string]any `toml:"features"`
		}
		if err := toml.Unmarshal(edit.TOMLWithout(b, "projects"), &c); err != nil {
			return codexStoreSetting{}, fmt.Errorf("%s doesn't read as TOML", path)
		}
		s := codexStoreSetting{Mode: c.Store, Secrets: secretAuthStorageDefault}
		if v, ok := c.Features["secret_auth_storage"].(bool); ok {
			s.Secrets = v
		}
		return s, nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return codexStoreSetting{Mode: "file", Secrets: secretAuthStorageDefault}, nil
	}
	if err != nil {
		return codexStoreSetting{}, err
	}
	switch s.Mode {
	case "":
		s.Mode = "file"
	case "file", "keyring", "auto", "ephemeral":
	default:
		return codexStoreSetting{}, fmt.Errorf("cli_auth_credentials_store = %q in %s isn't one Codex knows", s.Mode, path)
	}
	return s, nil
}

// keychainItem: the store is the keychain item magpie reads.
func (s codexStoreSetting) keychainItem() bool {
	return (s.Mode == "keyring" || s.Mode == "auto") && !s.Secrets && codexKeychainHere
}

// where names the store keyring and auto mean here.
func (s codexStoreSetting) where() string {
	switch {
	case s.Secrets:
		return "Codex's encrypted auth storage (features.secret_auth_storage)"
	case runtime.GOOS == "darwin" || codexKeychainHere:
		return "the macOS keychain"
	}
	return "the system keychain"
}

// codexStoreError: Codex keeps its sign-in where magpie couldn't read it,
// so whether it is signed in, and to whom, isn't known.
type codexStoreError struct {
	Setting codexStoreSetting
	Config  string
	Cause   error // nil: a store magpie doesn't read on this system
}

func (e *codexStoreError) Error() string {
	if e.Setting.Mode == "ephemeral" {
		return fmt.Sprintf("Codex keeps its sign-in in its own memory (cli_auth_credentials_store = \"ephemeral\" in %s), which no other program can read", e.Config)
	}
	if e.Cause != nil {
		return fmt.Sprintf("Codex keeps its sign-in in %s (cli_auth_credentials_store = %q in %s), and %v", e.Setting.where(), e.Setting.Mode, e.Config, e.Cause)
	}
	return fmt.Sprintf("Codex keeps its sign-in in %s (cli_auth_credentials_store = %q in %s), which magpie doesn't read", e.Setting.where(), e.Setting.Mode, e.Config)
}

// codexStore is where a Codex sign-in was read from, so that a refresh
// writes its rotated tokens back there.
type codexStore struct {
	home     string
	keychain bool
}

func (s codexStore) file() string { return filepath.Join(s.home, "auth.json") }

// codexKeychainCache keeps what the keychain gave for a home: reading the
// secret may put a prompt in front of the user, and every request and
// page reads the sign-in. A read is kept while the item is unchanged:
// after codexKeychainCheck its modification date is looked at again
// (Stat, no prompt), and the secret is read again only when that moved,
// so an Allow given once isn't asked for again at the next poll (#2367).
// A refusal, or a prompt with no answer, is kept 10 minutes, so that a
// prompt isn't put up again at once (as Copilot's, copilotSecret), or
// until Try again (RetryCodexKeychain).
//
// codexKeychainMu guards the maps only; no `security` runs under it. One
// read of the secret at a time per item (codexKeychainAsks): two at once
// would put up two prompts. A reader waits keychainHold for it at most;
// the read goes on while the user answers, and what it gets is kept.
var (
	codexKeychainMu    sync.Mutex
	codexKeychainCache = map[string]codexKeychainRead{}
	codexKeychainAsks  = map[string]*codexKeychainAsk{}
	// codexKeychainGen: a read begun before resetCodexKeychain isn't kept
	codexKeychainGen uint64
)

type codexKeychainRead struct {
	b       []byte
	err     error
	mdat    string    // the item's modification date when b was read; "" not known
	at      time.Time // when it was read
	checked time.Time // when the item was last seen unchanged
}

// codexKeychainAsk is a read of the secret in flight; b and err are set
// before done closes.
type codexKeychainAsk struct {
	done  chan struct{}
	start time.Time
	b     []byte
	err   error
}

const (
	codexKeychainCheck   = 10 * time.Second
	codexKeychainRefused = 10 * time.Minute
)

// keychainHold is how long a reader waits on a read of the secret in
// flight, counted from when the read began: one with no prompt is done
// well within it, and a reader that comes while a prompt is up is told so
// at once (errKeychainWaiting) instead of waiting on the user.
var keychainHold = 5 * time.Second

// findCodexKeychain reads the item for home; fresh looks at the item now,
// for a read whose tokens will be spent or saved, and asks again after a
// refusal.
func findCodexKeychain(home string, fresh bool) ([]byte, error) {
	account := codexKeychainAccount(home)
	now := time.Now()
	codexKeychainMu.Lock()
	r, kept := codexKeychainCache[account]
	switch {
	case !kept:
	case r.err == nil:
		if !fresh && now.Sub(r.checked) < codexKeychainCheck {
			codexKeychainMu.Unlock()
			return r.b, nil
		}
		if !fresh {
			// others go by it while this reader looks
			r.checked = now
			codexKeychainCache[account] = r
		}
	case errors.Is(r.err, fs.ErrNotExist):
		if !fresh && now.Sub(r.at) < codexKeychainCheck {
			codexKeychainMu.Unlock()
			return nil, r.err
		}
		kept = false
	default:
		if !fresh && now.Sub(r.at) < codexKeychainRefused {
			codexKeychainMu.Unlock()
			return nil, r.err
		}
		kept = false
	}
	gen := codexKeychainGen
	codexKeychainMu.Unlock()
	if kept {
		mdat, err := codexKeychain.Stat(codexKeychainService, account)
		switch {
		case err == nil && r.mdat != "" && mdat == r.mdat:
			keepCodexKeychainRead(account, gen, r.at, codexKeychainRead{b: r.b, mdat: r.mdat, at: r.at, checked: time.Now()})
			return r.b, nil
		case errors.Is(err, fs.ErrNotExist):
			keepCodexKeychainRead(account, gen, r.at, codexKeychainRead{err: err, at: time.Now()})
			return nil, err
		case err != nil && !fresh:
			// not known whether it changed: the last read stands
			return r.b, nil
		}
		// changed, or not known for a read that will be spent
	}
	return askCodexKeychain(account)
}

// keepCodexKeychainRead keeps r for account unless the keychain was reset,
// or a newer read kept, since the one it replaces (read at was).
func keepCodexKeychainRead(account string, gen uint64, was time.Time, r codexKeychainRead) {
	codexKeychainMu.Lock()
	defer codexKeychainMu.Unlock()
	if cur, ok := codexKeychainCache[account]; gen != codexKeychainGen || ok && cur.at.After(was) {
		return
	}
	codexKeychainCache[account] = r
}

// askCodexKeychain reads the secret, or joins the read in flight.
func askCodexKeychain(account string) ([]byte, error) {
	codexKeychainMu.Lock()
	a := codexKeychainAsks[account]
	if a == nil {
		a = &codexKeychainAsk{done: make(chan struct{}), start: time.Now()}
		codexKeychainAsks[account] = a
		go a.run(account, codexKeychain, codexKeychainGen)
	}
	codexKeychainMu.Unlock()
	hold := time.NewTimer(max(0, keychainHold-time.Since(a.start)))
	defer hold.Stop()
	select {
	case <-a.done:
		return a.b, a.err
	case <-hold.C:
	}
	select {
	case <-a.done:
		return a.b, a.err
	default:
		return nil, errKeychainWaiting
	}
}

func (a *codexKeychainAsk) run(account string, k keychain, gen uint64) {
	// the date before the secret: an item changed while the prompt was up
	// reads as changed at the next look
	mdat, err := k.Stat(codexKeychainService, account)
	var b []byte
	if !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			mdat = ""
		}
		b, err = k.Find(codexKeychainService, account)
		if err == nil {
			var obj map[string]json.RawMessage
			if json.Unmarshal(b, &obj) != nil || obj == nil {
				b, err = nil, errors.New("the keychain item isn't a Codex sign-in magpie can read")
			}
		}
	}
	now := time.Now()
	codexKeychainMu.Lock()
	if cur, ok := codexKeychainCache[account]; gen == codexKeychainGen && (!ok || !cur.at.After(a.start)) {
		codexKeychainCache[account] = codexKeychainRead{b: b, err: err, mdat: mdat, at: now, checked: now}
	}
	if codexKeychainAsks[account] == a {
		delete(codexKeychainAsks, account)
	}
	a.b, a.err = b, err
	codexKeychainMu.Unlock()
	close(a.done)
}

// keepCodexKeychain keeps what magpie itself just wrote to the item for
// home, at the item's date now.
func keepCodexKeychain(home string, b []byte) {
	account := codexKeychainAccount(home)
	mdat, err := codexKeychain.Stat(codexKeychainService, account)
	if err != nil {
		mdat = ""
	}
	now := time.Now()
	codexKeychainMu.Lock()
	codexKeychainCache[account] = codexKeychainRead{b: b, mdat: mdat, at: now, checked: now}
	codexKeychainMu.Unlock()
}

// forgetCodexKeychain has the next read look at the item again: a kept
// read is used while the item's date is unchanged, no item is looked for
// again. A refusal stands (RetryCodexKeychain clears it): asking again
// would put the prompt up again.
func forgetCodexKeychain() {
	codexKeychainMu.Lock()
	defer codexKeychainMu.Unlock()
	for account, r := range codexKeychainCache {
		switch {
		case r.err == nil:
			r.checked = time.Time{}
			codexKeychainCache[account] = r
		case errors.Is(r.err, fs.ErrNotExist):
			delete(codexKeychainCache, account)
		}
	}
}

// resetCodexKeychain drops everything kept, and has a read in flight
// kept nowhere: a test's keychain is another.
func resetCodexKeychain() {
	codexKeychainMu.Lock()
	clear(codexKeychainCache)
	clear(codexKeychainAsks)
	codexKeychainGen++
	codexKeychainMu.Unlock()
}

// RetryCodexKeychain is Try again beside a sign-in the keychain didn't
// give (#2367): Codex's item is read once, now, past a refusal or a
// prompt with no answer kept (a fresh read), and what it gets is kept in
// their place. It gives what that read gave: errKeychainWaiting while the
// prompt is up (joining it, never a second one), its answer kept when it
// comes.
func RetryCodexKeychain() error {
	defer ForgetAccounts()
	_, _, err := readCodexLogin(codexHome(), true)
	var se *codexStoreError
	if errors.As(err, &se) && se.Cause != nil {
		return se.Cause
	}
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("there is no Codex sign-in in the keychain")
	}
	return err
}

// codexKeychainUnread: Codex's sign-in is kept in the keychain item, and
// the keychain didn't give it (refused, no answer, waiting): Try again
// can help.
func codexKeychainUnread(err error) bool {
	var se *codexStoreError
	return errors.As(err, &se) && se.Setting.keychainItem() &&
		(errors.Is(se.Cause, errKeychainRefused) || errors.Is(se.Cause, errKeychainNoAnswer) ||
			errors.Is(se.Cause, errKeychainWaiting) || errors.Is(se.Cause, errKeychainUnread))
}

// readCodexLogin is Codex's sign-in under home as Codex would load it,
// and where it came from. fs.ErrNotExist (wrapped or not): signed out,
// as far as magpie can see. *codexStoreError: kept where magpie couldn't
// read it, so not known. A config.toml that doesn't read leaves auth.json
// read as before there was a setting (Codex itself wouldn't start), and
// nothing is saved over it (saveCodexLogin).
func readCodexLogin(home string, fresh bool) ([]byte, codexStore, error) {
	st := codexStore{home: home}
	if discoveryOff() {
		return nil, st, errAccountDiscoveryOff
	}
	s, err := readCodexStoreSetting(home)
	if err != nil {
		b, err := readAccountFile(st.file())
		return b, st, err
	}
	cfg := filepath.Join(home, "config.toml")
	switch {
	case s.Mode == "ephemeral":
		return nil, st, &codexStoreError{Setting: s, Config: cfg}
	case s.keychainItem():
		b, kerr := findCodexKeychain(home, fresh)
		if kerr == nil {
			st.keychain = true
			return b, st, nil
		}
		if s.Mode == "keyring" {
			if errors.Is(kerr, fs.ErrNotExist) {
				return nil, st, kerr
			}
			return nil, st, &codexStoreError{Setting: s, Config: cfg, Cause: kerr}
		}
		// auto: the file, as Codex falls back to it on no item and on an
		// item it couldn't load
		b, err := readAccountFile(st.file())
		if err != nil && !errors.Is(kerr, fs.ErrNotExist) {
			// Codex, which the item lets in, may well be signed in
			return nil, st, &codexStoreError{Setting: s, Config: cfg, Cause: kerr}
		}
		return b, st, err
	case s.Mode == "keyring":
		return nil, st, &codexStoreError{Setting: s, Config: cfg}
	case s.Mode == "auto":
		// a store magpie doesn't read: auth.json is there only when Codex
		// is on its fallback (a save to the store removes it)
		b, err := readAccountFile(st.file())
		if errors.Is(err, fs.ErrNotExist) {
			return nil, st, &codexStoreError{Setting: s, Config: cfg}
		}
		return b, st, err
	}
	b, err := readAccountFile(st.file())
	return b, st, err
}

// write puts a sign-in back where it was read from: rotated tokens, every
// other field as it was. The keychain item takes it on one line, as Codex
// writes it; `security -w` prints one with a newline as hex (#70).
func (s codexStore) write(b []byte) error {
	if discoveryOff() {
		return errAccountDiscoveryOff
	}
	if !s.keychain {
		// the user's own sign-in, which Codex CLI and the Codex app
		// read: written beside it, flushed and renamed in, never
		// truncated in place (#2239, #1505)
		return writePrivate(s.file(), append(bytes.TrimSpace(b), '\n'))
	}
	var one bytes.Buffer
	if err := json.Compact(&one, b); err != nil {
		return errors.New("the Codex sign-in to save isn't JSON")
	}
	if err := codexKeychain.Put(codexKeychainService, codexKeychainAccount(s.home), one.Bytes()); err != nil {
		forgetCodexKeychain()
		return err
	}
	keepCodexKeychain(s.home, one.Bytes())
	return nil
}

// saveCodexLogin makes b the sign-in Codex goes by (a switch, or the
// first sign-in in magpie), in the store its config.toml names. The
// store's own sign-in is read first, never taken for absent when it
// couldn't be read: one that wasn't read is never written over. auth.json
// left beside a keychain item is left to Codex, which removes it at its
// next save; the item goes first when it reads.
func saveCodexLogin(home string, b []byte) error {
	if discoveryOff() {
		return errAccountDiscoveryOff
	}
	s, err := readCodexStoreSetting(home)
	if err != nil {
		return fmt.Errorf("where Codex keeps its sign-in isn't known (%v); nothing was changed", err)
	}
	st := codexStore{home: home}
	cfg := filepath.Join(home, "config.toml")
	switch {
	case s.Mode == "ephemeral":
		return &codexStoreError{Setting: s, Config: cfg}
	case s.keychainItem():
		_, kerr := findCodexKeychain(home, true)
		if kerr != nil && !errors.Is(kerr, fs.ErrNotExist) {
			return &codexStoreError{Setting: s, Config: cfg, Cause: kerr}
		}
		st.keychain = true
		werr := st.write(b)
		if werr == nil || s.Mode == "keyring" || kerr == nil {
			// an item that is there goes first with Codex: a file beside
			// it would be passed over
			return werr
		}
		// auto with no item, and the keychain not taking one: Codex
		// saves to the file then, and loads it
		st.keychain = false
	case s.Mode == "keyring":
		return &codexStoreError{Setting: s, Config: cfg}
	case s.Mode == "auto":
		// a store magpie doesn't read: only a Codex on its file fallback
		// goes by auth.json
		if _, err := os.Stat(st.file()); err != nil {
			return &codexStoreError{Setting: s, Config: cfg}
		}
	}
	return st.write(b)
}

// deleteCodexLogin signs Codex out, as `codex logout` does: the item
// for this home and auth.json both go (auto would fall back to the file).
// The item not removed, the file is left too.
func deleteCodexLogin(home string) error {
	if discoveryOff() {
		return errAccountDiscoveryOff
	}
	s, err := readCodexStoreSetting(home)
	if err != nil {
		return fmt.Errorf("where Codex keeps its sign-in isn't known (%v); nothing was changed", err)
	}
	cfg := filepath.Join(home, "config.toml")
	switch {
	case s.Mode == "ephemeral":
		return &codexStoreError{Setting: s, Config: cfg}
	case s.keychainItem():
		err := codexKeychain.Remove(codexKeychainService, codexKeychainAccount(home))
		forgetCodexKeychain()
		if err != nil {
			return err
		}
	case s.Mode == "keyring":
		return &codexStoreError{Setting: s, Config: cfg}
	}
	defer filememo.Forget()
	if err := os.Remove(filepath.Join(home, "auth.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// CodexSignIn is the sign-in Codex goes by under its home dir, from
// wherever its config.toml has it kept (readCodexLogin); ok is false for
// none, or one magpie couldn't read. Agents' wiring reads it here.
func CodexSignIn(dir string) (b []byte, ok bool) {
	b, _, err := readCodexLogin(dir, false)
	return b, err == nil
}

// CodexSignInKnown is CodexSignIn with whether the read says anything at
// all. A sign-in that isn't there (no auth.json: codex logout; no keychain
// item) or is kept where magpie doesn't read is known, ok false. One that
// couldn't be read, or doesn't parse, is not: Codex rewrites auth.json in
// place when it refreshes its tokens (codex-rs FileAuthStorage::save
// opens it with truncate), so a read in that moment finds it empty or cut
// off, and a keychain read can fail for a moment too. known false says
// nothing of the sign-in either way.
func CodexSignInKnown(dir string) (b []byte, ok, known bool) {
	b, _, err := readCodexLogin(dir, false)
	if err != nil {
		var se *codexStoreError
		gone := errors.Is(err, fs.ErrNotExist) || errors.Is(err, errAccountDiscoveryOff) ||
			errors.As(err, &se) && se.Cause == nil
		return nil, false, gone
	}
	if !json.Valid(b) {
		return nil, false, false
	}
	return b, true, true
}

// CodexKeychainForTest has Codex's keychain item read through find (no
// item: fs.ErrNotExist) on every system, until restore.
func CodexKeychainForTest(find func(service, account string) ([]byte, error)) (restore func()) {
	old, oldHere := codexKeychain, codexKeychainHere
	codexKeychain, codexKeychainHere = findOnly(find), true
	resetCodexKeychain()
	return func() {
		codexKeychain, codexKeychainHere = old, oldHere
		resetCodexKeychain()
	}
}

type findOnly func(service, account string) ([]byte, error)

func (f findOnly) Find(service, account string) ([]byte, error) { return f(service, account) }

// Stat dates the item by what find gives: it moves when the item
// changes, as macOS' modification date does.
func (f findOnly) Stat(service, account string) (string, error) {
	b, err := f(service, account)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8]), nil
}
func (findOnly) Put(string, string, []byte) error {
	return errors.New("this test's keychain takes nothing")
}
func (findOnly) Remove(string, string) error {
	return errors.New("this test's keychain removes nothing")
}

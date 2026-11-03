package plugin

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/settings"
)

// AuthEntries is plugin-auth.json's sign-ins by key, each as written, for
// a backup that carries them. A file that can't be read is an error,
// never no sign-ins: carried as none, it would read as every sign-in
// signed out.
func AuthEntries() (map[string]json.RawMessage, error) {
	b, err := readAuthFile()
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, nil
}

// AuthWho is who a sign-in entry is signed in as (its accountId, or the
// email it carries), "" when it doesn't say.
func AuthWho(raw json.RawMessage) string {
	var a storedAuth
	if json.Unmarshal(raw, &a) != nil {
		return ""
	}
	return firstNonEmpty(a.AccountID, a.Metadata.Email, a.Email)
}

// AuthExpires is when an OAuth sign-in entry's access token runs out, in
// Unix milliseconds: a later one is a later refresh. 0 when it doesn't say.
func AuthExpires(raw json.RawMessage) int64 {
	var a struct {
		Expires int64 `json:"expires"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return 0
	}
	return a.Expires
}

// EditAuth changes plugin-auth.json under plugin-auth.json.lock, which
// host.js takes too, read afresh there: f gets the sign-ins by key and
// says whether it changed them. A file that can't be read is not written
// over; the one there is kept as its last good generation first, and the
// new one is renamed in whole.
func EditAuth(f func(m map[string]json.RawMessage) (bool, error)) error {
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		return err
	}
	unlock := lockAuth()
	defer unlock()
	m, err := AuthEntries()
	if err != nil {
		return err
	}
	ch, err := f(m)
	if err != nil || !ch {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := lastgood.Keep(AuthPath(), validAuth, 0o600); err != nil {
		return err
	}
	if err := writeWhole(AuthPath(), append(b, '\n')); err != nil {
		return err
	}
	changed()
	return nil
}

package provider

// What magpie spent of a Codex account's rate-limit resets, kept so the
// user can see it afterwards (foobra12 on Discord: "magpie会自动使用codex
// 重置卡吗 我发现刚才好像不小心自动给我使用了一张"). Auto-use spends one
// in the background — its week used up, or one about to run out — and
// before this only the gateway's log said so; a reset gone from the card
// read the same whether magpie spent it, the user did, or it ran out. Each
// use is kept in codex-resets-used.json, by account, and shown on the
// account's card and by `magpie quota auto-reset`.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/settings"
)

// Why a reset was used: by itself as the account's week was used up and no
// other account could answer (AutoUseCodexReset), by itself as it was about
// to run out unused (SpendExpiringCodexResets), or by hand from the Usage
// page, the panel or the CLI (UseCodexReset).
const (
	ResetUsedWeek     = "week"
	ResetUsedExpiring = "expiring"
	ResetUsedByHand   = "hand"
)

// ResetUse is one reset magpie spent on an account.
type ResetUse struct {
	User    string    `json:"user"`
	At      time.Time `json:"at"`
	Why     string    `json:"why"`
	Windows int       `json:"windows,omitempty"` // how many windows started again
}

var (
	// resetsUsedKeep is how long a use is kept, and resetsUsedMost how many
	// at most: enough to answer "did magpie use one?" a while later
	resetsUsedKeep = 90 * 24 * time.Hour
	resetsUsedMost = 200
	// resetsUsedShown is how many of an account's uses its card gets
	resetsUsedShown = 5
)

func resetsUsedPath() string { return filepath.Join(settings.Dir(), "codex-resets-used.json") }

// resetsUsedMu keeps the file to one writer at a time, across the magpies
// on the computer too: the gateway spends by itself, a terminal's magpie
// by hand.
var resetsUsedMu = filelock.Mutex{Path: func() string { return resetsUsedPath() + ".lock" }}

// readResetsUsed reads the file: none yet is no uses; one that can't be
// read or parsed is an error, never taken for none.
func readResetsUsed() ([]ResetUse, error) {
	b, err := os.ReadFile(resetsUsedPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var us []ResetUse
	if err := json.Unmarshal(b, &us); err != nil {
		return nil, fmt.Errorf("%s: %w", resetsUsedPath(), err)
	}
	return us, nil
}

// noteResetUsed keeps that a reset of user's was used, for why, when out
// says one was, and says when keeping it failed: the reset is spent on
// the server either way, so the caller names the loss rather than treat
// the spend as failed (#5556). A file that doesn't parse is copied aside
// first, not written over unread.
func noteResetUsed(user, why string, out ResetOutcome) error {
	if user == "" || out.Code != "reset" {
		return nil
	}
	resetsUsedMu.Lock()
	defer resetsUsedMu.Unlock()
	us, err := readResetsUsed()
	if err != nil {
		p := resetsUsedPath()
		if b, rerr := os.ReadFile(p); rerr == nil {
			_ = os.WriteFile(p+".bad-"+time.Now().UTC().Format("20060102T150405.000000000"), b, 0o600)
		}
		us = nil
	}
	now := time.Now().UTC()
	us = slices.DeleteFunc(us, func(u ResetUse) bool { return now.Sub(u.At) > resetsUsedKeep })
	us = append(us, ResetUse{User: user, At: now, Why: why, Windows: out.Windows})
	if len(us) > resetsUsedMost {
		us = us[len(us)-resetsUsedMost:]
	}
	b, _ := json.MarshalIndent(us, "", "  ")
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		return fmt.Errorf("keeping that a reset was used to %s: %w", resetsUsedPath(), err)
	}
	if err := writeChanged(resetsUsedPath(), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("keeping that a reset was used to %s: %w", resetsUsedPath(), err)
	}
	return nil
}

// ResetsUsed is what magpie spent of the Codex account user's resets,
// newest first, resetsUsedShown at most; nil when none, or when the file
// can't be read.
func ResetsUsed(user string) []ResetUse {
	if user == "" {
		return nil
	}
	us, err := readResetsUsed()
	if err != nil {
		return nil
	}
	var out []ResetUse
	for i := len(us) - 1; i >= 0 && len(out) < resetsUsedShown; i-- {
		if strings.EqualFold(us[i].User, user) {
			out = append(out, us[i])
		}
	}
	return out
}

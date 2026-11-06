package provider

// What a Cline API key has left: ClinePass's 5-hour, weekly and monthly
// limits and the account's credit balance, read with the key as the Cline
// plugin reads them signed in (zRain on Discord: AxonHub reads them with a
// key). GET /users/me/plan/usage-limits answers a key as it answers an
// account; an account without ClinePass is a 404 there and has its credits
// alone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// clineUsageAPI is where the Cline API's account endpoints are, a var so
// tests point it elsewhere.
var clineUsageAPI = "https://api.cline.bot/api/v1"

// clineLimits are ClinePass's limits as usage-limits names them, in the
// order the card shows them, with the plugin's names.
var clineLimits = []struct {
	typ, name string
	span      time.Duration
}{
	{"five_hour", "5 hours", 5 * time.Hour},
	{"weekly", "Weekly", 7 * 24 * time.Hour},
	{"monthly", "Month", 30 * 24 * time.Hour},
}

// clineKeyUsage is the ClinePass limits and the credit balance of the
// provider's key in use.
func clineKeyUsage(ctx context.Context, p Provider) (windows []QuotaWindow, balance string, err error) {
	ctx = p.Via(ctx)
	var lim struct {
		Limits []struct {
			Type        string   `json:"type"`
			PercentUsed *float64 `json:"percentUsed"`
			ResetsAt    string   `json:"resetsAt"`
		} `json:"limits"`
	}
	limErr := clineGet(ctx, p.Key, "/users/me/plan/usage-limits", &lim)
	for _, l := range clineLimits {
		for _, x := range lim.Limits {
			if x.Type != l.typ {
				continue
			}
			w := QuotaWindow{Name: l.name, Span: l.span}
			if x.PercentUsed != nil && !math.IsNaN(*x.PercentUsed) {
				w.Used = min(100, max(0, *x.PercentUsed))
			}
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(x.ResetsAt)); err == nil {
				w.ResetsAt = &t
			}
			windows = append(windows, w)
			break
		}
	}
	balance, balErr := clineCredits(ctx, p.Key)
	var nf clineStatus
	if errors.As(limErr, &nf) && nf == http.StatusNotFound {
		limErr = nil // no ClinePass on the account: its credits alone
	}
	if len(windows) == 0 && balance == "" {
		// neither read: the limits' refusal says most (a key Cline
		// doesn't take), else the balance's
		if limErr == nil {
			limErr = balErr
		}
		if limErr == nil {
			limErr = errors.New("Cline told neither limits nor a balance")
		}
		return nil, "", limErr
	}
	if limErr != nil {
		// the credits alone, but the limits failed for a reason other
		// than no ClinePass: the card says so beside the balance, rather
		// than look like an account without ClinePass (#79)
		return nil, balance, fmt.Errorf("%s: %w", clineLimitsUnread, limErr)
	}
	return windows, balance, nil
}

// clineLimitsUnread is how a card whose ClinePass limits failed to read
// begins its error, the words the Cline plugin uses too: the GUI says them
// (quotaError) beside the balance read.
const clineLimitsUnread = "ClinePass limits couldn't be read"

// clineCredits is the account's credit balance, {balance} in millionths of
// a dollar, at /users/{id}/balance with the id /users/me tells.
func clineCredits(ctx context.Context, key string) (string, error) {
	var me struct {
		ID          string `json:"id"`
		ClineUserID string `json:"clineUserId"`
		Subject     string `json:"subject"`
	}
	if err := clineGet(ctx, key, "/users/me", &me); err != nil {
		return "", err
	}
	var last error = errors.New("Cline told no user id")
	for _, id := range []string{me.ClineUserID, me.Subject, me.ID} {
		if strings.TrimSpace(id) == "" {
			continue
		}
		var b struct {
			Balance json.Number `json:"balance"`
		}
		if last = clineGet(ctx, key, "/users/"+url.PathEscape(id)+"/balance", &b); last != nil {
			continue
		}
		n, err := strconv.ParseFloat(b.Balance.String(), 64)
		if err != nil || n < 0 {
			return "", errors.New("Cline told no balance")
		}
		return clineUSD(n / 1e6), nil
	}
	return "", last
}

// clineUSD is dollars as the plugin writes them: cents, or four decimals
// under a cent.
func clineUSD(n float64) string {
	if n == 0 || n >= 0.01 {
		return fmt.Sprintf("$%.2f", n)
	}
	return "$" + strconv.FormatFloat(math.Round(n*1e4)/1e4, 'f', -1, 64)
}

// clineStatus is an HTTP status the Cline API answered with.
type clineStatus int

func (s clineStatus) Error() string {
	return fmt.Sprintf("Cline answered %d %s", int(s), http.StatusText(int(s)))
}

// clineGet asks the Cline API at path with the key and reads its
// {success, data} reply's data into out.
func clineGet(ctx context.Context, key, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clineUsageAPI+path, nil)
	if err != nil {
		return err
	}
	ClineClient(req.Header)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var env struct {
		Success *bool           `json:"success"`
		Error   string          `json:"error"`
		Data    json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(b, &env)
	if res.StatusCode >= 300 {
		if env.Error != "" {
			return fmt.Errorf("%w: %s", clineStatus(res.StatusCode), env.Error)
		}
		return clineStatus(res.StatusCode)
	}
	if env.Success != nil && !*env.Success {
		return fmt.Errorf("Cline refused: %s", env.Error)
	}
	if env.Success != nil || len(env.Data) > 0 {
		b = env.Data
	}
	return json.Unmarshal(b, out)
}

// clineKeyCard says the provider's key card is read as Cline's: a Cline
// API key with no Balance URL of the user's own.
func clineKeyCard(p Provider) bool {
	return p.IsCline() && p.Account == nil && p.Key != "" && strings.TrimSpace(p.BalanceURL) == ""
}

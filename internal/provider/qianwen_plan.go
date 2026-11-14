package provider

// The Qwen AI platform's Token Plan (platform.qianwenai.com, sk-sp- keys at
// token-plan.maas.qianwenaiapi.com) tells its monthly or weekly window only
// to a sign-in on the platform, never to the plan's key: the console's
// usage API answers the key BailianGateway.Login.NotLogined. magpie signs in
// and reads it as the platform's own client, the QianWen CLI
// (@qianwenai/qianwen-cli, `qianwen auth login` and its Token Plan usage),
// does:
//
//  1. POST t.qianwenai.com/cli/device/code?client_id&code_challenge
//     &code_challenge_method=S256 gives a page for the user to approve the
//     sign-in on, in their own browser, and a token to wait on;
//  2. POST t.qianwenai.com/cli/device/token?client_id&token&code_verifier,
//     every Interval seconds, answers Status authorization_pending until
//     the user approves, then complete with Credentials{AccessToken,
//     ExpireTime, User}. There is no refresh token: when it runs out the
//     user signs in again, as the CLI asks;
//  3. POST cli.qianwenai.com/data/v2/api.json with Bearer the access token
//     and {product: ModelStudioRegional, action: GenerateCLIAccessToken}
//     gives a console token, cliAccessToken;
//  4. POST cs-data.qianwenai.com/cli/api.json?action=BroadScopeAspnGateway
//     &product=sfm_bailian&api=…/tokenplan/personal/api/v2/usage with Bearer
//     that token answers as Alibaba Cloud's console does (aliAskUsage), with
//     consoleSite QIANWENAI: per1MonthPercentage or per1WeekPercentage and
//     their reset times.
//
// The sign-in is kept in qianwen-auth.json beside providers.json, readable
// by the user alone, one per provider, as StepFun's is (stepfun_plan.go).

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
)

const (
	qianwenAuthHost = "t.qianwenai.com"
	qianwenAPIHost  = "cli.qianwenai.com"
	qianwenDataHost = "cs-data.qianwenai.com"
	// qianwenUA is the User-Agent the platform's own client sends
	qianwenUA       = "qianwen-cli/1.10.0"
	qianwenRegion   = "cn-beijing"
	qianwenPlanHost = "token-plan.maas.qianwenaiapi.com"
)

// QianwenPlan says p is on the Qwen AI platform's Token Plan, whose usage
// magpie reads with a sign-in to the platform.
func QianwenPlan(p Provider) bool {
	if p.Account != nil {
		return false
	}
	for _, base := range []string{p.Chat, p.Anthropic, p.Responses} {
		if hostOf(base) == qianwenPlanHost {
			return true
		}
	}
	return false
}

// qianwenSession is a sign-in on the platform.
type qianwenSession struct {
	Access string `json:"access"`
	// Expires is when the platform said the access token runs out; zero
	// when it said nothing magpie could read
	Expires  time.Time `json:"expires,omitzero"`
	Email    string    `json:"email,omitempty"`
	User     string    `json:"user,omitempty"`
	ClientID string    `json:"clientId,omitempty"`
}

var qianwenMu sync.Mutex

func qianwenPath() string { return filepath.Join(filepath.Dir(Path()), "qianwen-auth.json") }

// loadQianwen is every provider's sign-in, by provider id; the caller holds
// qianwenMu. A file that doesn't parse is no sign-in, and is left as it
// is (saveQianwen keeps it aside before a sign-in writes over it).
func loadQianwen() map[string]qianwenSession {
	out := map[string]qianwenSession{}
	b, err := os.ReadFile(qianwenPath())
	if err != nil {
		return out
	}
	if json.Unmarshal(b, &out) != nil {
		return map[string]qianwenSession{}
	}
	return out
}

func saveQianwen(all map[string]qianwenSession) error {
	defer filememo.Forget() // read again, where a request holds it
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(qianwenPath()), 0o700); err != nil {
		return err
	}
	if err := lastgood.KeepUnparsed(qianwenPath(), lastgood.Into[map[string]qianwenSession]); err != nil {
		return err
	}
	return writeFileAtomic(qianwenPath(), b)
}

// QianwenSignedIn reports whether magpie has a sign-in for the provider,
// reading the file once until it changes: a provider list asks it on
// every poll.
func QianwenSignedIn(id string) bool {
	s, ok := qianwenSignIn(id)
	return ok && s.Access != ""
}

// QianwenAccount is who the provider's sign-in is, as the platform named
// them: an email, or "" when it gave none.
func QianwenAccount(id string) string {
	s, _ := qianwenSignIn(id)
	return s.Email
}

func qianwenSignIn(id string) (qianwenSession, bool) {
	all, err := filememo.Read("qianwen-auth", qianwenPath(), func(b []byte) (map[string]qianwenSession, error) {
		m := map[string]qianwenSession{}
		return m, json.Unmarshal(b, &m)
	})
	if err != nil {
		return qianwenSession{}, false
	}
	s, ok := all[id]
	return s, ok
}

// SignOutQianwen forgets the provider's sign-in, and no other's.
func SignOutQianwen(id string) error {
	CancelQianwenSignIn(id)
	qianwenMu.Lock()
	all := loadQianwen()
	_, had := all[id]
	delete(all, id)
	var err error
	if had {
		err = saveQianwen(all)
	}
	qianwenMu.Unlock()
	qianwenForgetToken(id)
	forgetPlanQuotas()
	return err
}

// forgetQianwen drops the provider's sign-in, when it is removed.
func forgetQianwen(id string) error { return SignOutQianwen(id) }

// renameQianwen moves the provider's sign-in with it to its new id.
func renameQianwen(from, to string) error {
	qianwenMu.Lock()
	defer qianwenMu.Unlock()
	all := loadQianwen()
	s, ok := all[from]
	if !ok {
		return nil
	}
	delete(all, from)
	all[to] = s
	qianwenForgetToken(from)
	return saveQianwen(all)
}

// a sign-in under way, by provider id
type qianwenFlow struct {
	url    string
	state  string // "pending", "done" or "failed"
	err    string
	cancel context.CancelFunc
}

var qianwenFlows = struct {
	sync.Mutex
	m map[string]*qianwenFlow
}{m: map[string]*qianwenFlow{}}

// qianwenPoll is how often the platform is asked whether the user has
// approved, when it says nothing itself; a test shortens it.
var qianwenPoll = 2 * time.Second

func qianwenUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// qianwenPKCE is a code verifier and its S256 challenge.
func qianwenPKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// qianwenPost posts to the platform as its client does, the reply read
// whole (up to 1MB).
func qianwenPost(ctx context.Context, u, contentType string, body []byte, bearer string) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", qianwenUA)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, b, err
}

// StartQianwenSignIn starts a sign-in to the Qwen AI platform for the
// provider id: the page the user approves it on, in their browser. magpie
// waits for the approval itself (QianwenSignInState), until the page runs
// out.
func StartQianwenSignIn(ctx context.Context, id string) (string, error) {
	clientID := qianwenUUID()
	verifier, challenge := qianwenPKCE()
	q := url.Values{"client_id": {clientID}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	code, raw, err := qianwenPost(ctx, "https://"+qianwenAuthHost+"/cli/device/code?"+q.Encode(), "", nil, "")
	if err != nil {
		return "", fmt.Errorf("couldn't reach the Qwen AI platform: %w", err)
	}
	var r struct {
		Success bool
		Data    *struct {
			Token           string
			VerificationURL string `json:"VerificationUrl"`
			ExpiresIn       float64
			Interval        float64
		}
	}
	if code >= 300 || json.Unmarshal(raw, &r) != nil || !r.Success || r.Data == nil || r.Data.Token == "" || r.Data.VerificationURL == "" {
		return "", fmt.Errorf("the Qwen AI platform didn't start a sign-in: %d %s", code, volcHead(raw))
	}
	life := time.Duration(r.Data.ExpiresIn) * time.Second
	if life <= 0 || life > 15*time.Minute {
		life = 5 * time.Minute
	}
	every := time.Duration(r.Data.Interval) * time.Second
	if every <= 0 {
		every = qianwenPoll
	}
	every = min(every, qianwenPoll)
	wait, cancel := context.WithTimeout(context.Background(), life)
	f := &qianwenFlow{url: r.Data.VerificationURL, state: "pending", cancel: cancel}
	qianwenFlows.Lock()
	if old := qianwenFlows.m[id]; old != nil && old.cancel != nil {
		old.cancel()
	}
	qianwenFlows.m[id] = f
	qianwenFlows.Unlock()
	go func() {
		defer cancel()
		err := qianwenWait(wait, id, clientID, r.Data.Token, verifier, every)
		qianwenFlows.Lock()
		defer qianwenFlows.Unlock()
		if qianwenFlows.m[id] != f {
			return // a newer sign-in, or one cancelled
		}
		if err != nil {
			f.state, f.err = "failed", err.Error()
			return
		}
		f.state = "done"
	}()
	return f.url, nil
}

// QianwenSignInState is how the provider's sign-in under way goes:
// "pending", "done" or "failed" with why, or "" when none is.
func QianwenSignInState(id string) (state, why string) {
	qianwenFlows.Lock()
	defer qianwenFlows.Unlock()
	f := qianwenFlows.m[id]
	if f == nil {
		return "", ""
	}
	return f.state, f.err
}

// CancelQianwenSignIn stops waiting on the provider's sign-in.
func CancelQianwenSignIn(id string) {
	qianwenFlows.Lock()
	defer qianwenFlows.Unlock()
	if f := qianwenFlows.m[id]; f != nil {
		if f.cancel != nil {
			f.cancel()
		}
		delete(qianwenFlows.m, id)
	}
}

var qianwenStatusWord = lazyre.New(`(?i)expired|access_denied|slow_down`)

// qianwenWait asks the platform whether the user approved the sign-in,
// until they have, refused, or the page ran out, and keeps it.
func qianwenWait(ctx context.Context, id, clientID, token, verifier string, every time.Duration) error {
	q := url.Values{"client_id": {clientID}, "token": {token}, "code_verifier": {verifier}}
	u := "https://" + qianwenAuthHost + "/cli/device/token?" + q.Encode()
	t := time.NewTimer(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errors.New("the sign-in page ran out before it was approved: sign in again")
			}
			return ctx.Err()
		case <-t.C:
		}
		code, raw, err := qianwenPost(ctx, u, "", nil, "")
		status := ""
		var r struct {
			Data struct {
				Status      string
				Credentials *struct {
					AccessToken string
					ExpireTime  any
					User        *struct{ ID, Email, AliyunID string }
				}
			}
		}
		switch {
		case err != nil:
			if ctx.Err() != nil {
				continue
			}
			status = "authorization_pending" // a dropped poll is asked again
		case code >= 300:
			// the client reads an error's text for these three
			status = strings.ToLower(qianwenStatusWord.FindString(string(raw)))
			if status == "expired" {
				status = "expired_token"
			}
			if status == "" {
				return fmt.Errorf("the Qwen AI platform: %d %s", code, volcHead(raw))
			}
		default:
			if json.Unmarshal(raw, &r) != nil {
				return fmt.Errorf("the Qwen AI platform answered in a form magpie doesn't know: %s", volcHead(raw))
			}
			status = strings.ToLower(r.Data.Status)
			if status == "" {
				status = "authorization_pending"
			}
		}
		switch status {
		case "authorization_pending":
		case "slow_down":
			every += every
		case "complete":
			c := r.Data.Credentials
			if c == nil || c.AccessToken == "" {
				return errors.New("the Qwen AI platform approved the sign-in but gave no token: sign in again")
			}
			s := qianwenSession{Access: c.AccessToken, Expires: qianwenTime(c.ExpireTime), ClientID: clientID}
			if c.User != nil {
				s.Email, s.User = c.User.Email, c.User.ID
			}
			qianwenMu.Lock()
			all := loadQianwen()
			all[id] = s
			err := saveQianwen(all)
			qianwenMu.Unlock()
			qianwenForgetToken(id)
			forgetPlanQuotas()
			return err
		case "expired_token":
			return errors.New("the sign-in page ran out before it was approved: sign in again")
		case "access_denied":
			return errors.New("the sign-in was refused on the Qwen AI platform's page")
		default:
			return fmt.Errorf("the Qwen AI platform ended the sign-in: %s", status)
		}
		t.Reset(every)
	}
}

// qianwenTime reads ExpireTime as the client's Date.parse would: a date
// and time, or a number of seconds or milliseconds; zero when it can't.
func qianwenTime(v any) time.Time {
	switch x := v.(type) {
	case float64:
		if t, ok := stepfunTime(x); ok {
			return t
		}
	case string:
		x = strings.TrimSpace(x)
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return qianwenTime(f)
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", time.RFC1123, time.RFC1123Z} {
			if t, err := time.ParseInLocation(layout, x, time.Local); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// errQianwenSession is a sign-in the platform no longer takes, or one past
// its time: it has no refresh token, so the user signs in again.
var errQianwenSession = errors.New("signed out of the Qwen AI platform: sign in again in the provider's settings")

// qianwenNoSignIn is why a Qwen AI platform plan's card has no windows
// while magpie has no sign-in for it.
func qianwenNoSignIn(p Provider) string {
	return "this plan's usage is told only to a sign-in on the Qwen AI platform: sign in in " + p.Name + "'s settings"
}

var (
	qianwenTokensMu sync.Mutex
	qianwenTokens   = map[string]aliToken{}
)

func qianwenForgetToken(id string) {
	qianwenTokensMu.Lock()
	delete(qianwenTokens, id)
	qianwenTokensMu.Unlock()
}

// qianwenMint makes the console token from the sign-in's access token.
func qianwenMint(ctx context.Context, access string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"product": "ModelStudioRegional", "action": aliACSAction, "region": qianwenRegion,
		"params": map[string]any{}, "version": aliACSVersion,
	})
	code, raw, err := qianwenPost(ctx, "https://"+qianwenAPIHost+"/data/v2/api.json", "application/json", body, access)
	if err != nil {
		return "", err
	}
	var r struct {
		Code            string `json:"code"`
		Message         string `json:"message"`
		SuccessResponse bool   `json:"successResponse"`
		Data            struct {
			CliAccessToken string `json:"cliAccessToken"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &r)
	tok := strings.TrimSpace(r.Data.CliAccessToken)
	switch {
	case r.Code == "200" && r.SuccessResponse && tok != "" && tok != access:
		return tok, nil
	case strings.Contains(r.Code, "NeedLogin"), strings.Contains(r.Code, "NotLogin"),
		code == http.StatusUnauthorized, code == http.StatusForbidden:
		return "", errQianwenSession
	case r.Code != "" || r.Message != "":
		return "", fmt.Errorf("the Qwen AI platform's %s: %s %s", aliACSAction, r.Code, r.Message)
	}
	return "", fmt.Errorf("the Qwen AI platform's %s gave no token: %d %s", aliACSAction, code, volcHead(raw))
}

// qianwenConsole is the platform's console gateway.
func qianwenConsole() aliConsole {
	return aliConsole{
		url:    "https://" + qianwenDataHost + "/cli/api.json?action=BroadScopeAspnGateway&product=sfm_bailian&api=" + url.QueryEscape(aliUsageAPI),
		region: qianwenRegion, site: "QIANWENAI", who: "the Qwen AI platform's console", ua: qianwenUA,
	}
}

// qianwenReadUsage reads the plan's windows with the provider's sign-in: the
// console token kept for it, made again once when the console says it
// isn't logged in; a fresh one refused is a sign-in ended.
func qianwenReadUsage(ctx context.Context, id string, now func() time.Time) ([]QuotaWindow, error) {
	s, ok := qianwenSignIn(id)
	if !ok || s.Access == "" {
		return nil, errQianwenSession
	}
	if !s.Expires.IsZero() && now().After(s.Expires) {
		return nil, errQianwenSession
	}
	for try := 0; ; try++ {
		qianwenTokensMu.Lock()
		t, kept := qianwenTokens[id]
		qianwenTokensMu.Unlock()
		fresh := !kept || try > 0 || now().Sub(t.at) > aliTokenTTL
		if fresh {
			tok, err := qianwenMint(ctx, s.Access)
			if err != nil {
				return nil, err
			}
			t = aliToken{tok, now()}
			qianwenTokensMu.Lock()
			qianwenTokens[id] = t
			qianwenTokensMu.Unlock()
		}
		raw, err := aliAskUsage(ctx, t.token, qianwenConsole())
		if err == errAliNotLogged {
			qianwenForgetToken(id)
			if !fresh {
				continue
			}
			return nil, errQianwenSession
		}
		if err != nil {
			return nil, err
		}
		ws := aliReadWindows(raw)
		if len(ws) == 0 {
			return nil, fmt.Errorf("the Qwen AI platform told no window for this account (it may have no personal Token Plan): %s", volcHead(raw))
		}
		return ws, nil
	}
}

// qianwenPlanQuota is p's plan as a card, read with its sign-in.
func qianwenPlanQuota(ctx context.Context, p Provider) SubscriptionQuota {
	q := SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, Plan: aliPlanName, Windows: []QuotaWindow{}}
	if !QianwenSignedIn(p.ID) {
		q.Error, q.NeedsAccessKey = qianwenNoSignIn(p), true
		return q
	}
	q.User = QianwenAccount(p.ID)
	ws, err := qianwenReadUsage(ctx, p.ID, time.Now)
	if err != nil {
		q.Error = err.Error()
		q.NeedsAccessKey = errors.Is(err, errQianwenSession)
		return q
	}
	q.Windows = ws
	return q
}

// qianwenPlanQuotas is a card for each Qwen AI platform plan in use: one
// per provider, as two can be two accounts. One magpie has no sign-in for
// says so, with the way to its settings.
func qianwenPlanQuotas(ctx context.Context) []SubscriptionQuota {
	var jobs []Provider
	for _, p := range All() {
		if p.Hidden || p.Off || !QianwenPlan(p) || !wantsCard(ctx, p.ID, "") {
			continue
		}
		jobs = append(jobs, p)
	}
	out := make([]SubscriptionQuota, len(jobs))
	var wg sync.WaitGroup
	for i, p := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = qianwenPlanQuota(p.Via(ctx), p)
			if out[i].Error == "" {
				// what routing goes by, read just now (KeyAllowance)
				noteKeyAllowance(p, out[i].Windows, time.Now())
			}
			if QianwenSignedIn(p.ID) {
				out[i] = keepReading(ctx, out[i], keyTag("qianwen", p.ID))
			}
		}()
	}
	wg.Wait()
	return out
}

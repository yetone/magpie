package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/qoder"
)

// qoderMu serializes checking, rotating and saving tokens, including sign-in
// and removal. Credentials are decoded into private values on every read.
var qoderMu sync.Mutex
var qoderClient = &http.Client{Timeout: 20 * time.Second}
var qoderAPI = qoder.APIHost

// A rotated pair whose disk write failed must never spend its predecessor
// again. Retry persisting this private blob on the next access.
var qoderPending = map[string]struct{ before, after json.RawMessage }{}

func qoderPendingKey(user string) string { return loginsPath() + ":" + strings.ToLower(user) }

func qoderCurrent(l savedLogin) (qoder.Credential, bool, bool) {
	key := qoderPendingKey(l.User)
	if p, ok := qoderPending[key]; ok {
		if string(p.before) == string(l.Auth) {
			c, valid := qoderSaved(savedLogin{Auth: p.after})
			return c, valid, true
		}
		delete(qoderPending, key)
	}
	c, ok := qoderSaved(l)
	return c, ok, false
}

// qoderRefreshTimeout bounds a token refresh, which runs apart from the
// request that needed it: once Qoder has rotated the pair, the reply must be
// kept even if that request is gone.
const qoderRefreshTimeout = 20 * time.Second

// qoderLookup reads one saved account. loginsMu is held only for the read, so
// a Qoder refresh never stalls the other subscriptions; qoderMu, held by the
// caller, keeps every Qoder auth write out while it is refreshed.
func qoderLookup(user string) (savedLogin, bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "qoder" && strings.EqualFold(l.User, user) {
			return l, true
		}
	}
	return savedLogin{}, false
}

// qoderPersist writes c back into the account l as logins.json is now. The
// caller holds qoderMu, so l.Auth is still what is saved; the rest of the
// file is re-read under loginsMu so changes made meanwhile are kept.
func qoderPersist(l savedLogin, c qoder.Credential, renewed bool) error {
	auth, err := json.Marshal(c)
	if err != nil {
		return err
	}
	key := qoderPendingKey(l.User)
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	err = fmt.Errorf("Qoder: couldn't read the saved sign-in of %s back", l.User)
	for i := range ls {
		if ls[i].Agent != "qoder" || !strings.EqualFold(ls[i].User, l.User) {
			continue
		}
		ls[i].Auth = auth
		if renewed {
			ls[i].Renewed, ls[i].Lapsed = time.Now().UTC(), ""
		}
		err = writeLogins(ls)
		break
	}
	if err != nil {
		qoderPending[key] = struct{ before, after json.RawMessage }{l.Auth, auth}
		return err
	}
	delete(qoderPending, key)
	return nil
}

// qoderRefreshFailed marks the account lapsed when Qoder refused its refresh
// token: that sign-in is gone and has to be made again. A refresh that never
// got an answer marks nothing.
func qoderRefreshFailed(user string, err error) error {
	var job *qoder.JobTokenRefreshHTTPError
	var device *qoder.DeviceTokenRefreshHTTPError
	status := 0
	switch {
	case errors.As(err, &job):
		status = job.StatusCode
	case errors.As(err, &device):
		status = device.StatusCode
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return err
	}
	msg := user + "'s Qoder sign-in has expired — sign in again"
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == "qoder" && strings.EqualFold(ls[i].User, user) {
			ls[i].Lapsed = msg
		}
	}
	_ = writeLogins(ls)
	return fmt.Errorf("%s (%w)", msg, err)
}

func qoderWho(c qoder.Credential) string { return firstNonEmpty(c.Email, c.UID) }

func qoderSaved(l savedLogin) (qoder.Credential, bool) {
	var c qoder.Credential
	err := json.Unmarshal(l.Auth, &c)
	return c, err == nil && c.UID != "" && c.Token != ""
}

// migrateQoder moves the old single-account file exactly once. A newer
// logins.json entry takes precedence; failed writes leave the old file intact.
// With nothing to move it takes no lock, so listing accounts doesn't wait
// behind a refresh.
func migrateQoder() error {
	path := filepath.Join(filepath.Dir(Path()), "qoder.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	qoderMu.Lock()
	defer qoderMu.Unlock()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var c qoder.Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	if c.UID == "" || c.Token == "" {
		return fmt.Errorf("Qoder: unreadable legacy sign-in")
	}
	if c.MachineID == "" {
		c.MachineID = qoder.NewMachineID()
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	found := false
	for _, l := range ls {
		if l.Agent == "qoder" {
			old, ok := qoderSaved(l)
			if ok && old.UID == c.UID {
				found = true
				break
			}
		}
	}
	if !found {
		auth, err := json.Marshal(c)
		if err != nil {
			return err
		}
		ls = append(ls, savedLogin{Agent: "qoder", User: qoderWho(c), Auth: auth, On: true, Seen: time.Now().UTC()})
		if err := writeLogins(ls); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func qoderLogins() []sideLogin {
	if migrateQoder() != nil {
		return nil
	}
	ls := sideLogins("qoder", "", func(l savedLogin) bool { _, ok := qoderSaved(l); return ok })
	for i := range ls {
		ls[i].Lapsed = ls[i].saved.Lapsed // a refused refresh shows on the account
	}
	return ls
}

func QoderSignedIn() bool { return len(qoderLogins()) > 0 }

// QoderCredential returns an independent snapshot for the selected account.
func QoderCredential(ctx context.Context, user string) (*qoder.Credential, error) {
	if err := migrateQoder(); err != nil {
		return nil, err
	}
	if user == "" {
		ls := qoderLogins()
		if len(ls) == 0 {
			return nil, fmt.Errorf("Qoder: not signed in")
		}
		user = ls[0].User
	}
	qoderMu.Lock()
	defer qoderMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, found := qoderLookup(user)
	if !found {
		return nil, fmt.Errorf("no Qoder account %q", user)
	}
	c, ok, changed := qoderCurrent(l)
	if !ok {
		return nil, fmt.Errorf("Qoder: unreadable sign-in")
	}
	if c.MachineID == "" {
		c.MachineID = qoder.NewMachineID()
		changed = true
	}
	renewed := false
	if !c.Valid() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), qoderRefreshTimeout)
		fresh, err := c.Refresh(rctx, qoderClient)
		cancel()
		if err != nil {
			return nil, qoderRefreshFailed(l.User, err)
		}
		c, changed, renewed = fresh, true, true
	}
	if changed {
		if err := qoderPersist(l, c, renewed); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

func qoderUser(c *qoder.Credential) *qoder.User {
	return &qoder.User{UID: c.UID, Token: c.Token, Name: c.Name, Email: c.Email, MachineID: c.MachineID}
}

func qoderSave(c qoder.Credential) error {
	if c.UID == "" || c.Token == "" {
		return fmt.Errorf("Qoder: incomplete sign-in")
	}
	if err := migrateQoder(); err != nil {
		return err
	}
	qoderMu.Lock()
	defer qoderMu.Unlock()
	if c.MachineID == "" {
		c.MachineID = qoder.NewMachineID()
	}
	auth, err := json.Marshal(c)
	if err != nil {
		return err
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent != "qoder" {
			continue
		}
		old, ok := qoderSaved(ls[i])
		if ok && old.UID == c.UID {
			oldKey := qoderPendingKey(ls[i].User)
			ls[i].Auth, ls[i].User, ls[i].Seen, ls[i].Lapsed = auth, qoderWho(c), time.Now().UTC(), ""
			if err := writeLogins(ls); err != nil {
				return err
			}
			delete(qoderPending, oldKey)
			return nil
		}
	}
	return writeLogins(append(ls, savedLogin{Agent: "qoder", User: qoderWho(c), Auth: auth, On: true, Seen: time.Now().UTC()}))
}

func qoderFetchModels(ctx context.Context, user string) ([]catalog.Model, error) {
	c, err := QoderCredential(ctx, user)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := qoder.FetchModels(ctx, qoderClient, qoderAPI, qoderUser(c))
	if err != nil {
		return nil, err
	}
	ms, err := qoder.ParseModels(raw)
	if err != nil {
		return nil, err
	}
	qoderMu.Lock()
	err = editSideLogin("qoder", qoderWho(*c), func(ls []savedLogin, i int) ([]savedLogin, error) {
		latest, ok := qoderSaved(ls[i])
		if !ok || latest.UID != c.UID {
			return nil, fmt.Errorf("Qoder: account changed while fetching models")
		}
		latest.Models = raw
		ls[i].Auth, _ = json.Marshal(latest)
		return ls, nil
	})
	qoderMu.Unlock()
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive("qoder", "", ms)
}

// QoderModel accepts only an enabled model in this account's own model list.
func QoderModel(ctx context.Context, user, key string) (qoder.ModelInfo, error) {
	c, err := QoderCredential(ctx, user)
	if err != nil {
		return qoder.ModelInfo{}, err
	}
	if len(c.Models) == 0 {
		if _, err := qoderFetchModels(ctx, qoderWho(*c)); err != nil {
			return qoder.ModelInfo{}, err
		}
		c, err = QoderCredential(ctx, qoderWho(*c))
		if err != nil {
			return qoder.ModelInfo{}, err
		}
	}
	ms, err := qoder.ModelConfigs(c.Models)
	if err != nil {
		return qoder.ModelInfo{}, err
	}
	for _, m := range ms {
		if m.Key == key {
			return m, nil
		}
	}
	return qoder.ModelInfo{}, fmt.Errorf("Qoder: unknown or disabled model %q", key)
}

func qoderProvider(l sideLogin) Provider {
	user := l.User
	a := &Account{Agent: "qoder", User: user, Plan: l.Plan, Stream: true}
	a.models = func() []catalog.Model {
		loginsMu.Lock()
		defer loginsMu.Unlock()
		for _, l := range readLogins() {
			if l.Agent == "qoder" && strings.EqualFold(l.User, user) {
				c, _ := qoderSaved(l)
				ms, _ := qoder.ParseModels(c.Models)
				return ms
			}
		}
		return nil
	}
	a.fetch = func(ctx context.Context) ([]catalog.Model, error) { return qoderFetchModels(ctx, user) }
	return Provider{ID: "qoder", Name: "Qoder", Icon: "qoder", Website: "https://qoder.com", Account: a}
}

func qoderAccount() (Provider, bool) {
	ls := qoderLogins()
	if len(ls) == 0 {
		return Provider{}, false
	}
	return qoderProvider(ls[0]), true
}

func qoderAlsoOn() []Provider {
	var out []Provider
	for _, l := range qoderLogins() {
		if !l.Active && l.On {
			out = append(out, qoderProvider(l))
		}
	}
	return out
}

func forgetQoderLogin(user string) error {
	if err := migrateQoder(); err != nil {
		return err
	}
	qoderMu.Lock()
	defer qoderMu.Unlock()
	err := editSideLogin("qoder", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		return append(ls[:i], ls[i+1:]...), nil
	})
	if err == nil {
		delete(qoderPending, qoderPendingKey(user))
		forgetAccountCaches()
	}
	return err
}

type qoderFlow struct {
	verifier, nonce string
	client          *qoder.DeviceFlow
	deadline        time.Time
}

const qoderSignInTimeout = 15 * time.Minute

func QoderAuthURL() (url string, flow *qoderFlow, err error) {
	f := qoder.NewDeviceFlow(qoderClient)
	url, verifier, nonce, err := f.Authorization()
	if err != nil {
		return "", nil, err
	}
	return url, &qoderFlow{verifier: verifier, nonce: nonce, client: f, deadline: time.Now().Add(qoderSignInTimeout)}, nil
}

func QoderCompleteSignIn(ctx context.Context, fl *qoderFlow) (user string, err error) {
	ctx, cancel := context.WithDeadline(ctx, fl.deadline)
	defer cancel()
	dt, err := fl.client.PollDeviceToken(ctx, fl.nonce, fl.verifier, 2*time.Second)
	if err != nil {
		return "", err
	}
	jt, err := fl.client.JobToken(ctx, dt.Token)
	if err != nil {
		return "", err
	}
	life := jt.Expiry()
	if life <= 0 {
		life = 24 * time.Hour
	}
	cred := qoder.Credential{UID: dt.UserID, Token: jt.Token, RefreshToken: jt.RefreshToken,
		DeviceToken: dt.Token, DeviceRefresh: dt.RefreshToken, MachineID: fl.client.MachineID(),
		ExpiresAt: time.Now().Add(life).UnixMilli()}
	if ui, err := qoder.FetchUserInfo(ctx, qoderClient, dt.Token); err == nil && ui != nil {
		cred.Email, cred.Name = ui.Email, ui.Name
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := qoderSave(cred); err != nil {
		return "", err
	}
	_, _ = qoderFetchModels(ctx, qoderWho(cred))
	forgetAccountCaches()
	return qoderWho(cred), nil
}

func startQoderSignIn(s *signInFlow) error {
	authURL, fl, err := QoderAuthURL()
	if err != nil {
		return fmt.Errorf("Qoder sign-in: %w", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), fl.deadline)
	s.mu.Lock()
	s.st.URL, s.stop = authURL, cancel
	s.mu.Unlock()
	go func() {
		defer cancel()
		user, err := QoderCompleteSignIn(ctx, fl)
		if err != nil {
			s.finish(SignInState{State: "failed", Error: "Qoder: " + err.Error()})
			return
		}
		ls := qoderLogins()
		s.finish(SignInState{State: "done", User: user, Using: strings.EqualFold(activeOf(ls), user)})
	}()
	return nil
}

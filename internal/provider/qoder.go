package provider

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

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

// Qoder's two sites are two subscriptions: "qoder" (qoder.com) and "qoder-cn"
// (qoder.cn), whose accounts exist only there. Each keeps its own accounts in
// logins.json under its id, and each credential names its site.
var qoderAgents = []string{qoder.ProviderKey, QoderCNID}

// QoderCNID is the Qoder CN subscription's id, the same as Qoder CN's agent.
const QoderCNID = qoder.CNProviderKey

func qoderSiteOf(agent string) *qoder.Site { return qoder.SiteOf(agent) }

// A rotated pair whose disk write failed must never spend its predecessor
// again. Retry persisting this private blob on the next access.
var qoderPending = map[string]struct{ before, after json.RawMessage }{}

func qoderPendingKey(agent, user string) string {
	if agent != qoder.ProviderKey {
		user = agent + ":" + user
	}
	return loginsPath() + ":" + strings.ToLower(user)
}

func qoderCurrent(l savedLogin) (qoder.Credential, bool, bool) {
	key := qoderPendingKey(l.Agent, l.User)
	if p, ok := qoderPending[key]; ok {
		if string(p.before) == string(l.Auth) {
			c, valid := qoderSaved(savedLogin{Agent: l.Agent, Auth: p.after})
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
func qoderLookup(agent, user string) (savedLogin, bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
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
	key := qoderPendingKey(l.Agent, l.User)
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	err = fmt.Errorf("%s: couldn't read the saved sign-in of %s back", qoder.SiteOf(l.Agent).Name, l.User)
	for i := range ls {
		if ls[i].Agent != l.Agent || !strings.EqualFold(ls[i].User, l.User) {
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

// ErrQoderSignIn is under every error that says the Qoder sign-in itself is
// gone — not signed in, unreadable, or its refresh refused — as against a
// refresh that timed out or never reached Qoder, which may pass.
var ErrQoderSignIn = errors.New("Qoder sign-in")

// qoderRefreshFailed marks the account lapsed when Qoder refused its job
// refresh token: that sign-in is gone and has to be made again. A refresh
// that never got an answer marks nothing.
func qoderRefreshFailed(agent, user string, err error) error {
	var job *qoder.JobTokenRefreshHTTPError
	if !errors.As(err, &job) || job.StatusCode != http.StatusUnauthorized && job.StatusCode != http.StatusForbidden {
		return err
	}
	msg := user + "'s " + qoder.SiteOf(agent).Name + " sign-in has expired — sign in again"
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == agent && strings.EqualFold(ls[i].User, user) {
			ls[i].Lapsed = msg
		}
	}
	_ = writeLogins(ls)
	return fmt.Errorf("%s (%w)", msg, errors.Join(ErrQoderSignIn, err))
}

func qoderWho(c qoder.Credential) string { return firstNonEmpty(c.Email, c.UID) }

func qoderSaved(l savedLogin) (qoder.Credential, bool) {
	var c qoder.Credential
	err := json.Unmarshal(l.Auth, &c)
	if l.Agent == qoder.CNProviderKey {
		c.Site = qoder.CNProviderKey // the account's list says its site, whatever the blob does
	}
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

// qoderLogins are the global site's accounts.
func qoderLogins() []sideLogin { return qoderLoginsOf(qoder.ProviderKey) }

func qoderLoginsOf(agent string) []sideLogin {
	if agent == qoder.ProviderKey && migrateQoder() != nil {
		return nil
	}
	ls := sideLogins(agent, "", func(l savedLogin) bool { _, ok := qoderSaved(l); return ok })
	for i := range ls {
		ls[i].Lapsed = ls[i].saved.Lapsed // a refused refresh shows on the account
	}
	return ls
}

func QoderSignedIn() bool { return len(qoderLogins()) > 0 }

// QoderCredential returns an independent snapshot for the selected account
// on the global site.
func QoderCredential(ctx context.Context, user string) (*qoder.Credential, error) {
	return QoderCredentialOf(ctx, qoder.ProviderKey, user)
}

// QoderCredentialOf is QoderCredential for the site agent names ("qoder" or
// "qoder-cn").
func QoderCredentialOf(ctx context.Context, agent, user string) (*qoder.Credential, error) {
	name := qoder.SiteOf(agent).Name
	if agent == qoder.ProviderKey {
		if err := migrateQoder(); err != nil {
			return nil, err
		}
	}
	if user == "" {
		ls := qoderLoginsOf(agent)
		if len(ls) == 0 {
			return nil, fmt.Errorf("%s: not signed in (%w)", name, ErrQoderSignIn)
		}
		user = ls[0].User
	}
	qoderMu.Lock()
	defer qoderMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, found := qoderLookup(agent, user)
	if !found {
		return nil, fmt.Errorf("no %s account %q (%w)", name, user, ErrQoderSignIn)
	}
	c, ok, changed := qoderCurrent(l)
	if !ok {
		return nil, fmt.Errorf("%s: unreadable sign-in (%w)", name, ErrQoderSignIn)
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
			if c.RefreshToken == "" { // nothing to refresh with: sign in again
				err = errors.Join(ErrQoderSignIn, err)
			}
			return nil, qoderRefreshFailed(agent, l.User, err)
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

// qoderSave keeps c under its site's accounts.
func qoderSave(c qoder.Credential) error {
	site := c.OnSite()
	agent := site.ID
	if c.UID == "" || c.Token == "" {
		return fmt.Errorf("%s: incomplete sign-in", site.Name)
	}
	if agent == qoder.ProviderKey {
		c.Site = "" // a global sign-in is saved as it always was
		if err := migrateQoder(); err != nil {
			return err
		}
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
		if ls[i].Agent != agent {
			continue
		}
		old, ok := qoderSaved(ls[i])
		if ok && old.UID == c.UID {
			oldKey := qoderPendingKey(agent, ls[i].User)
			ls[i].Auth, ls[i].User, ls[i].Seen, ls[i].Lapsed = auth, qoderWho(c), time.Now().UTC(), ""
			if err := writeLogins(ls); err != nil {
				return err
			}
			delete(qoderPending, oldKey)
			return nil
		}
	}
	return writeLogins(append(ls, savedLogin{Agent: agent, User: qoderWho(c), Auth: auth, On: true, Seen: time.Now().UTC()}))
}

func qoderFetchModels(ctx context.Context, agent, user string) ([]catalog.Model, error) {
	c, err := QoderCredentialOf(ctx, agent, user)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := qoder.FetchModels(ctx, qoderClient, c.OnSite().API, qoderUser(c))
	if err != nil {
		return nil, err
	}
	ms, err := qoder.ParseModels(raw, agent)
	if err != nil {
		return nil, err
	}
	qoderMu.Lock()
	err = editSideLogin(agent, qoderWho(*c), func(ls []savedLogin, i int) ([]savedLogin, error) {
		latest, ok := qoderSaved(ls[i])
		if !ok || latest.UID != c.UID {
			return nil, fmt.Errorf("%s: account changed while fetching models", qoder.SiteOf(agent).Name)
		}
		latest.Models = raw
		ls[i].Auth, _ = json.Marshal(latest)
		return ls, nil
	})
	qoderMu.Unlock()
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive(agent, "", ms)
}

// QoderModel accepts only an enabled model in this account's own model list.
func QoderModel(ctx context.Context, user, key string) (qoder.ModelInfo, error) {
	return QoderModelOf(ctx, qoder.ProviderKey, user, key)
}

// QoderModelOf is QoderModel for the site agent names.
func QoderModelOf(ctx context.Context, agent, user, key string) (qoder.ModelInfo, error) {
	c, err := QoderCredentialOf(ctx, agent, user)
	if err != nil {
		return qoder.ModelInfo{}, err
	}
	if len(c.Models) == 0 {
		if _, err := qoderFetchModels(ctx, agent, qoderWho(*c)); err != nil {
			return qoder.ModelInfo{}, err
		}
		c, err = QoderCredentialOf(ctx, agent, qoderWho(*c))
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
	return qoder.ModelInfo{}, fmt.Errorf("%s: unknown or disabled model %q", qoder.SiteOf(agent).Name, key)
}

func qoderProvider(agent string, l sideLogin) Provider {
	user := l.User
	site := qoder.SiteOf(agent)
	a := &Account{Agent: agent, User: user, Plan: l.Plan, Stream: true}
	a.models = func() []catalog.Model {
		loginsMu.Lock()
		defer loginsMu.Unlock()
		for _, l := range readLogins() {
			if l.Agent == agent && strings.EqualFold(l.User, user) {
				c, _ := qoderSaved(l)
				ms, _ := qoder.ParseModels(c.Models, agent)
				return ms
			}
		}
		return nil
	}
	a.fetch = func(ctx context.Context) ([]catalog.Model, error) { return qoderFetchModels(ctx, agent, user) }
	return Provider{ID: agent, Name: site.Name, Icon: "qoder", Website: site.Web, Account: a}
}

func qoderAccount() (Provider, bool) { return qoderAccountOf(qoder.ProviderKey) }

func qoderAccountOf(agent string) (Provider, bool) {
	ls := qoderLoginsOf(agent)
	if len(ls) == 0 {
		return Provider{}, false
	}
	return qoderProvider(agent, ls[0]), true
}

func qoderAlsoOn(agent string) []Provider {
	var out []Provider
	for _, l := range qoderLoginsOf(agent) {
		if !l.Active && l.On {
			out = append(out, qoderProvider(agent, l))
		}
	}
	return out
}

func forgetQoderLogin(agent, user string) error {
	if agent == qoder.ProviderKey {
		if err := migrateQoder(); err != nil {
			return err
		}
	}
	qoderMu.Lock()
	defer qoderMu.Unlock()
	err := editSideLogin(agent, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		return append(ls[:i], ls[i+1:]...), nil
	})
	if err == nil {
		delete(qoderPending, qoderPendingKey(agent, user))
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

// QoderAuthURL starts a sign-in on the global site.
func QoderAuthURL() (url string, flow *qoderFlow, err error) { return qoderAuthURL(qoder.Global) }

func qoderAuthURL(site *qoder.Site) (url string, flow *qoderFlow, err error) {
	f := qoder.NewDeviceFlow(qoderClient, site)
	url, verifier, nonce, err := f.Authorization()
	if err != nil {
		return "", nil, err
	}
	return url, &qoderFlow{verifier: verifier, nonce: nonce, client: f, deadline: time.Now().Add(qoderSignInTimeout)}, nil
}

func QoderCompleteSignIn(ctx context.Context, fl *qoderFlow) (user string, err error) {
	ctx, cancel := context.WithDeadline(ctx, fl.deadline)
	defer cancel()
	site := fl.client.Site()
	dt, err := fl.client.PollDeviceToken(ctx, fl.nonce, fl.verifier, 2*time.Second)
	if err != nil {
		return "", err
	}
	cred := qoder.Credential{UID: dt.UserID, DeviceToken: dt.Token, DeviceRefresh: dt.RefreshToken,
		MachineID: fl.client.MachineID()}
	if site != qoder.Global {
		cred.Site = site.ID
	}
	jt, err := fl.client.JobToken(ctx, dt.Token)
	var refused *qoder.JobTokenHTTPError
	switch {
	case err == nil:
		life := jt.Expiry()
		if life <= 0 {
			life = 24 * time.Hour
		}
		cred.Token, cred.RefreshToken, cred.ExpiresAt = jt.Token, jt.RefreshToken, time.Now().Add(life).UnixMilli()
	case site == qoder.CN && errors.As(err, &refused) && refused.StatusCode/100 == 4:
		// Qoder CN's CLI never makes a job token: its device token is what
		// signs the model calls. If qoder.cn won't make one for the CLI's
		// client id, the account works the CLI's way.
		cred.Token, cred.RefreshToken, cred.DeviceChat = dt.Token, dt.RefreshToken, true
		cred.ExpiresAt = qoder.DeviceExpiry(*dt).UnixMilli()
	default:
		return "", err
	}
	if ui, err := qoder.FetchUserInfo(ctx, qoderClient, site, dt.Token); err == nil && ui != nil {
		cred.Email, cred.Name = ui.Email, ui.Name
	}
	if cred.Name == "" {
		cred.Name = dt.UserName
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := qoderSave(cred); err != nil {
		return "", err
	}
	_, _ = qoderFetchModels(ctx, site.ID, qoderWho(cred))
	forgetAccountCaches()
	return qoderWho(cred), nil
}

func startQoderSignIn(s *signInFlow, site *qoder.Site) error {
	authURL, fl, err := qoderAuthURL(site)
	if err != nil {
		return fmt.Errorf("%s sign-in: %w", site.Name, err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), fl.deadline)
	s.mu.Lock()
	s.st.URL, s.stop = authURL, cancel
	s.mu.Unlock()
	go func() {
		defer cancel()
		user, err := QoderCompleteSignIn(ctx, fl)
		if err != nil {
			s.finish(SignInState{State: "failed", Error: site.Name + ": " + err.Error()})
			return
		}
		ls := qoderLoginsOf(site.ID)
		s.finish(SignInState{State: "done", User: user, Using: strings.EqualFold(activeOf(ls), user)})
	}()
	return nil
}

package provider

// PLUGIN-SERVED (see AGENTS.md): ZCode ("zcode") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zcode-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zcode) and raise the
// mover's min in internal/provider/migrate_zcode.go.

// A team's GLM Coding Plan (团队套餐), bought by an organization on Z.ai or
// BigModel (智谱) and handed out as seats to its members, is served where a
// person's plan is (api.z.ai/api/anthropic, open.bigmodel.cn/api/anthropic)
// but to a key of the team's project, not of the member's own. As ZCode
// 3.14.3 does it (host/index.js):
//
//   - the account's organizations (getCustomerInfo) have the team's
//     project, of projectType 2 (isBigModelTeamCodingPlanProject);
//   - the seat is told by /api/biz/team/subscribe/product/querySubscribeDetail
//     asked with the business sign-in and the project's bigmodel-organization
//     and bigmodel-project headers: usable when status is EFFECTIVE and
//     memberGrantStatus VALID, UNASSIGNED when the admin gave no seat,
//     EXPIRED when the team's plan ran out (fetchTeamCodingPlanEntitlement);
//   - the key is the project's zcode-team-api-key of keyType 2, made when it
//     isn't there, with its secret (ensureBigModelTeamPlanProjectApiKeyWithStatus,
//     copyBigModelTeamPlanProjectApiKeySecret); requests carry it as a
//     person's key is carried, with no more headers;
//   - what is used is /api/monitor/usage/quota/limit?type=2 with that key and
//     the two headers (buildQuotaLimitUrl, createBigModelUsageHeaders): a
//     CREDIT_LIMIT for the five hours (unit 3) and one for the week (unit 6).
//
// BigModel's business API is bigmodel.cn, Z.ai's api.z.ai; BigModel's
// sign-in token goes bare in Authorization, Z.ai's business one as a
// Bearer. The resets a team member may spend are listed by
// /api/biz/customer-package-reset/list?targetType=TEAM, as bigmodel.cn's
// own usage page asks it.
//
// ZCode keeps no team key in its credential store: it finds it when it is
// switched to the team's plan (~/.zcode/v2/setting.json's
// providerFamilyConnectionSelections, resolveAccountTeamPlanRuntimeApiKey),
// and so does magpie for ZCode's own account.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func (k zcodeKey) team() bool { return k.Org != "" && k.Project != "" }

// zcodeBizRoot is the business API of the site a plan is served from.
func zcodeBizRoot(base string) string {
	if h := hostOf(base); h == hostOf(ZCodeBigModelBase) || strings.HasSuffix(h, "bigmodel.cn") {
		return zcodeBigModelAPI
	}
	return zcodeRoot(base)
}

// zcodeTeamHeaders are what the team's business and usage endpoints are
// asked with besides the Authorization.
func zcodeTeamHeaders(base, org, project string) map[string]string {
	lang := "en"
	if zcodeBizRoot(base) == zcodeBigModelAPI {
		lang = "zh"
	}
	return map[string]string{
		"Bigmodel-Organization": org,
		"Bigmodel-Project":      project,
		"Set-Language":          lang,
		"Accept-Language":       "en-US,en",
	}
}

// zcodeTeamRefusal is why a sign-in found a team but no seat on its plan.
type zcodeTeamRefusal string

type zcodeTeamProject struct{ org, project string }

// teamProjects are the account's team coding plan projects.
func (c zcodeCustomer) teamProjects() []zcodeTeamProject {
	var out []zcodeTeamProject
	for _, o := range c.Orgs {
		for _, p := range o.Projects {
			if o.ID != "" && p.ID != "" && fmt.Sprint(p.Type) == "2" {
				out = append(out, zcodeTeamProject{o.ID, p.ID})
			}
		}
	}
	return out
}

// zcodeTeamDetail is a team seat, as querySubscribeDetail tells it.
type zcodeTeamDetail struct {
	Has     *bool  `json:"hasSubscription"`
	Status  string `json:"status"`
	Grant   string `json:"memberGrantStatus"`
	Product string `json:"productName"`
	ID      any    `json:"productId"`
	End     any    `json:"subscribeEndTime"`
}

func (d zcodeTeamDetail) usable() bool {
	return (d.Has == nil || *d.Has) && strings.EqualFold(d.Status, "EFFECTIVE") && strings.EqualFold(d.Grant, "VALID")
}

// end is when the team's plan runs out: a time in Beijing, or a Unix time
// in seconds or milliseconds.
func (d zcodeTeamDetail) end() *time.Time {
	return zcodeWhen(d.End)
}

func zcodeWhen(v any) *time.Time {
	if s, ok := v.(string); ok {
		if t := zhipuTime(s); t != nil {
			return t
		}
	}
	if n, ok := zcodeNum(v); ok && n > 0 {
		var t time.Time
		if n > 1e12 {
			t = time.UnixMilli(int64(n))
		} else {
			t = time.Unix(int64(n), 0)
		}
		return &t
	}
	return nil
}

func zcodeTeamDetailOf(ctx context.Context, root, auth, base, org, project string) (zcodeTeamDetail, error) {
	var d zcodeTeamDetail
	err := zcodeCallH(ctx, http.MethodGet, root+"/api/biz/team/subscribe/product/querySubscribeDetail", auth, zcodeTeamHeaders(base, org, project), nil, &d)
	return d, err
}

// zcodeTeamSignIn is the account's seat on a team's plan: the first team
// project whose plan is in force and gives it a seat, and that project's
// key. When there is none, why says what there was.
func zcodeTeamSignIn(ctx context.Context, site, root, auth string, info zcodeCustomer) (k zcodeKey, plan string, why zcodeTeamRefusal, err error) {
	base := zcodeSiteBase(site)
	ps := info.teamProjects()
	if len(ps) == 0 {
		return zcodeKey{}, "", "", errors.New("no team plan")
	}
	var last error
	for _, p := range ps {
		d, err := zcodeTeamDetailOf(ctx, root, auth, base, p.org, p.project)
		switch {
		case err != nil:
			last = err
			continue
		case d.usable():
		case strings.EqualFold(d.Status, "EFFECTIVE") && strings.EqualFold(d.Grant, "UNASSIGNED"):
			if why == "" {
				why = zcodeTeamRefusal(fmt.Sprintf("this %s account is in a team with a GLM Coding Plan but has no seat on it yet — ask the team's admin to give it one, then add it again", zcodeSiteName(site)))
			}
			continue
		case strings.EqualFold(d.Status, "EXPIRED"):
			if why == "" {
				why = zcodeTeamRefusal(fmt.Sprintf("the %s team's GLM Coding Plan this account is in has expired", zcodeSiteName(site)))
			}
			continue
		default:
			continue
		}
		keys := root + "/api/biz/v1/organization/" + url.PathEscape(p.org) + "/projects/" + url.PathEscape(p.project) + "/api_keys"
		key, err := zcodeProjectKey(ctx, site, keys, auth, zcodeTeamHeaders(base, p.org, p.project), map[string]any{"name": "zcode-team-api-key", "keyType": 2})
		if err != nil {
			return zcodeKey{}, "", "", fmt.Errorf("the team's GLM Coding Plan: %w", err)
		}
		return zcodeKey{Key: key, Base: base, Token: auth, Org: p.org, Project: p.project}, firstNonEmpty(d.Product, "GLM Coding Team"), "", nil
	}
	if why != "" {
		return zcodeKey{}, "", why, errors.New(string(why))
	}
	return zcodeKey{}, "", "", fmt.Errorf("the team's GLM Coding Plan: %v", firstErr(last, "no seat on it"))
}

// ---- ZCode's own account on a team's plan -------------------------------------

// zcodeOwnTeam is ZCode's own account when ZCode is switched to a team's
// plan: the project it picked and the sign-in its key is found with.
func zcodeOwnTeam(store map[string]string, secret []byte) (zcodeKey, bool) {
	home, _ := os.UserHomeDir()
	var set struct {
		Domain string `json:"providerFamilyDomain"`
		Sel    map[string]struct {
			Kind    string `json:"kind"`
			Org     string `json:"organizationId"`
			Project string `json:"projectId"`
		} `json:"providerFamilyConnectionSelections"`
		Legacy map[string]string `json:"modelProviderFamilySelectedKeys"` // before providerFamilyConnectionSelections (migrateLegacyAccountConnectionSettings)
	}
	if !readJSON(filepath.Join(home, ".zcode", "v2", "setting.json"), &set) {
		return zcodeKey{}, false
	}
	families := []string{"zai", "bigmodel"}
	if set.Domain == "zai" || set.Domain == "bigmodel" {
		families = []string{set.Domain}
	}
	for _, fam := range families {
		org, project := "", ""
		if s, ok := set.Sel[fam]; ok {
			if s.Kind == "team-coding-plan" {
				org, project = strings.TrimSpace(s.Org), strings.TrimSpace(s.Project)
			}
		} else if rest, ok := strings.CutPrefix(strings.TrimSpace(set.Legacy[fam]), "team-plan:builtin:"+fam+"-coding-plan:"); ok {
			if parts := strings.Split(rest, ":"); len(parts) == 3 {
				org, _ = url.PathUnescape(parts[1])
				project, _ = url.PathUnescape(parts[2])
			}
		}
		if org == "" || project == "" {
			continue
		}
		v, ok := store["oauth:"+fam+":access_token"]
		if !ok {
			continue
		}
		tok, ok := zcodeDecrypt(secret, v)
		if tok = strings.TrimSpace(tok); !ok || tok == "" {
			continue
		}
		if jv, ok := store["zcodejwttoken"]; ok && fam == "bigmodel" {
			if jwt, ok := zcodeDecrypt(secret, jv); ok && strings.TrimSpace(jwt) == tok {
				continue // a stale token ZCode won't use either
			}
		}
		base := ZCodeZaiBase
		if fam == "bigmodel" {
			base = ZCodeBigModelBase
		}
		return zcodeKey{Base: base, Token: tok, Org: org, Project: project}, true
	}
	return zcodeKey{}, false
}

// zcodeTeamKeys are the team keys found for accounts that keep none,
// ZCode's own: asked again after a failure a minute later.
var zcodeTeamKeys = struct {
	sync.Mutex
	m map[string]zcodeTeamKeyAt
}{m: map[string]zcodeTeamKeyAt{}}

type zcodeTeamKeyAt struct {
	key string
	err error
	at  time.Time
}

// zcodeTeamKeyOf is the team project's key for an account that keeps none.
func zcodeTeamKeyOf(ctx context.Context, k zcodeKey) (string, error) {
	if k.Key != "" {
		return k.Key, nil
	}
	id := k.Token + "\x00" + k.Org + "\x00" + k.Project
	zcodeTeamKeys.Lock()
	got, ok := zcodeTeamKeys.m[id]
	zcodeTeamKeys.Unlock()
	if ok && (got.err == nil || time.Since(got.at) < time.Minute) {
		return got.key, got.err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	site := "zai"
	if zcodeBizRoot(k.Base) == zcodeBigModelAPI {
		site = "bigmodel"
	}
	root := zcodeBizRoot(k.Base)
	keys := root + "/api/biz/v1/organization/" + url.PathEscape(k.Org) + "/projects/" + url.PathEscape(k.Project) + "/api_keys"
	key, err := zcodeProjectKey(ctx, site, keys, k.Token, zcodeTeamHeaders(k.Base, k.Org, k.Project), map[string]any{"name": "zcode-team-api-key", "keyType": 2})
	if err != nil {
		err = fmt.Errorf("ZCode's team plan: %w — sign in to ZCode again", err)
	}
	zcodeTeamKeys.Lock()
	zcodeTeamKeys.m[id] = zcodeTeamKeyAt{key, err, time.Now()}
	zcodeTeamKeys.Unlock()
	return key, err
}

// ---- allowance ------------------------------------------------------------------

// zcodeTeamQuota is a team seat's allowance: the team plan's five hours
// and week, what is left of its resets, and the plan's name and end.
func zcodeTeamQuota(ctx context.Context, l Login, k zcodeKey) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "zcode", Name: "ZCode", Icon: "zcode", Plan: l.Plan, User: l.User, Windows: []QuotaWindow{}}
	key, err := zcodeTeamKeyOf(ctx, k)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	ws, err := zhipuTeamWindows(ctx, zcodeBizRoot(k.Base), key, k.Base, k.Org, k.Project)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	q.Windows = ws
	if k.Token == "" {
		return q
	}
	root := zcodeBizRoot(k.Base)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		q.Resets = zhipuTeamResets(ctx, root, k.Token, k.Base, k.Org, k.Project)
	}()
	if d, err := zcodeTeamDetailOf(ctx, root, k.Token, k.Base, k.Org, k.Project); err == nil && d.usable() {
		if d.Product != "" {
			q.Plan = d.Product
		}
		if t := d.end(); t != nil {
			q.Until, q.Renew = t, "off"
		}
	}
	wg.Wait()
	return q
}

// zhipuTeamWindows asks a team's windows: the quota with type=2, the key
// in Authorization and the project in the headers.
func zhipuTeamWindows(ctx context.Context, root, key, base, org, project string) ([]QuotaWindow, error) {
	var data zhipuLimits
	if err := zcodeCallH(ctx, http.MethodGet, root+"/api/monitor/usage/quota/limit?type=2", key, zcodeTeamHeaders(base, org, project), nil, &data); err != nil {
		return nil, err
	}
	return data.windows(), nil
}

// zhipuTeamResets is how many resets a team member may spend, of the five
// hours and of the week: those of customer-package-reset/list still
// available. nil when it can't be told or there are none.
func zhipuTeamResets(ctx context.Context, root, auth, base, org, project string) *ResetCredits {
	type reset struct {
		Available bool `json:"available"`
		Expire    any  `json:"expireTime"`
	}
	var data struct {
		FiveHour []reset `json:"fiveHourResets"`
		Week     []reset `json:"weekResets"`
	}
	if err := zcodeCallH(ctx, http.MethodGet, root+"/api/biz/customer-package-reset/list?targetType=TEAM", auth, zcodeTeamHeaders(base, org, project), nil, &data); err != nil {
		return nil
	}
	r := &ResetCredits{ByWindow: true}
	count := func(rs []reset) (n int) {
		for _, x := range rs {
			if !x.Available {
				continue
			}
			n++
			if t := zcodeWhen(x.Expire); t != nil && (r.Until == nil || t.Before(*r.Until)) {
				r.Until = t
			}
		}
		return n
	}
	r.FiveHour, r.Weekly = count(data.FiveHour), count(data.Week)
	r.Count = r.FiveHour + r.Weekly
	if r.Count == 0 {
		return nil
	}
	return r
}

// zhipuTeamOf is the team project a GLM key belongs to, when that key is a
// ZCode account's team key magpie holds.
func zhipuTeamOf(key string) (org, project string) {
	for _, l := range zcodeLogins() {
		if l.key.team() && l.key.Key == key {
			return l.key.Org, l.key.Project
		}
	}
	return "", ""
}


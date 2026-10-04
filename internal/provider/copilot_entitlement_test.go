package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCopilotEntitlementLabels(t *testing.T) {
	for _, x := range []struct{ plan, sku, want string }{
		{"individual", "free_educational_quota", "Education"},
		{"free", "free_educational_quota", "Education"},
		{"individual", "unknown-sku", "Pro"},
		{"individual", "free_limited_copilot", "Free"},
		{"individual_pro", "", "Pro+"}, {"business", "", "Business"},
		{"enterprise", "", "Enterprise"}, {"free", "", "Free"},
		{"", "free_limited_copilot", "Free"}, {"", "unknown-sku", ""},
		{"future_plan", "future-sku", "Future_plan"}, {"", "", ""},
	} {
		e := copilotEntitlement{Plan: x.plan, AccessSKU: x.sku}
		if got := e.label(); got != x.want {
			t.Errorf("%+v: %q, want %q", e, got, x.want)
		}
	}
}

func TestCopilotEntitlementRefresh(t *testing.T) {
	signIn(t)
	if err := addCopilotLogin("hubot", "Pro", "gho_hubot", ""); err != nil {
		t.Fatal(err)
	}
	copilotLoginList() // remember the editor's own account
	for _, app := range []copilotApp{{User: "hubot", Token: "gho_hubot"}, {User: "octocat", Token: "gho_x"}} {
		refreshCopilotEntitlement(app, "Education", "free_educational_quota")
		var found bool
		for _, l := range readLogins() {
			if l.Agent == "copilot" && l.User == app.User {
				found = l.Plan == "Education" && l.AccessSKU == "free_educational_quota"
			}
		}
		if !found {
			t.Fatalf("entitlement not refreshed for %s", app.User)
		}
		before, _ := os.ReadFile(loginsPath())
		refreshCopilotEntitlement(app, "Education", "free_educational_quota")
		refreshCopilotEntitlement(copilotApp{User: app.User, Token: "replaced-token"}, "Enterprise", "new")
		after, _ := os.ReadFile(loginsPath())
		if string(before) != string(after) {
			t.Fatal("unchanged or stale credentials changed login")
		}
	}
}

func TestCopilotReplacementClearsSKU(t *testing.T) {
	signIn(t)
	if err := addCopilotLogin("hubot", "Education", "old", ""); err != nil {
		t.Fatal(err)
	}
	refreshCopilotEntitlement(copilotApp{User: "hubot", Token: "old"}, "Education", "free_educational_quota")
	if err := addCopilotLogin("hubot", "Pro", "new", ""); err != nil {
		t.Fatal(err)
	}
	for _, l := range readLogins() {
		if l.User == "hubot" && (l.Plan != "Pro" || l.AccessSKU != "") {
			t.Fatal("replacement retained prior entitlement")
		}
	}
}

func TestCopilotOwnEntitlementSwitch(t *testing.T) {
	signIn(t)
	copilotLoginList()
	refreshCopilotEntitlement(copilotApp{User: "octocat", Token: "gho_x"}, "Education", "free_educational_quota")
	sideLogins("copilot", "another-user", func(savedLogin) bool { return false })
	for _, l := range readLogins() {
		if l.Agent == "copilot" && l.own() && (l.Plan != "" || l.AccessSKU != "") {
			t.Fatal("new editor account inherited previous entitlement")
		}
	}
}

func TestCopilotEducationQuotaRefresh(t *testing.T) {
	signIn(t)
	if err := addCopilotLogin("hubot", "Pro", "gho_hubot", ""); err != nil {
		t.Fatal(err)
	}
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"copilot_plan":"individual","access_type_sku":"free_educational_quota","quota_snapshots":{"chat":{"unlimited":true},"completions":{"unlimited":true},"premium_interactions":{"has_quota":true,"entitlement":200,"quota_remaining":200}}}`))
	}))
	defer srv.Close()
	old := CopilotUserURL
	CopilotUserURL = srv.URL
	defer func() { CopilotUserURL = old }()
	q := loginQuota(context.Background(), Login{Agent: "copilot", User: "hubot"})
	if q.Error != "" || q.Plan != "Education" || q.AccessSKU != "free_educational_quota" || len(q.Windows) != 3 {
		t.Fatalf("quota: %+v", q)
	}
	if q.Windows[0].Name != "Premium requests" {
		t.Fatalf("counted quota not first: %+v", q.Windows)
	}
	for _, w := range q.Windows[1:] {
		if !w.Unlimited || w.Display != "Unlimited" || w.Used != 0 || !w.Aside {
			t.Fatalf("unlimited: %+v", w)
		}
	}
	fail = true
	q = loginQuota(context.Background(), Login{Agent: "copilot", User: "hubot"})
	if q.Error == "" {
		t.Fatal("expected refresh failure")
	}
	for _, l := range readLogins() {
		if l.User == "hubot" && l.Plan != "Education" {
			t.Fatal("failed read erased plan")
		}
	}
}

func TestCopilotOwnEntitlementWithoutUser(t *testing.T) {
	signIn(t)
	writeFile(t, filepath.Join(copilotConfigDir(), "github-copilot", "apps.json"), map[string]any{"github.com": map[string]any{"oauth_token": "gho_x"}})
	ls := copilotLoginList()
	if len(ls) != 1 || ls[0].User != "GitHub" {
		t.Fatalf("logins %+v", ls)
	}
	refreshCopilotEntitlement(copilotApp{User: "GitHub", Token: "gho_x"}, "Free", "free_limited_copilot")
	if got := copilotLoginList()[0].Plan; got != "Free" {
		t.Fatalf("plan %q", got)
	}
}

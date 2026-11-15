package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Each of these files is read as empty when it doesn't parse, and the next
// write put that over it: every other account's, site's or day's record
// was gone, with no copy (#2263). What was there is copied aside first,
// valid JSON of another shape (an array for a map) too.
func TestUnreadStateKeptAside(t *testing.T) {
	for _, c := range []struct {
		name  string
		path  func() string
		write func()
	}{
		{"stepfun-auth.json", stepfunPath, func() { saveStepFun(map[string]stepfunSession{"cn": {}}) }},
		{"qianfan-auth.json", qianfanPath, func() { saveQianfan(map[string]qianfanSession{"baidu-qianfan": {}}) }},
		{"login-returns.json", loginReturnsPath, func() { setLoginReturn("codex", loginReturn{Back: "a", To: "b"}) }},
		{"quota-history.json", quotaHistPath, func() { writeQuotaHist(quotaHist{}) }},
		{"balance-history.json", balanceHistPath, func() { writeBalanceHist(balanceHist{}) }},
		{"credits-daily.json", creditsPath, func() {
			noteDailyCredits([]SubscriptionQuota{wbCard("workbuddy", "me", 1, 10)}, time.Now())
		}},
	} {
		for _, was := range []string{`{"someone": {"a": `, `[]`} {
			t.Run(c.name+" "+was, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("USERPROFILE", home)
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
				t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
				p := c.path()
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(was), 0o600); err != nil {
					t.Fatal(err)
				}
				c.write()
				bad, _ := filepath.Glob(p + ".bad-*")
				if len(bad) != 1 {
					t.Fatalf("kept aside: %v", bad)
				}
				if b, _ := os.ReadFile(bad[0]); string(b) != was {
					t.Fatalf("kept aside as %q, want %q", b, was)
				}
				if b, _ := os.ReadFile(p); string(b) == was {
					t.Fatal("the write didn't happen")
				}
			})
		}
	}
}

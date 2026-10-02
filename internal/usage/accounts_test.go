package usage

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// accountRecs are a month of two Codex accounts' calls, one Claude account's
// under the same email, an older Codex call that named its account only in
// its host, one that named none, and a keyed provider's, which has none.
func accountRecs(now time.Time) []Record {
	return []Record{
		{Time: now, Provider: "cx", Model: "gpt-5", Host: "chatgpt.com as a@example.com", ProviderAccount: "a@example.com", Input: 100, Output: 10, CacheRead: 1000, Status: 200},
		{Time: now, Provider: "cx", Model: "gpt-5", Host: "chatgpt.com as a@example.com", ProviderAccount: "a@example.com", Input: 50, Output: 5, Status: 200},
		{Time: now, Provider: "cx", Model: "gpt-5", Host: "chatgpt.com as b@example.com", ProviderAccount: "b@example.com", Input: 20, Output: 2, Status: 429},
		// written before the field: its host says the account it went out as
		{Time: now.Add(-time.Hour), Provider: "cx", Model: "gpt-5", Host: "chatgpt.com as b@example.com", Input: 30, Output: 3, Status: 200},
		// nothing says which account: not recorded, never guessed
		{Time: now.Add(-time.Hour), Provider: "cx", Model: "gpt-5", Host: "chatgpt.com", Input: 7, Status: 200},
		{Time: now, Provider: "claude", Model: "sonnet", Host: "api.anthropic.com as a@example.com", ProviderAccount: "a@example.com", Input: 9, Output: 1, Status: 200},
		{Time: now, Provider: "relay", Model: "m", Host: "relay.example", ProviderKeyID: provider.KeyID("relay-secret"), ProviderKeyName: "Team", Input: 5, Status: 200},
		// out of the period
		{Time: now.Add(-40 * 24 * time.Hour), Provider: "cx", Model: "gpt-5", ProviderAccount: "old@example.com", Input: 9999, Status: 200},
	}
}

// The usage summary tells each subscription account's calls apart (#557):
// by provider and the account that answered, priced, an older record by
// the account its host named, one naming none as its own "not recorded"
// group, and a keyed provider's calls in no account's.
func TestSummaryByAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{"gpt-5":{"id":"gpt-5","cost":{"input":1,"output":10,"cache_read":0.1}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "cx", Name: "Codex", Key: "cx-secret", Chat: "https://chatgpt.example/v1", Catalog: "openai"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s := summarize(Month, now, accountRecs(now))
	got := map[string]Group{}
	for _, g := range s.Accounts {
		got[g.ID] = g
	}
	if len(s.Accounts) != 4 {
		t.Fatalf("accounts: %+v", s.Accounts)
	}
	a := got["cx@a@example.com"]
	if a.Provider != "cx" || a.Account != "a@example.com" || a.Calls != 2 || a.Input != 150 || a.CacheRead != 1000 || a.Cost == 0 || a.Unpriced != 0 {
		t.Fatalf("codex a: %+v", a)
	}
	want := (150*1 + 15*10 + 1000*0.1) / 1e6
	if d := a.Cost - want; d > 1e-12 || d < -1e-12 {
		t.Fatalf("codex a cost %v, want %v", a.Cost, want)
	}
	if b := got["cx@b@example.com"]; b.Calls != 2 || b.Errors != 1 || b.Input != 50 {
		t.Fatalf("codex b, its older call by its host: %+v", b)
	}
	if n := got["cx@"]; n.Account != "" || n.Calls != 1 || n.Input != 7 {
		t.Fatalf("not recorded: %+v", n)
	}
	if c := got["claude@a@example.com"]; c.Calls != 1 || c.Input != 9 {
		t.Fatalf("the same email on Claude is its own account: %+v", c)
	}
	if s.Accounts[0].ID != "cx@a@example.com" {
		t.Fatalf("most tokens first: %+v", s.Accounts)
	}
	for _, g := range s.Accounts {
		if g.Provider == "relay" || g.Account == "old@example.com" {
			t.Fatalf("%+v is no account of the period", g)
		}
	}
	encoded, _ := json.Marshal(s)
	if !strings.Contains(string(encoded), `"accounts":[`) || strings.Contains(string(encoded), "secret") {
		t.Fatalf("summary JSON: %s", encoded)
	}
}

// The ledger keeps one account's calls, the CSV says the account of each,
// and a search finds them by it.
func TestLedgerByAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	recs := accountRecs(now)
	rows, _, _ := ledger(Month.Since(now), Filter{Account: "b@example.com"}, recs)
	if len(rows) != 2 {
		t.Fatalf("account b: %+v", rows)
	}
	rows, _, _ = ledger(Month.Since(now), Filter{Account: "a@example.com"}, recs)
	if len(rows) != 3 {
		t.Fatalf("account a, on Codex and Claude: %+v", rows)
	}
	if rows, _, _ := ledger(Month.Since(now), Filter{Account: "a@example.com", Provider: "claude"}, recs); len(rows) != 1 {
		t.Fatalf("account a on Claude: %+v", rows)
	}
	if rows, _, _ := ledger(Month.Since(now), Filter{Query: "B@EXAMPLE"}, recs); len(rows) != 2 {
		t.Fatalf("search by account: %+v", rows)
	}
	rows, _, _ = ledger(Month.Since(now), Filter{Provider: "cx"}, recs)
	var b strings.Builder
	if err := WriteCSV(&b, rows); err != nil {
		t.Fatal(err)
	}
	cells, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil || len(cells) != 6 {
		t.Fatalf("CSV: %v %s", err, b.String())
	}
	col := slices.Index(CSVHeader, "provider_account")
	if col < 0 || col != slices.Index(CSVHeader, "provider_key_name")+1 {
		t.Fatalf("provider_account belongs with the provider's identity columns: %v", CSVHeader)
	}
	var accounts []string
	for _, c := range cells[1:] {
		accounts = append(accounts, c[col])
	}
	slices.Sort(accounts)
	if strings.Join(accounts, ",") != ",a@example.com,a@example.com,b@example.com,b@example.com" {
		t.Fatalf("CSV accounts: %v", accounts)
	}
}

// The Requests page packs rows into its cache: the account must survive
// that, its filter keep the account's calls, and the page offer the
// accounts of the period, whatever is picked.
func TestRequestPageByAccount(t *testing.T) {
	pageHome(t)
	now := time.Now()
	for i, r := range accountRecs(now)[:7] {
		r.Time = now.Add(time.Duration(i) * time.Second)
		r.Agent = "codex"
		Append(r)
	}
	for repeat := 0; repeat < 2; repeat++ {
		page := QueryPage(All, Filter{Account: "b@example.com"}, 0, 100)
		if page.Total != 2 || len(page.Rows) != 2 {
			t.Fatalf("account filter: total %d, %+v", page.Total, page.Rows)
		}
		for _, r := range page.Rows {
			if r.Account() != "b@example.com" {
				t.Fatalf("row of another account: %+v", r)
			}
		}
		if p := QueryPage(All, Filter{}, 0, 100); !slices.ContainsFunc(p.Rows, func(r Row) bool { return r.ProviderAccount == "a@example.com" }) {
			t.Fatalf("rows lost their account: %+v", p.Rows)
		}
		var ids []string
		for _, g := range page.Accounts {
			ids = append(ids, g.ID)
		}
		slices.Sort(ids)
		if strings.Join(ids, ",") != "claude@a@example.com,cx@a@example.com,cx@b@example.com" {
			t.Fatalf("account choices: %v", ids)
		}
	}
	s := Summarize(All)
	if len(s.Accounts) != 4 {
		t.Fatalf("indexed summary accounts: %+v", s.Accounts)
	}
}

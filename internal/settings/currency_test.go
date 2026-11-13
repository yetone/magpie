package settings

import "testing"

// "By provider" (onkashiko on X): a provider's costs in what it sells in,
// a sum of several in the currency picked for totals.
func TestCostInByProvider(t *testing.T) {
	for _, c := range []struct {
		s                     Settings
		yuan, dollars, totals string
	}{
		{Settings{Currency: "usd"}, "usd", "usd", "usd"},
		{Settings{Currency: ""}, "usd", "usd", "usd"},
		{Settings{Currency: "cny"}, "cny", "cny", "cny"},
		{Settings{Currency: "cny", TotalCurrency: "usd"}, "cny", "cny", "cny"}, // only By provider reads it
		{Settings{Currency: CurrencyByProvider}, "cny", "usd", "usd"},
		{Settings{Currency: CurrencyByProvider, TotalCurrency: "cny"}, "cny", "usd", "cny"},
		{Settings{Currency: CurrencyByProvider, TotalCurrency: "usd"}, "cny", "usd", "usd"},
	} {
		if got := c.s.CostIn(true); got != c.yuan {
			t.Errorf("currency %q, totals %q: CostIn(yuan) = %s, want %s", c.s.Currency, c.s.TotalCurrency, got, c.yuan)
		}
		if got := c.s.CostIn(false); got != c.dollars {
			t.Errorf("currency %q, totals %q: CostIn(dollars) = %s, want %s", c.s.Currency, c.s.TotalCurrency, got, c.dollars)
		}
		if got := c.s.TotalCostIn(); got != c.totals {
			t.Errorf("currency %q, totals %q: TotalCostIn() = %s, want %s", c.s.Currency, c.s.TotalCurrency, got, c.totals)
		}
	}
	if err := Save(Settings{Currency: CurrencyByProvider, TotalCurrency: "cny"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Currency != CurrencyByProvider || s.TotalCurrency != "cny" {
		t.Fatalf("saved: currency %q, totals %q", s.Currency, s.TotalCurrency)
	}
	if Save(Settings{Currency: CurrencyByProvider, TotalCurrency: "provider"}) == nil {
		t.Fatal("totals by provider accepted: a sum of several has no one provider")
	}
	if Save(Settings{TotalCurrency: "eur"}) == nil {
		t.Fatal("bad total currency accepted")
	}
}

// The menu bar's currency is set apart from the totals' (onkashiko on X):
// unset it follows them, set it is its own, and it stays on this computer.
func TestTrayCostIn(t *testing.T) {
	for _, c := range []struct {
		s    Settings
		want string
	}{
		{Settings{}, "usd"},
		{Settings{Currency: "cny"}, "cny"},
		{Settings{Currency: CurrencyByProvider, TotalCurrency: "cny"}, "cny"},
		{Settings{Currency: "cny", TrayCurrency: "usd"}, "usd"},
		{Settings{Currency: "usd", TrayCurrency: "cny"}, "cny"},
		{Settings{Currency: CurrencyByProvider, TotalCurrency: "usd", TrayCurrency: "cny"}, "cny"},
	} {
		if got := c.s.TrayCostIn(); got != c.want {
			t.Errorf("currency %q, totals %q, menu bar %q: TrayCostIn() = %s, want %s", c.s.Currency, c.s.TotalCurrency, c.s.TrayCurrency, got, c.want)
		}
	}
	if Save(Settings{TrayCurrency: "eur"}) == nil {
		t.Fatal("bad menu bar currency accepted")
	}
	if Save(Settings{TrayCurrency: "provider"}) == nil {
		t.Fatal("menu bar by provider accepted: its total has no one provider")
	}
	from := Settings{TrayCurrency: "usd"}
	from.KeepOwn(Settings{TrayCurrency: "cny"})
	if from.TrayCurrency != "cny" {
		t.Fatalf("a synced file's menu bar currency replaced this computer's: %q", from.TrayCurrency)
	}
}

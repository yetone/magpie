package provider

import (
	"os"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/settings"
)

// jiangguochao666 on X asked the pickers to say which of each provider's
// models are free. PricedFree says so only where the provider's own price
// is $0: OpenRouter's foo:free, not foo, and not the same id another
// provider charges for. A model nothing prices is not free, nor one a
// plan's catalog lists at $0 (that is the plan's per-token price), nor one
// only its maker gives away (a relay serving Zhipu's free glm-4.7-flash
// may charge for it). A price the user set is theirs: one that isn't 0, at
// any size, makes the model paid, and a $0 one makes it free.
func TestPricedFreeIsTheProvidersOwnZero(t *testing.T) {
	prefsHome(t)
	data := `{
		"zfree":{"models":{
			"foo:free":{"id":"foo:free","cost":{"input":0,"output":0}},
			"foo":{"id":"foo","cost":{"input":1,"output":2}},
			"shared":{"id":"shared","cost":{"input":0,"output":0,"cache_read":0}},
			"cached":{"id":"cached","cost":{"input":0,"output":0,"cache_read":0.1}},
			"nocost":{"id":"nocost"}
		}},
		"zpaid":{"models":{"shared":{"id":"shared","cost":{"input":3,"output":15}}}},
		"zz-coding-plan":{"models":{"plan-model":{"id":"plan-model","cost":{"input":0,"output":0}}}},
		"zhipuai":{"models":{"glm-4.7-flash":{"id":"glm-4.7-flash","cost":{"input":0,"output":0}}}}
	}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()

	free := Provider{ID: "zf", Name: "ZF", Catalog: "zfree", Key: "k", Chat: "http://127.0.0.1:9/v1"}
	paid := Provider{ID: "zp", Name: "ZP", Catalog: "zpaid", Key: "k", Chat: "http://127.0.0.1:9/v1"}
	plan := Provider{ID: "zc", Name: "ZC", Catalog: "zz-coding-plan", Key: "k", Chat: "http://127.0.0.1:9/v1"}
	zhipu := Provider{ID: "zh", Name: "ZH", Catalog: "zhipuai", Key: "k", Chat: "http://127.0.0.1:9/v1"}
	relay := Provider{ID: "rl", Name: "RL", Key: "k", Chat: "http://127.0.0.1:9/v1"}
	none := settings.Settings{}
	for _, c := range []struct {
		p     Provider
		model string
		want  bool
	}{
		{free, "foo:free", true},
		{free, "foo", false},
		{free, "shared", true},
		{paid, "shared", false},
		{free, "cached", false},
		{free, "nocost", false},
		{free, "nobody-lists-this", false},
		{plan, "plan-model", false},
		{zhipu, "glm-4.7-flash", true},
		{relay, "glm-4.7-flash", false},
	} {
		if got := c.p.PricedFree(none, c.model); got != c.want {
			t.Errorf("%s's %s: PricedFree %v, want %v", c.p.ID, c.model, got, c.want)
		}
		samePricer(t, c.p, none, c.model)
	}

	zero, one := 0.0, 1.0
	s := settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		// a paid price over 1000 tokens of input
		"zf/foo:free": {Input: &zero, Output: &zero, CacheRead: &zero, CacheWrite: &zero, Tiers: []catalog.Tier{{Above: 1000, Input: 1}}},
		// what the user pays their relay for it: nothing
		"rl/glm-4.7-flash": {Input: &zero, Output: &zero, CacheRead: &zero, CacheWrite: &zero},
		// every model of ZH costs them something
		"zh/*": {Input: &one, Output: &one, CacheRead: &zero, CacheWrite: &zero},
	}}
	for _, c := range []struct {
		p     Provider
		model string
		want  bool
	}{
		{free, "foo:free", false},
		{relay, "glm-4.7-flash", true},
		{zhipu, "glm-4.7-flash", false},
		{free, "shared", true},
	} {
		if got := c.p.PricedFree(s, c.model); got != c.want {
			t.Errorf("with the user's prices, %s's %s: PricedFree %v, want %v", c.p.ID, c.model, got, c.want)
		}
		samePricer(t, c.p, s, c.model)
	}
}

// samePricer checks that the Providers page's Pricer gives model the
// price and the free mark ListPrice and PricedFree do.
func samePricer(t *testing.T, p Provider, s settings.Settings, model string) {
	t.Helper()
	list, listed, free := p.Pricer().Prices(s, model)
	want, wantListed := p.ListPrice(model)
	if listed != wantListed || !reflect.DeepEqual(list, want) {
		t.Errorf("%s's %s: Pricer's list price %+v %v, ListPrice's %+v %v", p.ID, model, list, listed, want, wantListed)
	}
	if wantFree := p.PricedFree(s, model); free != wantFree {
		t.Errorf("%s's %s: Pricer's free %v, PricedFree's %v", p.ID, model, free, wantFree)
	}
}

// The Providers page prices every model of every provider on each ask.
// A Pricer works the provider's catalogs out once and prices each model
// once for both its list price and its free mark, so a model priced on
// it allocates nothing, where ListPrice and PricedFree asked apart
// worked the catalogs out, and made the names to look up, twice.
func TestPricerAllocatesNothingPerModel(t *testing.T) {
	prefsHome(t)
	data := `{"zfree":{"models":{
		"foo:free":{"id":"foo:free","cost":{"input":0,"output":0}},
		"foo":{"id":"foo","cost":{"input":1,"output":2}}
	}}}`
	if err := os.WriteFile(catalog.CachePath(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	p := Provider{ID: "zf", Name: "ZF", Catalog: "zfree", Key: "k", Chat: "http://127.0.0.1:9/v1"}
	none := settings.Settings{}
	// as the page asks: under the request's holds (gui's held)
	defer Hold()()
	defer filememo.Hold()()
	r := p.Pricer()
	for _, model := range []string{"foo:free", "foo", "nobody-lists-this"} {
		r.Prices(none, model) // the catalog's memo filled
		if n := testing.AllocsPerRun(50, func() { r.Prices(none, model) }); n != 0 {
			t.Errorf("%s: a Pricer allocated %v times for one model, want 0", model, n)
		}
	}
}

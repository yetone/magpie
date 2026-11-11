package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// TestDshThroughBili (VantH_off on X): DeepSeek Harness had no row on
// billion-context's page. bili's plugin for dsh rides proxy settings of
// dsh's process, which a route at http://127.0.0.1 never takes, so dsh goes
// through the /bili/ prefix as Hermes does: the baseURL of magpie's route is
// behind it, and a sync, a pick and the serving round keep it. Off, it is on
// the gateway again.
func TestDshThroughBili(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	const prefix = "http://127.0.0.1:8787/bili/"
	gw := gateway.URL()
	a := dsh(home)
	if err := DshThrough(a, prefix); err == nil {
		t.Fatal("put a dsh not on magpie through bili")
	}
	os.WriteFile(web, []byte("# mine\n"+
		dshRouteFixture("deepseek/pro", "deepseek/flash")+
		"- id: agent-default-model\n  config:\n    provider: magpie\n    model: deepseek/flash\n"), 0o644)
	a = dsh(home)
	if DshURL(a) != gw+"/v1" {
		t.Fatalf("DshURL %q before", DshURL(a))
	}
	if err := DshThrough(a, prefix); err != nil {
		t.Fatal(err)
	}
	through := func(when, want string) {
		t.Helper()
		a := dsh(home)
		b, _ := os.ReadFile(web)
		if got := DshURL(a); got != want+gw+"/v1" || !strings.Contains(string(b), "# mine") {
			t.Fatalf("%s: DshURL %q:\n%s", when, got, b)
		}
		if c := a.Check(); c != "" {
			t.Fatalf("%s: %s", when, c)
		}
	}
	through("on", prefix)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	through("after a sync", prefix)
	// the serving round writes the route again as it is: behind the prefix
	b, _ := os.ReadFile(web)
	os.WriteFile(web, []byte(strings.Replace(string(b), "deepseek/flash", "deepseek/gone", 1)), 0o644)
	dshWiredOnce()
	through("after a round", prefix)
	// an address that isn't a local proxy's route goes back to the gateway
	b, _ = os.ReadFile(web)
	os.WriteFile(web, []byte(strings.ReplaceAll(string(b), prefix, "https://elsewhere.example/bili/")), 0o644)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	through("elsewhere", "")
	if err := DshThrough(a, prefix); err != nil {
		t.Fatal(err)
	}
	if err := DshThrough(a, ""); err != nil {
		t.Fatal(err)
	}
	through("off", "")
}

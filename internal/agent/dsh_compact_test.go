package agent

import (
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #5395 (Musikrv77852 on X): DeepSeek Harness's row picks the model its
// compactions go on, offered once dsh is on magpie's route. It is magpie's
// setting, read by the gateway on each request: picking it writes nothing
// into dsh's profiles, and needs no restart. DS Bot, whose requests the
// gateway can't tell from dsh's, has no square of its own.
func TestDshCompactionModelField(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	if err := os.WriteFile(web, []byte(dshProfileTemplate), 0o600); err != nil {
		t.Fatal(err)
	}
	a := dsh(home)
	f := a.Field("compact_model")
	if f == nil || !f.Live || !f.Quiet || f.Label != "compaction model" {
		t.Fatalf("no live compaction model field: %+v", f)
	}
	if opts := f.Options(nil); len(opts) != 0 {
		t.Fatalf("offered %d models before dsh is on magpie's route", len(opts))
	}
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	dshWiredOnce()
	a = dsh(home)
	f = a.Field("compact_model")
	// spelt as the model field's, magpie/<id>, so a bare value is still
	// the model's to the CLI (fieldForValue gives it to the first field
	// offering it)
	if !slices.ContainsFunc(f.Options(nil), func(o Option) bool { return o.Value == "magpie/deepseek/flash" }) {
		t.Fatalf("magpie's models not offered: %+v", f.Options(nil))
	}
	if slices.ContainsFunc(f.Options(nil), func(o Option) bool { return o.Value == "deepseek/flash" }) {
		t.Fatal("a bare id is offered, which the CLI would take from the model")
	}
	before, _ := os.ReadFile(web)
	for _, v := range []string{"magpie/deepseek/flash", "deepseek/flash"} {
		if err := f.Set(v); err != nil {
			t.Fatal(err)
		}
		if got := provider.DshCompactModel(); got != "deepseek/flash" || f.Get() != "magpie/deepseek/flash" {
			t.Fatalf("set %q: held %q, read %q", v, got, f.Get())
		}
	}
	if after, _ := os.ReadFile(web); string(after) != string(before) {
		t.Fatalf("the pick wrote dsh's profile:\n%s", after)
	}
	if err := f.Set(""); err != nil || provider.DshCompactModel() != "" {
		t.Fatalf("unset: %v, %q", err, provider.DshCompactModel())
	}
	bot := dshOn(here(home), dir, dsbotID, func() string { return "" }, func() string { return dsbotProfile })
	if bot.Field("compact_model") != nil {
		t.Fatal("DS Bot has a compaction model square of its own")
	}
}

package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// magpie model name and --reset keep and drop the user's name for a
// provider's model, a name of several words included.
func TestModelNameCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := modelCmd([]string{"name", "a/m", "My", "Model"}); err != nil {
		t.Fatal(err)
	}
	if n, ok := provider.ModelName("a", "m"); !ok || n != "My Model" {
		t.Fatal(n, ok)
	}
	if err := modelCmd([]string{"name", "a/m"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"names"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"name", "a/m", "--reset"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.ModelName("a", "m"); ok {
		t.Fatal("still named")
	}
	if err := modelCmd([]string{"name", "m", "x"}); err == nil {
		t.Fatal("named a model without its provider")
	}
	// a model whose reasoning levels aren't known can be given some
	if err := modelCmd([]string{"efforts", "a/m", "low,high"}); err != nil {
		t.Fatal(err)
	}
	if es := settings.Load().ModelEfforts["a/m"]; !slices.Equal(es, []string{"low", "high"}) {
		t.Fatal(settings.Load().ModelEfforts)
	}
	if err := modelCmd([]string{"efforts", "a/m", "--reset"}); err != nil {
		t.Fatal(err)
	}
	if es := settings.Load().ModelEfforts; len(es) != 0 {
		t.Fatal(es)
	}
}

// magpie model price says what a call to a model is counted at, and
// magpie model prices lists what the user has said. The list and its line in
// the help are added together: reaching it through `magpie model price` would
// have answered "unknown command".
func TestModelPriceCmd(t *testing.T) {
	groupsHome(t)
	if err := modelCmd([]string{"price", "a/m", "0.5,1.5,0.05,0"}); err != nil {
		t.Fatal(err)
	}
	if pr, ok := provider.EffectivePrice("a", "m"); !ok || pr.Input != 0.5 || pr.CacheWrite != 0 {
		t.Fatalf("the price given is not what the model is counted at: %+v %v", pr, ok)
	}
	// the list is what says which model it was and what it costs: running
	// it without error says nothing about either
	out, err := said(t, func() error { return modelCmd([]string{"prices"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a/m") || !strings.Contains(out, "$0.5/$1.5 per 1M in/out") {
		t.Errorf("magpie model prices: %q; want a/m and the $0.5/$1.5 it was given", out)
	}

	// a price no vendor could charge is refused where it is set, and the
	// one already given stands
	if err := modelCmd([]string{"price", "a/m", "-1,1.5,0.05,0"}); err == nil {
		t.Fatal("a negative price was set")
	}
	if pr, _ := provider.EffectivePrice("a", "m"); pr.Input != 0.5 {
		t.Fatalf("a price that was refused was written anyway: %+v", pr)
	}

	// one price for every model of a provider, which --reset on the model
	// leaves in force, and a second --reset takes away
	if err := modelCmd([]string{"price", "a/*", "2,3,0,0"}); err != nil {
		t.Fatal(err)
	}
	if pr, _ := provider.EffectivePrice("a", "only-a"); pr.Input != 2 {
		t.Fatalf("a price for the provider did not cover its other models: %+v", pr)
	}
	if err := modelCmd([]string{"price", "a/m", "--reset"}); err != nil {
		t.Fatal(err)
	}
	if pr, _ := provider.EffectivePrice("a", "m"); pr.Input != 2 {
		t.Fatalf("--reset did not take the model's own price away: %+v", pr)
	}
	if err := modelCmd([]string{"price", "a/*", "--reset"}); err != nil {
		t.Fatal(err)
	}
	if _, still := settings.Load().ModelPrices["a/*"]; still {
		t.Fatal("a price for every model of the provider is still there")
	}
}

// said runs f with what it prints taken off the terminal, the way a user
// reads a price off the command rather than out of the file.
func said(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	r.Close()
	return string(b), runErr
}

// magpie model prices is the one place the user sees what they have said
// models cost: every priced model with its price, a price a hand edit left
// with a part missing named as the part that is missing rather than billed
// at zero, and — with nothing priced yet — how to price one.
func TestModelPricesCmd(t *testing.T) {
	groupsHome(t)
	out, err := said(t, func() error { return modelCmd([]string{"prices"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no model is priced by you yet") {
		t.Errorf("with nothing priced: %q; want the machine told none is", out)
	}
	if !strings.Contains(out, "magpie model price <provider/model>") {
		t.Errorf("with nothing priced: %q; want the hint at how to price one", out)
	}

	// setting a broken price is refused, so it is written past the check
	// here: that is the only way the state is reachable, and it is
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"a/m":    {Input: new(0.5), Output: new(1.5), CacheRead: new(0.05), CacheWrite: new(0.1)},
		"b/*":    {Input: new(2.0), Output: new(20.0), CacheRead: new(0.2), CacheWrite: new(2.0)},
		"b/half": {Input: new(1.0)},
	}}); err != nil {
		t.Fatal(err)
	}
	out, err = said(t, func() error { return modelCmd([]string{"prices"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a/m", "$0.5/$1.5 per 1M in/out", "cache $0.05/$0.1",
		"b/*", "$2/$20 per 1M in/out",
		"b/half", "no output price given, and ignored",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("magpie model prices: %q; want %q among it", out, want)
		}
	}
}

// A price outlives the provider it was set for: that provider can be
// deleted, and `magpie model prices` still lists the price and usage is
// still counted at it, so --reset has to reach it by the key it is stored
// at rather than by asking for a provider that is not there any more. A
// reset that went looking for the provider first refused the one price the
// user most needed to take off.
func TestModelPriceResetAfterTheProviderIsGone(t *testing.T) {
	groupsHome(t)
	if err := modelCmd([]string{"price", "b/vendor/m", "0.5,1.5,0.05,0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"price", "b/*", "2,3,0,0"}); err != nil {
		t.Fatal(err)
	}
	out, err := said(t, func() error { return modelCmd([]string{"prices"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"b/vendor/m", "b/*"} {
		if !strings.Contains(out, want) {
			t.Fatalf("magpie model prices: %q; want %q among it", out, want)
		}
	}
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	// the price is still what the model is counted at: the provider going
	// did not take it out of the file
	if pr, ok := provider.EffectivePrice("b", "vendor/m"); !ok || pr.Input != 0.5 {
		t.Fatalf("with the provider gone: %+v, %v; want the stated $0.5", pr, ok)
	}
	for _, ref := range []string{"b/vendor/m", "b/*"} {
		out, err := said(t, func() error { return modelCmd([]string{"price", ref, "--reset"}) })
		if err != nil {
			t.Fatalf("%s --reset: %v", ref, err)
		}
		if !strings.Contains(out, ref) {
			t.Errorf("%s --reset printed %q; want the key it cleared named", ref, out)
		}
	}
	out, err = said(t, func() error { return modelCmd([]string{"prices"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"b/vendor/m", "b/*"} {
		if strings.Contains(out, gone) {
			t.Errorf("magpie model prices: %q; want %s no longer listed", out, gone)
		}
	}
	if left := settings.Load().ModelPrices; len(left) != 0 {
		t.Errorf("the prices are still in the file: %v", left)
	}
}

// A price is kept at a key that outlives the provider it was set for, and
// nothing rewrites that key when that provider goes — so the name it went by
// can be given to another provider, whose own price then sits under an id
// the user never typed. Asking for the provider first, the way setting a
// price does, cleared that second provider's key: nothing was stored under
// it, so nothing changed, the tick said a price had gone, and the one the
// user meant stayed in the file to be counted at.
func TestModelPriceResetTakesTheKeyThePriceIsStoredAt(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := modelCmd([]string{"price", "b/vendor/m", "0.5,1.5,0.05,0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{
		ID: "c", Name: "B", Key: "kc", Models: []string{"vendor/m"}, Chat: "http://127.0.0.1:1/v1",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := said(t, func() error { return modelCmd([]string{"price", "b/vendor/m", "--reset"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "b/vendor/m") {
		t.Errorf("--reset printed %q; want the key it cleared named, not one nothing is stored at", out)
	}
	if left := settings.Load().ModelPrices; len(left) != 0 {
		t.Errorf("the price is still counted at what the user paid: %v", left)
	}
}

// A price set through a display name is kept under the provider's id, and
// --reset has to reach it there, the way SetModelPrice writes it: a user who
// only ever names a provider by the name it goes by must not be told the
// price they set was never there.
func TestModelPriceResetByTheNameTheUserAsksFor(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := provider.Save(provider.Provider{
		ID: "c", Name: "Cee", Key: "kc", Models: []string{"vendor/m"}, Chat: "http://127.0.0.1:1/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"price", "Cee/vendor/m", "0.5,1.5,0.05,0.1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelPrices["c/vendor/m"]; !ok {
		t.Fatalf("the price is not kept under the provider's id: %v", settings.Load().ModelPrices)
	}

	out, err := said(t, func() error { return modelCmd([]string{"price", "Cee/vendor/m", "--reset"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "c/vendor/m") {
		t.Errorf("--reset printed %q; want the key it cleared named", out)
	}
	if left := settings.Load().ModelPrices; len(left) != 0 {
		t.Errorf("taking it away by the name it is asked for by left %v", left)
	}
}

// --reset says what it did. A key with no price under it has none to take
// away, and a tick over a price that is still in the file is the one thing a
// user cannot check for themselves: a mistyped id, or a model priced only
// through its provider, both read as a price gone.
func TestModelPriceResetSaysWhenThereIsNoPriceToTakeAway(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := modelCmd([]string{"price", "a/*", "2,3,0,0"}); err != nil {
		t.Fatal(err)
	}

	// a model of a provider whose every model is priced, but not of its own
	out, err := said(t, func() error { return modelCmd([]string{"price", "a/only-a", "--reset"}) })
	if err == nil {
		t.Fatalf("--reset of a model with no price of its own: %q; want it to say there was none", out)
	}
	if strings.Contains(out, "✓") {
		t.Errorf("--reset printed %q; a tick says a price was taken away", out)
	}
	if !strings.Contains(err.Error(), "no price of its own") {
		t.Errorf("error %q; want it to say the model has no price of its own", err)
	}
	// and where what it does cost comes from, so the user is not left
	// wondering what a model is counted at now
	if !strings.Contains(err.Error(), "a/*") {
		t.Errorf("error %q; want the provider-wide price still in force named", err)
	}
	if left := settings.Load().ModelPrices; len(left) != 1 {
		t.Errorf("prices now %v; want the provider-wide one left alone", left)
	}

	// and a model of a provider with no price of any kind
	out, err = said(t, func() error { return modelCmd([]string{"price", "b/vendor/m", "--reset"}) })
	if err == nil {
		t.Fatalf("--reset of a model never priced: %q; want it to say so", out)
	}
	if strings.Contains(err.Error(), "a/*") {
		t.Errorf("error %q; another provider's price for all its models is none of this one's business", err)
	}
}

// The help names the listing: a command the user cannot find is one they
// never run.
func TestModelHelpNamesThePricesListing(t *testing.T) {
	out, err := said(t, func() error { return modelCmd([]string{"help"}) })
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "magpie model prices") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("the help names no `magpie model prices`:\n%s", out)
	}
	if !strings.HasPrefix(line, "  magpie model prices") {
		t.Errorf("%q; want it listed among the other magpie model commands", line)
	}
}

// The help reads as one table: every description, and every line a
// description runs on to, starts at the same column. The `magpie model price`
// lines sat a column to the right of the rest, so the commands a price is
// set and taken off with read as a table of their own.
func TestModelUsageAlignsEveryDescription(t *testing.T) {
	out, err := said(t, func() error { return modelCmd([]string{"help"}) })
	if err != nil {
		t.Fatal(err)
	}
	col := -1
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			break // the end of the commands; what follows is prose of its own
		}
		if !strings.HasPrefix(line, "  magpie ") {
			at := len(line) - len(strings.TrimLeft(line, " "))
			if col >= 0 && at != col {
				t.Errorf("a line a description runs on to starts at column %d, want %d: %q", at, col, line)
			}
			continue
		}
		rest := line[len("  magpie"):]
		at := strings.Index(rest, "  ")
		if at < 0 {
			continue // the command is too long for a description beside it
		}
		at += len("  magpie") + len(rest[at:]) - len(strings.TrimLeft(rest[at:], " "))
		if col < 0 {
			col = at
		} else if at != col {
			t.Errorf("this description starts at column %d, want %d: %q", at, col, line)
		}
	}
	if col < 0 {
		t.Fatalf("no description column in the help:\n%s", out)
	}
}

// priceFrom is what tells a user where the price a model is counted at
// came from: the one given for it, the one given for every model of its
// provider, a price that is there but cannot be billed and is passed over,
// or none of those — which leaves the provider's own list price and then
// its maker's. The order is the one EffectivePrice looks in, and the two
// must tell the same story: a model counted at the provider's price is not
// told it came from its own.
func TestPriceFrom(t *testing.T) {
	own := settings.ModelPrice{Input: new(0.5), Output: new(1.5), CacheRead: new(0.05), CacheWrite: new(0.1)}
	wide := settings.ModelPrice{Input: new(9.0), Output: new(90.0), CacheRead: new(0.9), CacheWrite: new(9.0)}
	broken := settings.ModelPrice{Input: new(1.0)} // the other three parts are not given
	for _, tc := range []struct {
		what   string
		prices map[string]settings.ModelPrice
		want   string
	}{
		{"the price given for this model", map[string]settings.ModelPrice{"relay/sol": own}, "model"},
		{"the price given for every model of it", map[string]settings.ModelPrice{"relay/*": own}, "provider"},
		{"its own beats the provider's", map[string]settings.ModelPrice{"relay/sol": own, "relay/*": wide}, "model"},
		{"a price with a part missing is passed over", map[string]settings.ModelPrice{"relay/sol": broken}, "ignored"},
		{"and the provider's own stands instead", map[string]settings.ModelPrice{"relay/sol": broken, "relay/*": own}, "provider"},
		{"a wildcard with a part missing is passed over too", map[string]settings.ModelPrice{"relay/*": broken}, "ignored"},
		{"nothing given at all", nil, ""},
		{"another provider's price", map[string]settings.ModelPrice{"other/sol": own}, ""},
		{"another model of this provider", map[string]settings.ModelPrice{"relay/other": own}, ""},
	} {
		if got := priceFrom(settings.Settings{ModelPrices: tc.prices}, "relay", "sol"); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.what, got, tc.want)
		}
	}
}

// priceFrom in isolation says where a price came from; what the user reads
// is the line under it. These are the four, and each is the one a wrong
// priceFrom would swap for another — a price set but passed over read as
// the provider's own is the one that would send a user to set it again.
func TestModelPriceCmdSaysWhereThePriceCameFrom(t *testing.T) {
	groupsHome(t)
	// a maker's catalogue is what the last line is reached through, and
	// groupsHome leaves no cache behind; seed one the way a real run has it.
	catalog.Changed = nil // no agent's files are rewritten here
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{
	  "claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":5,"output":25,"cache_read":0.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	show := func(model string) string {
		out, err := said(t, func() error { return modelCmd([]string{"price", "a/" + model}) })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// "m" is a model nothing in the catalogue lists a price for; the last
	// line is reached through one a maker does.
	if out := show("claude-opus-5-5"); !strings.Contains(out, "what its provider lists, else its maker's on models.dev") {
		t.Errorf("nothing given: printed %q, want it to say the price is the provider's or the maker's", out)
	}
	for _, tc := range []struct {
		what string
		set  []string
		want string
	}{
		{"a price for every model of the provider", []string{"price", "a/*", "2,3,0,0"}, "what you said every model of this provider costs"},
		{"and then one for this model", []string{"price", "a/m", "0.5,1.5,0.05,0"}, "what you said this model costs"},
	} {
		if err := modelCmd(tc.set); err != nil {
			t.Fatal(err)
		}
		if out := show("m"); !strings.Contains(out, tc.want) {
			t.Errorf("%s: printed %q, want it to say %q", tc.what, out, tc.want)
		}
	}
	// a price that is there but unusable is the one line that must not
	// read as the provider's own: setting it again would change nothing.
	if err := modelCmd([]string{"price", "a/m", "--reset"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"price", "a/*", "--reset"}); err != nil {
		t.Fatal(err)
	}
	// a price that is there but unusable is the one line that must not read
	// as the provider's own: setting it again would change nothing. It is
	// written straight into the file, which is the only way to leave one
	// broken — the command refuses to.
	leave := func(key string, m settings.ModelPrice) {
		s := settings.Load()
		if s.ModelPrices == nil {
			s.ModelPrices = map[string]settings.ModelPrice{}
		}
		s.ModelPrices[key] = m
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	// "m" is a model nothing prices, so a broken price there ends the
	// command before it says where the price came from; one a maker prices
	// is what reaches that line.
	leave("a/claude-opus-5-5", settings.ModelPrice{Input: new(0.5)})
	if out := show("claude-opus-5-5"); !strings.Contains(out, "not usable and is ignored") {
		t.Errorf("a price with a part missing: printed %q, want it to say it is ignored", out)
	}
	// and the same for a wildcard left broken on its own
	if err := modelCmd([]string{"price", "a/claude-opus-5-5", "--reset"}); err != nil {
		t.Fatal(err)
	}
	leave("a/*", settings.ModelPrice{Output: new(1.5)})
	if out := show("claude-opus-5-5"); !strings.Contains(out, "not usable and is ignored") {
		t.Errorf("a wildcard with a part missing: printed %q, want it to say it is ignored", out)
	}
}

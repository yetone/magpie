package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
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
// said runs f with what it prints taken off the terminal, the way a user
// reads a number of tokens off the command rather than out of the file.
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

// magpie model context and output: the provider's own "*" says what it
// gives every model, 0 is refused rather than read as a --reset and the
// refusal says which is, and a model the provider does not serve is
// refused the same as one is for a name.
func TestModelLimitCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here

	if err := modelCmd([]string{"context", "a/*", "200000"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"output", "a/*", "128k"}); err != nil {
		t.Fatal(err)
	}
	// the wildcard is not a model, so it has no entry of its own; asking
	// for it must still answer with the value it holds
	for _, verb := range []string{"context", "output"} {
		out, err := said(t, func() error { return modelCmd([]string{verb, "a/*"}) })
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "is not a model") {
			t.Errorf("%s a/*: %q; the provider's own value is what was asked for", verb, out)
		}
	}
	if out, _ := said(t, func() error { return modelCmd([]string{"context", "a/*"}) }); !strings.Contains(out, "200k") {
		t.Errorf("context a/*: %q; want the 200k set for every model", out)
	}
	if out, _ := said(t, func() error { return modelCmd([]string{"output", "a/*"}) }); !strings.Contains(out, "128k") {
		t.Errorf("output a/*: %q; want the 128k set for every model", out)
	}

	// 0 tokens is not a limit; refusing it has to say which one is, and
	// --reset is how a limit is taken off a model
	for _, c := range []struct{ verb, what string }{
		{"context", "a window"},
		{"output", "a reply limit"},
	} {
		err := modelCmd([]string{c.verb, "a/m", "0"})
		if err == nil {
			t.Errorf("%s of 0 tokens was accepted", c.what)
			continue
		}
		// the refusal is a command to paste, not a flag to find in the
		// help: "a/m --reset" leaves the reader to work out the verb
		if !strings.Contains(err.Error(), "magpie model "+c.verb+" a/m --reset") {
			t.Errorf("%s of 0 tokens: %q; it does not give the command that takes one away", c.what, err.Error())
		}
	}
	// and a model the provider does not serve is refused, not kept
	if err := modelCmd([]string{"context", "a/typo", "1m"}); err == nil {
		t.Error("a window for a model a does not serve was accepted")
	}
	if err := modelCmd([]string{"output", "a/typo", "1m"}); err == nil {
		t.Error("a reply limit for a model a does not serve was accepted")
	}
	if n := settings.Load().ModelOutputs["a/typo"]; n != 0 {
		t.Errorf("a refused reply limit was kept anyway: %d", n)
	}
}

// A provider kept unlisted, or switched off, still serves its models and
// still takes the window a user gives one, so the query has to answer about
// the very ids the setter takes: "is not a model this provider serves" on
// one, and the next command storing a window on it, is magpie arguing with
// itself.
func TestModelLimitCmdOnAProviderTheCatalogHasNot(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	for _, c := range []struct {
		id, model     string
		unlisted, off bool
	}{
		{"a", "m", true, false},        // unlisted: still served, through a group
		{"b", "vendor/m", false, true}, // switched off: takes what it is given
	} {
		id := c.id + "/" + c.model
		p, err := provider.Find(c.id)
		if err != nil {
			t.Fatal(err)
		}
		p.Unlisted, p.Off = c.unlisted, c.off
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
		if err := modelCmd([]string{"context", id, "256k"}); err != nil {
			t.Fatalf("context %s 256k: %v", id, err)
		}
		if err := modelCmd([]string{"output", id, "128k"}); err != nil {
			t.Fatalf("output %s 128k: %v", id, err)
		}
		for _, verb := range []string{"context", "output"} {
			out, err := said(t, func() error { return modelCmd([]string{verb, id}) })
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "is not a model") {
				t.Errorf("%s %s: %q; the setter took the very id the query refuses", verb, id, out)
			}
		}
		if out, _ := said(t, func() error { return modelCmd([]string{"context", id}) }); !strings.Contains(out, "256k") {
			t.Errorf("context %s: %q; want the 256k just set", id, out)
		}
		if out, _ := said(t, func() error { return modelCmd([]string{"output", id}) }); !strings.Contains(out, "128k") {
			t.Errorf("output %s: %q; want the 128k just set", id, out)
		}
	}
}

// A model taken off a provider leaves a window and a reply limit keyed by an
// id the provider no longer serves. Both --reset have to clear them all the
// same, or the numbers are only ever off by hand: the user is told the
// provider has no such model and left with the file.
func TestModelLimitCmdResetsAModelTheProviderHasNot(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	for _, verb := range []string{"context", "output"} {
		if err := modelCmd([]string{verb, "a/m", "200k"}); err != nil {
			t.Fatal(err)
		}
	}
	// magpie provider set a models=...: m is not on the list any more
	p, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	p.Models = []string{"only-a"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"context", "output"} {
		out, err := said(t, func() error { return modelCmd([]string{verb, "a/m", "--reset"}) })
		if err != nil {
			t.Errorf("%s a/m --reset: %v; the limit of a model that has gone cannot be taken off", verb, err)
			continue
		}
		if !strings.Contains(out, "a/m") {
			t.Errorf("%s a/m --reset: %q; it does not name the model it cleared", verb, out)
		}
	}
	if n := settings.Load().ModelOutputs["a/m"]; n != 0 {
		t.Errorf("a/m still answers with at most %d tokens", n)
	}
	stored, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	if n := stored.Contexts["m"]; n != 0 {
		t.Errorf("a/m still takes %d tokens", n)
	}
	// while setting one is still refused: it would answer no request, and
	// nothing would ever say the number is not in force
	if err := modelCmd([]string{"output", "a/m", "128k"}); err == nil {
		t.Error("a reply limit for a model a no longer serves was accepted")
	}
}

// A reply limit outlives the provider it was set for, and that provider can
// be deleted — magpie provider delete a — leaving the entry in the settings
// under an id nothing serves any more. The --reset has to go looking for the
// provider no more than the file needs it to: with the ref resolved first it
// refuses, and the number is then only ever off by hand. And the line it
// prints must not point at a query that could only report the model is gone.
func TestModelLimitCmdResetsALimitOfAProviderThatIsGone(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := modelCmd([]string{"output", "a/m", "128k"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"output", "a/*", "64k"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete("a"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a/m", "a/*"} {
		out, err := said(t, func() error { return modelCmd([]string{"output", id, "--reset"}) })
		if err != nil {
			t.Errorf("output %s --reset: %v; a limit of a provider that is gone cannot be taken off", id, err)
			continue
		}
		if !strings.Contains(out, id) {
			t.Errorf("output %s --reset: %q; it does not name the entry it cleared", id, out)
		}
		if strings.Contains(out, "says what it answers with now") {
			t.Errorf("output %s --reset: %q; it points at a query that cannot answer, nothing serves %s any more", id, out, id)
		}
	}
	if left := settings.Load().ModelOutputs; len(left) != 0 {
		t.Errorf("the settings still hold %v; want none", left)
	}
	// setting one is still refused, and so is a group's: with no provider to
	// resolve, both stand on the key itself
	if err := modelCmd([]string{"output", "a/m", "128k"}); err == nil {
		t.Error("a reply limit for a provider that is gone was accepted")
	}
	if err := modelCmd([]string{"output", "group/auto", "--reset"}); err == nil {
		t.Error("a routing group's was taken as a model's limit")
	}
}

// A reply limit outlives the provider it was set for, and nothing rewrites
// its key when that provider is deleted, so "a/m" is still in the settings
// while a provider of another id has since been given the display name "A".
// The --reset has to clear the entry it is stored under and name that
// entry: resolved as a name first it clears the other provider's own limit
// instead — the user is told that is gone — and leaves the entry they asked
// about in the file to go on answering with.
func TestModelLimitCmdResetsTheKeyTheEntryIsStoredUnder(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	for _, id := range []string{"a/m", "a/*"} {
		if err := modelCmd([]string{"output", id, "128k"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Delete("a"); err != nil {
		t.Fatal(err)
	}
	// a provider of another id given the name a is what those entries have
	// to survive
	if err := provider.Save(provider.Provider{ID: "c", Name: "A", Key: "kc", Models: []string{"m"},
		Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c/m", "c/*"} {
		if err := modelCmd([]string{"output", id, "64k"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a/m", "a/*"} {
		out, err := said(t, func() error { return modelCmd([]string{"output", id, "--reset"}) })
		if err != nil {
			t.Errorf("output %s --reset: %v; a limit of a provider that is gone cannot be taken off", id, err)
			continue
		}
		if !strings.Contains(out, id) {
			t.Errorf("output %s --reset: %q; it does not name the entry it cleared", id, out)
		}
		if strings.Contains(out, "c/") {
			t.Errorf("output %s --reset: %q; it is about another provider, which is not the entry the user asked for", id, out)
		}
	}
	// and that provider's own limits are the ones left standing
	outs := settings.Load().ModelOutputs
	if len(outs) != 2 || outs["c/m"] != 64000 || outs["c/*"] != 64000 {
		t.Errorf("the settings hold %v; want the other provider's two limits of 64k and none of a's", outs)
	}
	// and an id nothing is stored under is not cleared by the back door: the
	// command says there was no limit of its own to take away, and takes
	// nothing else off
	out, err := said(t, func() error { return modelCmd([]string{"output", "typo/m", "--reset"}) })
	if err == nil {
		t.Errorf("output typo/m --reset: %q; want it to say there was no limit there to take", out)
	} else if !strings.Contains(err.Error(), "typo/m") || !strings.Contains(err.Error(), "no reply limit of its own") {
		t.Errorf("output typo/m --reset: %q; want it to name the key and say it has no limit of its own", err)
	}
	if n := settings.Load().ModelOutputs["c/m"]; n != 64000 {
		t.Errorf("c/m answers with at most %d; want its own 64k", n)
	}
}

// A model's own window over its provider's every-model one: --reset takes
// the model's away and leaves the provider's, so the query has to name where
// it goes — telling the user it goes to the vendor's list would send it to a
// number nobody set.
func TestModelLimitCmdSaysWhereResetGoes(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	for _, verb := range []string{"context", "output"} {
		if err := modelCmd([]string{verb, "a/*", "200k"}); err != nil { // every model of a
			t.Fatal(err)
		}
		if err := modelCmd([]string{verb, "a/m", "300k"}); err != nil { // the model's own
			t.Fatal(err)
		}
		out, err := said(t, func() error { return modelCmd([]string{verb, "a/m"}) })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "goes back to the one you set for every model") {
			t.Errorf("%s a/m: %q; it does not say what --reset goes back to", verb, out)
		}
		if strings.Contains(out, "vendor's list") {
			t.Errorf("%s a/m: %q; --reset goes to the 200k given to every model, not to the vendor's list", verb, out)
		}
		// and once the model's own is reset, the provider's answers, which
		// is what the hint promised
		if err := modelCmd([]string{verb, "a/m", "--reset"}); err != nil {
			t.Fatal(err)
		}
		out, _ = said(t, func() error { return modelCmd([]string{verb, "a/m"}) })
		if !strings.Contains(out, "200k") {
			t.Errorf("%s a/m after --reset: %q; want the 200k set for every model", verb, out)
		}
	}
}

// The help says once, in one sentence, what a name, its levels and its limits
// are per-provider's: a second copy of the claim beside the first is what a
// reader takes for two different rules.
func TestModelUsageSaysPerProviderOnce(t *testing.T) {
	// the help is wrapped to fit a terminal, so match on the words rather
	// than on where a line happens to break
	flat := strings.Join(strings.Fields(modelUsage), " ")
	if n := strings.Count(flat, "The same model from another provider"); n != 1 {
		t.Errorf("the help says what another provider's own model keeps %d times; want once", n)
	}
	if !strings.Contains(flat, "keeps its own name, levels and limits") {
		t.Error("the help leaves a window and a reply limit out of what another provider's own model keeps")
	}
}

// A --reset over a limit that was never set takes nothing off, and a ✓ over
// one says the model had a limit of its own — leaving the user to work out
// which of their commands has been ignored. Every way in has to say there was
// none to take: a model the provider does not serve, a model with nothing set
// on it, and the second --reset of a limit already taken off.
func TestModelLimitCmdSaysWhenThereIsNoLimitToTakeAway(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	for _, verb := range []string{"context", "output"} {
		for _, id := range []string{"a/nosuch", "a/m"} {
			out, err := said(t, func() error { return modelCmd([]string{verb, id, "--reset"}) })
			if err == nil {
				t.Errorf("%s %s --reset: %q; want it to say there was no limit to take away", verb, id, out)
				continue
			}
			if strings.Contains(out, "✓") {
				t.Errorf("%s %s --reset: printed %q; a tick says a limit was taken off", verb, id, out)
			}
			if !strings.Contains(err.Error(), "of its own") {
				t.Errorf("%s %s --reset: %q; want it to say the model has no limit of its own", verb, id, err)
			}
		}
		// and the same command a second time over a limit the first took
		// off, which is the case a user hits after mistyping a --reset
		if err := modelCmd([]string{verb, "a/m", "200k"}); err != nil {
			t.Fatal(err)
		}
		if err := modelCmd([]string{verb, "a/m", "--reset"}); err != nil {
			t.Fatalf("%s a/m --reset: %v; the limit set just now cannot be taken off", verb, err)
		}
		out, err := said(t, func() error { return modelCmd([]string{verb, "a/m", "--reset"}) })
		if err == nil {
			t.Errorf("%s a/m --reset: %q; want it to say no limit was left to take away", verb, out)
		}
		if strings.Contains(out, "✓") {
			t.Errorf("%s a/m --reset: printed %q; a tick over a limit taken off a moment ago", verb, out)
		}
	}

	// what the model has instead is the provider's own every-model value,
	// and that is the command that takes it away — quoted, as a shell
	// reads a bare * as a glob
	for _, verb := range []string{"context", "output"} {
		if err := modelCmd([]string{verb, "a/*", "200k"}); err != nil {
			t.Fatal(err)
		}
		out, err := said(t, func() error { return modelCmd([]string{verb, "a/m", "--reset"}) })
		if err == nil {
			t.Fatalf("%s a/m --reset: %q; want it to say the model has no limit of its own", verb, out)
		}
		if !strings.Contains(err.Error(), "magpie model "+verb+" 'a/*' --reset") {
			t.Errorf("%s a/m --reset: %q; want it to point at the provider's own every-model value", verb, err)
		}
		if out, err := said(t, func() error { return modelCmd([]string{verb, "a/*", "--reset"}) }); err != nil {
			t.Errorf("%s 'a/*' --reset: %q; the value the refusal pointed at cannot be taken off", verb, out)
		}
	}
	if n := settings.Load().ModelOutputs["a/*"]; n != 0 {
		t.Errorf("a/* still answers with at most %d tokens; want none", n)
	}
	stored, err := provider.Find("a")
	if err != nil {
		t.Fatal(err)
	}
	if n := stored.Contexts["*"]; n != 0 {
		t.Errorf("a/* still takes %d tokens for every model; want none", n)
	}
}

// 0 is no limit, and the command the refusal gives is one the user has to be
// able to paste. The wildcard is quoted, as a shell reads a bare * as a glob
// of the files in the directory the command is run from, and it is named as
// what it takes off: every model of the provider, which is what a/* is and
// not a model of its own.
func TestModelLimitCmdZeroNamesEveryModelOfTheProvider(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	for _, verb := range []string{"context", "output"} {
		err := modelCmd([]string{verb, "a/*", "0"})
		if err == nil {
			t.Fatalf("%s 'a/*' 0 was accepted", verb)
		}
		if !strings.Contains(err.Error(), "magpie model "+verb+" 'a/*' --reset") {
			t.Errorf("%s 'a/*' 0: %q; the command it gives has its wildcard quoted", verb, err)
		}
		if !strings.Contains(err.Error(), "every model of a") {
			t.Errorf("%s 'a/*' 0: %q; the wildcard is every model of the provider, not this model", verb, err)
		}
	}
}

// A reply limit outlives the provider it was set for and nothing rewrites its
// key when that provider is deleted, so "b/vendor/m" is still in the settings
// while a provider of another id has since been given the display name "b".
// The entry this --reset cleared is b's, but a query spelled that way is
// resolved as a name and reads c's own model — so the line it prints must not
// send the user to a number this command never touched.
func TestModelLimitCmdResetDoesNotPointAtAnotherProvidersModel(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := modelCmd([]string{"output", "b/vendor/m", "128k"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	// and a provider of another id given the name b, with a limit of its
	// own for the query to answer with
	if err := provider.Save(provider.Provider{ID: "c", Name: "b", Key: "kc", Models: []string{"vendor/m"},
		Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"output", "c/vendor/m", "64k"}); err != nil {
		t.Fatal(err)
	}
	out, err := said(t, func() error { return modelCmd([]string{"output", "b/vendor/m", "--reset"}) })
	if err != nil {
		t.Fatalf("output b/vendor/m --reset: %v; the entry it names cannot be taken off", err)
	}
	if !strings.Contains(out, "b/vendor/m") {
		t.Errorf("output b/vendor/m --reset: %q; it does not name the entry it cleared", out)
	}
	if strings.Contains(out, "says what it answers with now") {
		t.Errorf("output b/vendor/m --reset: %q; that query answers with c's model, which this command never touched", out)
	}
	// the entry is gone and the other provider's own limit is left standing
	if n, ok := settings.Load().ModelOutputs["b/vendor/m"]; ok {
		t.Errorf("b/vendor/m still answers with at most %d tokens; want none", n)
	}
	if n := settings.Load().ModelOutputs["c/vendor/m"]; n != 64000 {
		t.Errorf("c/vendor/m answers with at most %d; want its own 64k", n)
	}
	// which is what the spelling resolves to, and so what a query for it
	// would have said
	if out, _ := said(t, func() error { return modelCmd([]string{"output", "b/vendor/m"}) }); !strings.Contains(out, "c/vendor/m") {
		t.Errorf("output b/vendor/m: %q; the name resolves to c, so that is the entry a query answers with", out)
	}
}

// The help reads as one table: a command, and what it does, the two a couple
// of spaces apart. A command that fills the column leaves a single space
// between them, which is no column at all — the row reads as one run-on line
// and the command as one the help never explains.
func TestModelUsageGivesEveryCommandADescription(t *testing.T) {
	out, err := said(t, func() error { return modelCmd([]string{"help"}) })
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(out, "\n")
	col := usageColumn(rows)
	if col < 0 {
		t.Fatalf("the help has no column its descriptions start in:\n%s", out)
	}
	seen := 0
	for i, line := range rows {
		if !strings.HasPrefix(line, "  magpie model ") {
			continue
		}
		seen++
		if d := usageDescription(rows, i, col); d == "" {
			t.Errorf("this command is given no description: %q", strings.TrimSpace(line))
		}
	}
	if seen == 0 {
		t.Fatalf("the help lists no `magpie model` command:\n%s", out)
	}
}

// usageColumn is where the help's descriptions start: the column its commands
// butt their descriptions against, taken from the first command long enough
// to leave one.
func usageColumn(rows []string) int {
	for _, l := range rows {
		if !strings.HasPrefix(l, "  magpie model ") {
			continue
		}
		rest := l[len("  magpie"):]
		at := strings.Index(rest, "  ")
		if at < 0 {
			continue // the command is too long for a description beside it
		}
		return len("  magpie") + at + len(rest[at:]) - len(strings.TrimLeft(rest[at:], " "))
	}
	return -1
}

// usageDescription is what the help says the command on rows[i] does: the
// text beside it, or, where the command fills the column, the lines under it
// the description runs on to.
func usageDescription(rows []string, i, col int) string {
	rest := rows[i][len("  magpie"):]
	if at := strings.Index(rest, "  "); at >= 0 {
		return strings.TrimSpace(rest[at:])
	}
	var b strings.Builder
	for _, on := range rows[i+1:] {
		if strings.TrimSpace(on) == "" || len(on)-len(strings.TrimLeft(on, " ")) != col {
			break
		}
		b.WriteString(" " + strings.TrimSpace(on))
	}
	return strings.TrimSpace(b.String())
}

// Every command magpie shows is one the user is meant to paste, and a shell
// reads a bare * in it as a glob of the directory the command runs in rather
// than as the name: zsh fails the whole line on a name it cannot expand, and
// bash hands magpie the files there instead. So a ref carrying the wildcard
// is quoted wherever it is shown as part of a command, the help's table
// included, which spells it the way #280 does.
func TestModelCmdQuotesAWildcardInEveryCommandItShows(t *testing.T) {
	for _, verb := range []string{"context", "output"} {
		row := helpRow("magpie model " + verb + " <provider/model> <n>")
		if row == "" {
			t.Fatalf("the help has no `magpie model %s <provider/model> <n>` row:\n%s", verb, modelUsage)
		}
		if !strings.Contains(row, "'<provider>/*'") {
			t.Errorf("magpie model %s <provider/model> <n>: %q; the wildcard it hands over is not quoted", verb, row)
		}
	}

	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	type shown struct{ as, out string }
	var shows []shown
	run := func(args ...string) {
		out, err := said(t, func() error { return modelCmd(args) })
		if err != nil {
			out += "\n" + err.Error()
		}
		// the same command says something else once a value is set, so
		// every run is kept: the last one would hide the first
		shows = append(shows, shown{strings.Join(args, " "), out})
	}
	for _, verb := range []string{"context", "output"} {
		run(verb, "a/*") // nothing set for every model of the provider yet
		run(verb, "a/m")
	}
	run("price", "a/m")
	if err := modelCmd([]string{"price", "a/*", "2,3,0,0"}); err != nil {
		t.Fatal(err)
	}
	run("price", "a/m")
	for _, verb := range []string{"context", "output"} {
		if err := modelCmd([]string{verb, "a/*", "128k"}); err != nil {
			t.Fatal(err)
		}
		run(verb, "a/*")
		run(verb, "a/*", "--reset")
		run(verb, "a/m", "--reset")
		run(verb, "a/m", "0")
	}
	run("price", "a/m", "--reset")
	if len(shows) == 0 {
		t.Fatal("no command printed anything to check")
	}
	for _, s := range shows {
		for _, m := range shownCommand.FindAllStringSubmatch(s.out, -1) {
			if ref := m[1]; strings.Contains(ref, "*") && !(strings.HasPrefix(ref, "'") && strings.HasSuffix(ref, "'")) {
				t.Errorf("`magpie model %s` printed %q; the wildcard in %q is a glob to a shell", s.as, s.out, ref)
			}
		}
	}
}

// shownCommand is a command as it is shown to the user: the verb and the ref
// that follows it.
var shownCommand = regexp.MustCompile(`magpie model [a-z][a-z-]* (\S+)`)

// helpRow is the one line of the help that starts with the given command.
func helpRow(cmd string) string {
	for _, l := range strings.Split(modelUsage, "\n") {
		if strings.HasPrefix(l, "  "+cmd) {
			return l
		}
	}
	return ""
}

// Where a number came from is said in the verb of the command that sets it: a
// window is what a model takes, a reply limit what it answers with. The two
// --reset lines name the same thing the same way and one had drifted, so the
// query a reply limit pointed at read as though it reported a window.
func TestModelLimitCmdSaysWhatALimitIsInTheVerbOfItsOwnCommand(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	for _, tc := range []struct{ verb, says string }{
		{"context", "takes"},
		{"output", "answers with"},
	} {
		if err := modelCmd([]string{tc.verb, "a/m", "128k"}); err != nil {
			t.Fatal(err)
		}
		out, err := said(t, func() error { return modelCmd([]string{tc.verb, "a/m", "--reset"}) })
		if err != nil {
			t.Fatalf("%s a/m --reset: %v; the limit set just now cannot be taken off", tc.verb, err)
		}
		if !strings.Contains(out, "magpie model "+tc.verb+" a/m says what it "+tc.says+" now") {
			t.Errorf("%s a/m --reset: %q; it does not say the limit in the verb of its own command", tc.verb, out)
		}
		other := "answers with"
		if tc.says == other {
			other = "takes"
		}
		if strings.Contains(out, "says what it "+other+" now") {
			t.Errorf("%s a/m --reset: %q; %q is the other command's verb", tc.verb, out, other)
		}
	}
}

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

// saidArgs is what a command printed, which is where the wire name is
// reported and what the messages about it have to be read from. It is named
// for its arguments rather than said so it does not collide with the said of
// a per-model limits or a price command, which runs a closure instead of a
// command line: they are three separate pull requests and the three of them
// in one tree must still be one package.
func saidArgs(t *testing.T, args ...string) string {
	t.Helper()
	out, err := refusedArgs(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// refusedArgs is what a command printed and the error it refused with, which
// is the pair a command that answers with an error instead of a line has to
// be read as: whether a tick was printed anyway is half of what a user sees
// of a refused command, and the other half is the exit code the error gives
// it, which is 1 for anything this returns (main).
func refusedArgs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		b, _ := io.ReadAll(r)
		out.Write(b)
		close(done)
	}()
	err = modelCmd(args)
	w.Close()
	os.Stdout = old
	<-done
	r.Close()
	return out.String(), err
}

func mustFind(t *testing.T, id string) provider.Provider {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

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

// magpie model wire keeps and drops the name a provider's models are asked
// for, and says what is in force after every change. A provider-wide name
// stands on a model that provider really serves rather than on an id made up
// for it, a name with no "*" in it is not explained with one, and a name of
// only whitespace is a name taken away, which is answered as the removal it
// is rather than as a setting that took.
func TestModelWireCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	wires := func() map[string]string { return settings.Load().ModelWires }

	// a custom provider's every model, with the model in the name: the
	// example is one of that provider's own, which is the only kind this
	// exists for — a relay — and not an id no provider here serves
	if out := saidArgs(t, "wire", "b/*", "vendor-c/*"); !strings.Contains(out, "b/vendor/m is asked for as vendor-c/vendor/m") {
		t.Fatalf("naming every model said %q; want an example that is one of B's own", out)
	} else if strings.Contains(out, "model-2") {
		t.Fatalf("the example is a model no provider here serves: %q", out)
	}
	if n := wires()["b/*"]; n != "vendor-c/*" {
		t.Fatalf("the name given for every model of B is %q; want vendor-c/*", n)
	}
	if got := provider.UpstreamName(mustFind(t, "b"), "gpt-5.5"); got != "vendor-c/gpt-5.5" {
		t.Errorf("every model asked for as %q; want vendor-c/gpt-5.5", got)
	}

	// the query explains the "*" only where there is one: a name with none
	// asks for every model alike, and a sentence about the star would
	// contradict the name printed above it
	if out := saidArgs(t, "wire", "b/*", "vendor-c/one"); !strings.Contains(out, "B is asked for every one of its models as vendor-c/one") {
		t.Errorf("naming every model alike said %q", out)
	}
	if out := saidArgs(t, "wire", "b/*"); !strings.Contains(out, "B is asked for every one of its models as vendor-c/one") ||
		strings.Contains(out, "* is the model magpie knows each of them by") {
		t.Errorf("asking about a name with no star in it said %q; want no sentence about the star", out)
	}
	if out := saidArgs(t, "wire", "b/*", "vendor-c/*"); !strings.Contains(out, "* is the model magpie knows each of them by") {
		t.Errorf("naming every model with a star in it said %q; want the star explained", out)
	}
	if out := saidArgs(t, "wire", "b/*"); !strings.Contains(out, "b/vendor/m is asked for as vendor-c/vendor/m") ||
		!strings.Contains(out, "* is the model magpie knows each of them by") {
		t.Errorf("asking about a name with a star in it said %q", out)
	}

	// one model of its own, which the provider's name cannot outrank
	if out := saidArgs(t, "wire", "a/m", "vendor-c/m"); !strings.Contains(out, "is asked for as vendor-c/m") ||
		!strings.Contains(out, "magpie and the agents still know it as m") {
		t.Errorf("naming one model said %q", out)
	}
	if n := wires()["a/m"]; n != "vendor-c/m" {
		t.Fatalf("the name given for a/m is %q; want vendor-c/m", n)
	}
	if out := saidArgs(t, "wire", "a/m"); !strings.Contains(out, "vendor-c/m · what A is asked for, for a/m") ||
		!strings.Contains(out, "--reset asks for that again") {
		t.Errorf("asking about one model said %q", out)
	}
	// a name of only whitespace takes the one away, and where nothing is in
	// force for the model it is asked for by the name magpie knows it by
	// again — not read as a name just given
	if out := saidArgs(t, "wire", "a/m", "   "); !strings.Contains(out, "is asked for as m again") ||
		strings.Contains(out, "magpie and the agents still know it as m") {
		t.Errorf("a name of only whitespace said %q; want the removal it is", out)
	}
	if _, ok := wires()["a/m"]; ok {
		t.Error("a name of only whitespace left one behind for a/m")
	}
	// a blank name on a model that never had one takes nothing away, and is
	// refused as what it is: there is no name there to be taken
	if out, err := refusedArgs(t, "wire", "a/claude-opus-5-5", " "); err == nil {
		t.Errorf("a blank name on a model that never had one said %q", out)
	}
	if got := len(wires()); got != 1 {
		t.Errorf("the names given are %v; a blank name is none", wires())
	}

	// a name given for every model of the provider, which a model with no
	// name of its own falls back to and a blank name leaves in force
	if err := provider.SetUpstreamName("a/*", "vendor-c/one"); err != nil {
		t.Fatal(err)
	}
	if out := saidArgs(t, "wire", "a/only-a"); !strings.Contains(out, "from the name given for every model of A") {
		t.Errorf("a model with no name of its own said %q; want the provider's", out)
	}
	if got := provider.UpstreamName(mustFind(t, "a"), "m"); got != "vendor-c/one" {
		t.Errorf("with a/m taken away, m is asked for as %q; want the provider's vendor-c/one", got)
	}

	// a name of only whitespace under the provider's is the removal of a
	// name a/m never had of its own, and is refused — and what it says is
	// the name that *is* in force, the provider's, which a tick over a key
	// holding nothing would have left the user with the wrong idea of
	out, err := refusedArgs(t, "wire", "a/m", "  ")
	if err == nil {
		t.Fatalf("a name of only whitespace under the provider's said %q", out)
	}
	if !strings.Contains(err.Error(), "a/m has no name of its own") ||
		!strings.Contains(err.Error(), "vendor-c/one") {
		t.Errorf("the refusal is %q; want a/m's own name missing and the provider's in force", err)
	}
	if _, ok := wires()["a/m"]; ok {
		t.Error("a name of only whitespace left one behind for a/m")
	}

	// --reset takes away only the model's own, leaving the provider's
	if out := saidArgs(t, "wire", "a/m", "vendor-c/m"); !strings.Contains(out, "is asked for as vendor-c/m") {
		t.Fatalf("naming a/m again said %q", out)
	}
	if out := saidArgs(t, "wire", "a/m", "--reset"); !strings.Contains(out, "has no name of its own again") {
		t.Errorf("--reset said %q", out)
	}
	if _, ok := wires()["a/m"]; ok {
		t.Error("--reset left a name for a/m")
	}
	if n := wires()["a/*"]; n != "vendor-c/one" {
		t.Errorf("--reset on a model took the provider's %q away too", n)
	}
	if out := saidArgs(t, "wire", "a/*", "--reset"); !strings.Contains(out, "every model of A is asked for by the name magpie knows it by again") {
		t.Errorf("--reset on every model said %q", out)
	}
	if got := len(wires()); got != 1 {
		t.Errorf("the names given are %v; want only the ones this left", wires())
	}

	if err := modelCmd([]string{"wire", "m", "x"}); err == nil {
		t.Error("named a model without its provider")
	}
}

// The example a name given for a provider's every model stands on is a
// model of that provider the user can actually see. Available is every
// model the vendor is known to serve, and a relay fronting one vendor under
// ids of its own shares that vendor's catalogue, so the first of those is a
// model nobody is offered: an example on it names a model that cannot be
// picked and shows the name standing for a model it was not given for.
func TestModelWireExampleIsAModelTheUserIsShown(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	// C lists two models of its own while its fetched list has a third
	// first: the one only the fetched list knows is the one a user never
	// sees, and the one an example read off that list would name.
	if err := provider.Save(provider.Provider{
		ID: "c", Name: "C", Key: "kc", Models: []string{"shown-1", "shown-2"}, Chat: "http://127.0.0.1:1/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("c")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.LivePath("c"), []byte(`{"models":[
	  {"id":"only-in-the-fetched-list"},{"id":"shown-1"},{"id":"shown-2"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if ms := mustFind(t, "c").Available(); len(ms) != 3 || ms[0].ID != "only-in-the-fetched-list" {
		t.Fatalf("what C is known to serve is %+v; want the fetched list's three, the unknown one first", ms)
	}
	if ms := mustFind(t, "c").Exposed(); len(ms) != 2 || ms[0].ID != "shown-1" {
		t.Fatalf("what C shows is %+v; want the two of its own file", ms)
	}
	// both the line that sets the name and the one that asks about it stand
	// a model on, so both have to be one the user is shown
	for _, args := range [][]string{{"wire", "c/*", "vendor-c/*"}, {"wire", "c/*"}} {
		out := saidArgs(t, args...)
		if !strings.Contains(out, "c/shown-1 is asked for as vendor-c/shown-1") {
			t.Errorf("%v said %q; want the example to stand on shown-1", args, out)
		}
		if strings.Contains(out, "only-in-the-fetched-list") {
			t.Errorf("%v said %q; the example stands on a model C does not show", args, out)
		}
	}
}

// The model a provider-wide name is shown with is one that name is really in
// force for. A model's own key wins over the provider's, so a model already
// given a name is asked for by that one however the provider's entry reads,
// and an example standing on it named a name no request goes out under — and
// named it right after the CLI had set one, which is what the user is
// reading about. So the example stands on a model with no name of its own,
// and a provider whose every model has one is left with the pattern alone.
func TestModelWireExampleStandsOnAModelWithNoNameOfItsOwn(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	// a relay of three models, of which the first is given a name of its
	// own before the provider's own name is set
	if err := provider.Save(provider.Provider{
		ID: "relay-a", Name: "Relay A", Key: "k", Chat: "http://127.0.0.1:1/v1",
		Models: []string{"sol", "sol-preview", "sol-low"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"wire", "relay-a/sol", "vendor-c/sol-preview"}); err != nil {
		t.Fatal(err)
	}
	// the name in force for sol is the one given for sol itself, and the
	// example stands for the provider's
	if out := saidArgs(t, "wire", "relay-a/sol"); !strings.Contains(out, "vendor-c/sol-preview") {
		t.Fatalf("magpie model wire relay-a/sol said %q; want the name given for sol itself to be the one in force", out)
	}
	for _, args := range [][]string{{"wire", "relay-a/*", "vendor-c/*"}, {"wire", "relay-a/*"}} {
		out := saidArgs(t, args...)
		if !strings.Contains(out, "relay-a/sol-preview is asked for as vendor-c/sol-preview") {
			t.Errorf("%v said %q; want the example to stand on sol-preview, the first model given no name of its own", args, out)
		}
		if strings.Contains(out, "relay-a/sol is asked for as ") {
			t.Errorf("%v said %q; the example stands on sol, which is asked for by the name given for sol itself", args, out)
		}
	}
	// a name of its own for every model the relay shows leaves nothing to
	// stand one on, and the pattern is all that can be said of it
	for _, m := range []string{"sol-preview", "sol-low"} {
		if err := modelCmd([]string{"wire", "relay-a/" + m, "vendor-c/" + m}); err != nil {
			t.Fatal(err)
		}
	}
	out := saidArgs(t, "wire", "relay-a/*")
	if want := "Relay A is asked for every one of its models as vendor-c/*"; !strings.Contains(out, want) {
		t.Errorf("magpie model wire relay-a/* said %q; want the pattern alone, %q", out, want)
	}
	if strings.Contains(out, "is asked for as ") {
		t.Errorf("magpie model wire relay-a/* said %q; every model it shows has a name of its own, so no model stands for the provider's", out)
	}
}

// --reset on a name given for every model of a provider takes that one away
// and nothing else: a model's own key wins over the provider's, so a model
// given a name of its own is asked for by that one however the provider's
// entry reads. A tick saying every model of the provider is asked for by the
// name magpie knows it by is therefore false the moment one model keeps a
// name, and leaves the reader with the opposite of what the command did — so
// the models that keep one are named with the name in force for each of them,
// and the claim is made only where there are none.
func TestModelWireResetWildcardNamesTheModelsThatKeepAName(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here

	if err := modelCmd([]string{"wire", "a/m", "vendor-c/m"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"wire", "a/*", "vendor-c/one"}); err != nil {
		t.Fatal(err)
	}
	out := saidArgs(t, "wire", "a/*", "--reset")
	if strings.Contains(out, "every model of A is asked for by the name magpie knows it by again") {
		t.Errorf("magpie model wire a/* --reset said %q; a/m is asked for as vendor-c/m, not by the name magpie knows it by", out)
	}
	if !strings.Contains(out, "m is asked for as vendor-c/m, a name of its own") {
		t.Errorf("magpie model wire a/* --reset said %q; want the model that keeps a name named with the one in force for it", out)
	}
	// and what it says is what the provider is asked for
	if got := provider.UpstreamName(mustFind(t, "a"), "m"); got != "vendor-c/m" {
		t.Errorf("after --reset on every model, m is asked for as %q; want the name given for m itself", got)
	}
	if got := provider.UpstreamName(mustFind(t, "a"), "only-a"); got != "only-a" {
		t.Errorf("after --reset on every model, only-a is asked for as %q; want the name magpie knows it by", got)
	}
	if provider.HasUpstreamName("a/*") {
		t.Error("--reset on every model left the provider's own name behind")
	}

	// two models keeping one, where the answer is the plural and has to
	// name both of them and not one of them
	if err := modelCmd([]string{"wire", "a/only-a", "vendor-c/only-a"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"wire", "a/*", "vendor-c/two"}); err != nil {
		t.Fatal(err)
	}
	out = saidArgs(t, "wire", "a/*", "--reset")
	if !strings.Contains(out, "m, only-a are asked for as vendor-c/m, vendor-c/only-a, names of their own") {
		t.Errorf("magpie model wire a/* --reset said %q; want both the models that keep a name, each with its own", out)
	}

	// a name kept for a model the provider does not serve is not one of
	// them: magpie asks for no such a model, so nothing goes out under
	// that name, and saying it did would claim a request that is not made
	if err := provider.Save(provider.Provider{
		ID: "a", Name: "A", Key: "ka", Models: []string{"claude-opus-5-5"}, Chat: "http://127.0.0.1:1/v1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"wire", "a/*", "vendor-c/three"}); err != nil {
		t.Fatal(err)
	}
	out = saidArgs(t, "wire", "a/*", "--reset")
	if !strings.Contains(out, "every model of A is asked for by the name magpie knows it by again") {
		t.Errorf("magpie model wire a/* --reset said %q; the names left are for models A no longer serves, and it is asked for none of them", out)
	}
	if strings.Contains(out, "vendor-c/m") || strings.Contains(out, "vendor-c/only-a") {
		t.Errorf("magpie model wire a/* --reset said %q; it names models A no longer serves, which magpie never asks for", out)
	}
}

// The refusal for a pattern with nothing left under it says every model of
// the provider is asked for by the name magpie knows it by, and that is false
// the moment one model keeps a name of its own: the model's own key wins over
// the provider's, so it is asked for under that one however the provider's
// entry reads. The second --reset over a provider-wide name is exactly where
// a reader is left with the opposite of what magpie did, so the models that
// keep a name are named with the name in force for each of them, as the tick
// over the same removal already does.
func TestModelWireResetWildcardRefusalNamesTheModelsThatKeepAName(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	wires := func() map[string]string { return settings.Load().ModelWires }

	if out := saidArgs(t, "wire", "a/*", "vendor-c/*"); !strings.Contains(out, "every model of A is asked for as") {
		t.Fatalf("naming every model of A said %q", out)
	}
	if out := saidArgs(t, "wire", "a/m", "own-m"); !strings.Contains(out, "is asked for as own-m") {
		t.Fatalf("naming a/m said %q", out)
	}
	if out := saidArgs(t, "wire", "a/*", "--reset"); !strings.Contains(out, "m is asked for as own-m, a name of its own") {
		t.Fatalf("the first --reset on every model said %q", out)
	}

	out, err := refusedArgs(t, "wire", "a/*", "--reset")
	if err == nil {
		t.Fatalf("the second --reset on every model said %q; the provider's own name was already taken away", out)
	}
	if strings.Contains(out, "✓") {
		t.Errorf("a --reset with nothing under the key printed a tick: %q", out)
	}
	if strings.Contains(err.Error(), "every model of A is already asked for by the name magpie knows it by") {
		t.Errorf("the second --reset says %q; m is asked for as own-m, a name of its own", err)
	}
	if !strings.Contains(err.Error(), "m is asked for as own-m, a name of its own") {
		t.Errorf("the second --reset says %q; want the model that keeps a name named with the one in force for it", err)
	}
	// and the name the refusal is about is still standing, which is the
	// whole of what makes the claim it does not make false
	if n := wires()["a/m"]; n != "own-m" {
		t.Errorf("m's name is %q after the second --reset; want own-m", n)
	}
	if provider.HasUpstreamName("a/*") {
		t.Error("the second --reset put a name back for every model of A")
	}
}

// A name given for a model may stand for that model with a star in it, the
// way one given for every model of a provider does. So the sentence naming
// the models a provider's own entry does not reach has to say the name the
// request goes out under, and the star in it is the model magpie knows it
// by — a star left standing there would name an id no vendor is ever asked
// for, in the one line whose whole claim is that this is the name in force.
func TestModelWireNamesTheModelsWithAStarInTheirOwnName(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here

	if err := modelCmd([]string{"wire", "a/m", "vendor-c/*"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.UpstreamName(mustFind(t, "a"), "m"); got != "vendor-c/m" {
		t.Fatalf("m is asked for as %q; the test is not standing on a name with a star in it", got)
	}
	out := saidArgs(t, "wire", "a/*", "vendor-d/*")
	if !strings.Contains(out, "m is asked for as vendor-c/m, a name of its own") {
		t.Errorf("magpie model wire a/* vendor-d/* said %q; want m named with the name the request goes out under", out)
	}
	if strings.Contains(out, "vendor-c/*") {
		t.Errorf("magpie model wire a/* vendor-d/* said %q; the star in a model's own name stands for the model, and no request goes out under it", out)
	}
	// and the same is said of a removal, which is the other way the
	// sentence is reached
	out = saidArgs(t, "wire", "a/*", "--reset")
	if !strings.Contains(out, "m is asked for as vendor-c/m, a name of its own") {
		t.Errorf("magpie model wire a/* --reset said %q; want m named with the name the request goes out under", out)
	}
	if strings.Contains(out, "vendor-c/*") {
		t.Errorf("magpie model wire a/* --reset said %q; the star in a model's own name stands for the model, and no request goes out under it", out)
	}

	// two models keeping one, each with a star of its own, so the plural
	// expands each for the model it was given for rather than one for all
	if err := modelCmd([]string{"wire", "a/only-a", "vendor-e/*"}); err != nil {
		t.Fatal(err)
	}
	out = saidArgs(t, "wire", "a/*", "vendor-d/*")
	if !strings.Contains(out, "m, only-a are asked for as vendor-c/m, vendor-e/only-a, names of their own") {
		t.Errorf("magpie model wire a/* vendor-d/* said %q; want both names, each with the star stood for by its own model", out)
	}
}

// A name given for every model of a provider does not reach a model that has
// one of its own, and the example such a name is shown with deliberately
// stands on a model it does reach — which is what a ✓ claiming every model of
// that provider is asked for under it leaves out, in the one case where the
// precedence between the two is the thing the user needs told. So the line
// narrows to the models with no name of their own, and the ones left alone
// are named with the name in force for each.
func TestModelWireWildcardSaysTheModelsItDoesNotReach(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here

	if err := modelCmd([]string{"wire", "a/m", "vendor-c/m"}); err != nil {
		t.Fatal(err)
	}
	out := saidArgs(t, "wire", "a/*", "vendor-c/*")
	if strings.Contains(out, "· every model of A is asked for as") {
		t.Errorf("magpie model wire a/* vendor-c/* said %q; a/m keeps a name of its own and is not asked for under that one", out)
	}
	if !strings.Contains(out, "with no name of its own is asked for as vendor-c/*") {
		t.Errorf("magpie model wire a/* vendor-c/* said %q; want the claim narrowed to the models it reaches", out)
	}
	if !strings.Contains(out, "m is asked for as vendor-c/m, a name of its own") {
		t.Errorf("magpie model wire a/* vendor-c/* said %q; want the model it does not reach named with the name in force for it", out)
	}
	// the example still stands on a model the name does reach, so the two
	// lines are about different models and neither contradicts the other
	if !strings.Contains(out, "a/only-a is asked for as vendor-c/only-a") {
		t.Errorf("magpie model wire a/* vendor-c/* said %q; want the example on a model the name does reach", out)
	}
	// and where no model of the provider keeps a name, the claim is the
	// whole of it
	if out := saidArgs(t, "wire", "b/*", "vendor-c/*"); !strings.Contains(out, "every model of B is asked for as vendor-c/*") {
		t.Errorf("magpie model wire b/* vendor-c/* said %q; no model of B keeps a name, so every one of them is asked for under it", out)
	}
}

// The wire names are the one thing a listing of what the user has said does
// not show: `magpie model names` is a list of what the models are called and
// of the levels they offer, both of which are magpie's own, and a relay
// serving a model under an id of its own changes neither — so a name given
// for every model of one was written with no command that read it back.
// `magpie model wires` is that listing, and it is read off the file rather
// than off a provider, since a name outlives the provider it was given for
// and is in force for whichever provider takes that id next.
func TestModelWiresCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	wires := func() map[string]string { return settings.Load().ModelWires }
	listed := func(t *testing.T) string {
		t.Helper()
		out, err := said(t, func() error { return modelCmd([]string{"wires"}) })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	if out := listed(t); !strings.Contains(out, "no vendor is asked for a model by another name yet") {
		t.Errorf("magpie model wires with nothing given said %q; want it to say there is none", out)
	}
	for _, ref := range [][2]string{{"a/m", "vendor-c/m"}, {"b/*", "vendor-c/one"}} {
		if err := modelCmd([]string{"wire", ref[0], ref[1]}); err != nil {
			t.Fatal(err)
		}
	}
	out := listed(t)
	// a name is listed with the key it is stored at, on a line of its own:
	// a model's own name and the provider's are two entries, and merging
	// them would read as the one in force for a model that has its own
	for _, ref := range [][2]string{{"a/m", "vendor-c/m"}, {"b/*", "vendor-c/one"}} {
		lines := 0
		for _, l := range strings.Split(out, "\n") {
			if !strings.Contains(l, ref[0]) {
				continue
			}
			lines++
			if !strings.Contains(l, ref[1]) {
				t.Errorf("magpie model wires put %q on a line of its own, %q: %q; want the name with the key it is stored at", ref[0], ref[1], out)
			}
		}
		if lines != 1 {
			t.Errorf("magpie model wires listed %q on %d lines, %q; want it once", ref[0], lines, out)
		}
	}
	// a name outlives the provider it was given for, so it is still listed
	// once that provider is gone — it is in force for whichever provider
	// takes that id next
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if out := listed(t); !strings.Contains(out, "b/*") || !strings.Contains(out, "vendor-c/one") {
		t.Errorf("magpie model wires after b was deleted said %q; want the name it outlived listed still", out)
	}
	if len(wires()) != 2 {
		t.Errorf("the names kept are %v; a listing takes none away", wires())
	}
}

// A name that is only whitespace is no name, as it is where one is given and
// where one is looked up: UpstreamNames leaves it out and a request under it
// goes out by the name magpie knows the model by. So the listing reads it the
// way the file means it, one an older magpie stored or one hand-edited in,
// rather than printing a line with nothing on it — a listing whose whole
// purpose is reading the names back cannot be the one place a name shows up
// that no vendor is ever asked for by.
func TestModelWiresCmdSkipsANameOfOnlyWhitespace(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	// magpie will not write one itself — a name of only whitespace is
	// taken as a removal — so it is the file that is read, as one left by
	// an older magpie or edited by hand is
	s := settings.Load()
	s.ModelWires = map[string]string{"a/m": "  vendor-c/m  ", "a/only-a": "   "}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	out := saidArgs(t, "wires")
	listed := false
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "only-a") {
			t.Errorf("magpie model wires listed %q; a name of only whitespace is no name, and no request goes out under one", l)
		}
		if strings.Contains(l, "a/m") {
			listed = true
			// the name ends the line, so what the file stored around
			// it is not in force and is not what a request goes out as
			if !strings.HasSuffix(l, "vendor-c/m") {
				t.Errorf("magpie model wires said %q; want the name listed as it will be sent, trimmed", out)
			}
		}
	}
	if !listed {
		t.Errorf("magpie model wires said %q; want the name that is in force listed", out)
	}

	// and where every name in the file is one of those, the listing says
	// there is none rather than printing nothing at all
	s.ModelWires = map[string]string{"a/m": "   "}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if out := saidArgs(t, "wires"); !strings.Contains(out, "no vendor is asked for a model by another name yet") {
		t.Errorf("magpie model wires with nothing but whitespace names said %q; want it to say no vendor is asked for one by another name", out)
	}
}

// --reset takes a name away, and where there is no name to take away the
// model is already asked for by the name magpie knows it by, or by the name
// its provider's own entry puts in force. A tick over either would report a
// removal that did not happen — and under a provider-wide name it would
// leave the reader with the one thing that is false, that the model's own
// name is the one in force. So a removal with nothing under the key is
// refused, as `magpie model price --reset` refuses a price that was never
// set, and the refusal says which of the two the model is asked for by. An
// error here is a command that exits 1.
func TestModelWireResetRefusesANameThereIsNot(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	wires := func() map[string]string { return settings.Load().ModelWires }

	// the second --reset of a name that is gone: the first took it, so this
	// one has nothing left to do
	if out := saidArgs(t, "wire", "a/m", "vendor-c/m"); !strings.Contains(out, "is asked for as vendor-c/m") {
		t.Fatalf("naming a/m said %q", out)
	}
	if out := saidArgs(t, "wire", "a/m", "--reset"); !strings.Contains(out, "is asked for as m again") {
		t.Fatalf("the first --reset said %q", out)
	}
	out, err := refusedArgs(t, "wire", "a/m", "--reset")
	if err == nil {
		t.Fatalf("--reset over a name already taken away said %q; there was none to take", out)
	}
	if strings.Contains(out, "✓") {
		t.Errorf("--reset over no name at all printed a tick: %q", out)
	}
	if !strings.Contains(err.Error(), "a/m has no name of its own to reset") {
		t.Errorf("the second --reset says %q; want it to say a/m had no name of its own", err)
	}

	// A model the provider does not serve is one magpie never asks for by
	// any name, so there is none to take away — and saying which name it is
	// asked for under would claim a request magpie does not make, which is
	// the one thing a mistyped id must not be answered with. It is refused
	// as not one of the provider's models, and the same id is refused when
	// a name is given for it, which is what the README says a name is
	// given only for.
	out, err = refusedArgs(t, "wire", "a/nosuch", "--reset")
	if err == nil {
		t.Fatalf("--reset on a model A does not serve said %q; A is never asked for a nosuch", out)
	}
	if strings.Contains(out, "✓") {
		t.Errorf("--reset on a model A does not serve printed a tick: %q", out)
	}
	if !strings.Contains(err.Error(), "a/nosuch is not a model of A magpie knows of") {
		t.Errorf("--reset on a model A does not serve says %q; want it to say magpie knows no such model", err)
	}
	if strings.Contains(err.Error(), "already asked for as") {
		t.Errorf("--reset on a model A does not serve says %q; that model is asked for by no name at all", err)
	}
	if _, ok := wires()["a/nosuch"]; ok {
		t.Error("a refused --reset left a name behind")
	}
	if out, err := refusedArgs(t, "wire", "a/nosuch", "vendor-c/nosuch"); err == nil {
		t.Errorf("a name was given for a model A does not serve: %q", out)
	}
	if _, ok := wires()["a/nosuch"]; ok {
		t.Error("a name for a model A does not serve was stored anyway")
	}

	// the same over the provider's own name, which is a name of its own
	if out := saidArgs(t, "wire", "a/*", "vendor-c/one"); !strings.Contains(out, "every model of A is asked for as vendor-c/one") {
		t.Fatalf("naming every model of A said %q", out)
	}
	if out := saidArgs(t, "wire", "a/*", "--reset"); !strings.Contains(out, "every model of A is asked for by the name magpie knows it by again") {
		t.Fatalf("the first --reset on every model said %q", out)
	}
	out, err = refusedArgs(t, "wire", "a/*", "--reset")
	if err == nil {
		t.Fatalf("--reset over the provider's own name, twice, said %q", out)
	}
	if strings.Contains(out, "✓") {
		t.Errorf("--reset over no name at all printed a tick: %q", out)
	}
	if !strings.Contains(err.Error(), "a/* has no name of its own to reset") ||
		!strings.Contains(err.Error(), "every model of A is already asked for by the name magpie knows it by") {
		t.Errorf("the second --reset on every model says %q", err)
	}

	// a model with only the provider's name over it: --reset reaches the
	// model's own and nothing else, so the one that takes this away is the
	// 'a/*' one, and it is named rather than left to a tick over a key that
	// was never set
	if err := provider.SetUpstreamName("a/*", "vendor-c/one"); err != nil {
		t.Fatal(err)
	}
	out, err = refusedArgs(t, "wire", "a/m", "--reset")
	if err == nil {
		t.Fatalf("--reset on a model with no name of its own said %q", out)
	}
	if !strings.Contains(err.Error(), "a/m has no name of its own") ||
		!strings.Contains(err.Error(), "magpie model wire 'a/*' --reset") {
		t.Errorf("the refusal is %q; want the 'a/*' --reset that takes the name in force away", err)
	}
	if strings.Contains(out, "✓") {
		t.Errorf("--reset with only the provider's name in force printed a tick: %q", out)
	}
	if n := wires()["a/*"]; n != "vendor-c/one" {
		t.Errorf("the provider's own name is %q; a refused --reset took it away", n)
	}
	// and what is in force for that model is what the refusal says, so a
	// user who meant to take that name away is sent to the one command
	// that does take it
	if got := provider.UpstreamName(mustFind(t, "a"), "m"); got != "vendor-c/one" {
		t.Errorf("with only the provider's name in force, m is asked for as %q", got)
	}
	// a model A does not serve is outside all of that: the provider's own
	// name reaches every model of A, and a model that is not one of them is
	// asked for by it no more than by a name of its own, so the answer is
	// the one about magpie not knowing the model rather than the name in
	// force for the others
	out, err = refusedArgs(t, "wire", "a/nosuch", "--reset")
	if err == nil {
		t.Fatalf("--reset on a model A does not serve, under the provider's own name, said %q", out)
	}
	if !strings.Contains(err.Error(), "a/nosuch is not a model of A magpie knows of") ||
		strings.Contains(err.Error(), "vendor-c/one") {
		t.Errorf("the refusal is %q; want magpie knowing no such model, not the name in force", err)
	}
	if out := saidArgs(t, "wire", "a/*", "--reset"); !strings.Contains(out, "every model of A is asked for by the name magpie knows it by again") {
		t.Errorf("--reset on every model said %q", out)
	}
	if got := len(wires()); got != 0 {
		t.Errorf("the names left are %v; every one of them was taken away", wires())
	}
}

// A wire name outlives the provider it was given for: that provider can be
// deleted, and the name is then in force for whichever provider takes that
// id next, its models going out renamed to a vendor that never heard of
// them, with nothing saying so. --reset therefore works off the key the
// name is stored at rather than off a provider that is not there any more —
// the one name most in need of taking off is the one a reset that resolved
// the provider first could not take off at all.
func TestModelWireResetAfterTheProviderIsGone(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	wires := func() map[string]string { return settings.Load().ModelWires }

	if out := saidArgs(t, "wire", "b/vendor/m", "vendor-c/m"); !strings.Contains(out, "is asked for as vendor-c/m") {
		t.Fatalf("naming one of b's models said %q", out)
	}
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	// deleting the provider takes the models out of the catalog, not the
	// name out of the file: the id is free again, and whoever takes it next
	// would be asked for its models under the old vendor's name
	if n := wires()["b/vendor/m"]; n != "vendor-c/m" {
		t.Fatalf("with b deleted the name under b/vendor/m is %q; the point is that one is still there", n)
	}

	if out := saidArgs(t, "wire", "b/vendor/m", "--reset"); !strings.Contains(out, "b/vendor/m") ||
		!strings.Contains(out, "no longer has a name of its own") {
		t.Errorf("--reset with the provider gone said %q", out)
	}
	if left := wires(); len(left) != 0 {
		t.Errorf("--reset left the names behind: %v", left)
	}

	// a provider that takes the id afterwards is asked for its models by
	// the names magpie knows them by, which is the whole of what clearing
	// the name buys
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "kb",
		Models: []string{"vendor/m"}, Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.UpstreamName(mustFind(t, "b"), "vendor/m"); got != "vendor/m" {
		t.Errorf("the new b is asked for its model as %q; want vendor/m", got)
	}

	// an id that is mistyped rather than a deleted provider's is still the
	// error it was: there is no name under it to take away, and the user is
	// told which provider is missing rather than that nothing was there
	if err := modelCmd([]string{"wire", "bb/vendor/m", "--reset"}); err == nil {
		t.Error("--reset on a provider that never existed was taken as a removal")
	}
}

// The id a wire name is stored under is free again once its provider is
// deleted, and another provider can be shown by it — the name a provider is
// shown by is a spelling any other provider answers to as well. So with b's
// provider gone and a provider c shown as B, `wire b/vendor/m --reset` still
// has to take the name off b/vendor/m, which is in force for whichever
// provider takes that id next, going out renamed to a vendor that never
// heard of it. Resolving the ref first lands on c, where what a reset would
// remove is c's own name — one that is really in force — and the name left
// behind is the one doing the damage, with nothing having said so.
func TestModelWireResetUnderAProviderShownAsTheGoneId(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	wires := func() map[string]string { return settings.Load().ModelWires }

	if out := saidArgs(t, "wire", "b/vendor/m", "vendor-c/m"); !strings.Contains(out, "is asked for as vendor-c/m") {
		t.Fatalf("naming one of b's models said %q", out)
	}
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	// a provider whose own id is c, shown by the id b that is now nobody's,
	// with a name of its own that a reset has to leave in force
	if err := provider.Save(provider.Provider{ID: "c", Name: "B", Key: "kc",
		Models: []string{"vendor/m"}, Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("c/vendor/m", "vendor-d/m"); err != nil {
		t.Fatal(err)
	}

	if out := saidArgs(t, "wire", "b/vendor/m", "--reset"); !strings.Contains(out, "b/vendor/m") ||
		!strings.Contains(out, "no longer has a name of its own") {
		t.Errorf("--reset with the id b shown by another provider said %q", out)
	}
	if _, ok := wires()["b/vendor/m"]; ok {
		t.Errorf("b/vendor/m still has a name, %v; it is in force for whichever provider takes that id next", wires())
	}
	if got := wires()["c/vendor/m"]; got != "vendor-d/m" {
		t.Errorf("c's own name is %q; --reset took away the name c's model is asked for", got)
	}
}

// The help is two columns, and every description starts in the second of
// them — a continuation line included. A description a column early lines
// its text up under the line above rather than under the command it belongs
// to, which is how the wire lines read until they were put in line.
func TestModelUsageLinesUpItsDescriptions(t *testing.T) {
	const cmd = "  magpie model "
	// where a description starts: past the command and the run of spaces
	// after it, or — for a wrapped description — at its own first letter
	startsAt := func(l string) (int, bool) {
		if !strings.HasPrefix(l, cmd) {
			return 0, false
		}
		rest := l[len(cmd):]
		i := strings.Index(rest, "  ")
		if i < 0 {
			return 0, false
		}
		return len(cmd) + i + len(rest[i:]) - len(strings.TrimLeft(rest[i:], " ")), true
	}
	// a line that is neither a command nor prose is a description wrapped
	// onto a line of its own, and starts where those start
	wrappedAt := func(l string) (int, bool) {
		lead := len(l) - len(strings.TrimLeft(l, " "))
		return lead, strings.TrimSpace(l) != "" && lead > len(cmd)
	}

	want, laid := -1, 0
	for _, l := range strings.Split(modelUsage, "\n") {
		at, ok := startsAt(l)
		if !ok {
			at, ok = wrappedAt(l)
		}
		if !ok {
			continue // a paragraph of prose, which is in no column
		}
		if want < 0 {
			want = at
		}
		if at != want {
			t.Errorf("the description on %q starts at column %d; the rest start at %d", l, at, want)
		}
		laid++
	}
	if laid < 11 {
		t.Fatalf("only %d lines of the help were laid out; the check is not reading it", laid)
	}
}

// The columns line up whatever a description belongs to, so a command with a
// description another command's line finishes is laid out perfectly and
// reads as though the two of them said the same thing: the wire lines all
// carried on into the stack under them, and which of the three each
// sentence went with was anybody's guess. What says who a wrapped line
// belongs to is that it follows its own command, so this reads the block in
// the groups a reader takes it in — a command and the lines wrapped under
// it — and fails on a group whose description stops on a word no sentence
// ends on, which is what a description whose continuation went to another
// command looks like.
//
// The gap after a command is not always two spaces: a command as long as the
// column the descriptions are laid out in leaves one, and to a check that
// counts spaces that reads as a command with no description at all. So the
// column is read off the block rather than written down, and such a line's
// description is taken from the first space past it — which is where the
// layout puts it, and which a reader reads as readily as any other line.
func TestModelUsageGivesEveryCommandItsOwnDescription(t *testing.T) {
	const cmd = "  magpie model "
	// words a sentence cannot end on, which is what a description is left
	// reading as when the line it carried on into was somebody else's. A
	// preposition like "by" is not one of them: a description can end on it
	// and does — the name a model goes by.
	dangling := map[string]bool{
		"and": true, "or": true, "but": true, "in": true, "of": true,
		"to": true, "for": true, "as": true, "with": true, "that": true,
		"which": true, "a": true, "an": true, "the": true,
	}
	lines := strings.Split(modelUsage, "\n")
	// the column the descriptions are laid out in: the leftmost one a
	// command with room for a gap reaches. It is read off the block rather
	// than written down, so the layout can move and still be read.
	col := -1
	gapAt := func(l string) int {
		i := strings.Index(l[len(cmd):], "  ")
		if i < 0 {
			return -1
		}
		k := len(cmd) + i
		for k < len(l) && l[k] == ' ' {
			k++
		}
		return k
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, cmd) {
			continue
		}
		if k := gapAt(l); k >= 0 && (col < 0 || k < col) {
			col = k
		}
	}
	// a command with no description beside it carries its words onto the
	// next line, and that is what tells such a line from one whose command
	// is as long as the column and its description is pushed past it
	wrapped := func(i int) bool {
		return col >= 0 && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" &&
			len(lines[i+1])-len(strings.TrimLeft(lines[i+1], " ")) == col
	}
	type group struct{ command, said string }
	var groups []group
	laid := 0
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, cmd):
			// the command, and the description that starts past the run
			// of spaces after it — the column the block is laid out in,
			// or, for a command that fills that column, the one space it
			// leaves before the description
			g := group{command: strings.TrimSpace(l)}
			rest := l[len(cmd):]
			switch k := gapAt(l); {
			case k >= 0:
				g.command, g.said = strings.TrimSpace(rest[:k-len(cmd)]), strings.TrimSpace(l[k:])
			case col >= 0 && col < len(l) && !wrapped(i):
				if k := col + strings.Index(l[col:], " "); k >= 0 {
					g.command, g.said = strings.TrimSpace(rest[:k-len(cmd)]), strings.TrimSpace(l[k+1:])
				}
			}
			groups = append(groups, g)
			laid++

		case strings.HasPrefix(l, strings.Repeat(" ", len(cmd)+1)):
			// a description wrapped onto a line of its own, which belongs
			// to the command above it and to no other
			if len(groups) == 0 {
				t.Fatalf("a description is wrapped onto a line of its own with no command above it: %q", l)
			}
			groups[len(groups)-1].said += " " + strings.TrimSpace(l)
			laid++
		}
	}
	if laid < 11 {
		t.Fatalf("only %d lines of the help were read; the check is not reading it", laid)
	}
	for _, g := range groups {
		if g.said == "" {
			t.Errorf("%s is a command with no description of its own", g.command)
			continue
		}
		last := g.said[strings.LastIndex(g.said, " ")+1:]
		if dangling[last] {
			t.Errorf("%s is described as %q, which stops on %q: its continuation is under another command", g.command, g.said, last)
		}
	}
}

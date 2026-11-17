package masking

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/pii"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// Masking asks the model the user picked; with none picked, AstrLink
// Guard (Privacy Filter isn't on disk in a test).
func TestMaskingUsesThePickedModel(t *testing.T) {
	for name, want := range map[string]string{"": pii.AstrLinkGuard, pii.PrivacyFilter: pii.PrivacyFilter, pii.AstrLinkGuard: pii.AstrLinkGuard} {
		if got := picked(settings.Settings{RedactModel: true, RedactModelName: name}).ID(); got != want {
			t.Errorf("picked %q: %s, want %s", name, got, want)
		}
	}
}

// scored is a model that finds three names, as sure as their scores.
type scored struct{ pii.Model }

func (scored) ID() string        { return pii.AstrLinkGuard }
func (scored) MinScore() float64 { return 0.8 }
func (scored) Find(context.Context, []string) ([][]redact.Found, error) {
	return [][]redact.Found{{{Start: 0, End: 1, Label: "private_person", Score: 0.6},
		{Start: 2, End: 3, Label: "private_person", Score: 0.85}, {Start: 4, End: 5, Label: "private_person", Score: 0.95}}}, nil
}

// What the model is less sure of than the threshold the user set for it
// goes as written; with none set, or one out of range, its own default
// (@Yayoi_no_yume on X).
func TestModelThreshold(t *testing.T) {
	for _, c := range []struct {
		set  map[string]float64
		want []int
	}{
		{nil, []int{2, 4}},
		{map[string]float64{pii.AstrLinkGuard: 0.9}, []int{4}},
		{map[string]float64{pii.AstrLinkGuard: 0.5}, []int{0, 2, 4}},
		{map[string]float64{pii.PrivacyFilter: 0.95}, []int{2, 4}},
		{map[string]float64{pii.AstrLinkGuard: 0.3}, []int{2, 4}},
		{map[string]float64{pii.AstrLinkGuard: 1}, []int{2, 4}},
	} {
		var m pii.Model = scored{}
		var got []int
		for _, f := range model(context.Background(), m, pii.Threshold(c.set, m), true)([]string{"a b c"})[0] {
			got = append(got, f.Start)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("threshold set %v: kept %v, want %v", c.set, got, c.want)
		}
	}
}

// The local model is on by default: for a user who neither turned it on
// nor off, and never had one downloaded or picked, which a user did who
// turned it off before it was on by default.
func TestModelOnByDefault(t *testing.T) {
	disk := false
	was := onDisk
	onDisk = func() bool { return disk }
	t.Cleanup(func() { onDisk = was })
	for _, c := range []struct {
		st   settings.Settings
		disk bool
		want bool
	}{
		{settings.Settings{}, false, true},
		{settings.Settings{}, true, false},
		{settings.Settings{RedactModelName: pii.AstrLinkGuard}, false, false},
		{settings.Settings{RedactModelOff: true}, false, false},
		{settings.Settings{RedactModelOff: true, RedactModelAuto: true}, true, false},
		{settings.Settings{RedactModelAuto: true, RedactModelName: pii.AstrLinkGuard}, true, true},
		{settings.Settings{RedactModel: true, RedactModelName: pii.PrivacyFilter}, true, true},
	} {
		disk = c.disk
		if got := ModelWanted(c.st); got != c.want {
			t.Errorf("on %v, off %v, auto %v, name %q, on disk %v: wanted %v, want %v", c.st.RedactModel, c.st.RedactModelOff, c.st.RedactModelAuto, c.st.RedactModelName, c.disk, got, c.want)
		}
	}
}

// fake is a model that is on disk or not, and fails or finds a name.
type fake struct {
	pii.Model
	installed, fails bool
}

func (fake) ID() string           { return pii.AstrLinkGuard }
func (fake) MinScore() float64    { return 0.8 }
func (f fake) Installed() bool    { return f.installed }
func (fake) ManagesWeights() bool { return true }
func (fake) Generation() string   { return "" }
func (f fake) Find(_ context.Context, texts []string) ([][]redact.Found, error) {
	if f.fails {
		return nil, errors.New("pf-serve exited")
	}
	return [][]redact.Found{{{Start: 0, End: 6, Label: "private_person", Score: 0.9}}}, nil
}

// Until the model is on disk the rules mask alone and its download is
// started, the default's too: nothing is held back for it. On disk,
// AstrLink Guard finds Chinese names itself (NamesByModel).
func TestNoModelYetMasksByTheRules(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	m := fake{}
	var fetches []bool
	wasPicked, wasFetch, wasDisk := picked, fetch, onDisk
	picked = func(settings.Settings) pii.Model { return m }
	fetch = func(_ pii.Model, on bool) { fetches = append(fetches, on) }
	onDisk = func() bool { return m.installed }
	t.Cleanup(func() { picked, fetch, onDisk = wasPicked, wasFetch, wasDisk })
	o := options()
	if o.Model != nil || !slices.Equal(fetches, []bool{false}) {
		t.Fatalf("not on disk: model %v, fetches %v", o.Model != nil, fetches)
	}
	if out, n := redact.Mask("key sk-ant-api03-"+strings.Repeat("a", 40), o); n != 1 || strings.Contains(out, "aaaa") {
		t.Fatalf("the rules didn't mask: %q", out)
	}
	m.installed = true
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true, RedactModelAuto: true, RedactModelName: pii.AstrLinkGuard}); err != nil {
		t.Fatal(err)
	}
	if o := options(); o.Model == nil || !o.NamesByModel || len(fetches) != 1 {
		t.Fatalf("on disk: model %v, names by model %v, fetches %v", o.Model != nil, o.NamesByModel, fetches)
	}
}

// pluginFake is a plugin's model: its weights are the user's, nothing to
// download, and gen is its build.
type pluginFake struct {
	fake
	gen string
}

func (pluginFake) ID() string           { return "external/plugin/0123456789abcdef/v1" }
func (pluginFake) ManagesWeights() bool { return false }
func (p pluginFake) Generation() string { return p.gen }

// A plugin's model picked while its plugin is off or its files are gone
// masks by the rules alone, though the user turned the model on, and
// starts no download; there, its build goes with what it finds.
func TestPluginModelUnavailableIsTheRules(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	m := pluginFake{gen: "g1"}
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true, RedactModel: true, RedactModelName: m.ID()}); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	wasPicked, wasFetch, wasDisk := picked, fetch, onDisk
	picked = func(settings.Settings) pii.Model { return m }
	fetch = func(pii.Model, bool) { fetches++ }
	onDisk = func() bool { return false }
	t.Cleanup(func() { picked, fetch, onDisk = wasPicked, wasFetch, wasDisk })
	o := options()
	if o.Model != nil || fetches != 0 {
		t.Fatalf("unavailable: model %v, fetches %d", o.Model != nil, fetches)
	}
	if out, n := redact.Mask("key sk-ant-api03-"+strings.Repeat("a", 40), o); n != 1 || strings.Contains(out, "aaaa") {
		t.Fatalf("the rules didn't mask: %q", out)
	}
	m.installed = true
	if o := options(); o.Model == nil || o.ModelGeneration != "g1" || o.NamesByModel {
		t.Fatalf("available: model %v, generation %q, names by model %v", o.Model != nil, o.ModelGeneration, o.NamesByModel)
	}
}

// A model that fails holds the request back only where the user turned
// it on; on by default, it finds nothing and the rules mask alone.
func TestDefaultModelFailingIsTheRules(t *testing.T) {
	m := fake{installed: true, fails: true}
	if got := model(context.Background(), m, 0.8, false)([]string{"a", "b"}); len(got) != 2 || len(got[0])+len(got[1]) != 0 {
		t.Fatalf("default: %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("turned on by the user, a failing model didn't hold the request back")
		}
	}()
	model(context.Background(), m, 0.8, true)([]string{"a"})
}

// hangs is a model on disk that never answers: one loading or reading a
// long history on a slow CPU (a Docker container's), or stuck. Find
// ignores ctx, as a read of its cache from disk or a load does.
type hangs struct {
	fake
	asked, release chan struct{}
}

func (h hangs) Find(context.Context, []string) ([][]redact.Found, error) {
	h.asked <- struct{}{}
	<-h.release // the test's end
	return nil, errors.New("released")
}

// shensmart on Discord (10-28): with the model on by default, a request
// waited for it as long as its client did, masking on the Requests
// page, every one after it queued behind. On by default, it is waited for
// defaultWait at most and the rules mask alone; turned on by the user, it
// is waited for longer (strictWait).
func TestDefaultModelHangingIsTheRules(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true, RedactModelAuto: true, RedactModelName: pii.AstrLinkGuard}); err != nil {
		t.Fatal(err)
	}
	m := hangs{fake{installed: true}, make(chan struct{}, 8), make(chan struct{})}
	t.Cleanup(func() { close(m.release) })
	wasPicked, wasFetch, wasDisk, wasWait := picked, fetch, onDisk, defaultWait
	picked = func(settings.Settings) pii.Model { return m }
	fetch = func(pii.Model, bool) {}
	onDisk = func() bool { return true }
	defaultWait = 200 * time.Millisecond
	t.Cleanup(func() { picked, fetch, onDisk, defaultWait = wasPicked, wasFetch, wasDisk, wasWait })

	body := []byte(`{"input":[{"role":"user","content":"张伟 key sk-ant-api03-` + strings.Repeat("a", 40) + `"}]}`)
	type res struct {
		b []byte
		n int
	}
	got := make(chan res, 1)
	began := time.Now()
	go func() {
		b, hits := Masker{}.JSONHitsContext(context.Background(), body)
		got <- res{b, len(hits)}
	}()
	select {
	case r := <-got:
		if took := time.Since(began); took > 5*time.Second {
			t.Fatalf("held %s", took)
		}
		if strings.Contains(string(r.b), "aaaa") || r.n == 0 {
			t.Fatalf("the rules didn't mask: %s", r.b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("on by default, a model that never answers held the request")
	}
	select {
	case <-m.asked:
	default:
		t.Fatal("the model was never asked")
	}

	// turned on by the user, the request waits for the model longer
	// than the default's wait (strictWait; its end is
	// TestStrictModelStalledIsTheRulesForWhatItDidntRead's)
	wasStrict := strictWait
	strictWait = time.Hour
	behind.Store(0) // not behind: a strict wait is waited whole
	t.Cleanup(func() { strictWait = wasStrict })
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		model(context.Background(), m, 0.8, true)([]string{"a"})
	}()
	<-m.asked
	select {
	case <-done:
		t.Fatal("turned on by the user, the model wasn't waited for")
	case <-time.After(3 * defaultWait):
	}
}

// partly is a model that read the first text (a history it saw before)
// and not the rest when the wait ended: the burst ahead of it in the
// queue (xRayySummers on X).
type partly struct{ hangs }

func (p partly) Find(ctx context.Context, texts []string) ([][]redact.Found, error) {
	p.asked <- struct{}{}
	<-ctx.Done()
	out, missing := make([][]redact.Found, len(texts)), 0
	for i, s := range texts {
		if strings.Contains(s, "new tool result") {
			missing++
		} else if k := strings.Index(s, "Liu Xiang"); k >= 0 {
			out[i] = []redact.Found{{Start: k, End: k + 9, Label: "private_person", Score: 0.9}}
		}
	}
	return out, &pii.Partial{Missing: missing, Of: len(texts), Err: ctx.Err()}
}

// xRayySummers on X (magpie 0.1.1877, Linux, AstrLink Guard turned on):
// in a burst of requests with text the model hadn't read, one waited
// 290s, and each that stopped waiting was "masking failed, so nothing
// was sent". Turned on by the user, the model is waited for strictWait
// at most: what it read is masked with its finds, the rest by the rules,
// and the request goes. Nothing the rules mask goes as written. The old
// code waited with no bound, and held the request back when the wait
// ended.
func TestStrictModelStalledIsTheRulesForWhatItDidntRead(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true, RedactModel: true, RedactModelName: pii.AstrLinkGuard}); err != nil {
		t.Fatal(err)
	}
	stalled := hangs{fake{installed: true}, make(chan struct{}, 8), make(chan struct{})}
	t.Cleanup(func() { close(stalled.release) })
	var m pii.Model = stalled
	wasPicked, wasFetch, wasDisk, wasWait := picked, fetch, onDisk, strictWait
	picked = func(settings.Settings) pii.Model { return m }
	fetch = func(pii.Model, bool) {}
	onDisk = func() bool { return true }
	strictWait = 200 * time.Millisecond
	t.Cleanup(func() { picked, fetch, onDisk, strictWait = wasPicked, wasFetch, wasDisk, wasWait; behind.Store(0) })

	key := "sk-ant-api03-" + strings.Repeat("a", 40)
	mask := func(body []byte) ([]byte, int, any) {
		type res struct {
			b []byte
			n int
			p any
		}
		got := make(chan res, 1)
		go func() {
			var r res
			defer func() { r.p = recover(); got <- r }()
			b, hits := Masker{}.JSONHitsContext(context.Background(), body)
			r.b, r.n = b, len(hits)
		}()
		select {
		case r := <-got:
			return r.b, r.n, r.p
		case <-time.After(20 * strictWait):
			t.Fatal("turned on by the user, a model that never answers held the request with no bound")
		}
		return nil, 0, nil
	}

	// a model that never answers: the rules mask alone
	b, n, p := mask([]byte(`{"input":[{"role":"user","content":"new tool result: key ` + key + `"}]}`))
	if p != nil {
		t.Fatalf("held back: %v", p)
	}
	if strings.Contains(string(b), "aaaa") || n == 0 {
		t.Fatalf("the rules didn't mask: %s", b)
	}

	// a model that read the history and not the new text: its find is
	// masked there, and the rules mask the new text
	m = partly{stalled}
	b, _, p = mask([]byte(`{"input":[{"role":"user","content":"Liu Xiang said deploy"},{"role":"user","content":"new tool result: key ` + key + `"}]}`))
	if p != nil {
		t.Fatalf("held back: %v", p)
	}
	if strings.Contains(string(b), "Liu Xiang") || strings.Contains(string(b), "aaaa") {
		t.Fatalf("what the model read wasn't masked with its find, or the rest not by the rules: %s", b)
	}
}

// slow is a model that reads a text it has "seen" at once and needs
// longer than any wait for one it hasn't: a CPU too slow for what the
// agents send.
type slow struct{ fake }

func (slow) Find(ctx context.Context, texts []string) ([][]redact.Found, error) {
	missing := 0
	for _, s := range texts {
		if strings.Contains(s, "unread") {
			missing++
		}
	}
	if missing == 0 {
		return make([][]redact.Found, len(texts)), nil
	}
	<-ctx.Done()
	return make([][]redact.Found, len(texts)), &pii.Partial{Missing: missing, Of: len(texts), Err: ctx.Err()}
}

// vsoullai on X, #4856 (Windows, the local model turned on): the model
// couldn't keep up, and each request waited all of strictWait for it, one
// after another. Once a request's own text wasn't read in its strictWait,
// the next wait defaultWait, until a request is read in full again. The
// rules mask what the model didn't read either way.
func TestStrictModelBehindWaitsTheDefault(t *testing.T) {
	wasStrict, wasDefault := strictWait, defaultWait
	strictWait, defaultWait = 2*time.Second, 50*time.Millisecond
	behind.Store(0)
	t.Cleanup(func() { strictWait, defaultWait = wasStrict, wasDefault; behind.Store(0) })
	m := slow{fake{installed: true}}
	took := func(text string) time.Duration {
		start := time.Now()
		model(context.Background(), m, 0.8, true)([]string{text})
		return time.Since(start)
	}
	if d := took("unread tool result"); d < strictWait {
		t.Fatalf("the first request didn't wait strictWait: %v", d)
	}
	for i := range 3 {
		if d := took("unread tool result"); d >= strictWait/2 {
			t.Fatalf("request %d after the model fell behind waited %v", i+2, d)
		}
	}
	// a request read in full: the model caught up, strictWait again
	took("read before")
	if d := took("unread tool result"); d < strictWait {
		t.Fatalf("after one read in full, a request waited %v", d)
	}
}

// bounds is a model that says whether it was asked Bounded.
type bounds struct {
	fake
	got *bool
}

func (b bounds) Find(ctx context.Context, texts []string) ([][]redact.Found, error) {
	*b.got = pii.IsBounded(ctx)
	return make([][]redact.Found, len(texts)), nil
}

// On by default the model is asked Bounded: what a request adds first,
// and of a history it went without reading, a little a request
// (xinvxueyuan and shensmart on Discord, 10-28). Turned on by the user,
// it reads all of it.
func TestDefaultModelIsBounded(t *testing.T) {
	var got bool
	model(context.Background(), bounds{fake{installed: true}, &got}, 0.8, false)([]string{"a"})
	if !got {
		t.Fatal("on by default, the model wasn't asked Bounded")
	}
	model(context.Background(), bounds{fake{installed: true}, &got}, 0.8, true)([]string{"a"})
	if got {
		t.Fatal("turned on by the user, the model was asked Bounded")
	}
}

// panics is a model whose Find panics, as AstrLink Guard's tokenizer did
// on a stray byte (#4553).
type panics struct{ fake }

func (panics) Find(context.Context, []string) ([][]redact.Found, error) {
	var s []int
	_ = s[3]
	return nil, nil
}

// A model that panics is the request's error, not a panic on find's own
// goroutine, which ended magpie on Windows (#4553).
func TestModelPanicIsTheRequestsError(t *testing.T) {
	_, err := find(context.Background(), panics{fake{installed: true}}, []string{"hello \xff"})
	if err == nil || !strings.Contains(err.Error(), "failed reading this request") {
		t.Fatalf("err = %v", err)
	}
}

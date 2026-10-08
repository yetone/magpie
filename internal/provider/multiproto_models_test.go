package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/catalog"
)

// A provider whose protocols are served at bases of their own is asked
// each of them, and the lists are merged (#904): a vendor that lists its
// Claude models at its Anthropic base and its GPT models at its Chat base
// kept only the first list that answered, so the models of every other
// endpoint were invisible in the product — in the agents' model lists, in
// the saved preferences, and even behind a model's fixed protocol, which
// setModelAPI refuses for a model the catalog does not list.
func TestFetchUnionsProtocolLists(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var chatHits, anthropicHits atomic.Int32
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		chatHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a","display_name":"GPT A",` +
			`"supported_reasoning_levels":[{"effort":"high"}],` +
			`"modalities":{"input":["text"]}}]}`))
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		anthropicHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"claude-b","display_name":"Claude B"}]}`))
	}))
	defer anthropic.Close()

	p := Provider{
		ID:        "dual",
		Name:      "Dual",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-d",
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	q, err := Find("dual")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := map[string]catalog.Model{}
	for _, m := range ms {
		got[m.ID] = m
	}
	if len(got) != 2 || got["gpt-a"].ID == "" || got["claude-b"].ID == "" {
		t.Fatalf("listed %v, want gpt-a and claude-b together", ms)
	}
	if chatHits.Load() != 1 || anthropicHits.Load() != 1 {
		t.Errorf("asked chat %d times, anthropic %d", chatHits.Load(), anthropicHits.Load())
	}
	// the list a model came from keeps its own fields
	if g := got["gpt-a"]; g.Name != "GPT A" || len(g.Efforts) != 1 || g.Efforts[0] != "high" {
		t.Errorf("gpt-a: %+v", g)
	}
	if g := got["claude-b"]; g.Name != "Claude B" {
		t.Errorf("claude-b: %+v", g)
	}
	// the models an agent is offered, and a model's own protocol setting
	// (serves, then SetModelAPI), both read the catalog the fetch kept:
	// a model only the second endpoint lists has to be in it
	avail := map[string]bool{}
	for _, m := range q.Available() {
		avail[m.ID] = true
	}
	if !avail["gpt-a"] || !avail["claude-b"] {
		t.Errorf("available %v, want gpt-a and claude-b", q.Available())
	}
	if !q.serves("claude-b") {
		t.Error("claude-b is not a model of the provider")
	}
	// the base kept for routing is the one the first protocol answered at
	if l, err := q.fetchOne(context.Background()); err != nil || l.base != chat.URL+"/v1" {
		t.Errorf("base %q (%v), want the chat one", l.base, err)
	}
}

// The order the protocols are asked in must not decide which models are
// kept: a provider with a Responses base and an Anthropic base and no Chat
// one is asked both.
func TestFetchUnionsProtocolListsWithoutChat(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var responsesHits, anthropicHits atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		responsesHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer responses.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		anthropicHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "dual2",
		Name:      "Dual 2",
		Responses: responses.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-d",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("dual2")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 2 || ms[0].ID != "gpt-a" || ms[1].ID != "claude-b" {
		t.Fatalf("listed %v, want gpt-a then claude-b", ms)
	}
	if responsesHits.Load() != 1 || anthropicHits.Load() != 1 {
		t.Errorf("asked responses %d times, anthropic %d", responsesHits.Load(), anthropicHits.Load())
	}
}

// One model in two lists is one model: its image capability is merged the
// way the keys' lists are (an explicit text-only answer wins), not taken
// from whichever list was read last.
func TestFetchUnionsSharedModel(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a","display_name":"GPT A",` +
			`"supported_reasoning_levels":[{"effort":"high"}],` +
			`"modalities":{"input":["text"]}}]}`))
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		// the same model, as the Anthropic endpoint answers for it
		w.Write([]byte(`{"data":[{"id":"gpt-a","display_name":"GPT A",` +
			`"supported_reasoning_levels":[{"effort":"high"}],` +
			`"modalities":{"input":["text","image"]}}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "shared",
		Name:      "Shared",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-d",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("shared")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("listed %v, want one model", ms)
	}
	m := ms[0]
	if m.ID != "gpt-a" || m.Name != "GPT A" || len(m.Efforts) != 1 || m.Efforts[0] != "high" {
		t.Errorf("fields lost: %+v", m)
	}
	if m.ImageInput == nil || *m.ImageInput {
		t.Errorf("image input %v, want the explicit text-only answer kept", m.ImageInput)
	}
	if m.Images {
		t.Errorf("images on, want off: %+v", m)
	}
}

// A provider whose every protocol answers keeps asking only the bases it
// speaks: one protocol means the one request it made before.
func TestFetchAsksOneProtocolOnce(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var hits atomic.Int32
	one := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`))
	}))
	defer one.Close()

	if err := Save(Provider{ID: "one", Name: "One", Chat: one.URL + "/v1", Key: "sk-o"}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("one")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 2 || ms[0].ID != "gpt-a" || ms[1].ID != "gpt-b" {
		t.Errorf("listed %v", ms)
	}
	if hits.Load() != 1 {
		t.Errorf("asked %d times, want once", hits.Load())
	}
}

// Chat and Responses at one base ask it once, as they did when the first
// answer ended the fetch.
func TestFetchAsksASharedBaseOnce(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var hits atomic.Int32
	shared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer shared.Close()

	if err := Save(Provider{
		ID:        "same",
		Name:      "Same",
		Chat:      shared.URL + "/v1",
		Responses: shared.URL + "/v1",
		Key:       "sk-s",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("same")
	if err != nil {
		t.Fatal(err)
	}
	if ms, err := q.Fetch(context.Background()); err != nil || len(ms) != 1 {
		t.Fatalf("%v %v", ms, err)
	}
	if hits.Load() != 1 {
		t.Errorf("asked %d times, want once", hits.Load())
	}
}

// Where no protocol answers, the error is the one it was: every base asked
// is named in it, with what each said, and the ids can still be typed in.
func TestFetchUnionsFailsWithEveryURL(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "dead",
		Name:      "Dead",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-x",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("dead")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err == nil {
		t.Fatalf("listed %v with no endpoint answering", ms)
	}
	msg := err.Error()
	if !strings.Contains(msg, chat.URL+"/v1/models: 404") || !strings.Contains(msg, anthropic.URL) {
		t.Errorf("error does not name every base asked: %s", msg)
	}
	if !strings.Contains(msg, "by hand") {
		t.Errorf("error lost the way out: %s", msg)
	}
	if ms != nil {
		t.Errorf("listed %v with no endpoint answering", ms)
	}
	if _, _, ok := catalog.Live("dead"); ok {
		t.Error("a catalog was kept for a provider that answered nothing")
	}
}

// A models URL is where the user said its list is: that URL alone is asked,
// once, and no base is.
func TestFetchModelsURLAsksNoBase(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var baseHits atomic.Int32
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baseHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer base.Close()
	var urlHits atomic.Int32
	list := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlHits.Add(1)
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer list.Close()

	if err := Save(Provider{
		ID:        "listed",
		Name:      "Listed",
		Chat:      base.URL + "/v1",
		Anthropic: base.URL + "/v1",
		ModelsURL: list.URL + "/v1/models",
		Key:       "sk-l",
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("listed")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := q.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ms) != 1 || ms[0].ID != "claude-b" {
		t.Errorf("listed %v, want the models URL's own list", ms)
	}
	if urlHits.Load() != 1 {
		t.Errorf("asked the models URL %d times, want once", urlHits.Load())
	}
	if baseHits.Load() != 0 {
		t.Errorf("asked a base %d times beside the models URL", baseHits.Load())
	}
}

// A protocol of a multi-protocol provider that can't be asked now keeps the
// models it listed last time, as a key that can't be asked does: a failed
// read says nothing about what the vendor serves today, and without it a
// picked Claude model is gone from the user's provider for good (#904,
// yetone's review of the union fetch: an Anthropic base answering 502
// deleted its models from the picks and never brought them back).
func TestFetchKeepsTheModelsAFailedProtocolListed(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	var list atomic.Value
	list.Store(`{"data":[{"id":"claude-b"}]}`)
	var down atomic.Bool
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(list.Load().(string)))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "failing",
		Name:      "Failing",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-f",
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		p, err := Find("failing")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Fetch(context.Background())
		return err
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	// both models picked from the list both protocols answered with
	p, _ := Find("failing")
	p.Models = []string{"gpt-a", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// the Anthropic base is down: the fetch still succeeds on the Chat one,
	// and nothing the failed endpoint listed goes missing
	down.Store(true)
	if err := fetch(); err != nil {
		t.Fatalf("fetch with one protocol down: %v", err)
	}
	p, _ = Find("failing")
	if want := []string{"gpt-a", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after a protocol failed: %v, want %v", p.Models, want)
	}
	live, _, ok := p.live()
	if !ok {
		t.Fatal("no list kept after a protocol failed")
	}
	if ids := idsOf(live); !slices.Equal(ids, []string{"gpt-a", "claude-b"}) {
		t.Fatalf("list after a protocol failed: %v, want gpt-a and claude-b", ids)
	}
	if !p.serves("claude-b") {
		t.Error("claude-b is not a model of the provider any more")
	}

	// and once the endpoint answers again its real list stands: a model it
	// has dropped since is dropped here, so keeping is not keeping forever
	down.Store(false)
	list.Store(`{"data":[{"id":"claude-c"}]}`)
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("failing")
	if want := []string{"gpt-a"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after the endpoint answered again: %v, want %v", p.Models, want)
	}
	live, _, ok = p.live()
	if !ok {
		t.Fatal("no list kept after the endpoint answered again")
	}
	if ids := idsOf(live); !slices.Equal(ids, []string{"gpt-a", "claude-c"}) {
		t.Fatalf("list after the endpoint answered again: %v, want gpt-a and claude-c", ids)
	}
}

// The other way round, which is what stops the fix from keeping every old
// model: a model only the Chat base listed is gone from that base's list,
// and the Anthropic base failing says nothing about it. Keeping every model
// of a failed fetch would resurrect it (yetone's review).
func TestFetchDropsAGoneModelWhileAnotherProtocolFails(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var list atomic.Value
	list.Store(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(list.Load().(string)))
	}))
	defer chat.Close()
	var down atomic.Bool
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "half",
		Name:      "Half",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-h",
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		p, err := Find("half")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Fetch(context.Background())
		return err
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("half")
	p.Models = []string{"gpt-a", "gpt-b", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// the Chat base has dropped gpt-b, and the Anthropic base is down
	list.Store(`{"data":[{"id":"gpt-a"}]}`)
	down.Store(true)
	if err := fetch(); err != nil {
		t.Fatalf("fetch with one protocol down: %v", err)
	}
	p, _ = Find("half")
	if want := []string{"gpt-a", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after the Chat list dropped a model: %v, want %v", p.Models, want)
	}
	live, _, ok := p.live()
	if !ok {
		t.Fatal("no list kept after a protocol failed")
	}
	if ids := idsOf(live); !slices.Equal(ids, []string{"gpt-a", "claude-b"}) {
		t.Fatalf("list after the Chat list dropped a model: %v, want gpt-a and claude-b", ids)
	}
}

// idsOf are the ids of a list, in its order.
func idsOf(ms []catalog.Model) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// The same where each of several keys has a list of its own: a base of the
// provider's that can't be asked keeps its models, and every model's keys
// are its own still.
func TestFetchKeepsTheModelsAFailedProtocolListedPerKey(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	var down atomic.Bool
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "keys",
		Name:      "Keys",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-1",
		Keys:      []KeyAccount{{Key: "sk-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		p, err := Find("keys")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Fetch(context.Background())
		return err
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("keys")
	p.Models = []string{"gpt-a", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	down.Store(true)
	if err := fetch(); err != nil {
		t.Fatalf("fetch with one protocol down: %v", err)
	}
	p, _ = Find("keys")
	if want := []string{"gpt-a", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after a protocol failed: %v, want %v", p.Models, want)
	}
	live, _, ok := p.live()
	if !ok {
		t.Fatal("no list kept after a protocol failed")
	}
	if ids := idsOf(live); !slices.Equal(ids, []string{"gpt-a", "claude-b"}) {
		t.Fatalf("list after a protocol failed: %v, want gpt-a and claude-b", ids)
	}
	// both keys still see both models: a model kept for a base that failed
	// is not a model of the keys that answered beside it alone
	for _, m := range live {
		if len(m.Keys) != 2 || !slices.Contains(m.Keys, KeyID("sk-1")) || !slices.Contains(m.Keys, KeyID("sk-2")) {
			t.Errorf("%s keys %v, want both", m.ID, m.Keys)
		}
	}
}

// A list saved before the parts were kept says nothing about which base
// listed what, so the models no answering base lists are kept this once, and
// the next fetch with every base answering settles it: what that base does
// not list is gone, however long it was kept for.
func TestFetchKeepsModelsOfAListSavedWithoutParts(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	var down atomic.Bool
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "unrecorded",
		Name:      "Unrecorded",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-u",
	}); err != nil {
		t.Fatal(err)
	}
	// the list a fetch before this change kept: the union, with no part
	// saying which base listed what
	if err := catalog.SaveLive("unrecorded", chat.URL+"/v1", []catalog.Model{
		{ID: "gpt-a"}, {ID: "gpt-b"}, {ID: "claude-b"},
	}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("unrecorded")
	p.Models = []string{"gpt-a", "gpt-b", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	down.Store(true)
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("fetch with one protocol down: %v", err)
	}
	p, _ = Find("unrecorded")
	if want := []string{"gpt-a", "gpt-b", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after a protocol failed: %v, want %v", p.Models, want)
	}

	// every base answering now says which models are its own: gpt-b, gone
	// from the Chat list, is gone
	down.Store(false)
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("unrecorded")
	if want := []string{"gpt-a", "claude-b"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after every base answered: %v, want %v", p.Models, want)
	}
}

// An endpoint that has never answered keeps nothing. The parts saved with
// the list say which endpoint listed what, and an endpoint in none of them
// is one that never answered: the whole list saved before is not its own,
// and a model the endpoint that does answer has dropped is gone, however
// many rounds the other one stays at 404. Reading an absent part as an
// empty one instead fixed that guess as what the endpoint listed, and the
// model could never be dropped again (#904, yetone's review of #1006).
func TestFetchDropsAModelWhileAnEndpointHasNeverAnswered(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var list atomic.Value
	list.Store(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`)
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(list.Load().(string)))
	}))
	defer chat.Close()
	// the Anthropic base never answers, from the first round on: no part
	// of the list is ever its own
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "never",
		Name:      "Never",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-n",
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		p, err := Find("never")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Fetch(context.Background())
		return err
	}
	// round 0: both models listed by the one endpoint that answers, and
	// both picked
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("never")
	p.Models = []string{"gpt-a", "gpt-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	live, _, ok := p.live()
	if !ok {
		t.Fatal("no list kept after the first round")
	}
	if ids, want := idsOf(live), []string{"gpt-a", "gpt-b"}; !slices.Equal(ids, want) {
		t.Fatalf("list after the first round: %v", ids)
	}

	// the Chat base has dropped gpt-b. The Anthropic base, which never
	// answered, has no say in it: it is gone from the second round on, and
	// stays gone however long that base keeps answering 404.
	list.Store(`{"data":[{"id":"gpt-a"}]}`)
	for round := 1; round <= 4; round++ {
		if err := fetch(); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		p, _ = Find("never")
		if want := []string{"gpt-a"}; !slices.Equal(p.Models, want) {
			t.Fatalf("round %d picks: %v, want %v", round, p.Models, want)
		}
		live, _, ok = p.live()
		if !ok {
			t.Fatalf("round %d: no list kept", round)
		}
		if ids, want := idsOf(live), []string{"gpt-a"}; !slices.Equal(ids, want) {
			t.Fatalf("round %d list: %v, want %v", round, ids, want)
		}
		// an endpoint that never answered is not saved as one that listed
		// nothing: it has said nothing at all, and a part here would be a
		// guess fixed as what it listed
		sides, _, ok := catalog.LiveSplit("never")
		if !ok {
			t.Fatalf("round %d: no parts kept", round)
		}
		// the Anthropic base is kept as the root /v1/messages is asked at
		// (normalize), which is the key its part is saved under
		if _, saved := sides[anthropic.URL]; saved {
			t.Fatalf("round %d: a part was saved for an endpoint that never answered", round)
		}
	}
}

// A key that can't be asked at any of its bases keeps the models it saw
// last time, and they keep the bases that listed them: the next time that
// key answers at its Chat base while its Anthropic base is still down, the
// Claude model only that key saw is that base's own, and is kept as a
// single key's failed base is. Kept as a whole list with no part saying
// where it came from it belongs to no base at all, and is read as gone the
// first time that key answers half-way (#904, yetone's review of #1006).
func TestFetchKeepsAModelsBaseAfterItsKeyFailedWhole(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	// each key has a Claude model of its own, so claude-b is one base's own
	// and of the second key alone
	var chatDown, anthropicDown, swapped atomic.Bool
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if chatDown.Load() && r.Header.Get("Authorization") == "Bearer sk-2" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-2" {
			w.Write([]byte(`{"data":[{"id":"claude-1"}]}`))
			return
		}
		if anthropicDown.Load() {
			http.NotFound(w, r)
			return
		}
		if swapped.Load() {
			w.Write([]byte(`{"data":[{"id":"claude-c"}]}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "wholekey",
		Name:      "Whole key",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-1",
		Keys:      []KeyAccount{{Key: "sk-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() ([]string, []string) {
		p, err := Find("wholekey")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
		p, _ = Find("wholekey")
		live, _, ok := p.live()
		if !ok {
			t.Fatal("no list kept")
		}
		return idsOf(live), p.Models
	}
	all := []string{"gpt-a", "claude-1", "claude-b"}

	// round 0: every base of every key answers
	got, _ := fetch()
	if !slices.Equal(got, all) {
		t.Fatalf("list after the first round: %v, want %v", got, all)
	}
	p, _ := Find("wholekey")
	p.Models = []string{"gpt-a", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// round 1: the second key can't be asked at any of its bases, so its
	// Claude model is kept from the list saved before
	chatDown.Store(true)
	anthropicDown.Store(true)
	got, _ = fetch()
	if !slices.Equal(got, all) {
		t.Fatalf("list after the second key failed whole: %v, want %v", got, all)
	}

	// round 2: that key answers at its Chat base again while its Anthropic
	// base is still down
	chatDown.Store(false)
	got, picks := fetch()
	if !slices.Equal(got, all) {
		t.Fatalf("list after the second key answered half-way: %v, want %v", got, all)
	}
	if !slices.Equal(picks, []string{"gpt-a", "claude-b"}) {
		t.Fatalf("picks after the second key answered half-way: %v, want gpt-a and claude-b", picks)
	}

	// round 3: that key's Anthropic base answers again and has dropped the
	// model, which is then gone: keeping it is not keeping it for good
	anthropicDown.Store(false)
	swapped.Store(true)
	got, picks = fetch()
	if want := []string{"gpt-a", "claude-1", "claude-c"}; !slices.Equal(got, want) {
		t.Fatalf("list after that base answered again: %v, want %v", got, want)
	}
	if !slices.Equal(picks, []string{"gpt-a"}) {
		t.Fatalf("picks after that base answered again: %v, want gpt-a", picks)
	}
}

// The other way round for the same rule, which is what keeps sides saying
// what the bases listed: a model kept for a key that could not be asked at
// all belongs to the bases that listed it, but not to one that answered
// this round for another of the model's keys and no longer serves it. A
// part here said the Anthropic base still served claude-b, and the fetch
// after that read it as a base that couldn't be asked, holding a model its
// vendor had dropped for one round longer (yetone's review of #1006, found
// on the path TestFetchKeepsAModelsBaseAfterItsKeyFailedWhole fixed).
//
// It is the same two keys as that test, and the difference is in one thing:
// there claude-b is only the second key's, so the Anthropic base never
// listed it for the first one and its answering says nothing about it. Here
// the first key saw claude-b too, and this round stopped being served it.
func TestFetchDropsAModelABaseStoppedServingWhileAKeyIsDown(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var secondDown, dropped, anthDown atomic.Bool
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if secondDown.Load() && r.Header.Get("Authorization") == "Bearer sk-2" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"}]}`))
	}))
	defer chat.Close()
	// both keys are served claude-b to begin with; the drop below is the
	// vendor stopping both of them
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if anthDown.Load() {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") == "Bearer sk-2" && secondDown.Load() {
			http.NotFound(w, r)
			return
		}
		if dropped.Load() {
			w.Write([]byte(`{"data":[{"id":"claude-1"}]}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-1"},{"id":"claude-b"}]}`))
	}))
	defer anthropic.Close()

	if err := Save(Provider{
		ID:        "stopped",
		Name:      "Stopped",
		Chat:      chat.URL + "/v1",
		Anthropic: anthropic.URL + "/v1",
		Key:       "sk-1",
		Keys:      []KeyAccount{{Key: "sk-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func() ([]string, []string) {
		p, err := Find("stopped")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
		p, _ = Find("stopped")
		live, _, ok := p.live()
		if !ok {
			t.Fatal("no list kept")
		}
		return idsOf(live), p.Models
	}

	// round 0: both keys see claude-b
	got, _ := fetch()
	if want := []string{"gpt-a", "claude-1", "claude-b"}; !slices.Equal(got, want) {
		t.Fatalf("list after the first round: %v, want %v", got, want)
	}
	p, _ := Find("stopped")
	p.Models = []string{"gpt-a", "claude-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// round 1: the second key is down at both of its bases, so claude-b is
	// kept for it, and in the same round the Anthropic base answers for the
	// first key without listing it. The base is not that model's any more,
	// and no part says it is.
	secondDown.Store(true)
	dropped.Store(true)
	got, _ = fetch()
	if want := []string{"gpt-a", "claude-1", "claude-b"}; !slices.Equal(got, want) {
		t.Fatalf("list while a key is down and the base dropped a model: %v, want %v", got, want)
	}
	sides, _, ok := catalog.LiveSplit("stopped")
	if !ok {
		t.Fatal("no parts kept")
	}
	if ids := idsOf(sides[anthropic.URL]); !slices.Equal(ids, []string{"claude-1"}) {
		t.Fatalf("part of the Anthropic base: %v, want claude-1 alone", ids)
	}

	// round 2: that key is asked again and its Anthropic base is down for
	// both keys, so nothing keeps claude-b and it is gone
	secondDown.Store(false)
	anthDown.Store(true)
	got, picks := fetch()
	if want := []string{"gpt-a", "claude-1"}; !slices.Equal(got, want) {
		t.Fatalf("list after the base went down for every key: %v, want %v", got, want)
	}
	if !slices.Equal(picks, []string{"gpt-a"}) {
		t.Fatalf("picks after the base went down for every key: %v, want gpt-a", picks)
	}

	// round 3: and it stays gone once the base answers again without it
	anthDown.Store(false)
	got, picks = fetch()
	if want := []string{"gpt-a", "claude-1"}; !slices.Equal(got, want) {
		t.Fatalf("list after the base answered again: %v, want %v", got, want)
	}
	if !slices.Equal(picks, []string{"gpt-a"}) {
		t.Fatalf("picks after the base answered again: %v, want gpt-a", picks)
	}
}

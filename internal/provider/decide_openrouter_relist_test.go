package provider

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// OpenRouter's baseten/elephant-1 as its decisions list served it on
// 2026-11-26 (added 10-21), cut to the fields magpie reads.
const orElephant = `{"id":"baseten/elephant-1","canonical_slug":"baseten/elephant-1-20261021","name":"Baseten: Elephant 1","context_length":16384,"architecture":{"modality":"text+image->decisions","input_modalities":["text","image"],"output_modalities":["decisions"],"tokenizer":"Other","instruct_type":null},"pricing":{"prompt":"0.000000035","completion":"0"},"supported_parameters":[],"endpoints":["/api/alpha/decisions"]}`

// coeo91 on Discord (11-26): baseten/elephant-1:free, picked by hand as a
// routing classifier at the built-in OpenRouter, was refused 400 "is a
// decisions model … Use the /api/alpha/decisions endpoint instead": the
// list of OpenRouter's decision models magpie had was fetched once, before
// OpenRouter added Elephant 1, so its variant was taken for a chat model.
// The gateway's loop now fetches that list again once it is decideListAge
// old; a fresh one isn't asked for, one that can't be had keeps the last,
// and a provider switched off isn't asked.
func TestOpenRouterDecisionListFollowsOpenRouter(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	var lists atomic.Int32
	var added, down atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("output_modalities") {
		case "decisions":
			lists.Add(1)
			switch {
			case down.Load():
				w.WriteHeader(502)
			case added.Load():
				w.Write([]byte(orDecisionsList[:len(orDecisionsList)-2] + "," + orElephant + "]}"))
			default:
				w.Write([]byte(orDecisionsList))
			}
		case "image":
			w.Write([]byte(`{"data":[]}`))
		default:
			w.Write([]byte(orChatList))
		}
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = tr
	t.Cleanup(func() { http.DefaultClient.Transport = old; tr.CloseIdleConnections() })

	const free = "baseten/elephant-1:free"
	if err := Save(Provider{ID: "openrouter", Name: "OpenRouter", Preset: "openrouter", Key: "k",
		Chat: "https://openrouter.ai/api/v1", Models: []string{"openai/gpt-5", free}}); err != nil {
		t.Fatal(err)
	}
	find := func() *Provider {
		t.Helper()
		p, err := Find("openrouter")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := find().Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if find().DecidesModel(free) {
		t.Fatal("the list before Elephant 1 already names it")
	}

	// OpenRouter adds Elephant 1; the list magpie has is a minute old
	added.Store(true)
	ctx := context.Background()
	n := lists.Load()
	if got := relistDecisions(ctx, time.Now()); len(got) != 0 || lists.Load() != n {
		t.Fatalf("a fresh list asked for again: %v", got)
	}
	later := time.Now().Add(decideListAge + time.Minute)
	if got := relistDecisions(ctx, later); !slices.Equal(got, []string{"openrouter"}) {
		t.Fatalf("a list %s old not asked for again: %v", decideListAge+time.Minute, got)
	}
	p := find()
	if !p.DecidesModel(free) || !p.DecidesModel("baseten/elephant-1") {
		t.Fatalf("%s is still taken for a chat model", free)
	}
	if !slices.ContainsFunc(p.DecisionModels(), func(m catalog.Model) bool { return m.ID == "baseten/elephant-1" }) {
		t.Fatalf("the classifier picker doesn't offer Elephant 1: %v", p.DecisionModels())
	}
	if p.DecidesModel("openai/gpt-5") {
		t.Fatal("a chat model taken for a decision model")
	}

	// OpenRouter can't be reached: the list there was stays
	down.Store(true)
	n = lists.Load()
	relistDecisions(ctx, later.Add(decideListAge+time.Minute))
	if lists.Load() != n+1 {
		t.Fatal("OpenRouter wasn't asked")
	}
	if !find().DecidesModel(free) || !find().DecidesModel("inception/mercury-decide") {
		t.Fatal("a failed fetch lost the list")
	}

	// switched off, it isn't asked
	down.Store(false)
	if err := SetOff("openrouter", true); err != nil {
		t.Fatal(err)
	}
	n = lists.Load()
	if got := relistDecisions(ctx, later.Add(3*decideListAge)); len(got) != 0 || lists.Load() != n {
		t.Fatalf("a provider switched off asked: %v", got)
	}
}

// The gateway's loop asks for it by itself.
func TestKeepAccountsListedRelistsDecisions(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fakeOpenRouterLists(t)
	if err := Save(Provider{ID: "openrouter", Name: "OpenRouter", Preset: "openrouter", Key: "k",
		Chat: "https://openrouter.ai/api/v1"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, first, ok := catalog.Live(decisionsID("openrouter"))
	if !ok {
		t.Fatal("no decisions list")
	}

	oldFirst, oldEvery, oldAge := accountRelistFirst, accountRelistEvery, decideListAge
	accountRelistFirst, accountRelistEvery, decideListAge = time.Millisecond, time.Millisecond, 0
	defer func() { accountRelistFirst, accountRelistEvery, decideListAge = oldFirst, oldEvery, oldAge }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		KeepAccountsListed(ctx)
	}()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, at, _ := catalog.Live(decisionsID("openrouter")); at.After(first) {
			break
		}
		<-time.After(5 * time.Millisecond)
	}
	cancel()
	<-done
	if _, at, _ := catalog.Live(decisionsID("openrouter")); !at.After(first) {
		t.Fatal("the loop didn't fetch the decisions list again in 30s")
	}
}

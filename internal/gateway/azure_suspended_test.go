package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Azure's answer to every request while a subscription is disabled for its
// credit spent, and to a wrong key alike (canlee886 on X).
const azureDenied = `{"error":{"code":"401","message":"Access denied due to invalid subscription key or wrong API endpoint. Make sure to provide a valid key for an active subscription and use a correct regional API endpoint for your resource."}}`

// restsUnder is every rest whose key starts with id.
func restsUnder(id string) []Rest {
	restingUntil.Lock()
	var keys []string
	for k := range restingUntil.m {
		if k == id || strings.HasPrefix(k, id+"#") || strings.HasPrefix(k, id+"/") {
			keys = append(keys, k)
		}
	}
	restingUntil.Unlock()
	var out []Rest
	for _, k := range keys {
		if r, ok := restOf(k); ok {
			out = append(out, r)
		}
	}
	return out
}

// azureGroup saves group/sol: the Azure key az first, then a backup bk
// that always answers, and counts the requests each was sent.
func azureGroup(t *testing.T, az http.HandlerFunc) (azHits, bkHits *atomic.Int32) {
	t.Helper()
	azHits, bkHits = new(atomic.Int32), new(atomic.Int32)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			azHits.Add(1)
		}
		az(w, r)
	}))
	t.Cleanup(up.Close)
	bk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bkHits.Add(1)
		servedBy(provider.Chat, "gpt-sol")(w, r)
	}))
	t.Cleanup(bk.Close)
	for _, p := range []provider.Provider{
		{ID: "az", Name: "Azure", Preset: provider.AzurePreset, Key: "az-key", Chat: up.URL + "/openai/v1", Models: []string{"gpt-sol"}},
		{ID: "bk", Name: "Backup", Key: "bk-key", Chat: bk.URL + "/v1", Models: []string{"gpt-sol"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Sol", Members: []string{"az/gpt-sol", "bk/gpt-sol"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	return azHits, bkHits
}

func askSol(t *testing.T, s *Server) {
	t.Helper()
	if code, body := postAs(t, s, "", `{"model":"group/sol","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("group/sol answered %d %s", code, body)
	}
}

// canlee886 on X: an Azure student subscription is disabled when its
// monthly credit runs out, every request 401 until Azure enables it again
// on the reset day, and the provider had to be turned off and on by hand.
// A key that answered before and is refused 401 now rests suspended — its
// group's requests go to the next member meanwhile, Azure not asked — is
// asked in the background with its own key (the deployments list, as the
// real List asks it), and is back as soon as Azure answers again.
func TestAzureKeyThatAnsweredRestsSuspendedTillItAnswers(t *testing.T) {
	fresh(t)
	var down atomic.Bool
	var lists atomic.Int32
	azHits, bkHits := azureGroup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "az-key" {
			t.Errorf("%s asked with api-key %q", r.URL.Path, r.Header.Get("api-key"))
		}
		isList := !strings.HasSuffix(r.URL.Path, "/chat/completions")
		if isList {
			lists.Add(1)
		}
		if down.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, azureDenied)
			return
		}
		if isList {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"gpt-sol","object":"model"}]}`)
			return
		}
		servedBy(provider.Chat, "gpt-sol")(w, r)
	})
	s := New()
	steps, was := restProbeSteps, listForProbe
	restProbeSteps = []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	listForProbe = func(ctx context.Context, p provider.Provider) error { _, err := p.List(ctx); return err }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.keepProbingRests(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; restProbeSteps, listForProbe = steps, was })
	untilState(t, "the loop to start", func() bool {
		restProbes.Lock()
		defer restProbes.Unlock()
		return restProbes.asking[s]
	})

	askSol(t, s)
	if azHits.Load() != 1 || bkHits.Load() != 0 {
		t.Fatalf("a working Azure key: az %d, bk %d", azHits.Load(), bkHits.Load())
	}
	down.Store(true) // the credit spent: Azure disables the subscription
	askSol(t, s)     // answered by the backup
	rs := restsUnder("az")
	if len(rs) != 1 {
		t.Fatalf("rests %+v, want the key's one", rs)
	}
	r := rs[0]
	if r.Why != failSuspended || r.By != "suspended" || !r.Probed || r.Status != http.StatusUnauthorized {
		t.Fatalf("a key that answered before, refused 401, rests %+v: not suspended and asked in the background", r)
	}
	if left := time.Until(r.Until); left < suspendedRest-time.Minute {
		t.Fatalf("rests only %v: asked again by requests, each refused", left)
	}
	// still disabled: the probe's lists are refused, it stays out, and
	// requests aren't sent it
	untilState(t, "the probe to ask while disabled", func() bool { return lists.Load() >= 2 })
	askSol(t, s)
	if azHits.Load() != 2 || len(restsUnder("az")) != 1 {
		t.Fatalf("while Azure refused its list: az asked %d times, rests %+v", azHits.Load(), restsUnder("az"))
	}
	down.Store(false) // the credit renewed: Azure enables it again
	untilState(t, "the rest to be lifted", func() bool { return len(restsUnder("az")) == 0 })
	askSol(t, s)
	if azHits.Load() != 3 {
		t.Fatalf("back, Azure asked %d times, want 3", azHits.Load())
	}
}

// A key refused 401 on its first request — mistyped, revoked, another
// resource's — is a bad key, as before: it rests the short backoff and is
// shown refused, never read as a subscription suspended or asked in the
// background for ever.
func TestAzureKeyNeverAnsweredIsABadKey(t *testing.T) {
	fresh(t)
	var lists atomic.Int32
	azureGroup(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			lists.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, azureDenied)
	})
	s := New()
	askSol(t, s)
	rs := restsUnder("az")
	if len(rs) != 1 {
		t.Fatalf("rests %+v, want the key's one", rs)
	}
	if r := rs[0]; r.Why != failOther || r.By != "backoff" || r.Probed || time.Until(r.Until) > longestRetry {
		t.Fatalf("a key never answered, refused 401, rests %+v: not as a bad key", r)
	}
	if probeQueued(rs[0].Key) {
		t.Fatal("a bad key is asked in the background")
	}
	if n := lists.Load(); n != 0 {
		t.Fatalf("a bad key's list asked %d times", n)
	}
}

// The rule is Azure's: a 401 on another vendor's key that answered before
// rests as a refused key did, its list never asked for it (OpenRouter's
// answers without a key).
func TestSuspendedIsAzureOnly(t *testing.T) {
	fresh(t)
	was := lastServedFor
	t.Cleanup(func() { lastServedFor = was })
	s := probing(t, func(context.Context, provider.Provider) error { return nil })
	for _, tc := range []struct {
		name string
		p    provider.Provider
		want string
	}{
		{"azure", provider.Provider{ID: "sus-az", Preset: provider.AzurePreset, Key: "k", Chat: "https://r.openai.azure.com/openai/v1"}, failSuspended},
		{"azure host", provider.Provider{ID: "sus-azh", Key: "k", Chat: "https://r.cognitiveservices.azure.com/openai/v1"}, failSuspended},
		{"relay", provider.Provider{ID: "sus-relay", Key: "k", Chat: "https://openrouter.ai/api/v1"}, failOther},
		{"azure account", provider.Provider{ID: "sus-acct", Preset: provider.AzurePreset, Key: "k", Chat: "https://r.openai.azure.com/openai/v1",
			Account: &provider.Account{Agent: "codex", User: "a@b.c"}}, failOther},
	} {
		c := probeKey(t, tc.p.ID)
		c.p = tc.p
		lastServedFor = func() map[string]time.Time {
			return map[string]time.Time{provider.ServedID(tc.p): time.Now().Add(-time.Hour)}
		}
		r := s.restAfter(c, http.StatusUnauthorized, http.Header{}, []byte(azureDenied))
		if r.Why != tc.want || (r.Why == failSuspended) != r.Probed {
			t.Errorf("%s: rests %+v, want %s", tc.name, r, tc.want)
		}
		clearRest(c.restKey())
		// a 403 or a 5xx on an Azure key that answered is no suspension
		if tc.want == failSuspended {
			for _, st := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
				if r := s.restAfter(c, st, http.Header{}, []byte(azureDenied)); r.Why == failSuspended {
					t.Errorf("%s: a %d rests suspended", tc.name, st)
				}
				clearRest(c.restKey())
			}
		}
	}
}

// A key resting suspended stays so when its rest ends and Azure still
// refuses it, after its last answer has aged out of served.json; served.json
// unreadable is no answer on record.
func TestSuspendedOutlivesItsLastAnswer(t *testing.T) {
	fresh(t)
	was := lastServedFor
	t.Cleanup(func() { lastServedFor = was })
	s := probing(t, func(context.Context, provider.Provider) error { return context.DeadlineExceeded })
	c := probeKey(t, "sus-long")
	c.p = provider.Provider{ID: "sus-long", Preset: provider.AzurePreset, Key: "k", Chat: "https://r.openai.azure.com/openai/v1"}
	lastServedFor = func() map[string]time.Time { return map[string]time.Time{provider.ServedID(c.p): time.Now()} }
	if r := s.restAfter(c, http.StatusUnauthorized, http.Header{}, []byte(azureDenied)); r.Why != failSuspended {
		t.Fatalf("rests %+v", r)
	}
	lastServedFor = func() map[string]time.Time { return map[string]time.Time{} }
	restingUntil.Lock()
	restingUntil.m[c.restKey()] = time.Now().Add(-time.Second) // its day is over
	restingUntil.Unlock()
	if r := s.restAfter(c, http.StatusUnauthorized, http.Header{}, []byte(azureDenied)); r.Why != failSuspended {
		t.Fatalf("refused again after its rest, with its answer gone from the file: %+v", r)
	}
	clearRest(c.restKey()) // Try now, or an answer
	if r := s.restAfter(c, http.StatusUnauthorized, http.Header{}, []byte(azureDenied)); r.Why != failOther {
		t.Fatalf("no answer on record and no suspension resting, it rests %+v", r)
	}
}

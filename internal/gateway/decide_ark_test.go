package gateway

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// arkUp stands in for ark.cn-beijing.volces.com, in process: its System
// One answers only with the beta header, as it did on 2026-11-01 (the
// reply is the one it gave for a choice and a score; without the header,
// a bare 404). Other hosts go to the test's own servers.
type arkUp struct {
	mu    sync.Mutex
	asked []string
	next  http.RoundTripper
}

const arkChoiceReply = `{"model":"doubao-seed-2-1-lite-260915","answers":{"effort":{"type":"score","score":2.049442630883676,"probabilities":{"0":0.01045358664265636,"1":0.09317180355155118,"2":0.7328530020852525,"3":0.16352160772053995},"confidence":0.42819709728784927,"legend":{"3":"high","0":"none","1":"low","2":"medium"}},"intent":{"type":"choice","choice":"coding","probabilities":{"coding":0.9739693776599165,"none":0.0000752175921947392,"chat":0.025955404747888693},"confidence":0.8897008747807184},"level":{"type":"choice","choice":"coding","probabilities":{"chat":0.019124036750888904,"coding":0.9808759632491112},"confidence":0.8635063896029489}},"usage":{"input_tokens":452,"output_tokens":0}}`

func (u *arkUp) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() != "ark.cn-beijing.volces.com" {
		return u.next.RoundTrip(r)
	}
	io.Copy(io.Discard, r.Body)
	beta := r.Header.Get("ark-beta-systemone-api")
	u.mu.Lock()
	u.asked = append(u.asked, r.URL.Path+" "+beta)
	u.mu.Unlock()
	if beta != "true" || r.URL.Path != "/api/v1/systemone" {
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		Body: io.NopCloser(strings.NewReader(arkChoiceReply)), Request: r}, nil
}

// axelburks on Discord: magpie's System One API, asked for Ark's decision
// model, reaches Ark with its beta header and gives its answer back.
func TestArkSystemOneSendsBetaHeader(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "volcengine-decision", Name: "Volcengine Ark Decision", Preset: "volcengine-decision",
		Key: "ark-1", Decide: "https://ark.cn-beijing.volces.com/api/v1"}); err != nil {
		t.Fatal(err)
	}
	up := &arkUp{next: http.DefaultTransport}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = up
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	s := New()
	s.client = &http.Client{Transport: up}
	code, out := sendTo(s, "/v1/systemone", `{"model":"volcengine-decision/`+provider.ArkDecision+`","state":{"message":"Refactor the auth module to use JWT and add tests"},"questions":{"intent":{"type":"choice","criteria":{"coding":null,"chat":null}}}}`)
	if code != 200 || !strings.Contains(out, `"choice":"coding"`) {
		t.Fatalf("System One: %d %s", code, out)
	}
	if got := up.got(); !slices.Equal(got, []string{"/api/v1/systemone true"}) {
		t.Fatalf("Ark asked: %v", got)
	}
}

func (u *arkUp) got() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.asked)
}

// An Ark provider at pay as you go answers System One there, on its own
// key; a key of it on the Coding Plan is asked at the plan's endpoints,
// which have none, so a decision is never sent with it.
func TestArkDecisionSkipsPlanKeys(t *testing.T) {
	fresh(t)
	const v3 = "https://ark.cn-beijing.volces.com/api/v3"
	if err := provider.Save(provider.Provider{ID: "volc", Name: "Volc", Preset: "volcengine", Chat: v3, Responses: v3,
		Key: "k-api", KeyName: "API", Models: []string{"glm-4-7-251222"},
		Keys: []provider.KeyAccount{{Name: "Coding", Key: "k-coding", Plan: "coding"}}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("volc")
	if err != nil {
		t.Fatal(err)
	}
	if p.Decide != "https://ark.cn-beijing.volces.com/api/v1" {
		t.Fatalf("System One at %q", p.Decide)
	}
	// the vendor's decision model, and one both plans serve (the Coding
	// Plan's list has it, so PlanServing keeps its key)
	for _, m := range []string{provider.ArkDecision, "doubao-seed-2.1-lite"} {
		cs, _ := New().decideKeys(*p, m)
		var keys []string
		for _, c := range cs {
			keys = append(keys, c.p.Key+" "+c.p.Decide)
		}
		if !slices.Equal(keys, []string{"k-api https://ark.cn-beijing.volces.com/api/v1"}) {
			t.Fatalf("%s asked with: %q", m, keys)
		}
	}
}

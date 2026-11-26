package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// What a member says, as refusedMember answers.
const (
	sayAnswer   = ""
	say451      = "451"     // HTTP 451 with an error object
	sayMagpie   = "magpie"  // another magpie's refusal, as it tells its agent
	sayFiltered = "filter"  // the vendor's safety filter ending the reply with nothing said
	sayLimited  = "limited" // a rate limit: the account's own
)

// magpieRefused is what a magpie answers a Chat client when its member's
// safety filter refused and nobody else answered (refusalTold), the
// reporter's 400 "… refused this request (safety filter: 451)" (Winnie7572
// on X), byte for byte as this gateway writes it; magpieRefusedMessages is
// the same on Messages.
const (
	magpieRefused         = `{"error":{"code":null,"message":"A: The request was blocked. (HTTP 451)\nmagpie: m refused this request (safety filter: 451) via A","param":null,"type":"invalid_request_error"}}`
	magpieRefusedMessages = `{"error":{"message":"A: The request was blocked. (HTTP 451)\nmagpie: m refused this request (safety filter: 451) via A","type":"invalid_request_error"},"type":"error"}`
)

// refusedMember is a member that answers, or says what it is set to.
type refusedMember struct {
	mu  sync.Mutex
	n   int
	say string
}

func (m *refusedMember) set(say string) {
	m.mu.Lock()
	m.say = say
	m.mu.Unlock()
}

func (m *refusedMember) asked() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n
}

func (m *refusedMember) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.n++
	say := m.say
	m.mu.Unlock()
	messages := strings.HasSuffix(r.URL.Path, "/messages")
	stream := strings.Contains(string(b), `"stream":true`)
	w.Header().Set("Content-Type", "application/json")
	switch say {
	case say451:
		w.WriteHeader(http.StatusUnavailableForLegalReasons)
		io.WriteString(w, `{"error":{"message":"The request was blocked.","type":"invalid_request_error"}}`)
		return
	case sayMagpie:
		w.Header().Set(providerHeader, "a")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, map[bool]string{false: magpieRefused, true: magpieRefusedMessages}[messages])
		return
	case sayLimited:
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`)
		return
	}
	switch {
	case messages && stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, map[bool]string{false: anthropicAnswer, true: anthropicRefusal}[say == sayFiltered])
	case messages:
		io.WriteString(w, map[bool]string{false: anthropicAnswerWhole, true: anthropicRefusalWhole}[say == sayFiltered])
	case stream:
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, map[bool]string{false: chatStreamOK, true: chatFiltered}[say == sayFiltered])
	case say == sayFiltered:
		io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`)
	default:
		io.WriteString(w, chatOK)
	}
}

// refusedGroup saves providers a to f, each a refusedMember serving m on
// proto, and group G of them in that order, routed and kept as given.
func refusedGroup(t *testing.T, proto provider.Protocol, routing, affinity string) map[string]*refusedMember {
	t.Helper()
	ms := map[string]*refusedMember{}
	var members []string
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		m := &refusedMember{}
		up := httptest.NewServer(m)
		t.Cleanup(up.Close)
		p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Models: []string{"m"}}
		if proto == provider.Anthropic {
			p.Anthropic = up.URL
		} else {
			p.Chat = up.URL + "/v1"
		}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		ms[id], members = m, append(members, id+"/m")
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: members, Routing: routing, Affinity: affinity}); err != nil {
		t.Fatal(err)
	}
	return ms
}

// conversation sends turns of one session's conversation to s, each a
// user message more than the last.
type conversation struct {
	s      *Server
	proto  provider.Protocol
	stream bool
	turns  []string
}

func (c *conversation) next(say string) (int, string) {
	c.turns = append(c.turns, say)
	var msgs []string
	for i, x := range c.turns {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":"ok"}`)
		}
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":%q}`, x))
	}
	path, body := "/v1/chat/completions", fmt.Sprintf(`{"model":"group/g","stream":%v,"messages":[%s]}`, c.stream, strings.Join(msgs, ","))
	if c.proto == provider.Anthropic {
		path, body = "/v1/messages", fmt.Sprintf(`{"model":"group/g","max_tokens":16,"stream":%v,"messages":[%s]}`, c.stream, strings.Join(msgs, ","))
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("x-session-id", "s1")
	c.s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// answerer is the member a turn went to: the only one asked so far.
func answerer(t *testing.T, ms map[string]*refusedMember) string {
	t.Helper()
	who := ""
	for id, m := range ms {
		if m.asked() > 0 {
			if who != "" {
				t.Fatalf("first turn asked %s and %s", who, id)
			}
			who = id
		}
	}
	return who
}

// The reporter's case (Winnie7572 on X, after v0.1.4847): a group kept
// Strict, whose member answered the conversation, then refused one
// message with a 451 every time it was asked, five members ready. Strict
// kept only that member, so the refusal reached the agent at once,
// tries=[{status:400, fail:"refused"}]. A refusal is about the content
// and that vendor, not the account Strict keeps: the members Strict left
// out are asked, the first that answers keeps the conversation, and the
// member that refused doesn't rest. Every refusal magpie reads takes the
// same route: a 451, the safety filter ending the reply, and another
// magpie's refusal.
func TestStrictConversationRefusedGoesToTheOthers(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Anthropic} {
		for _, routing := range []string{provider.Ordered, ""} {
			for _, stream := range []bool{false, true} {
				for _, say := range []string{say451, sayFiltered, sayMagpie} {
					name := fmt.Sprintf("%s/%s/stream=%v/%s", proto, map[string]string{"": "smart"}[routing]+routing, stream, say)
					t.Run(name, func(t *testing.T) {
						fresh(t)
						ms := refusedGroup(t, proto, routing, provider.AffinityStrict)
						s := New()
						c := &conversation{s: s, proto: proto, stream: stream}
						if code, out := c.next("hi"); code != http.StatusOK {
							t.Fatalf("first turn: %d %s", code, out)
						}
						kept := answerer(t, ms)
						ms[kept].set(say)
						code, out := c.next("the words it refuses")
						r := lastRoute(s)
						if code != http.StatusOK || len(r.Tries) != 2 || r.Tries[0].ID != kept || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil {
							t.Fatalf("refused turn: %d %.300s\ntries %+v", code, out, r.Tries)
						}
						if strings.Contains(out, "refused") || strings.Contains(out, "451") {
							t.Fatalf("the agent was told the refusal: %s", out)
						}
						if s.resting(kept) {
							t.Fatalf("%s rests for a refusal", kept)
						}
						took := r.Tries[1].ID
						// the conversation is the one that answered it now,
						// kept there as Strict keeps it
						if code, out := c.next("go on"); code != http.StatusOK {
							t.Fatalf("next turn: %d %s", code, out)
						}
						if r := lastRoute(s); len(r.Tries) != 1 || r.Tries[0].ID != took {
							t.Fatalf("next turn went to %+v, not %s", r.Tries, took)
						}
					})
				}
			}
		}
	}
}

// Every member refusing, the agent has the refusal, each member asked
// once, nobody resting.
func TestStrictConversationRefusedByEveryMember(t *testing.T) {
	fresh(t)
	ms := refusedGroup(t, provider.Chat, provider.Ordered, provider.AffinityStrict)
	s := New()
	c := &conversation{s: s, proto: provider.Chat}
	if code, out := c.next("hi"); code != http.StatusOK {
		t.Fatalf("first turn: %d %s", code, out)
	}
	for _, m := range ms {
		m.set(say451)
	}
	code, out := c.next("the words they all refuse")
	if code != refusedStatus || !strings.Contains(out, "refused this request (safety filter: 451)") {
		t.Fatalf("%d %s", code, out)
	}
	if r := lastRoute(s); len(r.Tries) != len(ms) {
		t.Fatalf("tries %+v", r.Tries)
	}
	for id, m := range ms {
		if want := map[bool]int{true: 2, false: 1}[id == "a"]; m.asked() != want || s.resting(id) {
			t.Fatalf("%s asked %d times, resting %v", id, m.asked(), s.resting(id))
		}
	}
}

// Strict still keeps the conversation through the account's own
// failures: its rate limit goes to the agent, and no other member is
// asked. And Settings' "Ask again after a refusal" set to never keeps a
// refusal where it was given.
func TestStrictConversationKeptPastItsOwnFailures(t *testing.T) {
	for _, tc := range []struct {
		name, say string
		retries   int
	}{
		{"rate limit", sayLimited, -1},
		{"refusal, never asked again", say451, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			if tc.retries >= 0 {
				refusalRetries(t, tc.retries)
			}
			ms := refusedGroup(t, provider.Chat, provider.Ordered, provider.AffinityStrict)
			s := New()
			c := &conversation{s: s, proto: provider.Chat}
			if code, out := c.next("hi"); code != http.StatusOK {
				t.Fatalf("first turn: %d %s", code, out)
			}
			kept := answerer(t, ms)
			ms[kept].set(tc.say)
			code, out := c.next("more")
			if code == http.StatusOK {
				t.Fatalf("%d %s", code, out)
			}
			for id, m := range ms {
				if id != kept && m.asked() != 0 {
					t.Fatalf("%s was asked: %+v", id, lastRoute(s).Tries)
				}
			}
		})
	}
}

// Another magpie's refusal, a 400 in its words with no code (refusalTold),
// is a refusal here too, with no Strict: the next member is asked and the
// member that passed it on doesn't rest. Before, it was a 400 about the
// request, and the group ended at it.
func TestAnotherMagpiesRefusalFailsOver(t *testing.T) {
	for _, proto := range []provider.Protocol{provider.Chat, provider.Anthropic} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", proto, stream), func(t *testing.T) {
				fresh(t)
				ms := refusedGroup(t, proto, provider.Ordered, "")
				ms["a"].set(sayMagpie)
				s := New()
				c := &conversation{s: s, proto: proto, stream: stream}
				code, out := c.next("the words it refuses")
				r := lastRoute(s)
				if code != http.StatusOK || len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil || ms["b"].asked() != 1 {
					t.Fatalf("%d %.300s\ntries %+v", code, out, r.Tries)
				}
				if s.resting("a") {
					t.Fatal("a refusal set the member aside")
				}
			})
		}
	}
}

package provider

// A Remote magpie whose computer came back at another address
// (luckymanjc on Discord): computer A's router hands it a new IP now and
// then, a boot often enough, and B's Remote magpie, saved at the old
// one, reached nothing until the user typed the new one in. So when B
// can't connect to it at all, B looks for it on the networks it shares
// with it: every address of the old one's /24 and of each of B's own
// private IPv4 networks, at the port A served on.
//
// Whatever takes the connection there may be anyone's: on a café's or a
// hotel's network, or with A simply off, any host listening on that port
// would have been handed the provider's key in clear and, answering a
// list, become the provider, given the user's prompts and files. So the
// key, and the provider's own headers, go to no address that hasn't
// proved first that it holds the key (proves): B sends a nonce of its
// own and the key's id (sha256 of the key, RemoteKeyID) to
// RemoteProvePath, which needs no key, and only a magpie with an enabled
// gateway key of that id answers the HMAC of the nonce and the address
// it was reached at (RemoteProof) under the key. Another magpie, an older
// one without the route, and anything else answer nothing B can check,
// and are passed over without a keyed request. An address that proved
// is then asked its model list with the key; one whose list this magpie
// made (its magpie_via ends with catalog.Self, remote_self.go) is this
// magpie and is passed over. The address found is saved as the
// provider's, as if the user had typed it, and the request or list that
// failed is asked there.
//
// An address given by name (a-pc.local, a Tailscale name) is the name's
// to follow, and a public address isn't looked around: neither is
// looked for. A look is made at most once a minute for each provider.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
)

// seekDial is how long each address is given to take the connection.
var seekDial = 500 * time.Millisecond

// seekAnswer is how long an address that took it is given to list.
var seekAnswer = 5 * time.Second

// seekFor is how long a list that failed waits for a look and the list
// asked again.
var seekFor = 20 * time.Second

// seekEvery is how soon after one look another is made for a provider.
var seekEvery = time.Minute

// seekWide is how many addresses are dialled at once.
const seekWide = 128

// seekNets is how many /24 networks one look covers at most.
const seekNets = 4

var seekMu sync.Mutex
var seekLast = map[string]time.Time{}

// seekAt is the addresses (host:port) a Remote magpie last at host:port
// may be at now, nearest first. Tests give their own. Whatever takes the
// connection at one is sent nothing keyed until it proves it holds the
// key (isRemote, proves).
var seekAt = lanNeighbours

// SeekHere has every look made at ats alone, as if any look made before
// were long ago, until restore: for the gateway's tests, whose Remote
// magpie is on loopback.
func SeekHere(ats []string) (restore func()) {
	seekMu.Lock()
	old := seekAt
	seekAt = func(string, string) []string { return ats }
	clear(seekLast)
	seekMu.Unlock()
	return func() {
		seekMu.Lock()
		seekAt = old
		clear(seekLast)
		seekMu.Unlock()
	}
}

// unconnected says err is a connection that was never made: what a
// computer that moved, or is off, leaves behind.
var unconnected = lazyre.New(`dial tcp |connectex|no route to host|host is down|network is unreachable`)

// SeekMoved is p, a Remote magpie a request found no one at (err), at
// the address it was found at on this computer's network, saved there:
// the request is made again at it. Requests failing at once wait for one
// look, and one is made at most every seekEvery.
func SeekMoved(ctx context.Context, p Provider, err error) (Provider, bool) {
	if !p.IsRemoteMagpie() || err == nil || !unconnected.MatchString(err.Error()) {
		return p, false
	}
	ctx, cancel := context.WithTimeout(ctx, seekFor)
	defer cancel()
	q := seekOnce(ctx, p.ID)
	if q == nil || strings.TrimSpace(q.Chat) == strings.TrimSpace(p.Chat) {
		return p, false
	}
	p.Chat, p.Responses, p.Anthropic, p.Decide = q.Chat, q.Responses, q.Anthropic, q.Decide
	return p, true
}

// a look under way, for those who wait for it
type seekRun struct {
	done  chan struct{}
	found *Provider
}

var seekRunning = map[string]*seekRun{}

// seekOnce is seekMoved, made once for those asking at once, and not
// again within seekEvery.
func seekOnce(ctx context.Context, id string) *Provider {
	seekMu.Lock()
	if r := seekRunning[id]; r != nil {
		seekMu.Unlock()
		select {
		case <-r.done:
			return r.found
		case <-ctx.Done():
			return nil
		}
	}
	if time.Since(seekLast[id]) < seekEvery {
		seekMu.Unlock()
		return nil
	}
	r := &seekRun{done: make(chan struct{})}
	seekRunning[id], seekLast[id] = r, time.Now()
	seekMu.Unlock()
	r.found, _ = seekMoved(ctx, id)
	seekMu.Lock()
	delete(seekRunning, id)
	seekMu.Unlock()
	close(r.done)
	return r.found
}

// seekMoved looks for provider id at another address, and is it as saved
// at the address it was found at, if it was.
func seekMoved(ctx context.Context, id string) (*Provider, bool) {
	p, err := Find(id)
	if err != nil || p.ID != id || !p.IsRemoteMagpie() {
		return nil, false
	}
	u, err := url.Parse(p.Chat)
	if err != nil || u.Port() == "" {
		return nil, false
	}
	if p.Key == "" {
		if on := p.KeysOn(); len(on) > 0 {
			p.Key = on[0].Key
		}
	}
	if p.Key == "" {
		return nil, false
	}
	seekMu.Lock()
	look := seekAt
	seekMu.Unlock()
	open := dialable(ctx, look(u.Hostname(), u.Port()))
	for _, at := range open {
		if ctx.Err() != nil {
			return nil, false
		}
		if !isRemote(ctx, *p, u.Scheme, at) {
			continue
		}
		q, err := Find(id)
		if err != nil || q.Chat != p.Chat {
			return nil, false // changed meanwhile: the user's word stands
		}
		was := q.Chat
		q.Chat, q.Responses, q.Anthropic, q.Decide = u.Scheme+"://"+at, "", "", ""
		if err := Save(*q); err != nil {
			log.Printf("%s: found at %s, not saved: %v", id, at, err)
			return nil, false
		}
		log.Printf("%s: %s didn't answer; found at %s, saved there", id, was, at)
		q, err = Find(id)
		return q, err == nil
	}
	return nil, false
}

// isRemote says the magpie at at is p's: it proves it holds p's key, then
// lists for it, and isn't this magpie. The key goes out only after the
// proof.
func isRemote(ctx context.Context, p Provider, scheme, at string) bool {
	if !proves(ctx, scheme, at, p.Key) {
		return false
	}
	p.Chat, p.Responses, p.Anthropic, p.Decide = scheme+"://"+at, "", "", ""
	p.remoteMagpieEndpoints()
	b, err := remoteBody(ctx, p, "GET", "/v1/models", seekAnswer)
	if err != nil {
		return false
	}
	var list struct {
		Data []struct {
			Via []string `json:"magpie_via"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &list) != nil {
		return false
	}
	for _, m := range list.Data {
		if len(m.Via) > 0 {
			// a test's own gateway serves its Remote magpie (LeaveOutSelf)
			return !LeaveOutSelf || m.Via[len(m.Via)-1] != catalog.Self()
		}
	}
	return true
}

// RemoteProvePath is where a magpie proves, with no key sent to it, that
// it holds a gateway key: POST {"nonce": hex, "key_id": RemoteKeyID(key)}
// is answered {"proof": hex of RemoteProof(key, nonce, the address it was
// reached at)}, or 404 when no enabled gateway key there has that id
// (the gateway's remote_prove.go).
const RemoteProvePath = "/v1/magpie/prove"

// RemoteKeyID is the id a proof names key by: its sha256, in hex.
func RemoteKeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// RemoteProof is what a magpie holding key, reached at at (host:port),
// answers nonce with: HMAC-SHA256 under key of the nonce and the address.
// The address is in it so that a host passing B's nonce on to the real
// magpie elsewhere gets a proof for that magpie's address, not its own.
func RemoteProof(key, nonce, at string) []byte {
	m := hmac.New(sha256.New, []byte(key))
	io.WriteString(m, "magpie-prove-v1\n"+nonce+"\n"+proofAt(at))
	return m.Sum(nil)
}

// proofAt is at with its IP written one way (an IPv4 address mapped into
// IPv6 as plain IPv4), as both sides of a proof write it.
func proofAt(at string) string {
	host, port, err := net.SplitHostPort(at)
	if err != nil {
		return at
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		host = ip.String()
	}
	return net.JoinHostPort(host, port)
}

// proveClient asks for a proof straight at the address dialled, as
// dialable dialled it (no proxy), and follows no redirect.
var proveClient = func() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}()

// proves says the magpie at at holds key: it answered a nonce of ours with
// the proof only key's holder, reached at at, can make. Neither key nor
// any header of the provider's own goes with the ask.
func proves(ctx context.Context, scheme, at, key string) bool {
	if key == "" {
		return false
	}
	n := make([]byte, 32)
	if _, err := rand.Read(n); err != nil {
		return false
	}
	nonce := hex.EncodeToString(n)
	body, _ := json.Marshal(map[string]string{"nonce": nonce, "key_id": RemoteKeyID(key)})
	ctx, cancel := context.WithTimeout(ctx, seekAnswer)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, scheme+"://"+at+RemoteProvePath, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := proveClient.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false
	}
	var out struct {
		Proof string `json:"proof"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4<<10)).Decode(&out) != nil {
		return false
	}
	got, err := hex.DecodeString(out.Proof)
	return err == nil && hmac.Equal(got, RemoteProof(key, nonce, at))
}

// dialable is the addresses of ats that take a connection, in ats' order.
func dialable(ctx context.Context, ats []string) []string {
	ok := make([]bool, len(ats))
	slots := make(chan struct{}, seekWide)
	var wg sync.WaitGroup
	d := net.Dialer{Timeout: seekDial}
	for i, at := range ats {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			if c, err := d.DialContext(ctx, "tcp", at); err == nil {
				c.Close()
				ok[i] = true
			}
		}()
	}
	wg.Wait()
	var out []string
	for i, at := range ats {
		if ok[i] {
			out = append(out, at)
		}
	}
	return out
}

// lanNeighbours is every address at port of host's /24 and of each of
// this computer's own private IPv4 networks' /24, this computer's own
// addresses and host itself left out. A host that is a name, or not a
// private IPv4 address, has none.
func lanNeighbours(host, port string) []string {
	old := net.ParseIP(host).To4()
	if old == nil || !old.IsPrivate() {
		return nil
	}
	own := map[string]bool{old.String(): true}
	nets := []net.IP{old.Mask(net.CIDRMask(24, 32))}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := n.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			own[ip.String()] = true
			base := ip.Mask(net.CIDRMask(24, 32))
			if !containsIP(nets, base) && len(nets) < seekNets {
				nets = append(nets, base)
			}
		}
	}
	var out []string
	for _, base := range nets {
		for i := 1; i < 255; i++ {
			ip := net.IPv4(base[0], base[1], base[2], byte(i)).String()
			if !own[ip] {
				out = append(out, net.JoinHostPort(ip, port))
			}
		}
	}
	return out
}

func containsIP(ips []net.IP, ip net.IP) bool {
	for _, x := range ips {
		if x.Equal(ip) {
			return true
		}
	}
	return false
}

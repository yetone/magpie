package netproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A Remote magpie reached by more than one way to the same computer
// (evjohnicu on X: the home network's address, Tailscale's and
// EasyTier's): WithFailover names its addresses, the one in the base URL
// first. A request to one of them goes first to the one that last
// answered, and when that one takes no connection (a dial that failed: a
// network this computer isn't on, a name that doesn't resolve, a refused
// port) to the next, in the order given. Only a connection never made
// moves a request on: an answer from the remote, a refusal or an error
// among them, is the remote's own and is returned as it is, and the
// address that gave it is the one kept. The URL's path below the address,
// its query and the request itself go as they were; only where it is
// sent changes, so whatever is asked of the remote (a turn, its model
// list, its quotas) follows the address in use.

type failoverKey struct{}

type vouchKey struct{}

// Vouch says whether root, one of a failover's addresses, may be sent a
// request made on its remote's behalf, which carries its key: an
// address on a network this computer may not be on now (a café's, where
// whatever answers at the home network's address is anyone's) has to
// prove it is the remote first. ask sends a request the way the request
// itself would go (its proxy, its dial), and nothing keyed goes with it.
// at is the host:port to connect to in root's place ("" for root's own:
// a name is connected to at the address that proved), ok false passes
// root over as an address that took no connection.
type Vouch func(ctx context.Context, root *url.URL, ask func(*http.Request) (*http.Response, error)) (at string, ok bool)

// WithVouch is ctx whose failover (WithFailover) sends a request to an
// address only once v vouches for it.
func WithVouch(ctx context.Context, v Vouch) context.Context {
	if v == nil {
		return ctx
	}
	return context.WithValue(ctx, vouchKey{}, v)
}

// errUnvouched is a failover's address passed over for not proving it is
// its remote.
type errUnvouched struct{ root string }

func (e errUnvouched) Error() string {
	return e.root + " did not prove it is this Remote magpie (it holds no gateway key of this provider's), so it was sent nothing"
}

type failover struct {
	roots []*url.URL
	key   string
}

// WithFailover is ctx for requests to roots, the addresses of one remote,
// the first the one its URLs are made with. Fewer than two leaves ctx as
// it is.
func WithFailover(ctx context.Context, roots ...string) context.Context {
	f := failover{}
	for _, r := range roots {
		u, err := url.Parse(strings.TrimRight(strings.TrimSpace(r), "/"))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		u.RawQuery, u.Fragment, u.RawPath = "", "", ""
		f.roots = append(f.roots, u)
	}
	if len(f.roots) < 2 {
		return ctx
	}
	parts := make([]string, len(f.roots))
	for i, u := range f.roots {
		parts[i] = u.String()
	}
	f.key = strings.Join(parts, "\n")
	return context.WithValue(ctx, failoverKey{}, f)
}

// failoverTry is how long a connection to an address that isn't the last
// left is waited for before the next is asked.
var failoverTry = 5 * time.Second

// lastAnswered keeps, for each set of addresses (failover.key), the index
// of the one that last took a connection.
var lastAnswered = struct {
	sync.Mutex
	m map[string]int
}{m: map[string]int{}}

// under says which of f's roots u is at, and the path below it.
func (f failover) under(u *url.URL) (int, string, bool) {
	for i, r := range f.roots {
		if !strings.EqualFold(u.Scheme, r.Scheme) || rootKey(u) != rootKey(r) {
			continue
		}
		if r.Path == "" {
			return i, u.Path, true
		}
		if rest, ok := strings.CutPrefix(u.Path, r.Path); ok && (rest == "" || rest[0] == '/') {
			return i, rest, true
		}
	}
	return 0, "", false
}

// rootKey is u's host and port, the scheme's port when none is written,
// so http://a and http://a:80 are one address.
func rootKey(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// order is the roots to try: the one that last answered, then the rest
// in the order given.
func (f failover) order() []int {
	lastAnswered.Lock()
	first := lastAnswered.m[f.key]
	lastAnswered.Unlock()
	out := []int{first}
	for i := range f.roots {
		if i != first {
			out = append(out, i)
		}
	}
	return out
}

func (f failover) answered(i int) {
	lastAnswered.Lock()
	defer lastAnswered.Unlock()
	if len(lastAnswered.m) >= 256 {
		clear(lastAnswered.m)
	}
	if i == 0 {
		delete(lastAnswered.m, f.key)
		return
	}
	lastAnswered.m[f.key] = i
}

type tryCapKey struct{}

// tryCap is how long a dial made under ctx is given (failoverTry), 0 for
// as long as it would be given otherwise.
func tryCap(ctx context.Context) time.Duration {
	d, _ := ctx.Value(tryCapKey{}).(time.Duration)
	return d
}

// neverConnected says err is a connection that was never made: the dial
// failed, so nothing of the request reached anyone.
func neverConnected(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// failingOver sends req through send, at each of its remote's addresses
// in turn while none takes the connection (WithFailover); ok false when
// req is for no such remote.
func failingOver(req *http.Request, send func(*http.Request) (*http.Response, error)) (*http.Response, bool, error) {
	f, has := req.Context().Value(failoverKey{}).(failover)
	if !has || req.URL == nil {
		return nil, false, nil
	}
	at, rest, under := f.under(req.URL)
	if !under {
		return nil, false, nil
	}
	// a body read once can't be sent again: one try, where it was sent
	replay := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
	order := f.order()
	if !replay {
		order = []int{at}
	}
	vouch, _ := req.Context().Value(vouchKey{}).(Vouch)
	var res *http.Response
	var err error
	for n, i := range order {
		if n > 0 && req.Context().Err() != nil {
			break
		}
		r := req
		if n > 0 || i != at {
			ctx := req.Context()
			if n < len(order)-1 {
				ctx = context.WithValue(ctx, tryCapKey{}, failoverTry)
			}
			r = req.Clone(ctx)
			if n > 0 && req.GetBody != nil {
				b, berr := req.GetBody()
				if berr != nil {
					return nil, true, berr
				}
				r.Body = b
			}
			if i != at {
				to := *f.roots[i]
				u := *req.URL
				u.Scheme, u.Host, u.User = to.Scheme, to.Host, to.User
				u.Path, u.RawPath = to.Path+rest, ""
				r.URL, r.Host = &u, ""
			}
		} else if len(order) > 1 {
			r = req.WithContext(context.WithValue(req.Context(), tryCapKey{}, failoverTry))
		}
		if vouch != nil {
			at, ok := vouch(r.Context(), f.roots[i], send)
			if !ok {
				res, err = nil, errUnvouched{f.roots[i].Redacted()}
				continue
			}
			if at != "" && at != r.URL.Host {
				// sent to the address that proved, under its name
				host := r.Host
				if host == "" {
					host = r.URL.Host
				}
				u := *r.URL
				u.Host = at
				r = r.WithContext(r.Context())
				r.URL, r.Host = &u, host
			}
		}
		res, err = send(r)
		if err == nil {
			f.answered(i)
			return res, true, nil
		}
		if !neverConnected(err) {
			return res, true, err
		}
	}
	return res, true, err
}

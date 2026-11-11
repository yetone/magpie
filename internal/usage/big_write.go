package usage

import "time"

// BigWrite is the request that wrote the most to the cache among a page's
// counted rows that the gateway served (RouteID is the request the Routing
// page keeps; a call read from an agent's own session file has none, so
// there is no request to jump to). A page's totals hide one request
// that rewrote a long conversation behind many that only read it
// (recih on X, #6199).
type BigWrite struct {
	N       int       `json:"n,omitempty"`
	RouteID int64     `json:"route,omitempty"`
	At      time.Time `json:"at,omitzero"`
	Model   string    `json:"model,omitempty"`
}

// add counts r, a counted row the filter keeps.
func (b *BigWrite) add(r *Row) {
	if r.RouteID == 0 || r.CacheWrite <= 0 {
		return
	}
	if r.CacheWrite > b.N || r.CacheWrite == b.N && r.Time.After(b.At) {
		*b = BigWrite{N: r.CacheWrite, RouteID: r.RouteID, At: r.Time, Model: r.Model}
	}
}

// merge keeps the larger of b and o.
func (b *BigWrite) merge(o BigWrite) {
	if o.N > b.N || o.N == b.N && o.N > 0 && o.At.After(b.At) {
		*b = o
	}
}

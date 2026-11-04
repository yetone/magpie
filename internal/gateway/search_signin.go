package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A plugin's search API may sign in as the same plugin's provider
// (provider.SearchVendor.Auth) rather than take a key: a search is then
// one of that provider's accounts, those on in the order its requests take
// them (accountsOn), each asked as a key is (askSearchAPIKeys). An account
// the API refuses (429, out of credit, turned away) rests for search alone
// (restSearchKey, by the account's id, never its token): the provider's
// own rests, which its models' requests read, are never touched, so a
// search's rate limit leaves the account's models as they were.

// runPluginSearchAs asks a plugin's search API as one account in the host
// (a test's has no host).
var runPluginSearchAs = plugin.RunSearchAs

// searchAccountID is how an account is known among a search API's rests:
// beside its keys, by its key in the plugin host's sign-ins.
func searchAccountID(p provider.Provider) string { return "account " + p.PluginAccountKey() }

func (s *Server) askSearchSignedIn(ctx context.Context, a provider.SearchAPI, v provider.SearchVendor, query string) ([]foundPage, error) {
	name, p := provider.SearchSignIn(v)
	var cs []candidate
	if p != nil {
		cs = accountsOn(*p, "")
	}
	if len(cs) == 0 {
		err := fmt.Errorf("not signed in to %s: sign in to it on Providers", name)
		if p != nil {
			err = fmt.Errorf("no account of %s is on", name)
		}
		logSearch(ctx, usage.Record{Requested: a.Name()}, query, time.Now(), 0, err)
		return nil, err
	}
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = searchAccountID(c.p)
	}
	// the accounts in the provider's own order, one refused lately last
	inOrder := provider.SearchAPI{Vendor: a.Vendor, Pick: provider.SearchPickOrder}
	var errs []error
	for _, i := range searchKeyOrder(inOrder, ids, time.Now()) {
		q := cs[i].p
		ctx, cancel := context.WithTimeout(q.Via(ctx), searchTimeout/4)
		start := time.Now()
		pages, err := s.askPluginSearchAs(ctx, v, q, query)
		cancel()
		rec := usage.Record{Requested: a.Name(), ProviderKeyID: searchKeyID(a.Vendor, ids[i]), ProviderKeyName: q.Account.User}
		logSearch(ctx, rec, query, start, len(pages), err)
		if err == nil {
			wakeSearchKey(a.Vendor, ids[i])
			return pages, nil
		}
		var r searchRefused
		refused := errors.As(err, &r)
		if refused {
			d, why := searchRestFor(a.Vendor, r.status, r.retryAfter, time.Now())
			restSearchKey(a.Vendor, ids[i], d, why, time.Now())
		}
		if len(cs) > 1 {
			err = fmt.Errorf("account %s: %w", q.Account.User, err)
		}
		errs = append(errs, err)
		// signed out since it was listed: the next account may still be in
		if !refused && !strings.Contains(err.Error(), "not signed in") {
			break
		}
	}
	return nil, errors.Join(errs...)
}

// SearchAccountsOn is how many of p's accounts a search that signs in as
// it may take (accountsOn), for the GUI's row.
func SearchAccountsOn(p provider.Provider) int { return len(accountsOn(p, "")) }

// askPluginSearchAs is askPluginSearch as one account of the provider the
// API signs in to.
func (s *Server) askPluginSearchAs(ctx context.Context, v provider.SearchVendor, p provider.Provider, query string) ([]foundPage, error) {
	got, err := runPluginSearchAs(ctx, v.Plugin, v.PluginID, query, searchHits, p.PluginAccountKey())
	if err != nil {
		var r plugin.SearchRefused
		if errors.As(err, &r) {
			if keyRefusal(r.Status) {
				return nil, searchRefused{r.Status, r.Msg, r.RetryAfter}
			}
			return nil, errors.New(r.Msg)
		}
		if ctx.Err() != nil {
			return nil, errors.New("no answer in time")
		}
		return nil, err
	}
	return pluginPages(got), nil
}

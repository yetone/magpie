package gateway

// An Azure subscription with a spending limit — a student's, a free
// trial's, a Visual Studio subscriber's monthly credit — is disabled by
// Azure when its credit runs out, and enabled again on its own when the
// credit renews. Meanwhile every request to the resource, with the key that
// worked an hour ago, answers 401 "Access denied due to invalid subscription
// key or wrong API endpoint" (canlee886 on X): the same words a wrong key
// gets. A 401 on a key that answered requests before is read as that
// subscription suspended, not as a bad key: the key sits out until it
// answers again (failSuspended), and the gateway asks its deployments list
// in the background meanwhile (rest_probe.go), so it is back within minutes
// of Azure enabling it, with nobody turning the provider off and on.
//
// What tells the two apart is the key's own past: provider.LastServed has
// when it last answered a request through the gateway (served.json, kept
// 30 days). A key that never answered — new, mistyped, or edited since
// (its KeyID is another then) — fails as before: a 401 shown as the key
// refused, on the short backoff, never asked in the background. A file that
// can't be read is no answer on record, the same. A key already resting
// suspended stays read so when its rest ends and it is refused again, after
// its last answer has left the file.
//
// Azure's alone: a 401 says nothing of why on any vendor, and a vendor
// whose model list answers without the key (OpenRouter's is public) would
// have a probe lift a key it still refuses. Azure's data plane checks the
// key on its deployments list as on its chat completions.

import (
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// failSuspended: an Azure key that answered before is refused 401, as a
// subscription Azure disabled is (its credit used up). It rests
// suspendedRest, asked again in the background, and is back as soon as its
// list answers.
const failSuspended = "suspended"

// suspendedRest is how long a suspended key sits out with no answer from
// its probe: a day, after which a request asks it once more. The probe
// lifts it as soon as the vendor answers, so this bounds only a key the
// probe can't speak for (a list that answered while requests didn't,
// probeMissed).
const suspendedRest = 24 * time.Hour

// lastServedFor is provider.LastServed; swapped in tests.
var lastServedFor = provider.LastServed

// azureSuspended says c's 401 is a suspended subscription's: c is an Azure
// key (no account), the reply is a 401, and the key answered a request
// before, or is resting suspended already.
func azureSuspended(c candidate, status int) bool {
	if status != http.StatusUnauthorized || c.p.Account != nil || !c.p.IsAzure() {
		return false
	}
	restingUntil.Lock()
	was := restingUntil.note[c.restKey()].Why == failSuspended
	restingUntil.Unlock()
	if was {
		return true
	}
	return !lastServedFor()[provider.ServedID(c.p)].IsZero()
}

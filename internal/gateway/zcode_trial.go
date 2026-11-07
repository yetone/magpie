package gateway

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// zcodeEndedWords and zcodeSpentWords are the ZCode plugin's own answers
// for a Start Plan trial entry (<model>-Trial) it can't serve, given before
// anything is sent: a 400 "ZCode has no active limited quota for
// GLM-5.3-Trial" when no live plan holds the model any more (the trial
// ended), and a 429
// "ZCode's limited quota for glm-5.3 is exhausted or temporarily
// unavailable" when its bucket is spent. The plugin's words, after any
// "<provider>: " put in front of them.
var (
	zcodeEndedWords = regexp.MustCompile(`(?:^|: )ZCode has no active limited quota for (\S+)$`)
	zcodeSpentWords = regexp.MustCompile(`(?:^|: )ZCode's limited quota for (\S+) is exhausted or temporarily unavailable$`)
)

// zcodeTrialRefused says c, a ZCode plugin account asked for one of its
// Start Plan trial entries, was refused for that entry's own quota
// (zcodeEndedWords, zcodeSpentWords), naming the model asked for. The
// trial is that model's: the account's GLM Coding Plan still serves its
// own models, so only the trial entry rests (#3661, orion-nebula-dev: two
// expired trials rested "zcode@zcode", and GLM-5.3 and GLM-5.3-Flash with
// it). Any other
// refusal of a ZCode account, an account-wide 429 or 1113 among them,
// rests the account as before.
func zcodeTrialRefused(c candidate, status int, body []byte) bool {
	if c.p.PluginProvider() != "zcode" || !strings.HasSuffix(strings.ToLower(c.model), "-trial") {
		return false
	}
	said := strings.TrimSpace(vendorMessage(body))
	switch status {
	case http.StatusBadRequest:
		m := zcodeEndedWords.FindStringSubmatch(said)
		return m != nil && strings.EqualFold(m[1], c.model)
	case http.StatusTooManyRequests:
		// the plugin names the bucket's own model, the entry less -Trial
		m := zcodeSpentWords.FindStringSubmatch(said)
		return m != nil && strings.EqualFold(m[1]+"-Trial", c.model)
	}
	return false
}

// zcodeTrialBlocked says c, a ZCode plugin account asked for one of its
// Start Plan trial entries, got zcode.z.ai's edge firewall's block page
// (Alibaba Cloud's 405, provider.EdgeBlocked, as the plugin's startBlocked
// words it). A trial entry is sent to zcode.z.ai alone; a dual account's
// plain models go to its GLM Coding Plan at api.z.ai, which answered while
// zcode.z.ai blocked the address (plugins#545, jinpy666). So the block
// rests the trial entry, and the account's plain model is still asked: on
// a gift-only account it goes to zcode.z.ai too, is blocked the same, and
// that rests the account (windy_66454 on Discord, 10-31: glm-trial's two
// members, GLM-5.3-Flash and GLM-5.3-Flash-Trial, shared one account rest
// and the plain one was never asked). ZCode's risk control blocking the
// account (code 3012, failRestricted) is read before, and rests as
// zcodeTrialRestricted says.
func zcodeTrialBlocked(c candidate, status int, body []byte) bool {
	if status < 400 || c.p.PluginProvider() != "zcode" || !strings.HasSuffix(strings.ToLower(c.model), "-trial") {
		return false
	}
	return provider.EdgeBlocked(wordsOf(body)) && !restrictedSaid(status, wordsOf(body))
}

// zcodeTrialRestricted says c, a ZCode plugin account asked for one of its
// Start Plan trial entries, was refused by ZCode's risk control (code
// 3012, failRestricted). That is zcode.z.ai's answer, and the entry alone
// rests: a dual account's plain models go to its GLM Coding Plan on
// api.z.ai, which kept serving them (young02036 on Discord, 11-07:
// GLM-5.3-Flash-Trial answered 3012 while the account's GLM-5.3 worked,
// and the account rested whole for two hours). On a gift-only account the
// plain model goes to zcode.z.ai too, gets its own 3012, and that rests
// the account, as zcodeTrialBlocked's block page does.
func zcodeTrialRestricted(c candidate, why string) bool {
	return why == failRestricted && c.p.PluginProvider() == "zcode" && strings.HasSuffix(strings.ToLower(c.model), "-trial")
}

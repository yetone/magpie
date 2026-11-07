package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The web search APIs, from the terminal: magpie search (#419).

const searchUsage = `usage:
  magpie search                              the search APIs a model's web search goes to
  magpie search add <api> <key> [url=<address>] [pick=turns|order]
                                             add one, or give one a new key
                                             (several keys as k1,k2: searches take them in turn,
                                             or with pick=order the first until it is refused)
  magpie search add searxng url=<address> [<key>]
  magpie search add you [<key>]              You.com: free with no key (100 searches a day)
  magpie search off <api>                    keep one, key and all, but never ask it
  magpie search on <api>                     ask it again
  magpie search move <api> <n>               put one at place n in the order they are asked
  magpie search mix <api>,<api>[,…]          ask these together, at once, and give the model their
                                             pages merged, a page found twice once (at most 12)
  magpie search mix off                      ask them one at a time again, in order
  magpie search rm <api>                     take one away
  magpie search count [<n>]                  how many results a search is answered with when
                                             the client names no count (Claude Code's WebSearch,
                                             Codex's web_search): 1 to 20, 6 by default;
                                             "magpie search count default" puts 6 back

  apis: tavily, brave, exa, firecrawl, you, deepseek, volcengine, wsa, searxng
        and the ones a plugin gives (its search hook), as <plugin>:<id> in magpie's Settings
        › Models › Web search; a plugin that is off gives none

  url= is the API's base URL, for an endpoint of your own that answers as that API
  does (e.g. url=https://search.example.com/tavily). Without it the vendor's own is
  used; a new key alone keeps the url set before, and url= with nothing after it puts
  the vendor's own back. magpie puts the API's path after it (/search for Tavily), so
  a url given with that path at its end is taken as its base. The key is sent only
  to that url.

  With several keys, a key the API refused lately (turned away, out of credit, rate
  limited) rests and is asked only after the others: a rate limit for as long as the API
  says (30s to 10m), a key turned away for 10 minutes, one out of credit for an hour.
  For Tavily, magpie search shows what each key has used of its limit (GET /usage).

  A client's web search (Claude Code's WebSearch, Codex's web_search) is done by the
  model's provider when it can search, then by a provider that can (a Claude or ChatGPT
  account, OpenAI, Anthropic, OpenRouter…), and then by these, in their order (the order
  they were added, until one is moved), skipping one switched off: what they find goes
  to the model as it is, and no model is asked to read it.
  Without any of them, the client's search is left out.

  Each result carries up to 1500 bytes of its page's text, so twice the results is
  about twice as much for the model to read. Whether an API charges more for more
  results is its own pricing's.

  Two or more mixed (magpie search mix) are asked first, together: a search then spends
  each one's quota, takes as long as the slowest, and gives the model up to twice the
  results (20 at most). The others are asked, in order, only when none of the mixed ones finds anything.

  e.g. magpie search add tavily tvly-…
       magpie search add searxng url=https://searx.example.com
       magpie search add you`

func searchCmd(args []string) error {
	if len(args) == 0 {
		as := provider.StoredSearchAPIs()
		if len(as) == 0 {
			fmt.Println(muted.Render("  no search API · magpie search add <tavily|brave|exa|firecrawl|you|deepseek|volcengine|wsa|searxng> <key>"))
			return nil
		}
		for i, a := range as {
			what := a.MaskedKey()
			if a.Key == "" {
				what = "no key"
			}
			// keys switched off in Settings stay in its list, never asked
			if off := len(a.SearchKeys()) - len(a.Keys()); off > 0 {
				what += fmt.Sprintf("  +%d off", off)
			}
			if a.URL != "" {
				what += "  " + a.URL
			}
			if len(a.Keys()) > 1 {
				what += map[bool]string{true: "  in order", false: "  in turn"}[a.Pick == provider.SearchPickOrder]
			}
			line := fmt.Sprintf("  %d. %s  %s", i+1, pad(a.Name(), 14), muted.Render(what))
			if !a.Ready() {
				line += "  " + faint.Render("needs its key")
			}
			if a.Off {
				line += "  " + amber.Render("off")
			}
			if a.Mix {
				line += "  " + green.Render("mix")
			}
			fmt.Println(line)
			if gateway.CanReadSearchQuota(a) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				for _, q := range gateway.SearchQuotas(ctx, a, true) {
					fmt.Println("     " + pad(q.Key, 14) + " " + quotaLine(q))
				}
				cancel()
			}
		}
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Println(searchUsage)
		return nil
	case "add", "set":
		if len(args) < 2 {
			return fmt.Errorf("magpie search add <api> <key> [url=<address>]")
		}
		a := provider.SearchAPI{Vendor: strings.ToLower(args[1])}
		// a new key alone keeps the address set before (#1580); url= with
		// nothing after it puts the vendor's own back
		urlGiven := false
		for _, v := range args[2:] {
			if u, ok := strings.CutPrefix(v, "url="); ok {
				a.URL, urlGiven = u, true
			} else if p, ok := strings.CutPrefix(v, "pick="); ok {
				if a.Pick = p; p == "" {
					a.Pick = "turns"
				}
			} else if a.Key == "" {
				a.Key = v
			} else {
				return fmt.Errorf("magpie search add <api> <key> [url=<address>] [pick=turns|order]")
			}
		}
		if i := slices.IndexFunc(provider.StoredSearchAPIs(), func(x provider.SearchAPI) bool { return x.Vendor == a.Vendor }); i >= 0 && !urlGiven {
			a.URL = provider.StoredSearchAPIs()[i].URL
		}
		if err := provider.SetSearchAPI(a); err != nil {
			return err
		}
		as := provider.StoredSearchAPIs()
		i := slices.IndexFunc(as, func(x provider.SearchAPI) bool { return x.Vendor == a.Vendor })
		fmt.Println(green.Render("✓"), as[i].Name(), muted.Render(fmt.Sprintf("search API %d of %d", i+1, len(as))))
		return nil
	case "on", "off":
		if len(args) != 2 {
			return fmt.Errorf("magpie search %s <api>", args[0])
		}
		if err := provider.SwitchSearchAPI(strings.ToLower(args[1]), args[0] == "on"); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), strings.ToLower(args[1]), args[0])
		return nil
	case "move", "mv":
		n := 0
		if len(args) == 3 {
			fmt.Sscan(args[2], &n)
		}
		as := provider.StoredSearchAPIs()
		i := slices.IndexFunc(as, func(x provider.SearchAPI) bool { return len(args) > 1 && x.Vendor == strings.ToLower(args[1]) })
		if len(args) != 3 || n < 1 || n > len(as) {
			return fmt.Errorf("magpie search move <api> <n>, n from 1 to %d", len(as))
		}
		if i < 0 {
			return fmt.Errorf("no %s search API is set up", strings.ToLower(args[1]))
		}
		order := []string{}
		for _, a := range as {
			order = append(order, a.Vendor)
		}
		v := order[i]
		order = slices.Insert(slices.Delete(order, i, i+1), n-1, v)
		if err := provider.ArrangeSearchAPIs(order); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), as[i].Name(), muted.Render(fmt.Sprintf("search API %d of %d", n, len(as))))
		return nil
	case "count":
		return searchCountCmd(args[1:])
	case "mix":
		if len(args) != 2 {
			return fmt.Errorf("magpie search mix <api>,<api>[,…] | off")
		}
		var vendors []string
		if strings.ToLower(args[1]) != "off" {
			for _, v := range strings.Split(args[1], ",") {
				if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
					vendors = append(vendors, v)
				}
			}
			if len(vendors) < 2 {
				return fmt.Errorf("magpie search mix <api>,<api>[,…]: name two or more")
			}
		}
		if err := provider.MixSearchAPIs(vendors); err != nil {
			return err
		}
		if len(vendors) == 0 {
			fmt.Println(green.Render("✓"), "search APIs asked one at a time, in order")
		} else {
			fmt.Println(green.Render("✓"), strings.Join(vendors, ", "), muted.Render("asked together"))
		}
		return nil
	case "rm", "remove":
		if len(args) != 2 {
			return fmt.Errorf("magpie search rm <api>")
		}
		if err := provider.RemoveSearchAPI(strings.ToLower(args[1])); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), "removed")
		return nil
	}
	return fmt.Errorf("magpie search [add|on|off|move|mix|rm|count] (magpie search help)")
}

// searchCountCmd is magpie search count (#5545, mdwsk88): with no word it
// says the count, with a number from 1 to 20 (or "default") it sets it; any
// other word is refused and nothing is written.
func searchCountCmd(args []string) error {
	usage := fmt.Errorf("magpie search count [<n>], n from 1 to %d, or default for %d", settings.SearchCountMost, settings.SearchCountDefault)
	s := settings.Load()
	switch len(args) {
	case 0:
		n := s.SearchPages()
		what := "results a search is answered with"
		if s.SearchCount == 0 {
			what += " (default)"
		}
		fmt.Println(" ", n, muted.Render(what))
		return nil
	case 1:
	default:
		return usage
	}
	n := 0
	if args[0] != "default" {
		var err error
		if n, err = strconv.Atoi(args[0]); err != nil || n < 1 || n > settings.SearchCountMost {
			return usage
		}
	}
	if s.SearchCount != n {
		s.SearchCount = n
		if err := settings.Save(s); err != nil {
			return err
		}
	}
	fmt.Println(green.Render("✓"), s.SearchPages(), muted.Render("results a search is answered with"))
	return nil
}

// quotaLine is what one key has used of its limit, as magpie search lists
// it: unknown, with why, when it couldn't be read, and never 0.
func quotaLine(q gateway.SearchKeyQuota) string {
	var line string
	switch {
	case !q.Known:
		line = amber.Render("unknown") + muted.Render(" · "+q.Err)
	case q.Unlimited:
		line = fmt.Sprintf("%s used · no limit", thousands(q.Used))
	default:
		line = fmt.Sprintf("%s / %s used", thousands(q.Used), thousands(q.Limit))
		if q.Of == "plan" && q.Plan != "" {
			line += muted.Render(" (" + q.Plan + " plan)")
		} else if q.Of == "plan" {
			line += muted.Render(" (plan)")
		}
		if q.PaygoLeft > 0 {
			line += muted.Render(fmt.Sprintf(" · %s pay-as-you-go left", thousands(q.PaygoLeft)))
		}
		if q.UsedUp {
			line += " " + amber.Render("used up")
		}
	}
	if q.Resting != "" {
		line += faint.Render(fmt.Sprintf(" · %s, asked last until %s", q.Resting, q.RestUntil.Local().Format("15:04")))
	}
	return line
}

// thousands is n with its thousands split by commas.
func thousands(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

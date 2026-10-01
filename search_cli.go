package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// The web search APIs, from the terminal: magpie search (#419).

const searchUsage = `usage:
  magpie search                              the search APIs a model's web search goes to
  magpie search add <api> <key> [url=<address>]   add one, or give one a new key
  magpie search add searxng url=<address> [<key>]
  magpie search rm <api>                     take one away

  apis: tavily, brave, exa, firecrawl, searxng

  A client's web search (Claude Code's WebSearch, Codex's web_search) is done by the
  model's provider when it can search, then by a provider that can (a Claude or ChatGPT
  account, OpenAI, Anthropic, OpenRouter…), and then by these, in the order they were
  added: what they find goes to the model as it is, and no model is asked to read it.
  Without any of them, the client's search is left out.

  e.g. magpie search add tavily tvly-…
       magpie search add searxng url=https://searx.example.com`

func searchCmd(args []string) error {
	if len(args) == 0 {
		as := provider.StoredSearchAPIs()
		if len(as) == 0 {
			fmt.Println(muted.Render("  no search API · magpie search add <tavily|brave|exa|firecrawl|searxng> <key>"))
			return nil
		}
		for i, a := range as {
			what := provider.Mask(a.Key)
			if a.Key == "" {
				what = "no key"
			}
			if a.URL != "" {
				what += "  " + a.URL
			}
			line := fmt.Sprintf("  %d. %s  %s", i+1, pad(a.Name(), 14), muted.Render(what))
			if !a.Ready() {
				line += "  " + faint.Render("needs its key")
			}
			fmt.Println(line)
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
		for _, v := range args[2:] {
			if u, ok := strings.CutPrefix(v, "url="); ok {
				a.URL = u
			} else if a.Key == "" {
				a.Key = v
			} else {
				return fmt.Errorf("magpie search add <api> <key> [url=<address>]")
			}
		}
		if err := provider.SetSearchAPI(a); err != nil {
			return err
		}
		as := provider.StoredSearchAPIs()
		i := slices.IndexFunc(as, func(x provider.SearchAPI) bool { return x.Vendor == a.Vendor })
		fmt.Println(green.Render("✓"), as[i].Name(), muted.Render(fmt.Sprintf("search API %d of %d", i+1, len(as))))
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
	return fmt.Errorf("magpie search [add|rm] (magpie search help)")
}

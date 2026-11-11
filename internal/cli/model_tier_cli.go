package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// magpie model tier: a model's own speed (kdajenks on X).

const tierUsage = `usage:
  magpie model tier <provider/model>              the speed it is sent at
  magpie model tier <provider/model> fast         send every request for it in its vendor's fast mode
  magpie model tier <provider/model> ultrafast    send it in the faster Ultrafast mode where an account offers it
                                                  (priority where not)
  magpie model tier <provider/model> usual        the usual speed again (also --reset)

An agent's own fast pick (the bolt in its model list) goes first, and a routing group
sends its members as it says.`

func modelTierCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", tierUsage)
	}
	if slices.ContainsFunc(args, isHelp) {
		fmt.Println(tierUsage)
		return nil
	}
	p, model, err := modelRef(args[0])
	if err != nil {
		return err
	}
	if len(args) == 1 {
		fmt.Printf("%s/%s: %s\n", p.ID, model, tierWord(provider.ModelTier(p.ID, model)))
		return nil
	}
	val := strings.TrimSpace(args[1])
	if isReset([]string{val}) {
		val = ""
	}
	if err := provider.SetModelTier(p.ID+"/"+model, val); err != nil {
		return err
	}
	fmt.Printf("%s/%s: %s\n", p.ID, model, tierWord(settings.Load().ModelTiers[p.ID+"/"+model]))
	return nil
}

func tierWord(t string) string {
	switch t {
	case "priority":
		return "fast"
	case "ultrafast":
		return "ultrafast"
	}
	return "usual"
}

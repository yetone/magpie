package cli

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const keyBalanceTokenUsage = "magpie provider key-balance-token <id> <key> <token|-> [user-id] | <id> <key> off"

// keyBalanceTokenCmd gives one of a provider's API keys the access token
// and user id of its own account, for its balance where the provider's
// balance token is another account's (Kylin_Cloud on X: keys of two
// New API accounts in one provider), or with off takes them away.
func keyBalanceTokenCmd(rest []string) error {
	if len(rest) < 3 || len(rest) > 4 {
		return fmt.Errorf("%s", keyBalanceTokenUsage)
	}
	// a flag (--help) is never taken for a name, a token or an id
	for _, a := range rest {
		if strings.HasPrefix(a, "--") {
			return fmt.Errorf("%s", keyBalanceTokenUsage)
		}
	}
	id, key, token := rest[0], rest[1], rest[2]
	if strings.EqualFold(token, "off") {
		if len(rest) != 3 {
			return fmt.Errorf("%s", keyBalanceTokenUsage)
		}
		if err := provider.SetKeyBalanceAuth(id, key, "", "", true); err != nil {
			return err
		}
		fmt.Println(muted.Render("the key's balance is asked as the provider says"))
		return nil
	}
	if token == "-" {
		token = ""
	}
	user := ""
	if len(rest) == 4 {
		user = rest[3]
	}
	if err := provider.SetKeyBalanceAuth(id, key, token, user, false); err != nil {
		return err
	}
	fmt.Println(muted.Render("the key's balance is asked with its own account's token"))
	return nil
}

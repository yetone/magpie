package redact

import (
	"strings"
	"testing"
)

// secretOut masks s with the local model taking v for a secret, as text
// and as a tool call's arguments, and says where v went out as written.
// Each starts with nothing masked before, so a value masked in one case
// is not known in the next.
func secretOut(t *testing.T, v, s string) (text, args bool) {
	t.Helper()
	f := &fakeModel{values: map[string]string{v: "secret"}}
	Forget()
	out, _ := Mask(s, Options{Secrets: true, Model: f.find})
	q := strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`)
	body := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"` + q + `","env":{"x":"` + q + `"}}}]}]}`
	Forget()
	j, _ := MaskJSON([]byte(body), Options{Secrets: true, Model: f.find})
	Forget()
	return strings.Contains(out, v), strings.Contains(string(j), v)
}

// The local model takes a name in code for a secret: a host's or a
// route's name, a class's or a product's, an identifier, a user agent's
// version, a field read through a class (StringKe, #7973). Each went as a
// placeholder, and the agent read one where a name was. Each goes as
// written now, in a message's text and in a tool call's arguments.
func TestCodeWordsAreNoSecret(t *testing.T) {
	for _, v := range []string{
		"edge-san-jose-01",               // a route's host
		"edge-singapore-01",              // another
		"Illuminate",                     // a class's
		"Speechmatics",                   // a product's
		"JazzCash",                       // another
		"dev_worker5",                    // an identifier
		"Mozilla/5.0",                    // a user agent's version
		"PaymentClient.merchant_api_key", // a field read through a class
	} {
		for _, s := range []string{"see " + v + " here", `{"route": {"host": "` + v + `"}}`} {
			if text, args := secretOut(t, v, s); !text || !args {
				t.Errorf("%q in %q is a name in code, yet it was masked (text kept %v, arguments kept %v)", v, s, text, args)
			}
		}
	}
}

// A word that is a name in code may be a password too. Where it is given a
// secret's name, called one, or put where a command takes a password, it
// is masked still: in a message, in a nested object, in a tool call's
// command.
func TestCodeWordAsPasswordIsMasked(t *testing.T) {
	for _, c := range []struct{ v, s string }{
		{"Illuminate", "api_key = Illuminate"},
		{"edge-san-jose-01", "api_key = edge-san-jose-01"},
		{"dev_worker5", "API_KEY=dev_worker5"},
		{"Speechmatics", "token: Speechmatics"},
		{"Sunshine", "the wifi password is Sunshine"},
		{"PurpleMonkey", `{"db": {"password": "PurpleMonkey"}}`},
		{"GoldenEagle", `{"user": "bob", "pw": "GoldenEagle"}`},
		{"SilverFalcon", "mysql -u root -p SilverFalcon -h db"},
		{"BlueOcean", "sshpass -p 'BlueOcean' ssh deploy@10.0.0.5"},
		{"RedDragon", "redis-cli -a RedDragon ping"},
		{"GreenForest", "curl -u admin:GreenForest https://api.acme.io"},
		{"WhiteTiger", "smbclient //nas/share -U admin%WhiteTiger"},
		{"BlackPanther", "openssl enc -pass pass:BlackPanther -in a"},
		{"Moonlight", "deploy --password=Moonlight"},
		{"Starlight", "deploy --secret-file x --pass Starlight"},
		{"Moonbeam", "login with Moonbeam"},
	} {
		if text, args := secretOut(t, c.v, c.s); text || args {
			t.Errorf("%q in %q went out (text %v, arguments %v)", c.v, c.s, text, args)
		}
	}
}

// What a key or a password is written with is in none of a name's
// shapes: digits scattered through a word, a long number, capitals at
// random. Each one the model takes for a secret is masked where nothing
// names it.
func TestKeyLikeValuesAreNoCodeName(t *testing.T) {
	for _, v := range []string{
		"Summer2026Prod",                       // a password
		"key-3ax6xnjp29jd6fds4gc373sgvjxteol0", // a Mailgun key
		"k3xq9vb2mz-p7r4t8w1",                  // a token with a dash
		"tunnel-a8f3k2j9x7q1",                  // a host's shape, a key's letters
		"acct-982347928347",                    // a long number
		"XkQpLmZnBv",                           // capitals at random
		"XkQpLmZnBvTrWsYu",                     // more of them
		"MTIzNDU2Nzg5MDEyMzQ1Njc4OQ.GaBcDe.AbCdEfGhIjKlMnOpQrStUvWx", // a Discord bot token's shape
		"Gw7.Xk2pQ9",            // a field's shape, a key's letters
		"Xk9q2Wz/1.0",           // a user agent's shape, a key's letters
		"QwErTyUiOp/1.2",        // another
		"XkQpLm.ZnBvTrWs",       // a field's shape, a random run's capitals
		"edge-1a2b3c4d5e6f7a8b", // a host's shape, a key's letters
	} {
		if text, args := secretOut(t, v, "see "+v+" here"); text || args {
			t.Errorf("%q went out as a name in code (text %v, arguments %v)", v, text, args)
		}
	}
}

package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// Eiv92wa on X ("Bug：会把英文软件名称当成密码脱敏"): a request from
// DeepSeek Harness had these lines in the user's message, and both
// Fail2Ban went to the vendor as {{SECRET_…}}, found as "之前见过" (known).
const fail2banLines = `{"seq":1915,"text":"服务器上安没安装Fail2Ban"}` + "\n" +
	`{"seq":1940,"text":"服务器上安没安装Fail2Ban这个软件"}` + "\n" +
	`{"seq":1965,"text":"服务器上安没安装Fail 2 Ban这个软件"}`

func fail2banBody() []byte {
	body, _ := json.Marshal(map[string]any{"model": "deepseek-v4.1-flash", "messages": []any{
		map[string]any{"role": "system", "content": "You are a helpful assistant."},
		map[string]any{"role": "user", "content": fail2banLines},
	}})
	return body
}

// A program's name in camel case with a number joining its words is no
// secret, whatever the local model takes it for: the reporter's
// Fail2Ban, and its kind, in a message and in a tool call's arguments.
func TestNumberedNamesAreNoSecret(t *testing.T) {
	Forget()
	defer Forget()
	o := Options{Secrets: true, Personal: true, Model: scoredModel("Fail2Ban")}
	out, n := MaskJSON(fail2banBody(), o)
	if n != 0 || strings.Contains(string(out), "{{SECRET_") || strings.Count(string(out), "Fail2Ban") != 2 {
		t.Errorf("the reporter's message: %d masked: %s", n, out)
	}
	// and the next request, the model finding nothing in it: nothing is
	// known to mask
	if out, n := MaskJSON(fail2banBody(), Options{Secrets: true, Personal: true, Model: scoredModel()}); n != 0 {
		t.Errorf("the next request: %d masked: %s", n, out)
	}
	for _, v := range []string{
		"Fail2Ban", "OAuth2Proxy", "K8sClient", "S3Bucket", "Win32Api",
		"Base64Encoder", "Neo4jDriver", "i18nHelper", "log4jCore", "Sha256Digest",
		"Http2Server", "X509Certificate", "Web3Auth",
	} {
		for _, s := range []string{"install " + v + " on the server", "服务器上安没安装" + v + "这个软件", `{"package": "` + v + `"}`} {
			if text, args := secretOut(t, v, s); !text || !args {
				t.Errorf("%q in %q is a program's name, yet it was masked (text kept %v, arguments kept %v)", v, s, text, args)
			}
		}
	}
}

// Such a name is a password still where one goes, and what a password
// is written with (a year, digits at its end or for letters, capitals at
// random) is no such name.
func TestNumberedNameShapedSecretsMasked(t *testing.T) {
	for _, c := range []struct{ v, s string }{
		{"Fail2Ban", "the root password is Fail2Ban"},
		{"Fail2Ban", `{"db": {"password": "Fail2Ban"}}`},
		{"OAuth2Proxy", "mysql -u root -p OAuth2Proxy -h db"},
		{"K8sClient", "curl -u admin:K8sClient https://api.acme.io"},
	} {
		if text, args := secretOut(t, c.v, c.s); text || args {
			t.Errorf("%q in %q went out (text %v, arguments %v)", c.v, c.s, text, args)
		}
	}
	for _, v := range []string{
		"Summer2026Prod", "Summer26Prod", "Dragon1999Fire", // a year
		"Welcome1", "Password123", "Spring2026", // digits at its end
		"Passw0rd", "P4ssword", "Adm1nistrator", "Tr0ub4dor", // digits for letters
		"Pw7nK4xQ9rTe", "Zq8vR2mT6wLp", "aB3dEfGhJk", "xQ9rTeWv", // at random
		"Abc1Def2Ghi3", // too many numbers
	} {
		if text, args := secretOut(t, v, "use "+v+" to sign in"); text || args {
			t.Errorf("%q went out as a program's name (text %v, arguments %v)", v, text, args)
		}
	}
}

// What a running magpie had remembered of such a name, the model alone
// having taken it for a secret, is not masked again as known where no
// password goes: the reporter's next request had Fail2Ban as known
// ("之前见过"). Found by a rule, or where a password goes, it is.
func TestKnownNumberedNameGoesAsWritten(t *testing.T) {
	Forget()
	defer Forget()
	// as an older magpie remembered it: masked as the model's, at its score
	p := placeholder("SECRET", "Fail2Ban")
	noteScores("Fail2Ban", []span{{0, len("Fail2Ban"), "SECRET", ByModel, 0.93, false}})
	o := Options{Secrets: true, Personal: true, Model: scoredModel()}
	if out, n := MaskJSON(fail2banBody(), o); n != 0 || strings.Contains(string(out), p) {
		t.Errorf("as known: %d masked: %s", n, out)
	}
	if out, _ := Mask("the root password is Fail2Ban", o); strings.Contains(out, "Fail2Ban") {
		t.Errorf("as known where a password goes: %q", out)
	}
	// a rule's find is masked as known wherever it comes again
	Forget()
	if out, _ := Mask("DB_PASSWORD=Fail2Ban", Options{Secrets: true}); strings.Contains(out, "Fail2Ban") {
		t.Fatalf("not masked by the rule: %q", out)
	}
	if out, _ := MaskJSON(fail2banBody(), o); strings.Count(string(out), "{{SECRET_") != 2 {
		t.Errorf("the rule's known value left: %s", out)
	}
}

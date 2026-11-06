package redact

import (
	"strings"
	"testing"
)

// A place in a file goes as written: AlpsNowX on X (10-28) saw L643-669.
// listed under Secrets, found by a rule, in two requests. A file whose
// name has token, secret or password in it, a colon and the lines were
// a field, its name and its value; and once masked, the lines were masked
// wherever they came again.
func TestLineRangeIsNoSecret(t *testing.T) {
	o := Options{Secrets: true, Personal: true}
	for _, in := range []string{
		"看一下 internal/auth/token.go:L643-669.",
		"L643-669.",
		"参考 src/secret_store.ts:L643-669. 和 L12-40",
		"见 tokenizer.rs:L643-669。",
		"cmd/apikey/main.go:L643-669, cmd/apikey/main.go:L12-40.",
		"see internal/redact/password_test.go:643-669 and #L643-L669",
		"src/credentials.py:L12-L40",
		"token_test.go:p2fa9c1d",
		"token.go:-1-2-3a",
		"internal/auth/token.go:L12-40",
	} {
		if out, n := Mask(in, o); n != 0 {
			t.Errorf("%q: %d masked: %q", in, n, out)
		}
	}
}

// Code naming a secret goes as written: binary4cat on X (10-28), function
// names were masked. A name made of words, one of them token, secret or
// password, after = or : is a function's or a variable's.
func TestNameInCodeIsNoSecret(t *testing.T) {
	for _, in := range []string{
		"const accessToken2 = refreshToken2;",
		"let secretV2 = loadSecretV2;",
		"secretKey:=base64Encode(raw)",
		"password = hashPassword256;",
		"self.token = generate_token_v2",
		`TOKEN_V2 = "TOKEN_V2"`,
		"tokenCount = countTokens4o",
		"  secret: env.SECRET_V2",
		"max_tokens=max_tokens_v3",
		"access_token = get_access_token_v2",
		"passwordInput.value = newPassword2",
		"fn(token=token_v2)",
		`const TOKEN_KEY = "access_token_v2"`,
	} {
		if out, n := Mask(in, Options{Secrets: true}); n != 0 {
			t.Errorf("%q: %d masked: %q", in, n, out)
		}
	}
}

// A path to where a secret is kept goes as written, and so does a
// plugin's skill: AkinoNya on X (10-28), folders and skills with a random
// string in their name were replaced.
func TestPathAndSkillAreNoSecret(t *testing.T) {
	for _, in := range []string{
		"PRIVATE_KEY_PATH=~/.ssh/id_ed25519",
		"GOOGLE_APPLICATION_CREDENTIALS=/Users/alice/.config/gcloud/legacy_credentials/x7k2p9q1/adc.json",
		`"credentials_file": "/Users/alice/.aws/sso/cache/3f9a2c7b1e.json"`,
		"私钥 ~/.ssh/id_ed25519_2024",
		"token_dir=/private/tmp/claude-501/x7k2p9q1-skill",
		"--token-file=/tmp/tok-a8f3k2x9/token",
		"skills/secret-scanner:x7k2p9q1",
		"- design-tokens:generate-v2: Generate tokens",
		"/secret-scan:audit-x7k2p9q1",
	} {
		if out, n := Mask(in, Options{Secrets: true}); n != 0 {
			t.Errorf("%q: %d masked: %q", in, n, out)
		}
	}
}

// A moment goes as written: dchaofei (#5002) saw a token's expiry masked
// to the hour, the minutes after it left as written. The field's name has
// token in it, its value is a time, and the head of it the SECRET rules'
// characters hold is a date's, not a key's.
func TestTimestampIsNoSecret(t *testing.T) {
	for _, in := range []string{
		`"access_token_expires_at": "2026-08-30T11:44:31.312645+08:00",`,
		`"access_token": "2026-08-30T11:44:31Z"`,
		`"token_expiry": "2026-08-30T11:44:31Z"`,
		`"secret_updated_at": "2026-08-30T11:44:31.312645+08:00"`,
		`"credential_expires": "2026-08-30T11:44:31Z"`,
		"token_expiry: 2026-08-30T11:44:31",
		"SERVER_KEY: 2026-08-30T11:44:31Z",
		"DB_PASS: 2026-08-30T11:44:31Z",
		"令牌是 2026-08-30T11:44:31",
		"密钥为 2026-08-30T11:44:31+08:00",
		"token 是 2026-08-30T11:44:31",
		"apikey 为 2026-08-30t11:44:31z",
	} {
		if out, n := Mask(in, Options{Secrets: true}); n != 0 {
			t.Errorf("%q: %d masked: %q", in, n, out)
		}
	}
	// In a tool call's arguments a field named for a secret takes its
	// value whatever it looks like (json.go); a moment it does not: the
	// field says when the token ends, and an agent reads that. The token
	// beside it still masks.
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"get_status","input":` +
		`{"access_token_expires_at":"2026-08-30T11:44:31.312645+08:00","access_token":"a1b2c3d4e5f6g7h8"}}]}]}`)
	out, n := MaskJSON(body, Options{Secrets: true})
	if n != 1 || !strings.Contains(string(out), "2026-08-30T11:44:31") || strings.Contains(unmarked(string(out)), "a1b2c3d4e5f6g7h8") {
		t.Errorf("the tool call's expiry masked with its token: %d: %s", n, out)
	}
}

// A value that only starts as a timestamp still masks: what follows the
// hour is the key's, not the moment's.
func TestTimestampHeadAndMoreIsASecret(t *testing.T) {
	for in, v := range map[string]string{
		`"token": "2026-08-30T11x9k2abc:def"`: "2026-08-30T11x9k2abc",
		"token=a1b2c3d4-2026-08-30T11:44":     "a1b2c3d4-2026-08-30T11",
		"token=2026-08-30T11-abc:def":         "2026-08-30T11-abc",
	} {
		if out, n := Mask(in, Options{Secrets: true}); n != 1 || strings.Contains(unmarked(out), v) {
			t.Errorf("%q: %d masked: %q", in, n, out)
		}
	}
}

// A secret of the same shape still masks: a value of one word and digits,
// a random one, one starting with L, a file of secrets' content, a key in
// a header with no space, a password in a connection string.
func TestSecretsBesideLinesAndNames(t *testing.T) {
	for in, v := range map[string]string{
		"password=hunter2pass9":                   "hunter2pass9",
		"API_KEY=AbC123dEf456GhI789":              "AbC123dEf456GhI789",
		"token=t0k3nValue2":                       "t0k3nValue2",
		"password: Passw0rd123":                   "Passw0rd123",
		"DB_PASSWORD=password123":                 "password123",
		"PASSWORD=SuperSecret2024":                "SuperSecret2024",
		"token: Lk8x9QmP2vR4":                     "Lk8x9QmP2vR4",
		"secret.txt:hunter2pass9":                 "hunter2pass9",
		"x-api-key:k9x2mp4qr7st1vw3":              "k9x2mp4qr7st1vw3",
		"x-api-key:ab12cd34ef56":                  "ab12cd34ef56",
		"Server=db;Password=Abc12345;User Id=sa":  "Abc12345",
		"api_token = 'tok-8f3a9c2e7b1d'":          "tok-8f3a9c2e7b1d",
		"密钥为12231241":                             "12231241",
		"私钥 ab12cd34ef56":                         "ab12cd34ef56",
		"SECRET_KEY=django-insecure-a1b2c3d4e5f6": "django-insecure-a1b2c3d4e5f6",
	} {
		if out, n := Mask(in, Options{Secrets: true}); n != 1 || strings.Contains(unmarked(out), v) {
			t.Errorf("%q: %d masked: %q", in, n, out)
		}
	}
}

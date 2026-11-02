package redact

import (
	"strings"
	"testing"
)

// everything is every category of masking on, the ones off by default
// (the IP address, the user and host) too.
func everything() Options {
	o := Options{Secrets: true, Personal: true, Kinds: map[string]bool{}}
	for _, c := range Categories {
		o.Kinds[c.ID] = true
	}
	return o
}

// A field set to nothing takes nothing from the next one: mx_albert on
// Discord, the Secrets category masked 127.0.0.1 in this command line, as
// the SECRET rule took UI_ADDR=127.0.0.1 for UI_PASSWORD's value. An
// address, a port, a path or a flag after an empty field goes as written,
// on any category.
func TestEmptyFieldTakesNotTheNext(t *testing.T) {
	for _, in := range []string{
		"env -i PATH HOME UI_USER= UI_PASSWORD= UI_ADDR=127.0.0.1:39117 DATA_DIR=/tmp/x ./syncer serve",
		"UI_PASSWORD= UI_ADDR=0.0.0.0:39117 ./syncer serve",
		"UI_TOKEN= LISTEN=[::1]:39117 ./syncer",
		"API_KEY= ADDR=localhost:39117 ./syncer",
		"DB_PASS= DB_HOST=127.0.0.1 DB_PORT=5432 psql",
		"SERVICE_KEY=\tBIND_ADDR=127.0.0.1:8080",
		"level=info password= addr=127.0.0.1:39117 data_dir=/tmp/x1",
		"client_secret: listen_addr=127.0.0.1:8080",
		"./syncer --password= --listen=127.0.0.1:39117",
		"secret= --data-dir=/var/lib/syncer2",
		"key= UPSTREAM_URL2=http://127.0.0.1:39117/v1",
		"密码= UI_ADDR=127.0.0.1:39117",
		"密钥： UI_ADDR=127.0.0.1:39117 ./syncer",
		"token 是 UI_ADDR=127.0.0.1:39117",
	} {
		if out, n := Mask(in, everything()); n != 0 {
			t.Errorf("%q: %d masked: %q", in, n, out)
		}
	}
}

// A secret still masks beside an empty field: its own, after its own
// name, and a value after a space that is no assignment (password =
// hunter2pass9, a base64 value with its padding).
func TestSecretBesideEmptyFieldMasks(t *testing.T) {
	for _, c := range []struct{ in, secret, kept string }{
		{"env -i UI_USER= UI_PASSWORD=hunter2pass9 UI_ADDR=127.0.0.1:39117 ./syncer serve", "hunter2pass9", "UI_ADDR=127.0.0.1:39117"},
		{"UI_PASSWORD= UI_TOKEN=t0k3nValue2 UI_ADDR=127.0.0.1:39117", "t0k3nValue2", "UI_ADDR=127.0.0.1:39117"},
		{"UI_PASSWORD= SERVICE_KEY=AbC123dEf456GhI789 ./syncer", "AbC123dEf456GhI789", "SERVICE_KEY="},
		{"password= api_key=AbC123dEf456GhI789", "AbC123dEf456GhI789", "api_key="},
		{"password = hunter2pass9", "hunter2pass9", "password = "},
		{"password: dGVzdDEyMzQ1Ng==", "dGVzdDEyMzQ1Ng==", "password: "},
		{"UI_PASSWORD= hunter2pass9", "hunter2pass9", "UI_PASSWORD= "},
		{"密码= UI_TOKEN=t0k3nValue2", "t0k3nValue2", "UI_TOKEN="},
	} {
		out, n := Mask(c.in, everything())
		if n == 0 || strings.Contains(out, c.secret) || !strings.Contains(out, c.kept) {
			t.Errorf("%q: %d masked: %q, want %q masked and %q kept", c.in, n, out, c.secret, c.kept)
		}
	}
}

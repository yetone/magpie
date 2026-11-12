package redact

import (
	"strings"
	"testing"
)

// Fake credentials in Cloudflare's documented shapes
// (developers.cloudflare.com/fundamentals/api/get-started/token-formats,
// .../ca-keys, and cloudflared's tunnel token, which is base64 of
// {"a":account,"t":tunnel,"s":secret}).
const (
	cfUserTok   = "cfut_Kq3vXm9ZpL2wRt7YbN4cHd8sJf6GaE1uVoTi5MxA" + "9f3c1b7e"
	cfAcctTok   = "cfat_Zr8dQw2LmN5vXc7BtY1pHk4sJg6FaE3uVoTi9MxP" + "02a8d4c6"
	cfGlobalNew = "cfk_Hb4nXw7ZpL2qRt9YcN3vMd8sJf6GaE1uKoTi5MyQ" + "5e1b7a90"
	cfOriginCA  = "v1.0-3f9a1c7e5b2d4a6c8e0f1a2b-" + "4d6f8a0c2e4b6d8f0a1c3e5a7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f"
	cfTunnelTok = "eyJhIjoiMGE0YzNmOWU3YjFkMmM2ZThmNWE5MDQ3YjNjMWUyZDQiLCJ0IjoiN2M5ZTFiM2EtNGQ2Zi00YThjLTllMGYtMWEyYjNjNGQ1ZTZmIiwicyI6IlpYaGhiWEJzWlhObFkzSmxkSFpoYkhWbE1USXpORFUyTnpnNU1BPT0ifQ=="
	cfGlobalOld = "c2547eb745079dac9320b638f5e225cf483cc"                            // 37 hex
	cfLegacyTok = "8M7wS6hCpXQ5QeL2Ek4xj3Bt9nVa1uRyZfDgHi0O"                         // 40 alnum
	cfR2KeyID   = "9d1f3b5a7c2e4a6c8e0b1d3f5a7c9e2b"                                 // 32 hex
	cfR2Secret  = "a3f5c7e9b1d3f5a7c9e1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5e7b9d1f3a5" // 64 hex
)

func TestCloudflareCredentials(t *testing.T) {
	cases := []struct{ name, in, gone string }{
		{"user token", "token is " + cfUserTok + " ok", cfUserTok},
		{"account token", "use " + cfAcctTok + ".", cfAcctTok},
		{"global key", "key=" + cfGlobalNew, cfGlobalNew},
		{"origin ca", "X-Auth-User-Service-Key: " + cfOriginCA, cfOriginCA},
		{"origin ca bare", "see " + cfOriginCA + " here", cfOriginCA},
		{"wrangler oauth", "cfoat_" + strings.Repeat("aB3dE5gH7j", 5) + "12345678", "cfoat_"},
		{"tunnel token flag", "cloudflared tunnel run --token " + cfTunnelTok, cfTunnelTok},
		{"tunnel token env", "TUNNEL_TOKEN=" + cfTunnelTok, cfTunnelTok},
		{"tunnel token bare", "run " + cfTunnelTok + " now", cfTunnelTok},
		{"legacy token env", "CLOUDFLARE_API_TOKEN=" + cfLegacyTok, cfLegacyTok},
		{"legacy token cf", "CF_API_TOKEN: " + cfLegacyTok, cfLegacyTok},
		{"bearer", "curl -H 'Authorization: Bearer " + cfLegacyTok + "' https://api.cloudflare.com/client/v4/user/tokens/verify", cfLegacyTok},
		{"global key header", "curl -H 'X-Auth-Key: " + cfGlobalOld + "' -H 'X-Auth-Email: a@b.co'", cfGlobalOld},
		{"global key env", "CLOUDFLARE_API_KEY=" + cfGlobalOld, cfGlobalOld},
		{"r2 secret env", "R2_SECRET_ACCESS_KEY=" + cfR2Secret, cfR2Secret},
		{"r2 aws env", "AWS_ACCESS_KEY_ID=" + cfR2KeyID + "\nAWS_SECRET_ACCESS_KEY=" + cfR2Secret + "\nendpoint https://acc.r2.cloudflarestorage.com", cfR2Secret},
		{"origin ca env", "CLOUDFLARE_ORIGIN_CA_KEY=" + cfOriginCA, cfOriginCA},
	}
	for _, c := range cases {
		out, n := Mask(c.in, Options{Secrets: true})
		if strings.Contains(unmarked(out), c.gone) || n == 0 {
			t.Errorf("%s: not masked (%d): %q", c.name, n, out)
		}
	}
}

func TestCloudflareLookalikesKept(t *testing.T) {
	sha1 := "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	sha256 := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	md5 := "d41d8cd98f00b204e9800998ecf8427e"
	b62 := "Zm9vYmFyYmF6cXV4Z3JhdWx0aGVsbG93b3JsZDEyMzQ1Ng"
	for _, in := range []string{
		"commit " + sha1,
		"sha256: " + sha256,
		"etag " + md5,
		"digest " + sha256 + " and " + sha1,
		"id " + b62,
		"version v1.0-beta.2 released, v1.0-rc1",
		"cloudflare zone id " + md5 + " in the dashboard",
		"cf account " + cfR2KeyID,
		"R2_ACCESS_KEY_ID=" + cfR2KeyID,
		"AWS_ACCESS_KEY_ID=" + cfR2KeyID,
		"cfut_ is the prefix of user tokens",
		"cfk_short",
		"Image digest sha256:" + sha256,
		"CLOUDFLARE_ACCOUNT_ID=" + cfR2KeyID,
		"eyJhbGciOiJub25lIn0 is a jwt header",
	} {
		out, n := Mask(in, Options{Secrets: true})
		if n != 0 || out != in {
			t.Errorf("masked a lookalike: %q -> %q", in, out)
		}
	}
}

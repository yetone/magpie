package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// allKinds is Mask personal data with every category turned on.
func allKinds() Options {
	o := Options{Secrets: true, Personal: true, Kinds: map[string]bool{}}
	for _, c := range Categories {
		o.Kinds[c.ID] = true
	}
	return o
}

// Each category finds its values, as the placeholder of its kind (an IP as
// a public-ip- name, a user name as a made-up one), and puts
// them back; and leaves alone what only looks like one: versions,
// timestamps, hashes, UUIDs, ordinary dates, model ids, URLs in code.
func TestMoreKinds(t *testing.T) {
	type find struct{ value, kind string }
	cases := []struct {
		in   string
		gone []find
		kept []string
	}{
		// SSN
		{in: "SSN 536-22-1847 on file", gone: []find{{"536-22-1847", "SSN"}}},
		{in: "my social security number is 536221847.", gone: []find{{"536221847", "SSN"}}},
		{in: "ssn: 536 22 1847", gone: []find{{"536 22 1847", "SSN"}}},
		// PASSPORT
		{in: "Passport No.: E12345678, valid", gone: []find{{"E12345678", "PASSPORT"}}},
		{in: "护照号码：G28233515", gone: []find{{"G28233515", "PASSPORT"}}},
		{in: "my passport number is 534761298", gone: []find{{"534761298", "PASSPORT"}}},
		{in: "パスポート番号: TZ1234567", gone: []find{{"TZ1234567", "PASSPORT"}}},
		// IBAN
		{in: "IBAN: GB82 WEST 1234 5698 7654 32 BIC", gone: []find{{"GB82 WEST 1234 5698 7654 32", "IBAN"}}, kept: []string{" BIC"}},
		{in: "send to DE89370400440532013000.", gone: []find{{"DE89370400440532013000", "IBAN"}}},
		{in: "FR1420041010050500013M02606", gone: []find{{"FR1420041010050500013M02606", "IBAN"}}},
		// BIRTHDAY
		{in: "生日：1990-03-15", gone: []find{{"1990-03-15", "BIRTHDAY"}}, kept: []string{"生日"}},
		{in: "DOB: 03/15/1990", gone: []find{{"03/15/1990", "BIRTHDAY"}}},
		{in: "I was born on March 5, 1988 in Ohio", gone: []find{{"March 5, 1988", "BIRTHDAY"}}, kept: []string{" in Ohio"}},
		{in: "出生于1985年7月2日", gone: []find{{"1985年7月2日", "BIRTHDAY"}}},
		{in: `{"birthday": "2001-12-31"}`, gone: []find{{"2001-12-31", "BIRTHDAY"}}},
		{in: "Geburtsdatum: 15.03.1990", gone: []find{{"15.03.1990", "BIRTHDAY"}}},
		// MAC
		{in: "ether 3c:22:fb:1a:9e:07 txqueuelen", gone: []find{{"3c:22:fb:1a:9e:07", "MAC"}}},
		{in: "Physical Address. . : A4-83-E7-2B-11-C9", gone: []find{{"A4-83-E7-2B-11-C9", "MAC"}}},
		// SERIAL
		{in: "Serial Number (system): C02XG2JHJGH5", gone: []find{{"C02XG2JHJGH5", "SERIAL"}}},
		{in: "S/N: 4CE0460D0G", gone: []find{{"4CE0460D0G", "SERIAL"}}},
		{in: "序列号 F9FX2LMNQ1GC", gone: []find{{"F9FX2LMNQ1GC", "SERIAL"}}},
		// home folders: the name only
		{in: "cwd: /Users/rinak/workspace/app", gone: []find{{"rinak", "USERNAME"}}, kept: []string{"/Users/", "/workspace/app"}},
		{in: "open /home/devrina/.bashrc", gone: []find{{"devrina", "USERNAME"}}, kept: []string{"/home/", "/.bashrc"}},
		{in: `C:\Users\Rina Kato\Desktop\a.txt`, gone: []find{{"Rina Kato", "USERNAME"}}, kept: []string{`C:\Users\`, `\Desktop\a.txt`}},
		{in: `"path":"C:\\Users\\rkato\\proj"`, gone: []find{{"rkato", "USERNAME"}}},
		{in: "file:///Users/rinak/notes.md", gone: []find{{"rinak", "USERNAME"}}},
		{in: "see /Users/rinak.", gone: []find{{"rinak", "USERNAME"}}, kept: []string{standInOf("rinak") + "."}},
		// user@host
		{in: "rinak@rina-mbp:~/src$ ls", gone: []find{{"rinak", "USERNAME"}, {"rina-mbp", "HOSTNAME"}}, kept: []string{":~/src$ ls"}},
		{in: "[deploy@build-07 ~]$ make", gone: []find{{"deploy", "USERNAME"}, {"build-07", "HOSTNAME"}}},
		{in: "run ssh rkato@bastion.corp.acme.io -p 22", gone: []find{{"rkato", "USERNAME"}, {"bastion.corp.acme.io", "HOSTNAME"}}},
		{in: "scp a.txt ops@db3.internal:/tmp/", gone: []find{{"ops", "USERNAME"}, {"db3.internal", "HOSTNAME"}}},
		{in: "Hostname: rina-mbp.local\n", gone: []find{{"rina-mbp.local", "HOSTNAME"}}},
		{in: "Host prod\n  HostName box17.acme.io\n  User rkato\n", gone: []find{{"box17.acme.io", "HOSTNAME"}, {"rkato", "USERNAME"}}},
		{in: "主机名：rina-pc", gone: []find{{"rina-pc", "HOSTNAME"}}},
		{in: "User: rkato", gone: []find{{"rkato", "USERNAME"}}},
		// IP
		{in: "the server 52.95.110.7 is up, the observer 52.95.110.8 too", gone: []find{{"52.95.110.7", "IP"}, {"52.95.110.8", "IP"}}},
		{in: "connect to 34.117.59.81:443", gone: []find{{"34.117.59.81", "IP"}}, kept: []string{":443"}},
		{in: "curl http://52.95.110.1/health", gone: []find{{"52.95.110.1", "IP"}}, kept: []string{"http://", "/health"}},
		{in: "my ip is 2a03:2880:f12f:83:face:b00c::25de.", gone: []find{{"2a03:2880:f12f:83:face:b00c::25de", "IP"}}},
		{in: "X-Forwarded-For: 61.135.169.121, 10.0.0.2", gone: []find{{"61.135.169.121", "IP"}}, kept: []string{"10.0.0.2"}},
		// buckets: the name, not the key
		{in: "aws s3 cp s3://acme-prod-logs/2024/01/a.gz .", gone: []find{{"acme-prod-logs", "BUCKET"}}, kept: []string{"s3://", "/2024/01/a.gz"}},
		{in: "gs://rina-ml-data/train.jsonl", gone: []find{{"rina-ml-data", "BUCKET"}}},
		{in: "oss://acme-backup-cn/db.sql", gone: []find{{"acme-backup-cn", "BUCKET"}}},
		{in: "https://acme-assets.s3.us-west-2.amazonaws.com/logo.png", gone: []find{{"acme-assets", "BUCKET"}}, kept: []string{".s3.us-west-2.amazonaws.com/logo.png"}},
		{in: "https://storage.googleapis.com/rina-public/x.bin", gone: []find{{"rina-public", "BUCKET"}}},
		{in: "https://acmestore01.blob.core.windows.net/invoices/a.pdf", gone: []find{{"acmestore01", "BUCKET"}, {"invoices", "BUCKET"}}},
		{in: "abfss://raw@acmelake.dfs.core.windows.net/x", gone: []find{{"raw", "BUCKET"}, {"acmelake", "BUCKET"}}},
		{in: "https://img-1250000000.cos.ap-guangzhou.myqcloud.com/a.png", gone: []find{{"img-1250000000", "BUCKET"}}},
	}
	o := allKinds()
	for _, c := range cases {
		out, n := Mask(c.in, o)
		if n != len(c.gone) {
			t.Errorf("%q: %d masked, want %d: %q", c.in, n, len(c.gone), out)
		}
		for _, g := range c.gone {
			// The made-up name is eight random letters, a new key each
			// run, and can spell a short user name: "ops" was still in
			// "zpxopsat@…" on ubuntu CI at 500f39886 (#4345).
			if strings.Contains(strings.ReplaceAll(unmarked(out), standInOf(g.value), ""), g.value) {
				t.Errorf("%q: %q still in %q", c.in, g.value, out)
			}
			p := placeholder(g.kind, g.value)
			if g.kind == "USERNAME" {
				p = standInOf(g.value) // a made-up name (shape.go)
			}
			if at, n, ok := ipStandIns(g.value); g.kind == "IP" && ok {
				for i := 0; i < n; i++ { // a public-ip- name (shape.go)
					if strings.Contains(out, at(i)) {
						p = at(i)
						break
					}
				}
			}
			if !strings.Contains(out, p) {
				t.Errorf("%q: no %s placeholder for %q in %q", c.in, g.kind, g.value, out)
			}
		}
		for _, k := range c.kept {
			if !strings.Contains(out, k) {
				t.Errorf("%q: %q gone from %q", c.in, k, out)
			}
		}
		if back := Restore(out, false); back != c.in {
			t.Errorf("round trip: %q → %q → %q", c.in, out, back)
		}
	}
}

// What looks like one of them but isn't goes as it is, with every category
// on.
func TestMoreKindsLeaveAlone(t *testing.T) {
	for _, s := range []string{
		// SSN: the ones never given out, examples, and pieces of longer numbers
		"000-12-3456", "666-12-3456", "912-34-5678", "123-45-6789", "078-05-1120", "536-00-1847", "536-22-0000",
		"build 2024-10-1234", "ticket ABC-123-45-6789", "id 536221848",
		// passports: no label, or a label with something else after it
		"E87654321", "passport.js handles auth", "import passport from 'passport'", "passport: ABCDEFG",
		// IBAN: a checksum off by one, a length that isn't its country's, a constant
		"GB82WEST12345698765433", "DE8937040044053201300", "ERROR_CODE AB12CDEFGHIJKLMNOP",
		"SHA256 DE89370400440532013000ABCDEF", "x = GB82 WEST 1234 5698 7654 33",
		// birthdays: a date with no word for it, a year alone, an impossible one
		"released 1990-04-16", "2024-01-01T12:34:56Z", "birthday: 1990", "DOB: 13/45/1990", "born in 1990", "updated 04/16/1990",
		"birthday party at 2024-05-01 is born from 2024-05-02", // a date not right after the word
		// MACs: examples, broadcast, timestamps, fingerprints, IPv6
		"00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "aa:bb:cc:dd:ee:ff", "01:23:45:67:89:ab", "12:34:56:78:90:12",
		"SHA1 Fingerprint=3C:22:FB:1A:9E:07:AB:CD:EF:01:23:45", "fe80::3e22:fbff:fe1a:9e07",
		// serials: a word that only starts like it, a port
		"serialVersionUID = 1234567890123L", "serial port /dev/ttyUSB0", "Serial.println(12345678)", "serial: 1", "sn=abc",
		// home folders: placeholders, CI, containers, a path inside another
		"/Users/$USER/x", "/home/<name>/x", "/home/user/x", "/home/runner/work/a", "/home/node/app", "/Users/Shared/x",
		`C:\Users\Public\x`, "~/home/rinak", "src/home/index.tsx", "https://acme.io/home/about", "/api/users/42",
		// user@host: git, emails, decorators, npm scopes
		"@types/node@20.1.0", "root@localhost:~#", "user@example.com:/x",
		"pip install foo@git+https://x",
		// hostname / user lines in code and chats
		"hostname = socket.gethostname()", "hostname: str", "hostname = os.hostname", `hostname: "localhost",`,
		"user = request.user", "user: User", "User: continue", "User: thanks", "username: string", "User guide",
		"user: str = None", "  User ${USER}",
		// IPs: private, loopback, CGNAT, docs, resolvers, versions, OIDs
		"10.1.2.3", "192.168.1.10", "127.0.0.1", "169.254.169.254", "100.64.0.1", "172.16.5.4", "192.0.2.1", "198.51.100.7", "203.0.113.9",
		"8.8.8.8", "1.1.1.1", "version 1.2.3.4", "Chrome/120.0.6099.109", "v1.2.3.4", "1.2.3.4.5", "OID 1.3.6.1.4.1", "==2.31.0.1",
		"node-18.17.1.1", "2001:db8::1", "fe80::1", "::1", "fd00::5", "12:34:56", "dead:beef:cafe",
		// buckets: examples, endpoints alone, the API's paths
		"s3://my-bucket/key", "s3://amzn-s3-demo-bucket/x", "https://s3.us-east-1.amazonaws.com", "s3.amazonaws.com",
		"https://storage.googleapis.com/upload/storage/v1/b", "oss-cn-hangzhou.aliyuncs.com", "https://cos.ap-guangzhou.myqcloud.com",
		// the usual coding fare
		"claude-opus-4-1-20250805", "gpt-5.1-codex-mini", "3f786850e387550fdab836ed7e6dc881de23001b", "123e4567-e89b-12d3-a456-426614174000",
		"go 1.26.8", "uses: actions/checkout@v4", "const url = 'https://api.acme.io/v1/users'", "2026-10-10 12:00:00",
	} {
		if out, n := Mask(s, allKinds()); n != 0 || out != s {
			t.Errorf("%q masked as %q", s, out)
		}
	}
}

// A date in the last year or ahead after a birthday's word is the day the
// agent runs on (JackyCcao on X: 2026-10-24 masked in 122 requests), not a
// birthday; one over a year ago still is. Today is held, and then real.
func TestBirthdayNotRecent(t *testing.T) {
	defer func(c func() time.Time) { dateClock = c }(dateClock)
	dateClock = func() time.Time { return time.Date(2026, 10, 24, 9, 30, 0, 0, time.Local) }
	for _, s := range []string{
		"生日：2026-10-24", "birthday: 2026-10-23", "DOB: 10/24/2026", "出生于2026年4月1日", "born on May 1, 2026",
		`{"birth_date": "2025-10-25"}`, "生日：2026-12-01", "Geburtsdatum: 01.01.2027",
	} {
		if out, n := Mask(s, allKinds()); n != 0 || out != s {
			t.Errorf("%q masked as %q", s, out)
		}
	}
	for _, s := range []string{"生日：2025-10-23", "DOB: 10/24/2024", "born on May 1, 1990"} {
		if out, _ := Mask(s, allKinds()); !strings.Contains(out, "BIRTHDAY") {
			t.Errorf("%q not masked: %q", s, out)
		}
	}
	dateClock = time.Now
	now := time.Now()
	for _, d := range []time.Time{now, now.AddDate(0, 0, -1), now.AddDate(0, 0, 1)} {
		s := "生日：" + d.Format("2006-01-02")
		if out, n := Mask(s, allKinds()); n != 0 || out != s {
			t.Errorf("%q masked as %q", s, out)
		}
	}
}

// The new categories are under Mask personal data: off while it is, and
// each on or off as Categories says until the user chooses.
func TestCategoryDefaults(t *testing.T) {
	text := "SSN 536-22-1847, cwd /Users/rinak/src, server 34.117.59.83, s3://acme-prod-logs/x, ether 3c:22:fb:1a:9e:07"
	if out, n := Mask(text, Options{Secrets: true}); n != 0 || out != text {
		t.Fatalf("personal data off, but masked: %q", out)
	}
	out, _ := Mask(text, Options{Secrets: true, Personal: true})
	for v, masked := range map[string]bool{"536-22-1847": true, "3c:22:fb:1a:9e:07": true, "rinak": false, "34.117.59.83": false, "acme-prod-logs": false} {
		if strings.Contains(out, v) == masked {
			t.Errorf("by default %q masked=%v, want %v: %q", v, !masked, masked, out)
		}
	}
	// the user's choice wins either way
	out, _ = Mask(text, Options{Secrets: true, Personal: true, Kinds: map[string]bool{"ssn": false, "ip": true}})
	if !strings.Contains(out, "536-22-1847") || strings.Contains(out, "34.117.59.83") || !strings.Contains(out, "rinak") {
		t.Fatalf("chosen: %q", out)
	}
	// and none without Mask personal data
	if out, n := Mask(text, Options{Secrets: true, Kinds: map[string]bool{"ip": true, "ssn": true}}); n != 0 {
		t.Fatalf("kinds on, personal data off, but masked: %q", out)
	}
	seen := map[string]bool{}
	for _, c := range Categories {
		if seen[c.ID] || len(c.Kinds) == 0 {
			t.Errorf("category %q twice or with no kinds", c.ID)
		}
		seen[c.ID] = true
		for _, k := range c.Kinds {
			if !containsString(kindCats[k], c.ID) {
				t.Errorf("category %q lists %s, which no rule of it finds", c.ID, k)
			}
		}
	}
	for k, cats := range kindCats {
		for _, cat := range cats {
			if cat != catPersonal && !seen[cat] {
				t.Errorf("%s is found under %q, which isn't one of Categories", k, cat)
			}
		}
	}
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// A user name in a home folder and the same one in user@host are one
// value, with one placeholder; one known once is masked again on the next
// turn in words no rule matches, as long as its category is on.
func TestMoreKindsKnown(t *testing.T) {
	o := allKinds()
	a, _ := Mask("cwd /Users/rkatodev/src", o)
	b, _ := Mask("rkatodev@rina-mbp:~$ ls", o)
	p := standInOf("rkatodev")
	if !strings.Contains(a, p) || !strings.Contains(b, p) {
		t.Fatalf("not one made-up name: %q %q", a, b)
	}
	reply := "Your account rkatodev has no access to 34.117.59.82."
	Mask("connect 34.117.59.82", o)
	if out, n := Mask(reply, o); n != 2 || strings.Contains(out, "rkatodev") || strings.Contains(out, "34.117.59.82") {
		t.Fatalf("%d masked: %q", n, out)
	}
	// the category turned off: its known values go as they are
	off := allKinds()
	off.Kinds["home"], off.Kinds["userhost"], off.Kinds["ip"] = false, false, false
	if out, n := Mask(reply, off); n != 0 {
		t.Fatalf("categories off, but masked: %q", out)
	}
	// a known address inside a longer one or a version is another
	for _, s := range []string{"1.34.117.59.82", "34.117.59.821", "v34.117.59.82"} {
		if out, n := Mask(s, o); n != 0 {
			t.Errorf("%q masked: %q", s, out)
		}
	}
}

// In a request body, a tool call's arguments and the code the agent sends
// stay valid JSON, and come back as they were.
func TestMoreKindsJSON(t *testing.T) {
	body := `{"model":"claude-opus-4-1","system":"cwd: C:\\Users\\rkato\\proj","messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_9","name":"bash","input":{"command":"aws s3 cp s3://acme-prod-logs/a.gz . ; ssh rkato@box17.acme.io","cwd":"/Users/rkato/proj"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_9","content":"inet 34.117.59.81 ether 3c:22:fb:1a:9e:07\nIBAN DE89370400440532013000"}]}]}`
	out, n := MaskJSON([]byte(body), allKinds())
	if !json.Valid(out) {
		t.Fatalf("not JSON: %s", out)
	}
	for _, v := range []string{"rkato", "acme-prod-logs", "box17.acme.io", "34.117.59.81", "3c:22:fb:1a:9e:07", "DE89370400440532013000"} {
		if strings.Contains(string(out), v) {
			t.Errorf("%q went out: %s", v, out)
		}
	}
	if n < 6 || !strings.Contains(string(out), `"toolu_9"`) || !strings.Contains(string(out), `"model":"claude-opus-4-1"`) {
		t.Fatalf("%d: %s", n, out)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if back := RestoreJSON(out); string(back) != body {
		t.Fatalf("restored:\n%s\n%s", back, body)
	}
}

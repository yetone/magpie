package redact

import (
	"strings"
	"testing"
)

// Digit strings agents send all the time are no phone numbers, whoever
// finds them: the PHONE rule (eleven digits that start 13 to 19) or the
// local model (private_phone, any region's shape). Each value is masked
// on the old code by one of them (Tomyu_2034 on X: "Masking 会把一些数字串
// 错误识别成 phone number").
func TestDigitStringsAreNoPhones(t *testing.T) {
	for _, c := range []struct{ text, value string }{
		// a count, a size or a time: a unit after it
		{"size 15012345678 bytes", "15012345678"},
		{"BenchmarkScan-12  1  13912345678 ns/op", "13912345678"},
		{"took 17643168001 ms", "17643168001"},
		{"usage: 15012345678 tokens", "15012345678"},
		{"共 13912345678 条", "13912345678"},
		{"耗时 15012345678 纳秒", "15012345678"},
		{"done 13912345678%", "13912345678"},
		{"+13912345678 bytes", "+13912345678"},
		// an amount: a currency before it
		{"total $15012345678", "15012345678"},
		{"合计 ￥15012345678", "15012345678"},
		{"cost USD 15012345678", "15012345678"},
		// a log's ids
		{"trace 13912345678 failed", "13912345678"},
		{"[req 15012345678] 200 OK", "15012345678"},
		{"build 13912345678 passed", "13912345678"},
		{"inode 13912345678", "13912345678"},
		{"id_13912345678", "13912345678"},
		{"session-15012345678", "15012345678"},
		{"批次号 13912345678", "13912345678"},
		// a piece of a path or a link, named by the piece before it
		{"/api/users/15012345678/profile", "15012345678"},
		{"https://item.jd.com/13912345678.html", "13912345678"},
		// the local model's: lists, dates, decimals, negatives, ids
		{"ports 3000 3425 8080 13912", "3000 3425 8080 13912"},
		{"1764316800 1764316801 1764316802", "1764316800 1764316801 1764316802"},
		{"date 2026-11-28", "2026-11-28"},
		{"version 2026.11.28.1391234", "2026.11.28.1391234"},
		{"coords 39.9042 116.4074", "39.9042 116.4074"},
		{"chat_id: -1001391234567", "-1001391234567"},
		{"chat_id: -1001391234567", "1001391234567"},
		{"isbn 978-7-111-12345-6", "978-7-111-12345-6"},
		{"invoice 2026-1128-0001", "2026-1128-0001"},
		{"nonce 015012345678", "015012345678"},
	} {
		if out := Preview(c.text, Options{Secrets: true, Personal: true}); out != c.text {
			t.Errorf("rule: %q masked: %q", c.text, out)
		}
		Forget()
		f := &fakeModel{values: map[string]string{c.value: "private_phone"}}
		if out, _ := Mask(c.text, Options{Secrets: true, Personal: true, Model: f.find}); out != c.text {
			t.Errorf("model: %q as a phone masked: %q", c.value, out)
		}
		Forget()
	}
}

// What went as written before still does: a Unix time, a snowflake, a
// longer digit run, a decimal's fraction, a date, a position, hex.
func TestDigitStringsStillWritten(t *testing.T) {
	for _, s := range []string{
		"created_at 1764316800",
		"Date.now() = 1764316800123",
		"time.time() -> 1764316800.123456",
		"https://x.com/Tomyu_2034/status/2126598926891700718",
		"tweet 2126598926891700718",
		"order 2026112813912345678 shipped",
		"numbers 913800138000123",
		"bigint 18446744073709551615",
		"pids: 13912, 13913, 14001, 15002",
		"v1.13912345678",
		"pi is 3.14159265359",
		"score 0.14285714286",
		"date 20261128 and 2026-11-28 13:45:12",
		"at app.js:13912:45678",
		"0x13912345678",
		"deadbeef 13912345678abcdef",
		"balance: 15,012,345,678",
		"trace_id=13912345678",
		"X-Request-Id: 15012345678",
		`{"duration": 15012345678}`,
	} {
		if out := Preview(s, Options{Secrets: true, Personal: true}); out != s {
			t.Errorf("%q masked: %q", s, out)
		}
	}
}

// Real phone numbers are masked still, in every region's shape: mainland
// China's mobiles by the rule, wherever nothing says they are something
// else, and each region's numbers as the local model finds them.
func TestPhonesOfEveryRegionStillMasked(t *testing.T) {
	o := Options{Secrets: true, Personal: true}
	for _, c := range []struct{ text, value string }{
		{"13800138000", "13800138000"},
		{"+86 13800138000", "13800138000"},
		{"+8613800138000", "13800138000"},
		{"我的手机号是 13800138000", "13800138000"},
		{"call 13800138000 now", "13800138000"},
		{"联系 13800138000 张经理", "13800138000"},
		{"phone-13800138000", "13800138000"},
		{"Tel.13800138000", "13800138000"},
		{"/contacts/13800138000", "13800138000"},
		{"电话 13800138000/13800138001", "13800138001"},
		{"wa.me/8613800138000", "13800138000"},
		{"user 13800138000 logged in", "13800138000"},
		{"13800138000 张三", "13800138000"},
		{"(13800138000)", "13800138000"},
		{"zhangsan_13800138000.pdf", "13800138000"},
		{"phone no. 13800138000", "13800138000"},
		{"phone: 13800138000 ms", "13800138000"},
	} {
		if out := Preview(c.text, o); strings.Contains(out, c.value) || !strings.Contains(out, "{{PHONE_") {
			t.Errorf("rule: %q not masked: %q", c.text, out)
		}
	}
	for _, c := range []struct{ text, value string }{
		{"call me on +86 138 0013 8000", "+86 138 0013 8000"},
		{"call me on 0086 138 0013 8000", "0086 138 0013 8000"},
		{"from the US dial 011 86 138 0013 8000", "011 86 138 0013 8000"},
		{"office 010-12345678", "010-12345678"},
		{"ring (415) 555-0123 after six", "(415) 555-0123"},
		{"ring +1 415 555 0123", "+1 415 555 0123"},
		{"ring 415-555-0123", "415-555-0123"},
		{"ring +44 20 7946 0958", "+44 20 7946 0958"},
		{"ring 020 7946 0958", "020 7946 0958"},
		{"携帯 +81 90-1234-5678", "+81 90-1234-5678"},
		{"携帯 090-1234-5678", "090-1234-5678"},
		{"電話 +852 9123 4567", "+852 9123 4567"},
		{"手機 0912-345-678", "0912-345-678"},
		{"전화 010-1234-5678", "010-1234-5678"},
		{"ruf +49 30 12345678 an", "+49 30 12345678"},
		{"call +91 98765 43210", "+91 98765 43210"},
		{"call +65 9123 4567", "+65 9123 4567"},
		{"call +61 4 1234 5678", "+61 4 1234 5678"},
		{"appelle +33 6 12 34 56 78", "+33 6 12 34 56 78"},
		{"liga +55 11 91234-5678", "+55 11 91234-5678"},
		{"my number is 020 7946 0958", "020 7946 0958"},
		{"Number (415) 555-0123", "(415) 555-0123"},
	} {
		Forget()
		f := &fakeModel{values: map[string]string{c.value: "private_phone"}}
		if out, _ := Mask(c.text, Options{Secrets: true, Personal: true, Model: f.find}); strings.Contains(out, c.value) || !strings.Contains(out, "{{PHONE_") {
			t.Errorf("model: %q not masked: %q", c.text, out)
		}
	}
	Forget()
}

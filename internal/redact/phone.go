package redact

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Eleven digits that start 13 to 19 are a mainland mobile number's form,
// and an id's: an order's, a user's, a row's (13800138000 as order_id,
// WHERE id = 18612345678, 订单号 13912345678). Every one went to the
// vendor as {{PHONE_…}} (_kaichen on X, 10-28: "长int，假如以1为开头的，
// 刚好匹配到长度，会被当作电话被mask"), and an agent can't look up an
// order it can't read. So the form alone is a phone only where nothing
// says it is something else:
//
//   - with a phone's own marks it is one wherever it is: +86 or 86 before
//     it (+86 138 0013 8000 reads as +86 and the rest);
//   - as a field's value (JSON, k=v, YAML, SQL's =, an XML element) it is
//     one only when the field's name has a phone's word in it: phone,
//     mobile, tel, cell, contact, 手机, 电话 (phone_number, mobileNo,
//     <tel>); under order_id, uid or id it is that id;
//   - in prose it is one, unless an id's word is right before it: order,
//     id, no., #, 订单号, 编号, 单号, 流水号, 工单 (我的手机号是 13800138000
//     still masks, 订单号 13912345678 已发货 doesn't).
//
// A letter or a digit against it on either side makes it a piece of a
// longer word, never a phone (the rule's bound): 9f13800138000abc is a
// hash.

// phoneWords are the words of a field's name that make its value a phone.
var phoneWords = []string{"phone", "phones", "mobile", "mobiles", "tel", "telephone", "cell", "cellphone", "contact", "contacts", "whatsapp", "sms", "msisdn", "fax"}

// phoneMarks, in a field's name in Chinese, make its value a phone.
var phoneMarks = []string{"手机", "电话", "電話", "联系", "聯繫", "联络"}

// idWords, right before a number in prose, make it an id: an order's, a
// ticket's, and a log's own (trace 13912345678 failed, [req 15012345678],
// build 13912345678 passed, session-15012345678).
var idWords = []string{"order", "id", "uid", "no", "no.", "#", "number", "ticket", "invoice",
	"trace", "span", "req", "request", "build", "run", "job", "task", "commit", "session", "inode", "offset", "seq", "pid", "tweet", "item", "sku", "tx", "txn", "block", "isbn", "nonce", "seed",
	"订单", "订单号", "单号", "编号", "流水号", "工单", "工单号", "运单号", "快递单号", "订单编号", "交易号", "序号", "账单号", "id号", "批次号", "批号"}

// countUnits, right after a number, make it a count, a size or a time: no
// phone is followed by one (15012345678 bytes, 13912345678 ns/op,
// 共 13912345678 条). A unit against the number (13912345678ns) is the
// rule's bound already.
var countUnits = []string{"ns", "ns/op", "us", "µs", "ms", "s", "sec", "secs", "second", "seconds", "min", "mins", "minutes", "hours", "hrs",
	"b", "b/op", "byte", "bytes", "kb", "kib", "mb", "mib", "gb", "gib", "tb", "tib", "bit", "bits",
	"token", "tokens", "row", "rows", "line", "lines", "item", "items", "record", "records", "entries", "ops", "allocs/op"}

// cjkUnits are countUnits in Chinese: what follows a count or an amount.
var cjkUnits = []string{"条", "个", "次", "元", "字节", "毫秒", "纳秒", "微秒", "秒", "行", "笔", "万", "亿"}

// currencies, right before a number, make it an amount ($15012345678).
var currencies = []string{"$", "¥", "￥", "€", "£", "usd", "cny", "rmb", "eur", "gbp"}

// phoneAt says the PHONE rule's s[a:b] is a phone, not an id of the same
// form.
func phoneAt(s string, a, b int) bool {
	if b-a > 11 {
		return true // +86, 86
	}
	if name, prev, ok := fieldName(s, a); ok {
		return phoneName(name) || prev != "" && phoneName(prev) || strings.EqualFold(name, "number")
	}
	if a > 0 && s[a-1] == '#' {
		return false // #13912345678
	}
	if amountOrCount(s, a, b) {
		return false
	}
	if seg, ok := pathSegment(s, a); ok {
		// a path's piece is named by the piece before it, as a field's
		// value by its name: /api/users/15012345678/profile is a user's
		// id, item.jd.com/13912345678.html an item's, /phone/13800138000
		// a phone
		return phoneName(seg)
	}
	return !idWordBefore(s, a, true)
}

// idWordBefore says one of idWords is right before s[a] in prose, as a
// whole word, with a space, a bracket, a quote, _ or - between: order
// 13800138101, id_13912345678, trace-13912345678, 订单号是 13912345673.
// A phone's number (phone no. 13800138000) is not an id's; with numbers
// false, no., # and number are no id's words at all (my number is …).
func idWordBefore(s string, a int, numbers bool) bool {
	before := strings.ToLower(strings.TrimRight(s[max(0, a-32):a], " \t(\"'“「_-"))
	for _, c := range []string{"是", "为", "為"} {
		before = strings.TrimRight(strings.TrimSuffix(before, c), " \t")
	}
	for _, w := range idWords {
		if !strings.HasSuffix(before, w) {
			continue
		}
		// a whole word: "order", not the end of "recorder"
		rest := before[:len(before)-len(w)]
		r, _ := utf8.DecodeLastRuneInString(rest)
		if len(rest) > 0 && w[0] < 0x80 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			continue
		}
		// but a phone's number: phone no. 13800138000
		if w == "no" || w == "no." || w == "number" || w == "#" {
			if !numbers {
				continue
			}
			rest = strings.TrimRight(rest, " ")
			if phoneName(rest[nameStart(rest, len(rest)):]) {
				return false
			}
		}
		return true
	}
	return false
}

// amountOrCount says the number at s[a:b] is an amount or a count by
// what is around it: a currency before it, a unit or a % after it.
func amountOrCount(s string, a, b int) bool {
	if b < len(s) && s[b] == '%' {
		return true
	}
	j := b
	if j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	rest := s[j:]
	for _, u := range cjkUnits {
		if strings.HasPrefix(rest, u) {
			return true
		}
	}
	k := 0
	for k < len(rest) && k < 12 && (rest[k] >= 'a' && rest[k] <= 'z' || rest[k] >= 'A' && rest[k] <= 'Z' || rest[k] == '/' || strings.HasPrefix(rest[k:], "µ")) {
		if rest[k] == 0xc2 {
			k++
		}
		k++
	}
	if k > 0 && (k == len(rest) || !(rest[k] >= 'a' && rest[k] <= 'z' || rest[k] >= 'A' && rest[k] <= 'Z' || rest[k] >= '0' && rest[k] <= '9' || rest[k] == '_')) {
		u := strings.ToLower(strings.TrimRight(rest[:k], "/"))
		for _, c := range countUnits {
			if u == c {
				return true
			}
		}
	}
	before := strings.ToLower(strings.TrimRight(s[max(0, a-8):a], " \t"))
	for _, c := range currencies {
		if !strings.HasSuffix(before, c) {
			continue
		}
		if r, _ := utf8.DecodeLastRuneInString(before[:len(before)-len(c)]); c[0] < 0x80 && c[0] >= 'a' && r >= 'a' && r <= 'z' {
			continue // a word's end: "abusd"
		}
		return true
	}
	return false
}

// pathSegment is the piece of a path or a link right before s[a], when a
// '/' is right before it and that piece has a letter in it:
// "users" in /api/users/15012345678/profile, "item.jd.com" in
// https://item.jd.com/13912345678.html. A piece of digits only (1/139…,
// 13800138000/13800138001, two phones as Chinese contacts write them) is
// no name.
func pathSegment(s string, a int) (string, bool) {
	if a < 2 || s[a-1] != '/' {
		return "", false
	}
	j := a - 1
	for j > 0 && !isSpace(s[j-1]) && strings.IndexByte("/\"'(<[=?&,;", s[j-1]) < 0 {
		j--
	}
	seg := s[j : a-1]
	if strings.IndexFunc(seg, func(r rune) bool { return r < 0x80 && unicode.IsLetter(r) }) < 0 {
		return "", false
	}
	return seg, true
}

// phoneName says a field's name is a phone's: one of its words is one of
// phoneWords (phone_number, mobileNo), or it has one of phoneMarks (手机号).
func phoneName(name string) bool {
	for _, w := range nameWords(name) {
		for _, p := range phoneWords {
			if w == p {
				return true
			}
		}
	}
	for _, m := range phoneMarks {
		if strings.Contains(name, m) {
			return true
		}
	}
	return false
}

// fieldName is the name of the field whose value starts at s[a]: the
// word before :, =, ：or an XML element's >, with quotes and spaces
// between, as JSON, k=v, YAML, SQL and XML have one, and prev the word
// before it with one space between (the phone of "phone no: …"). ok is
// false in prose, where no such separator is right before the value.
func fieldName(s string, a int) (name, prev string, ok bool) {
	i := a
	for i > 0 && strings.IndexByte(" \t\"'", s[i-1]) >= 0 {
		i--
	}
	switch {
	case i > 0 && s[i-1] == '>':
		// <tel>13800138000</tel>: the element's name
		k := strings.LastIndexByte(s[max(0, i-65):i-1], '<')
		if k < 0 {
			return "", "", false
		}
		tag := s[max(0, i-65)+k+1 : i-1]
		if sp := strings.IndexAny(tag, " \t"); sp >= 0 {
			tag = tag[:sp]
		}
		if tag == "" || strings.IndexFunc(tag, func(r rune) bool {
			return !(r < 0x80 && strings.IndexByte(alnum+"_.:-", byte(r)) >= 0)
		}) >= 0 {
			return "", "", false
		}
		return tag, "", true
	case i > 0 && (s[i-1] == ':' || s[i-1] == '='):
		i--
	case i >= len("：") && s[i-len("："):i] == "：":
		i -= len("：")
	default:
		return "", "", false
	}
	for i > 0 && strings.IndexByte(" \t\"'", s[i-1]) >= 0 {
		i--
	}
	j := nameStart(s, i)
	if j == i {
		return "", "", false
	}
	name = s[j:i]
	if n := utf8.RuneCountInString(name); n > 6 && hasCJK(name) {
		// a long run of CJK before a colon is a sentence's: its last few
		// characters name the value (我的手机号是：…)
		name = string([]rune(name)[n-6:])
	}
	if j >= 2 && s[j-1] == ' ' && s[j-2] != ' ' {
		prev = s[nameStart(s, j-1) : j-1]
	}
	return name, prev, true
}

// nameStart is where the name that ends at s[i] starts: letters, digits,
// _, . and - back from i, or Chinese characters.
func nameStart(s string, i int) int {
	j := i
	for j > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:j])
		if !(r < 0x80 && strings.IndexByte(alnum+"_.-", byte(r)) >= 0 || r >= 0x4e00 && r <= 0x9fff) {
			break
		}
		j -= n
	}
	return j
}

func hasCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

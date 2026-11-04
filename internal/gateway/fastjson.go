package gateway

import (
	"bytes"
	"context"
	"encoding"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/maphash"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/tidwall/gjson"
)

// decodeJSON is json.Unmarshal(data, dst), made for a request body: one
// field of a body of several megabytes is read without decoding the rest.
// encoding/json checks the whole input before decoding any of it, and
// steps through every byte of every string a byte at a time, so each read
// of a field of a long conversation cost as much as decoding all of it,
// and a request was read so a dozen times or more on its way (#1532,
// #1654: 0.1–0.3s of CPU at ~300k tokens).
//
// Here data is checked once (jsonValid, the same grammar as json.Valid),
// split at its members and elements without decoding them, and only the
// values dst has a place for are decoded: strings and booleans here,
// json.RawMessage as their bytes, structs, pointers, slices and maps of
// strings by their parts, and every other value by encoding/json itself.
// dst gets what json.Unmarshal gives it. Whatever this doesn't take on as
// json.Unmarshal would — data that isn't valid JSON, a type that decodes
// itself, an embedded field, a ",string" option, a key given twice, a value
// of the wrong type — goes to json.Unmarshal, dst zeroed first, so its
// error, and what it leaves in dst, are encoding/json's own. So does a dst
// that already holds something, which encoding/json decodes into in ways
// of its own (a slice's old elements are decoded over, not replaced).
func decodeJSON(data []byte, dst any) error { return decodeJSONIn(data, dst, false) }

// decodeJSONView is decodeJSON for a reader that keeps nothing it decodes
// past its return and writes none of it: every raw value shares data's
// bytes, capped, where decodeJSON copies what isn't a big share of a held
// body. A body a route has rewritten is held by nobody, so each reader of
// a rewritten Codex request copied its whole input to look at it: 1.4 of
// the 16.4 MB a 680 KB request allocated (#3383).
func decodeJSONView(data []byte, dst any) error { return decodeJSONIn(data, dst, true) }

func decodeJSONIn(data []byte, dst any, share bool) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() || !v.Elem().IsZero() {
		return json.Unmarshal(data, dst)
	}
	var members []memberSpan
	if len(data) >= validMemoMin {
		var valid bool
		if valid, members = longBody(data); !valid {
			return json.Unmarshal(data, dst)
		}
	} else if !jsonCheck(data) {
		return json.Unmarshal(data, dst)
	}
	start := skipJSONSpace(data, 0)
	if close := misfit(data[start], v.Type().Elem()); close != 0 {
		// An array or object where dst can't take one, as a caller trying
		// a string first and then a list of parts: encoding/json would
		// read all of it to say so. It says the same, at the same offset,
		// of the value's opening and its close.
		return json.Unmarshal(append(data[:start+1:start+1], close), dst)
	}
	if err := fillJSONIn(trimJSON(data), v.Elem(), members, share); err != nil {
		v.Elem().SetZero()
		return json.Unmarshal(data, dst)
	}
	return nil
}

// heldBodies are the request bodies being served (holdBody). A raw value
// decodeJSON finds inside one of them is that body's own bytes, not a
// copy. Each reader of a request decodes its messages into raw values —
// the images, the conversation's id, the prompt, the edits, the chat
// parse — and each copied every message: a 1.5 MB body allocated ~60 MB
// on its way through a group, and three agents sending such bodies at once
// held the heap at ~310 MB (yg6888 on X: ~1 GB). A copy is still made of
// anything else decoded — a buffer a stream fills again, a scanner's line,
// a body built and changed in place — since a raw value of it could be
// written over under its holder.
var heldBodies struct {
	sync.Mutex
	list atomic.Pointer[[][]byte]
	// facts: a held body's bodyFacts, by the body's first byte (factsOf)
	facts map[*byte]heldFacts
	// long: what longBody found of a held body, by its first byte
	long map[*byte]heldLong
}

// heldLong is what longBody found of a held body: valid or not, and its
// top level's members.
type heldLong struct {
	n       int
	valid   bool
	members []memberSpan
}

// heldBodiesMost bounds heldBodies: a body past it is decoded with copies, as
// before, rather than the list growing with requests whose context never
// ends (a test's, a caller's own).
const heldBodiesMost = 64

// heldShareMost bounds how much bigger than a shared value the body it
// keeps alive may be (inHeldBody).
const heldShareMost = 16

// holdBody shares body's bytes with what decodeJSON reads from it until
// ctx is done. body must be the request's as read, which nothing writes
// to: a request body is read whole once (readRequestBody) and only read
// after. The list keeps body itself, not its address, so its memory can't
// be handed to another allocation while it is listed.
func holdBody(ctx context.Context, body []byte) {
	if len(body) < validMemoMin || ctx.Done() == nil {
		return // a short body's copies cost little; one never released would stay listed
	}
	heldBodies.Lock()
	old := heldBodies.list.Load()
	var cur [][]byte
	if old != nil {
		cur = *old
	}
	if len(cur) >= heldBodiesMost || slices.ContainsFunc(cur, func(b []byte) bool { return unsafe.SliceData(b) == unsafe.SliceData(body) }) {
		heldBodies.Unlock()
		return // held already: one release ends it
	}
	next := append(slices.Clip(cur), body)
	heldBodies.list.Store(&next)
	heldBodies.Unlock()
	var unchanged func()
	if checkHeldBody != nil {
		unchanged = checkHeldBody(body)
	}
	context.AfterFunc(ctx, func() {
		if unchanged != nil {
			unchanged()
		}
		heldBodies.Lock()
		defer heldBodies.Unlock()
		cur := *heldBodies.list.Load()
		i := slices.IndexFunc(cur, func(b []byte) bool { return unsafe.SliceData(b) == unsafe.SliceData(body) })
		if i < 0 {
			return
		}
		delete(heldBodies.facts, unsafe.SliceData(body))
		delete(heldBodies.long, unsafe.SliceData(body))
		next := slices.Delete(slices.Clone(cur), i, i+1)
		heldBodies.list.Store(&next)
	})
}

// checkHeldBody, set by the tests, is handed every body holdBody lists and
// returns what checks, as it is released, that the body was not written
// while held. A decoded value shares a held body's bytes, and what is
// read from it is kept by it (factsOf): a body changed in place would
// change them under their holders, or leave them saying what it no longer
// does, so every route the tests serve checks it never is.
var checkHeldBody func(body []byte) (released func())

// inHeldBody says whether raw lies within a body holdBody holds and is
// at least 1/heldShareMost of it. A value kept past its request — a
// cache's entry, a remembered signature — keeps the whole body it shares
// alive, so a small one is still copied: a kept value holds at most
// heldShareMost times its own size. The values worth sharing are the big
// ones, the messages and the long turns, which each reader copied whole.
func inHeldBody(raw []byte) bool {
	l := heldBodies.list.Load()
	if l == nil || len(raw) == 0 {
		return false
	}
	p := uintptr(unsafe.Pointer(unsafe.SliceData(raw)))
	for _, b := range *l {
		s := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
		if p >= s && p+uintptr(len(raw)) <= s+uintptr(len(b)) {
			return len(raw)*heldShareMost >= len(b)
		}
	}
	return false
}

// misfit is the closing bracket of the array or object that open starts
// when a value of type t can't hold it, or 0.
func misfit(open byte, t reflect.Type) byte {
	for {
		if t == rawMessageType || t.Implements(jsonUnmarshalerType) || t.Implements(textUnmarshalerType) ||
			reflect.PointerTo(t).Implements(jsonUnmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType) {
			return 0
		}
		if t.Kind() != reflect.Pointer {
			break
		}
		t = t.Elem()
	}
	switch k := t.Kind(); {
	case k == reflect.Interface:
	case open == '[' && k != reflect.Slice && k != reflect.Array:
		return ']'
	case open == '{' && k != reflect.Struct && k != reflect.Map:
		return '}'
	}
	return 0
}

// errSlowJSON sends a value to json.Unmarshal (decodeJSON).
var errSlowJSON = errors.New("decoded by encoding/json")

var (
	rawMessageType      = reflect.TypeFor[json.RawMessage]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	stringType          = reflect.TypeFor[string]()
	fastFieldsByType    sync.Map // reflect.Type → *fastFields
	errFastFieldsUnfit  = errors.New("struct not decoded field by field")
	jsonNull            = []byte("null")
	jsonTrue, jsonFalse = []byte("true"), []byte("false")
)

// fastFields are the fields of a struct as encoding/json names them.
type fastFields struct {
	names []string
	index []int
	err   error
}

func jsonFieldsOf(t reflect.Type) *fastFields {
	if f, ok := fastFieldsByType.Load(t); ok {
		return f.(*fastFields)
	}
	f := &fastFields{}
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.Anonymous {
			f.err = errFastFieldsUnfit // promoted fields: encoding/json's
			break
		}
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := bytes.Cut([]byte(tag), []byte(","))
		for o := range bytes.SplitSeq(opts, []byte(",")) {
			if string(o) == "string" {
				f.err = errFastFieldsUnfit
			}
		}
		if !validTagName(string(name)) {
			name = []byte(sf.Name)
		}
		if slices.Contains(f.names, string(name)) {
			f.err = errFastFieldsUnfit // encoding/json picks one, or neither
		}
		f.names = append(f.names, string(name))
		f.index = append(f.index, i)
	}
	fastFieldsByType.Store(t, f)
	return f
}

// validTagName is encoding/json's isValidTag.
func validTagName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case bytes.ContainsRune([]byte("!#$%&()*+-./:;<=>?@[]^_{|}~ "), c):
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}

// field is the field of f that key names, as encoding/json picks it: its
// exact name, or else the first whose name matches without case.
func (f *fastFields) field(key []byte) int {
	for i, n := range f.names {
		if n == string(key) {
			return i
		}
	}
	for i, n := range f.names {
		if bytes.EqualFold([]byte(n), key) {
			return i
		}
	}
	return -1
}

// fillJSON decodes raw, a whole valid JSON value, into v; errSlowJSON or
// another error leaves it to encoding/json.
func fillJSON(raw []byte, v reflect.Value, share bool) error { return fillJSONIn(raw, v, nil, share) }

// fillJSONIn is fillJSON with where raw's members are, when known
// (longBody): a struct or map at raw's top is filled from them. share
// is decodeJSONView's: every raw value is raw's own bytes.
func fillJSONIn(raw []byte, v reflect.Value, members []memberSpan, share bool) error {
	t := v.Type()
	if t == rawMessageType {
		if share || inHeldBody(raw) {
			// the request's own body, which nothing writes to: shared,
			// and capped so an append to it can't write over the body
			v.SetBytes(raw[:len(raw):len(raw)])
		} else {
			v.SetBytes(bytes.Clone(raw)) // a copy, as encoding/json makes
		}
		return nil
	}
	if t.Kind() != reflect.Pointer && (t.Implements(jsonUnmarshalerType) || t.Implements(textUnmarshalerType) ||
		reflect.PointerTo(t).Implements(jsonUnmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType)) {
		return json.Unmarshal(raw, v.Addr().Interface()) // it decodes itself
	}
	null := bytes.Equal(raw, jsonNull)
	switch t.Kind() {
	case reflect.Pointer:
		if null {
			v.SetZero()
			return nil
		}
		if t.Implements(jsonUnmarshalerType) || t.Implements(textUnmarshalerType) {
			return json.Unmarshal(raw, v.Addr().Interface())
		}
		if v.IsNil() {
			v.Set(reflect.New(t.Elem()))
		}
		return fillJSONIn(raw, v.Elem(), members, share)
	case reflect.String:
		if null {
			return nil
		}
		if raw[0] != '"' {
			return errSlowJSON
		}
		s, ok := unquoteJSON(raw)
		if !ok {
			return errSlowJSON
		}
		v.SetString(s)
		return nil
	case reflect.Bool:
		switch {
		case null:
		case bytes.Equal(raw, jsonTrue):
			v.SetBool(true)
		case bytes.Equal(raw, jsonFalse):
			v.SetBool(false)
		default:
			return errSlowJSON
		}
		return nil
	case reflect.Struct:
		if null {
			return nil
		}
		if raw[0] != '{' {
			return errSlowJSON
		}
		f := jsonFieldsOf(t)
		if f.err != nil {
			return f.err
		}
		// seen is a bit a field, in few or in many when a struct has more
		// than 64: a []bool made for each struct decoded was one of a
		// request's commonest allocations
		var few uint64
		var many []bool
		var err error
		eachMemberIn(raw, members, func(key, val []byte) bool {
			name, ok := keyName(key)
			if !ok {
				err = errSlowJSON
				return false
			}
			i := f.field(name)
			if i < 0 {
				return true
			}
			twice := false
			if len(f.names) <= 64 {
				twice, few = few&(1<<i) != 0, few|1<<i
			} else {
				if many == nil {
					many = make([]bool, len(f.names))
				}
				twice, many[i] = many[i], true
			}
			if twice {
				err = errSlowJSON // a key twice: encoding/json merges them
				return false
			}
			err = fillJSON(val, v.Field(f.index[i]), share)
			return err == nil
		})
		return err
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return errSlowJSON // base64, or bytes of a type of their own
		}
		if null {
			v.SetZero()
			return nil
		}
		if raw[0] != '[' {
			return errSlowJSON
		}
		var elems [][]byte
		eachElement(raw, func(el []byte) bool {
			elems = append(elems, el)
			return true
		})
		s := reflect.MakeSlice(t, len(elems), len(elems))
		for i, el := range elems {
			if err := fillJSON(el, s.Index(i), share); err != nil {
				return err
			}
		}
		v.Set(s)
		return nil
	case reflect.Map:
		if t.Key() != stringType {
			return errSlowJSON
		}
		if null {
			v.SetZero()
			return nil
		}
		if raw[0] != '{' {
			return errSlowJSON
		}
		if v.IsNil() {
			v.Set(reflect.MakeMap(t))
		}
		var err error
		eachMemberIn(raw, members, func(key, val []byte) bool {
			name, ok := unquoteJSON(key)
			if !ok {
				err = errSlowJSON
				return false
			}
			el := reflect.New(t.Elem()).Elem()
			if err = fillJSON(val, el, share); err != nil {
				return false
			}
			v.SetMapIndex(reflect.ValueOf(name), el)
			return true
		})
		return err
	}
	// numbers, interfaces and the rest
	return json.Unmarshal(raw, v.Addr().Interface())
}

// rawObject is json.Unmarshal(data, &m) for m a map[string]json.RawMessage.
func rawObject(data []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	err := decodeJSON(data, &m)
	return m, err
}

// rawArray is json.Unmarshal(data, &a) for a a []json.RawMessage.
func rawArray(data []byte) ([]json.RawMessage, error) {
	var a []json.RawMessage
	err := decodeJSON(data, &a)
	return a, err
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func skipJSONSpace(data []byte, i int) int {
	const spaces = 0x2020202020202020 // a padded body's runs of spaces, 8 at a time
	for i+8 <= len(data) && binary.LittleEndian.Uint64(data[i:]) == spaces {
		i += 8
	}
	for i < len(data) && isJSONSpace(data[i]) {
		i++
	}
	return i
}

func trimJSON(data []byte) []byte {
	i := skipJSONSpace(data, 0)
	j := len(data)
	for j-8 >= i && binary.LittleEndian.Uint64(data[j-8:]) == 0x2020202020202020 {
		j -= 8
	}
	for j > i && isJSONSpace(data[j-1]) {
		j--
	}
	return data[i:j]
}

// eachMember calls f with each member of obj, a valid JSON object, its key
// still quoted, until f returns false.
func eachMember(obj []byte, f func(key, val []byte) bool) {
	eachMemberAt(obj, func(k0, k1, v0, v1 int) bool { return f(obj[k0:k1], obj[v0:v1:v1]) })
}

// eachMemberIn is eachMember, from where its members are when known
// (longBody), else by splitting obj.
func eachMemberIn(obj []byte, members []memberSpan, f func(key, val []byte) bool) {
	if members == nil {
		if h := topSplit.Load(); h != nil && len(obj) >= validMemoMin {
			(*h)(obj)
		}
		eachMember(obj, f)
		return
	}
	for _, m := range members {
		if !f(obj[m.k0:m.k1], obj[m.v0:m.v1:m.v1]) {
			return
		}
	}
}

// eachMemberAt is eachMember by offsets: the key at [k0, k1), the value
// at [v0, v1).
func eachMemberAt(obj []byte, f func(k0, k1, v0, v1 int) bool) {
	i := skipJSONSpace(obj, 1)
	if obj[i] == '}' {
		return
	}
	for {
		k := i
		i = skipJSONString(obj, i)
		k1 := i
		i = skipJSONSpace(obj, i)
		i = skipJSONSpace(obj, i+1) // ':'
		s := i
		i = skipJSONValue(obj, i)
		if !f(k, k1, s, i) {
			return
		}
		i = skipJSONSpace(obj, i)
		if obj[i] != ',' {
			return
		}
		i = skipJSONSpace(obj, i+1)
	}
}

// eachElement calls f with each element of arr, a valid JSON array, until
// f returns false.
func eachElement(arr []byte, f func(el []byte) bool) {
	i := skipJSONSpace(arr, 1)
	if arr[i] == ']' {
		return
	}
	for {
		s := i
		i = skipJSONValue(arr, i)
		if !f(arr[s:i:i]) {
			return
		}
		i = skipJSONSpace(arr, i)
		if arr[i] != ',' {
			return
		}
		i = skipJSONSpace(arr, i+1)
	}
}

// elementsOf is the elements of raw, a valid JSON value, when it is an array
// (nil otherwise), each a capped slice of raw so an append never writes
// into it. A reader that only looks at a list's items takes them this way
// rather than decoding a []json.RawMessage, which copies each one that
// doesn't share a held body.
func elementsOf(raw []byte) []json.RawMessage {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var out []json.RawMessage
	eachElement(raw, func(el []byte) bool {
		out = append(out, el)
		return true
	})
	return out
}

// inputList is a request's input list, raw from a decoded body (so valid
// JSON), for a reader that looks at its items and keeps none past its
// return: elementsOf, so no item is copied. Decoding it as a
// []json.RawMessage copied every item, since each is under a sixteenth of
// the body (heldShareMost): 3.7 of the 20 MB a 680 KB Codex request
// allocated went to such copies (#3383). ok is what that decode's error
// was: false when the input is missing or neither a list nor null.
func inputList(raw json.RawMessage) ([]json.RawMessage, bool) {
	raw = trimJSON(raw)
	switch {
	case len(raw) == 0:
		return nil, false
	case raw[0] == '[':
		return elementsOf(raw), true
	case string(raw) == "null":
		return nil, true
	}
	return nil, false
}

// skipJSONString is the end of the valid JSON string at i.
func skipJSONString(data []byte, i int) int {
	i++
	for {
		j := bytes.IndexByte(data[i:], '"')
		if j < 0 {
			return len(data)
		}
		i += j
		n := 0
		for k := i - 1; data[k] == '\\'; k-- {
			n++
		}
		i++
		if n%2 == 0 {
			return i
		}
	}
}

// skipJSONValue is the end of the valid JSON value at i.
func skipJSONValue(data []byte, i int) int {
	switch data[i] {
	case '"':
		return skipJSONString(data, i)
	case '{', '[':
		depth := 0
		for i < len(data) {
			switch data[i] {
			case '"':
				i = skipJSONString(data, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return i
	}
	for i < len(data) {
		switch c := data[i]; {
		case c == ',' || c == '}' || c == ']' || isJSONSpace(c):
			return i
		}
		i++
	}
	return i
}

// keyName is a member's key as unquoteJSON reads it, without making a
// string of it: a plain ASCII key, the kind nearly every key is, is the
// bytes between its quotes.
func keyName(key []byte) ([]byte, bool) {
	if len(key) < 2 || key[0] != '"' || key[len(key)-1] != '"' {
		return nil, false
	}
	k := key[1 : len(key)-1]
	for _, c := range k {
		if c == '\\' || c == '"' || c < ' ' || c >= utf8.RuneSelf {
			name, ok := unquoteJSON(key)
			return []byte(name), ok
		}
	}
	return k, true
}

// unquoteJSON is the string a valid JSON string literal holds, as
// encoding/json decodes it (unquoteBytes): invalid UTF-8 and lone
// surrogates become U+FFFD.
func unquoteJSON(q []byte) (string, bool) {
	if len(q) < 2 || q[0] != '"' || q[len(q)-1] != '"' {
		return "", false
	}
	s := q[1 : len(q)-1]
	r := 0
	for r < len(s) {
		c := s[r]
		if c == '\\' || c == '"' || c < ' ' {
			break
		}
		if c < utf8.RuneSelf {
			r++
			continue
		}
		rr, size := utf8.DecodeRune(s[r:])
		if rr == utf8.RuneError && size == 1 {
			break
		}
		r += size
	}
	if r == len(s) {
		return string(s), true
	}
	b := make([]byte, 0, len(s)+2*utf8.UTFMax)
	b = append(b, s[:r]...)
	for r < len(s) {
		switch c := s[r]; {
		case c == '\\':
			r++
			if r >= len(s) {
				return "", false
			}
			switch s[r] {
			default:
				return "", false
			case '"', '\\', '/', '\'':
				b = append(b, s[r])
				r++
			case 'b':
				b = append(b, '\b')
				r++
			case 'f':
				b = append(b, '\f')
				r++
			case 'n':
				b = append(b, '\n')
				r++
			case 'r':
				b = append(b, '\r')
				r++
			case 't':
				b = append(b, '\t')
				r++
			case 'u':
				r--
				rr := hex4(s[r:])
				if rr < 0 {
					return "", false
				}
				r += 6
				if utf16.IsSurrogate(rr) {
					if dec := utf16.DecodeRune(rr, hex4(s[r:])); dec != unicode.ReplacementChar {
						r += 6
						b = utf8.AppendRune(b, dec)
						break
					}
					rr = unicode.ReplacementChar
				}
				b = utf8.AppendRune(b, rr)
			}
		case c == '"', c < ' ':
			return "", false
		case c < utf8.RuneSelf:
			b = append(b, c)
			r++
		default:
			rr, size := utf8.DecodeRune(s[r:])
			r += size
			b = utf8.AppendRune(b, rr)
		}
	}
	if cap(b)-len(b) > len(b)/4 {
		// \u escapes came to much less than they were: a copy of its
		// own size, so a string kept long holds no room it doesn't use
		return string(b), true
	}
	// b is this string's own and nothing writes to it again: the string
	// is b, not a second copy of a long text (a turn's text has escapes,
	// its newlines at least, and was copied twice on every request)
	return unsafe.String(unsafe.SliceData(b), len(b)), true
}

// hex4 is encoding/json's getu4: the rune of the \uXXXX at s, or -1.
func hex4(s []byte) rune {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return -1
	}
	var r rune
	for _, c := range s[2:6] {
		switch {
		case '0' <= c && c <= '9':
			c = c - '0'
		case 'a' <= c && c <= 'f':
			c = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			c = c - 'A' + 10
		default:
			return -1
		}
		r = r*16 + rune(c)
	}
	return r
}

// jsonMaxDepth is encoding/json's maxNestingDepth.
const jsonMaxDepth = 10000

// validMemoMin is the size from which jsonValid remembers what it found
// valid. A request body is decoded a dozen times or more on its way
// (modelOf, parse, the prompt, the conversation, the effort, the images…),
// and each decodeJSON checked all of it again: ~2.5ms a time at 2 MB, a
// fifth of the CPU of serving it.
const validMemoMin = 32 << 10

// validMemo is the last bodies found valid, by length and a hash under a
// seed of this process's own.
var validMemo struct {
	sync.Mutex
	sums [8]validEntry
	next int
}

type validSum struct {
	n   int
	sum uint64
}

// validEntry is a body remembered valid, and where its top level's
// members are, once a decode has split it (nil before, and for a body not
// an object).
type validEntry struct {
	validSum
	members []memberSpan
}

// memberSpan is a member of a body's top-level object: its key, quoted,
// at [k0, k1) and its value at [v0, v1), of the body trimmed (trimJSON).
type memberSpan struct{ k0, k1, v0, v1 int }

// topSplit, when set (tests), is told of each object of validMemoMin or
// more that decodeJSON splits at its members.
var topSplit atomic.Pointer[func(data []byte)]

// longBody is what decodeJSON knows of a body of validMemoMin or more:
// valid or not, and where its top level's members are. The members are
// remembered with the body, as its validity is: a request's body was split
// at its top level again by each of the dozen decodes it went through,
// each a walk over every byte of the conversation.
func longBody(data []byte) (valid bool, members []memberSpan) {
	if e, held := heldLongOf(data); held {
		return e.valid, e.members
	}
	s := validSum{len(data), validHash(data)}
	validMemo.Lock()
	i := slices.IndexFunc(validMemo.sums[:], func(e validEntry) bool { return e.validSum == s })
	if i >= 0 && validMemo.sums[i].members != nil {
		members = validMemo.sums[i].members
		validMemo.Unlock()
		return true, members
	}
	validMemo.Unlock()
	if i < 0 && !heldMember(data) {
		if f := validScanned.Load(); f != nil {
			(*f)(data)
		}
		if !jsonCheck(data) {
			return false, nil
		}
	}
	raw := trimJSON(data)
	if raw[0] == '{' {
		if f := topSplit.Load(); f != nil {
			(*f)(data)
		}
		members = []memberSpan{}
		eachMemberAt(raw, func(k0, k1, v0, v1 int) bool {
			members = append(members, memberSpan{k0, k1, v0, v1})
			return true
		})
	}
	validMemo.Lock()
	if i = slices.IndexFunc(validMemo.sums[:], func(e validEntry) bool { return e.validSum == s }); i >= 0 {
		validMemo.sums[i].members = members
	} else {
		validMemo.sums[validMemo.next] = validEntry{s, members}
		validMemo.next = (validMemo.next + 1) % len(validMemo.sums)
	}
	validMemo.Unlock()
	return true, members
}

var validSeed = maphash.MakeSeed()

// validHash is data's sum in validMemo.
func validHash(data []byte) uint64 {
	if f := validHashed.Load(); f != nil {
		(*f)(data)
	}
	return maphash.Bytes(validSeed, data)
}

// validHashed, when set (tests), is told of each body validHash sums.
var validHashed atomic.Pointer[func(data []byte)]

// heldLongOf is what longBody finds of data when data is a body being
// served (holdBody), whole, and whether it is one. A held body is not
// written while held, so it is known again by where it is, not by a hash
// of all its bytes: each of the dozen decodes a request went through
// summed its whole body, a twentieth of the gateway's CPU on long Codex
// requests (#4371). What is found is kept until the body is released.
func heldLongOf(data []byte) (heldLong, bool) {
	key := unsafe.SliceData(data)
	heldBodies.Lock()
	e, ok := heldBodies.long[key]
	heldBodies.Unlock()
	if ok && e.n == len(data) {
		return e, true
	}
	l := heldBodies.list.Load()
	if l == nil || !slices.ContainsFunc(*l, func(b []byte) bool { return unsafe.SliceData(b) == key && len(b) == len(data) }) {
		return heldLong{}, false
	}
	e = heldLong{n: len(data)}
	if f := validScanned.Load(); f != nil {
		(*f)(data)
	}
	if e.valid = jsonCheck(data); e.valid {
		if raw := trimJSON(data); raw[0] == '{' {
			if f := topSplit.Load(); f != nil {
				(*f)(data)
			}
			e.members = []memberSpan{}
			eachMemberAt(raw, func(k0, k1, v0, v1 int) bool {
				e.members = append(e.members, memberSpan{k0, k1, v0, v1})
				return true
			})
		}
	}
	heldBodies.Lock()
	defer heldBodies.Unlock()
	// only while body is held: one released may be freed, and its memory
	// given to another body that would read as this one (factsOf)
	if l := heldBodies.list.Load(); l != nil && slices.ContainsFunc(*l, func(b []byte) bool { return unsafe.SliceData(b) == key && len(b) == len(data) }) {
		if heldBodies.long == nil {
			heldBodies.long = map[*byte]heldLong{}
		}
		heldBodies.long[key] = e
	}
	return e, true
}

// heldMember is whether data is the whole value of a top-level member of a
// body being served that heldLongOf found valid: a part of a valid value
// split at its members is valid itself, so it is not checked byte by byte
// again. A request's conversation (a Responses request's input, a chat's
// messages) is decoded alone by each of its readers, and the first decode
// of each, as a value of its own, checked all of it again: two readers at
// once (the prompt's, the folder's) each did, before the hash memo knew
// it. A 516 MB Codex turn of generated images was checked three times over
// before routing, past the 30s magpie gives its own work there under
// -race on a loaded CI machine (#5281).
func heldMember(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	p := uintptr(unsafe.Pointer(unsafe.SliceData(data)))
	l := heldBodies.list.Load()
	if l == nil {
		return false
	}
	heldBodies.Lock()
	defer heldBodies.Unlock()
	for _, b := range *l {
		e, ok := heldBodies.long[unsafe.SliceData(b)]
		if !ok || !e.valid || e.n != len(b) || len(e.members) == 0 {
			continue
		}
		raw := trimJSON(b)
		start := uintptr(unsafe.Pointer(unsafe.SliceData(raw)))
		if p < start || p+uintptr(len(data)) > start+uintptr(len(raw)) {
			continue
		}
		off := int(p - start)
		i, found := slices.BinarySearchFunc(e.members, off, func(m memberSpan, off int) int { return m.v0 - off })
		return found && e.members[i].v1-e.members[i].v0 == len(data)
	}
	return false
}

// validScanned, when set (tests), is told of each body of validMemoMin or
// more that jsonValid checks byte by byte.
var validScanned atomic.Pointer[func(data []byte)]

// jsonValid is json.Valid, without stepping through each byte by a call:
// the grammar, and the depth past which encoding/json gives up, are its.
// A long body found valid is remembered, and not checked again.
func jsonValid(data []byte) bool {
	if len(data) < validMemoMin {
		return jsonCheck(data)
	}
	if e, held := heldLongOf(data); held {
		return e.valid
	}
	if heldMember(data) {
		return true
	}
	s := validSum{len(data), validHash(data)}
	validMemo.Lock()
	known := slices.ContainsFunc(validMemo.sums[:], func(e validEntry) bool { return e.validSum == s })
	validMemo.Unlock()
	if known {
		return true
	}
	if f := validScanned.Load(); f != nil {
		(*f)(data)
	}
	if !jsonCheck(data) {
		return false
	}
	validMemo.Lock()
	validMemo.sums[validMemo.next] = validEntry{validSum: s}
	validMemo.next = (validMemo.next + 1) % len(validMemo.sums)
	validMemo.Unlock()
	return true
}

// jsonCheck is jsonValid's check itself.
func jsonCheck(data []byte) bool {
	i := skipJSONSpace(data, 0)
	i, ok := validJSONValue(data, i, 0)
	return ok && skipJSONSpace(data, i) == len(data)
}

// jsonStringByte marks the bytes that end a run of a string's plain bytes.
var jsonStringByte = func() (t [256]bool) {
	for c := range 0x20 {
		t[c] = true
	}
	t['"'], t['\\'] = true, true
	return t
}()

func validJSONValue(data []byte, i, depth int) (int, bool) {
	if i >= len(data) {
		return i, false
	}
	switch c := data[i]; {
	case c == '"':
		return validJSONString(data, i)
	case c == '{':
		if depth+1 > jsonMaxDepth {
			return i, false
		}
		i = skipJSONSpace(data, i+1)
		if i < len(data) && data[i] == '}' {
			return i + 1, true
		}
		for {
			if i >= len(data) || data[i] != '"' {
				return i, false
			}
			var ok bool
			if i, ok = validJSONString(data, i); !ok {
				return i, false
			}
			i = skipJSONSpace(data, i)
			if i >= len(data) || data[i] != ':' {
				return i, false
			}
			i = skipJSONSpace(data, i+1)
			if i, ok = validJSONValue(data, i, depth+1); !ok {
				return i, false
			}
			i = skipJSONSpace(data, i)
			if i >= len(data) {
				return i, false
			}
			switch data[i] {
			case '}':
				return i + 1, true
			case ',':
				i = skipJSONSpace(data, i+1)
			default:
				return i, false
			}
		}
	case c == '[':
		if depth+1 > jsonMaxDepth {
			return i, false
		}
		i = skipJSONSpace(data, i+1)
		if i < len(data) && data[i] == ']' {
			return i + 1, true
		}
		for {
			var ok bool
			if i, ok = validJSONValue(data, i, depth+1); !ok {
				return i, false
			}
			i = skipJSONSpace(data, i)
			if i >= len(data) {
				return i, false
			}
			switch data[i] {
			case ']':
				return i + 1, true
			case ',':
				i = skipJSONSpace(data, i+1)
			default:
				return i, false
			}
		}
	case c == 't':
		return i + 4, bytes.HasPrefix(data[i:], jsonTrue)
	case c == 'f':
		return i + 5, bytes.HasPrefix(data[i:], jsonFalse)
	case c == 'n':
		return i + 4, bytes.HasPrefix(data[i:], jsonNull)
	case c == '-' || '0' <= c && c <= '9':
		return validJSONNumber(data, i)
	}
	return i, false
}

// stopsString says whether any of the 8 bytes of x ends a run of a
// string's plain bytes: a control byte, a quote or a backslash. It may
// say so of a plain byte after one that does, never miss one.
func stopsString(x uint64) bool {
	const ones, highs = 0x0101010101010101, 0x8080808080808080
	quote, slash := x^(ones*'"'), x^(ones*'\\')
	return ((x-ones*0x20)&^x|(quote-ones)&^quote|(slash-ones)&^slash)&highs != 0
}

func validJSONString(data []byte, i int) (int, bool) {
	i++
	for i < len(data) {
		for i+8 <= len(data) && !stopsString(binary.LittleEndian.Uint64(data[i:])) {
			i += 8
		}
		if i >= len(data) {
			break
		}
		c := data[i]
		if !jsonStringByte[c] {
			i++
			continue
		}
		switch {
		case c == '"':
			return i + 1, true
		case c < 0x20:
			return i, false
		}
		// a backslash
		if i+1 >= len(data) {
			return i, false
		}
		switch data[i+1] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			i += 2
		case 'u':
			if i+6 > len(data) {
				return i, false
			}
			for _, h := range data[i+2 : i+6] {
				if !('0' <= h && h <= '9' || 'a' <= h && h <= 'f' || 'A' <= h && h <= 'F') {
					return i, false
				}
			}
			i += 6
		default:
			return i, false
		}
	}
	return i, false
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func validJSONNumber(data []byte, i int) (int, bool) {
	if data[i] == '-' {
		i++
	}
	switch {
	case i >= len(data):
		return i, false
	case data[i] == '0':
		i++
	case isDigit(data[i]):
		for i < len(data) && isDigit(data[i]) {
			i++
		}
	default:
		return i, false
	}
	if i < len(data) && data[i] == '.' {
		i++
		if i >= len(data) || !isDigit(data[i]) {
			return i, false
		}
		for i < len(data) && isDigit(data[i]) {
			i++
		}
	}
	if i < len(data) && (data[i] == 'e' || data[i] == 'E') {
		i++
		if i < len(data) && (data[i] == '+' || data[i] == '-') {
			i++
		}
		if i >= len(data) || !isDigit(data[i]) {
			return i, false
		}
		for i < len(data) && isDigit(data[i]) {
			i++
		}
	}
	return i, true
}

// topMember is the value of the member named key of body's top-level
// object, as decoding body into a map[string]json.RawMessage has it: the
// last of that name, the name read with its escapes. ok is false when
// body isn't a valid JSON object, found false when it has no such member.
// A long body is checked and split once (longBody), so a rewrite asking
// whether it has anything to do costs no walk over the conversation.
func topMember(body []byte, key string) (val []byte, found, ok bool) {
	var members []memberSpan
	if len(body) >= validMemoMin {
		var valid bool
		if valid, members = longBody(body); !valid {
			return nil, false, false
		}
	} else if !jsonCheck(body) {
		return nil, false, false
	}
	raw := trimJSON(body)
	if raw[0] != '{' {
		return nil, false, false
	}
	eachMemberIn(raw, members, func(k, v []byte) bool {
		if keyIs(k, key) {
			val, found = v, true
		}
		return true
	})
	return val, found, true
}

// withoutTopMember is body without its top-level members named key (every
// one, as a body may name a key twice), every other byte as it came, the
// comma and spaces of a member taken out going with it. A body that isn't a
// valid JSON object, or has no such member, is returned as it is: a body
// that can't be read is not rewritten.
func withoutTopMember(body []byte, key string) []byte {
	var members []memberSpan
	if len(body) >= validMemoMin {
		var valid bool
		if valid, members = longBody(body); !valid {
			return body
		}
	} else if !jsonCheck(body) {
		return body
	}
	start := skipJSONSpace(body, 0)
	raw := trimJSON(body)
	if raw[0] != '{' {
		return body
	}
	if members == nil {
		eachMemberAt(raw, func(k0, k1, v0, v1 int) bool {
			members = append(members, memberSpan{k0, k1, v0, v1})
			return true
		})
	}
	// each run of members named key is cut from its first key to the next
	// member's key, or, when it ends the object, from the end of the member
	// before it (its comma with it) to the end of its last value
	var cuts [][2]int
	for i := 0; i < len(members); {
		if !keyIs(raw[members[i].k0:members[i].k1], key) {
			i++
			continue
		}
		j := i + 1
		for j < len(members) && keyIs(raw[members[j].k0:members[j].k1], key) {
			j++
		}
		switch {
		case j < len(members):
			cuts = append(cuts, [2]int{members[i].k0, members[j].k0})
		case i > 0:
			cuts = append(cuts, [2]int{members[i-1].v1, members[j-1].v1})
		default:
			cuts = append(cuts, [2]int{1, len(raw) - 1})
		}
		i = j
	}
	if len(cuts) == 0 {
		return body
	}
	out := make([]byte, 0, len(body))
	out = append(out, body[:start]...)
	last := 0
	for _, c := range cuts {
		out = append(out, raw[last:c[0]]...)
		last = c[1]
	}
	out = append(out, raw[last:]...)
	return append(out, body[start+len(raw):]...)
}

// memberOf is topMember of obj, a valid JSON object.
func memberOf(obj []byte, key string) (val []byte, found bool) {
	eachMember(obj, func(k, v []byte) bool {
		if keyIs(k, key) {
			val, found = v, true
		}
		return true
	})
	return val, found
}

// keyIs reports whether quoted, a valid JSON string, is key.
func keyIs(quoted []byte, key string) bool {
	if len(quoted) == len(key)+2 && string(quoted[1:len(quoted)-1]) == key {
		return true
	}
	if bytes.IndexByte(quoted, '\\') < 0 {
		return false
	}
	s, _ := unquoteJSON(quoted)
	return s == key
}

// stringBytes is the string raw, a valid JSON value, holds, as encoding/json
// decodes it; false for any other kind of value. A plain ASCII string is
// raw's own bytes, nothing copied.
func stringBytes(raw []byte) ([]byte, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return nil, false
	}
	if s := raw[1 : len(raw)-1]; plainASCII(s) {
		return s, true
	}
	s, ok := unquoteJSON(raw)
	return []byte(s), ok
}

// plainASCII reports whether s, a JSON string's insides, is printable ASCII
// with no escape: what it decodes to is s itself.
func plainASCII(s []byte) bool {
	for _, c := range s {
		if c == '\\' || c >= 0x80 {
			return false
		}
	}
	return true
}

// hasPrefix is strings.HasPrefix of b, nothing copied.
func hasPrefix(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == prefix
}

// safeID is safeIDChars.Match of id, nothing allocated.
func safeID(id []byte) bool {
	for _, c := range id {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return len(id) > 0
}

// inputItems calls f with each item of a Responses body's input, as
// decoding the body into a map[string]json.RawMessage and its input into
// a []json.RawMessage has them, until f returns false. ok is false when
// the body isn't a valid object or its input isn't an array (nor null,
// which has no items).
func inputItems(body []byte, f func(item []byte) bool) (ok bool) {
	in, found, ok := topMember(body, "input")
	if !ok {
		return false
	}
	if !found || string(in) == "null" {
		return true
	}
	if in[0] != '[' {
		return false
	}
	eachElement(in, f)
	return true
}

// anyInputItem reports whether any of a Responses body's input items
// (inputItems) is one f is looking for: whether a rewrite of them has
// anything to do, told without decoding the conversation. Most turns it
// hasn't, and each rewrite decoded all of it and the decoding was most
// of the CPU of a long Codex session's turn (#1532, #2354).
func anyInputItem(body []byte, f func(item []byte) bool) (found bool) {
	inputItems(body, func(it []byte) bool {
		found = f(it)
		return !found
	})
	return found
}

// memberOrNil is memberOf of it when it is an object, as decoding it into
// a map[string]json.RawMessage reads it, and nil for any other value.
func memberOrNil(it []byte, key string) []byte {
	if len(it) == 0 || it[0] != '{' {
		return nil
	}
	v, _ := memberOf(it, key)
	return v
}

// marshalRaw is json.Marshal of v, or marshalPlain's encoding with
// escapeHTML false, for a map[string]json.RawMessage or a
// []json.RawMessage: a request body split at its members or its items
// and put back together. encoding/json writes each raw value through its
// scanner, a call a byte, so a Codex conversation of 5 MB with one item
// changed cost tens of milliseconds to write back, on every turn and by
// each rewrite (#1532). Here each value is checked by jsonCheck and
// copied, its strings whole; the bytes are encoding/json's. ok is false
// for any other type, and for a value encoding/json would refuse, which
// the caller then gives encoding/json to say so.
func marshalRaw(v any, escapeHTML bool) (_ []byte, ok bool) {
	switch v := v.(type) {
	case map[string]json.RawMessage:
		if v == nil {
			return []byte("null"), true
		}
		n := 2
		keys := make([]string, 0, len(v))
		for k, raw := range v {
			n += len(k) + len(raw) + 4
			keys = append(keys, k)
		}
		slices.Sort(keys)
		dst := make([]byte, 0, n)
		dst = append(dst, '{')
		for i, k := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			if dst, ok = appendJSONKey(dst, k, escapeHTML); !ok {
				return nil, false
			}
			dst = append(dst, ':')
			if dst, ok = appendRawJSON(dst, v[k], escapeHTML); !ok {
				return nil, false
			}
		}
		return append(dst, '}'), true
	case []json.RawMessage:
		if v == nil {
			return []byte("null"), true
		}
		n := 2
		for _, raw := range v {
			n += len(raw) + 1
		}
		dst := make([]byte, 0, n)
		dst = append(dst, '[')
		for i, raw := range v {
			if i > 0 {
				dst = append(dst, ',')
			}
			if dst, ok = appendRawJSON(dst, raw, escapeHTML); !ok {
				return nil, false
			}
		}
		return append(dst, ']'), true
	}
	return nil, false
}

// appendJSONKey is key quoted as encoding/json quotes a string: as it is
// when it is printable ASCII with nothing in it to escape, and otherwise
// by encoding/json itself.
func appendJSONKey(dst []byte, key string, escapeHTML bool) ([]byte, bool) {
	for i := 0; i < len(key); i++ {
		if c := key[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || escapeHTML && (c == '<' || c == '>' || c == '&') {
			var b bytes.Buffer
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(escapeHTML)
			if enc.Encode(key) != nil {
				return dst, false
			}
			return append(dst, bytes.TrimRight(b.Bytes(), "\n")...), true
		}
	}
	dst = append(dst, '"')
	dst = append(dst, key...)
	return append(dst, '"'), true
}

// appendRawJSON is what encoding/json writes of raw as a json.RawMessage:
// null for nil, and otherwise raw compacted (appendCompactJSON); false
// when raw isn't valid JSON.
func appendRawJSON(dst []byte, raw json.RawMessage, escapeHTML bool) ([]byte, bool) {
	if raw == nil {
		return append(dst, "null"...), true
	}
	if !jsonCheck(raw) {
		return dst, false
	}
	return appendCompactJSON(dst, raw, escapeHTML), true
}

// appendCompactJSON is encoding/json's appendCompact of src, a valid JSON
// value: without the space between its tokens, and with <, >, & and
// U+2028, U+2029 escaped when escapeHTML, as json.Marshal writes them.
// Those are only ever in a string, where a valid value's space is too, so
// strings are copied whole, or looked through for those alone.
func appendCompactJSON(dst, src []byte, escapeHTML bool) []byte {
	start := 0
	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"':
			j := skipJSONString(src, i)
			if escapeHTML {
				dst = append(dst, src[start:i]...)
				dst = appendHTMLEscaped(dst, src[i:j])
				start = j
			}
			i = j
		case isJSONSpace(c):
			dst = append(dst, src[start:i]...)
			i++
			start = i
		default:
			i++
		}
	}
	return append(dst, src[start:]...)
}

// appendHTMLEscaped is s with <, >, & and U+2028, U+2029 as \u escapes.
func appendHTMLEscaped(dst, s []byte) []byte {
	const hex = "0123456789abcdef"
	start := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '<' || c == '>' || c == '&':
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			start = i + 1
		case c == 0xE2 && i+2 < len(s) && s[i+1] == 0x80 && s[i+2]&^1 == 0xA8:
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hex[s[i+2]&0xF])
			start = i + 3
			i += 2
		}
	}
	return append(dst, s[start:]...)
}

// getIn is gjson.GetBytes read in place: what it finds points into body
// rather than into a copy. GetBytes copies the value it finds, and for a
// conversation's list that is the conversation, 45 MiB of a Codex turn
// for one look at its items (#4004). A string kept from the result keeps
// all of body: clone one that outlives the request, and never change body
// while one is held.
func getIn(body []byte, path string) gjson.Result {
	return gjson.Get(unsafe.String(unsafe.SliceData(body), len(body)), path)
}

// marshalBody is json.Marshal of a request body being built, byte for
// byte, its lists (the messages, the input items) written an item at a
// time into a buffer of the size they come to. json.Marshal writes the
// whole body into one buffer that doubles as it fills, then copies it
// out: a 45 MB Codex conversation built for Chat took the heap from 45 to
// 316 MiB, the buffers it outgrew being garbage until the next GC (#4004,
// jihuayu's profile on a 2 GiB server). Here no buffer outgrows the
// largest item. A value json.Marshal refuses gets json.Marshal's answer.
//
// spent names the list the builder made for this body alone (its
// messages, its input items): each of its items is let go once written,
// so the built conversation, strings joined from the request's parts
// among it, is garbage while the body fills instead of held beside it to
// the end (#4004). A value json.Marshal refuses is refused still, and the
// list is then left part spent.
//
// Past bodyKept of them, the written items aren't kept for the body
// either: each is measured, its bytes written to no buffer, and written
// again into the body once that is made. Kept, they were a copy of the
// conversation beside the body at its peak; written twice costs a large
// body's encoding once more, and a body under bodyKept nothing.
func marshalBody(out map[string]any, spent string) ([]byte, error) {
	keys := make([]string, 0, len(out))
	for k := range out {
		// a key json.Marshal writes another way, before anything is spent
		if _, ok := appendJSONKey(nil, k, true); !ok {
			return json.Marshal(out)
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	vals := make([][]json.RawMessage, len(keys))
	kept := bodyKept
	n := 2
	for i, k := range keys {
		items, sizes, ok := marshalItems(out[k], k == spent, &kept)
		if !ok {
			return json.Marshal(out)
		}
		vals[i] = items
		n += len(k) + 4 + len(items) + sizes
	}
	dst := make([]byte, 0, n)
	dst = append(dst, '{')
	for i, k := range keys {
		if i > 0 {
			dst = append(dst, ',')
		}
		var ok bool
		if dst, ok = appendJSONKey(dst, k, true); !ok {
			return json.Marshal(out)
		}
		dst = append(dst, ':')
		items := vals[i]
		vals[i] = nil
		if !isSplitList(out[k]) {
			dst = append(dst, items[0]...)
			continue
		}
		dst = append(dst, '[')
		list := reflect.ValueOf(out[k])
		for j, it := range items {
			if j > 0 {
				dst = append(dst, ',')
			}
			if it != nil {
				dst = append(dst, it...)
				continue
			}
			// measured only: written now, straight into the body
			var err error
			if dst, err = appendJSONValue(dst, list.Index(j).Addr().Interface()); err != nil {
				return nil, err
			}
			if k == spent {
				list.Index(j).SetZero()
			}
		}
		dst = append(dst, ']')
	}
	return append(dst, '}'), nil
}

// isSplitList says whether marshalItems writes v an item at a time: a
// slice of two or more that isn't bytes and doesn't write itself.
func isSplitList(v any) bool {
	if _, ok := v.(json.Marshaler); ok {
		return false
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() != reflect.Uint8 && rv.Len() > 1
}

// marshalItems is v as json.Marshal writes it: its items each on their
// own for a list isSplitList takes, or one piece; false when json.Marshal
// refuses any of it, and the bytes they come to. An item is kept while
// *kept, the bytes left to keep, is above 0; past it an item is only
// measured, nil among the items, and left as it was. spend sets each item
// kept to its zero value.
func marshalItems(v any, spend bool, kept *int) ([]json.RawMessage, int, bool) {
	if !isSplitList(v) {
		b, err := marshalSized(v)
		*kept -= len(b)
		return []json.RawMessage{b}, len(b), err == nil
	}
	rv := reflect.ValueOf(v)
	items := make([]json.RawMessage, rv.Len())
	size := 0
	for i := range items {
		// by its address, as json.Marshal reaches a slice's items: a
		// MarshalJSON on the pointer is called
		it := rv.Index(i).Addr().Interface()
		if *kept <= 0 {
			n, ok := jsonSize(it)
			if !ok {
				return nil, 0, false
			}
			size += n
			continue
		}
		b, err := marshalSized(it)
		if err != nil {
			return nil, 0, false
		}
		items[i] = b
		size += len(b)
		*kept -= len(b)
		if spend {
			rv.Index(i).SetZero()
		}
	}
	return items, size, true
}

// marshalSized is json.Marshal(v), written into one slice of its size.
// json.Marshal grows its buffer by doubling and then copies it out, so a
// long turn's text cost three to four times its size again on every
// request to a Chat upstream. The maps, lists, strings, bools and ints a
// body is built of are written here as encoding/json writes them; any
// other value is json.Marshal's own.
func marshalSized(v any) ([]byte, error) {
	n, ok := jsonSize(v)
	if !ok {
		return json.Marshal(v)
	}
	return appendJSONValue(make([]byte, 0, n), v)
}

// jsonSize is how long json.Marshal(v) is; false when it fails.
func jsonSize(v any) (int, bool) {
	switch v := v.(type) {
	case nil:
		return 4, true
	case string:
		return jsonStringSize(v), true
	case bool:
		if v {
			return 4, true
		}
		return 5, true
	case int:
		var b [20]byte
		return len(strconv.AppendInt(b[:0], int64(v), 10)), true
	case int64:
		var b [20]byte
		return len(strconv.AppendInt(b[:0], v, 10)), true
	case *any:
		// a []any's item, by its address
		if v == nil {
			return 4, true
		}
		return jsonSize(*v)
	case *map[string]any:
		if v == nil {
			return 4, true
		}
		return jsonSize(*v)
	case map[string]any:
		if v == nil {
			return 4, true
		}
		n := 2 + max(len(v)-1, 0)
		for k, x := range v {
			if !plainJSONKey(k) {
				return 0, false
			}
			m, ok := jsonSize(x)
			if !ok {
				return 0, false
			}
			n += len(k) + 3 + m
		}
		return n, true
	case *[]map[string]any:
		if v == nil {
			return 4, true
		}
		return jsonSize(*v)
	case []map[string]any:
		if v == nil {
			return 4, true
		}
		n := 2 + max(len(v)-1, 0)
		for _, x := range v {
			m, ok := jsonSize(x)
			if !ok {
				return 0, false
			}
			n += m
		}
		return n, true
	case *[]any:
		if v == nil {
			return 4, true
		}
		return jsonSize(*v)
	case []any:
		if v == nil {
			return 4, true
		}
		n := 2 + max(len(v)-1, 0)
		for _, x := range v {
			m, ok := jsonSize(x)
			if !ok {
				return 0, false
			}
			n += m
		}
		return n, true
	}
	b, err := json.Marshal(v)
	return len(b), err == nil
}

// appendJSONValue appends json.Marshal(v) to dst (jsonSize's values).
func appendJSONValue(dst []byte, v any) ([]byte, error) {
	switch v := v.(type) {
	case nil:
		return append(dst, "null"...), nil
	case string:
		return appendJSONString(dst, v), nil
	case bool:
		return strconv.AppendBool(dst, v), nil
	case int:
		return strconv.AppendInt(dst, int64(v), 10), nil
	case int64:
		return strconv.AppendInt(dst, v, 10), nil
	case *any:
		if v == nil {
			return append(dst, "null"...), nil
		}
		return appendJSONValue(dst, *v)
	case *map[string]any:
		if v == nil {
			return append(dst, "null"...), nil
		}
		return appendJSONValue(dst, *v)
	case map[string]any:
		if v == nil {
			return append(dst, "null"...), nil
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			if !plainJSONKey(k) {
				return appendMarshalled(dst, v)
			}
			keys = append(keys, k)
		}
		slices.Sort(keys)
		dst = append(dst, '{')
		for i, k := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = append(dst, '"')
			dst = append(dst, k...)
			dst = append(dst, '"', ':')
			var err error
			if dst, err = appendJSONValue(dst, v[k]); err != nil {
				return dst, err
			}
		}
		return append(dst, '}'), nil
	case *[]map[string]any:
		if v == nil {
			return append(dst, "null"...), nil
		}
		return appendJSONValue(dst, *v)
	case []map[string]any:
		if v == nil {
			return append(dst, "null"...), nil
		}
		dst = append(dst, '[')
		for i, x := range v {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = appendJSONValue(dst, x); err != nil {
				return dst, err
			}
		}
		return append(dst, ']'), nil
	case *[]any:
		if v == nil {
			return append(dst, "null"...), nil
		}
		return appendJSONValue(dst, *v)
	case []any:
		if v == nil {
			return append(dst, "null"...), nil
		}
		dst = append(dst, '[')
		for i, x := range v {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = appendJSONValue(dst, x); err != nil {
				return dst, err
			}
		}
		return append(dst, ']'), nil
	}
	return appendMarshalled(dst, v)
}

func appendMarshalled(dst []byte, v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return dst, err
	}
	return append(dst, b...), nil
}

// plainJSONKey is a key json.Marshal writes as it is, between quotes.
func plainJSONKey(k string) bool {
	for i := 0; i < len(k); i++ {
		if c := k[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

// jsonStringSize is how long json.Marshal(s) is.
func jsonStringSize(s string) int {
	n := 2
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"', c == '\\', c == '\b', c == '\f', c == '\n', c == '\r', c == '\t':
				n += 2
			case c < 0x20, c == '<', c == '>', c == '&':
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 || r == '\u2028' || r == '\u2029' {
			n += 6
		} else {
			n += size
		}
		i += size
	}
	return n
}

// appendJSONString appends s as encoding/json writes a string, its HTML
// characters escaped: invalid UTF-8 as \ufffd, and U+2028 and U+2029
// escaped.
func appendJSONString(dst []byte, s string) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, `\ufffd`...)
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hex[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// bodyKept is how many bytes of a body's written items marshalBody keeps
// to put in the body; past it they are measured and written again.
var bodyKept = 4 << 20

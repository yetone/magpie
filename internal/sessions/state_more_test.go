package sessions

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

// TestStateMoreZeroKnowsEveryField: a StateMore with any one field set is
// not zero, so a parse or a decode never drops what it holds; a field
// added to StateMore and not to zero() fails here.
func TestStateMoreZeroKnowsEveryField(t *testing.T) {
	if !(*StateMore)(nil).zero() || !(&StateMore{}).zero() {
		t.Fatal("an empty StateMore isn't zero")
	}
	typ := reflect.TypeFor[StateMore]()
	for i := range typ.NumField() {
		var m StateMore
		f := reflect.ValueOf(&m).Elem().Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x")
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		case reflect.Map:
			f.Set(reflect.MakeMap(f.Type()))
			f.SetMapIndex(reflect.New(f.Type().Key()).Elem(), reflect.New(f.Type().Elem()).Elem())
		case reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
		case reflect.Struct:
			if f.Type() != reflect.TypeFor[time.Time]() {
				t.Fatalf("%s: a struct field zero() can't be checked for", typ.Field(i).Name)
			}
			f.Set(reflect.ValueOf(time.Unix(1, 0)))
		default:
			t.Fatalf("%s: kind %s not set by this test", typ.Field(i).Name, f.Kind())
		}
		if m.zero() || moreOf(m) == nil {
			t.Errorf("%s set, and zero() says the StateMore is empty", typ.Field(i).Name)
		}
	}
}

// TestStateMoreReadsLeaveTheSharedEmptyOne: reading a state with no extra
// makes none and finds every field empty, and a write makes its own.
func TestStateMoreReadsLeaveTheSharedEmptyOne(t *testing.T) {
	s := &state{}
	if s.more().Named != "" || s.more().Agy != nil || s.StateMore != nil {
		t.Fatal("a read of an empty state made or found an extra")
	}
	s.own().Named = "n"
	if s.StateMore == nil || s.more().Named != "n" || !noMore.zero() || (&state{}).more().Named != "" {
		t.Fatal("a write through own() reached the shared empty StateMore")
	}
}

// TestStateCloneOwnsItsMore: a clone's extra is its own, so growing the
// clone (as every parse after the first does) leaves the kept state alone.
func TestStateCloneOwnsItsMore(t *testing.T) {
	s := &state{StateMore: &StateMore{Named: "a", Convs: []string{"c1"}, CodexSpawnNames: map[string]string{"k": "v"}, Compacts: []string{"u"}}}
	c := s.clone()
	if c.StateMore == s.StateMore {
		t.Fatal("the clone shares the kept state's StateMore")
	}
	c.own().Named = "b"
	c.own().Convs[0] = "c2"
	c.own().CodexSpawnNames["k"] = "w"
	c.own().Compacts = append(c.more().Compacts, "u2")
	if s.Named != "a" || s.Convs[0] != "c1" || s.CodexSpawnNames["k"] != "v" || len(s.Compacts) != 1 {
		t.Fatalf("a change to the clone reached the kept state: %+v", *s.StateMore)
	}
	if (&state{}).clone().StateMore != nil {
		t.Fatal("a clone of a state with no extra made one")
	}
}

// TestCacheKeepsNoEmptyMore: a cache written with states that have no
// extra, or an empty one, reads back with none, and one with an extra
// reads back with it.
func TestCacheKeepsNoEmptyMore(t *testing.T) {
	c := cacheFile{Version: cacheVersion, Zone: "UTC", Parsers: parseVersions, Files: map[string]*state{
		"/a": {Agent: "claude", Size: 1, Title: "t"},
		"/b": {Agent: "claude", Size: 2, StateMore: &StateMore{}},
		"/c": {Agent: "claude", Size: 3, StateMore: &StateMore{Named: "named", Root: "r"}},
	}}
	var b bytes.Buffer
	if err := encodeCache(&b, c); err != nil {
		t.Fatal(err)
	}
	files, ok := decodeCache(b.Bytes(), "UTC")
	if !ok || len(files) != 3 {
		t.Fatalf("decode: %v %d", ok, len(files))
	}
	if files["/a"].StateMore != nil || files["/b"].StateMore != nil {
		t.Fatal("an empty extra was kept by the decode")
	}
	if m := files["/c"].StateMore; m == nil || m.Named != "named" || m.Root != "r" {
		t.Fatalf("an extra was lost: %+v", m)
	}
}

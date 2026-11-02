package settingsapi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// home gives the test a home of its own, with no settings.json yet.
func home(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	testenv.SetHome(t, root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("APPDATA", filepath.Join(root, "AppData", "Roaming"))
}

// sample is a value of t that isn't was, as JSON carries it.
func sample(t reflect.Type, key string, was reflect.Value) reflect.Value {
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.Bool:
		v.SetBool(!was.Bool())
	case reflect.String:
		s := "x"
		switch key {
		case "vision", "imageGen", "searcher", "searcherThen":
			s = "off" // a model is checked against the catalog
		}
		for _, c := range Choices[key] {
			if c != was.String() {
				s = c
				break
			}
		}
		v.SetString(s)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(was.Int() + 7)
	case reflect.Float64, reflect.Float32:
		v.SetFloat(was.Float() + 1.5)
	case reflect.Pointer:
		elem := reflect.Zero(t.Elem())
		if !was.IsNil() {
			elem = was.Elem()
		}
		v.Set(reflect.New(t.Elem()))
		v.Elem().Set(sample(t.Elem(), key, elem))
	case reflect.Slice:
		el := sample(t.Elem(), key, reflect.Zero(t.Elem()))
		v.Set(reflect.Append(reflect.MakeSlice(t, 0, 1), el))
	case reflect.Map:
		v.Set(reflect.MakeMap(t))
		v.SetMapIndex(sample(t.Key(), "", reflect.Zero(t.Key())), sample(t.Elem(), "", reflect.Zero(t.Elem())))
	case reflect.Struct:
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				v.Field(i).Set(sample(f.Type, "", reflect.Zero(f.Type)))
			}
		}
	}
	return v
}

// Keys are the fields POST /api/settings saves from what it's sent, no
// more and no fewer: each field of settings.Settings is sent changed, on
// its own, and the ones the save takes are compared with Keys. A field
// added to Settings that the save takes (or carries) without Keys knowing
// fails here, and so `magpie settings` can't fall behind the window
// (#4745).
func TestKeysAreWhatTheSaveWrites(t *testing.T) {
	cur := settings.Defaults()
	curJSON, err := json.Marshal(cur)
	if err != nil {
		t.Fatal(err)
	}
	st := reflect.TypeFor[settings.Settings]()
	var written []string
	for i := range st.NumField() {
		f := st.Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		was := reflect.ValueOf(cur).Field(i)
		val := sample(f.Type, key, was)
		b, err := json.Marshal(val.Interface())
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(curJSON, &m)
		m[key] = b
		body, _ := json.Marshal(m)
		in, err := Merge(body, cur, false)
		if err != nil {
			t.Fatalf("%s sent as %s: %v", key, b, err)
		}
		if !reflect.DeepEqual(reflect.ValueOf(in).Field(i).Interface(), was.Interface()) {
			written = append(written, key)
		}
	}
	// settings.Save keeps the file's own: the page can send it, and it
	// is never written by this save (SetSessionReads writes it)
	written = slices.DeleteFunc(written, func(k string) bool { return k == "sessionReads" })
	got, want := slices.Sorted(slices.Values(written)), slices.Sorted(slices.Values(Keys))
	if !slices.Equal(got, want) {
		t.Errorf("the save writes keys Keys doesn't name, or Keys names keys it doesn't write:\n only saved: %v\n only in Keys: %v",
			minus(got, want), minus(want, got))
	}
	for _, k := range Keys {
		if _, ok := Field(k); !ok {
			t.Errorf("Keys names %q, which settings.Settings doesn't have", k)
		}
	}
	if len(Keys) != len(slices.Compact(slices.Sorted(slices.Values(Keys)))) {
		t.Error("Keys names a key twice")
	}
}

func minus(a, b []string) []string {
	var out []string
	for _, k := range a {
		if !slices.Contains(b, k) {
			out = append(out, k)
		}
	}
	return out
}

// Setting one key through Body leaves every other the user set as it
// is, and a key the save keeps when it isn't sent is set even to its
// zero value.
func TestBodySetsOneKeyOnly(t *testing.T) {
	home(t)
	s := settings.Defaults()
	s.Theme, s.NoBird, s.RedactShape, s.Currency, s.Proxy = "dark", true, true, "cny", "http://127.0.0.1:7890"
	two := 2
	s.RefusalRetries = &two
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	set := func(key, value string) settings.Settings {
		t.Helper()
		cur, err := settings.LoadChecked()
		if err != nil {
			t.Fatal(err)
		}
		body, err := Body(cur, key, json.RawMessage(value))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Apply(body, false); err != nil {
			t.Fatalf("%s=%s: %v", key, value, err)
		}
		return settings.Load()
	}
	got := set("noBird", "false")
	if got.NoBird {
		t.Error("noBird set to false stayed on: a zero value was read as not sent")
	}
	got = set("lang", `"ja"`)
	if got.Lang != "ja" || got.Theme != "dark" || !got.RedactShape || got.Currency != "cny" ||
		got.Proxy != "http://127.0.0.1:7890" || got.RefusalRetries == nil || *got.RefusalRetries != 2 {
		t.Errorf("setting lang changed what the user set: %+v", got)
	}
}

// A value the save refuses writes nothing, whichever way it came in.
func TestRefusedValueWritesNothing(t *testing.T) {
	home(t)
	s := settings.Defaults()
	s.Theme = "dark"
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(settings.Path())
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"theme":          `"neon"`,
		"lang":           `"xx"`,
		"trayUsageEvery": `4`,
		"vision":         `"nobody/nothing"`,
		"searcher":       `"nobody/nothing"`,
		"usageAlert":     `101`,
		"updateChannel":  `"nightly"`,
	} {
		cur := settings.Load()
		body, err := Body(cur, key, json.RawMessage(value))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Apply(body, false); err == nil {
			t.Errorf("%s=%s was saved", key, value)
		}
		after, _ := os.ReadFile(settings.Path())
		if !bytes.Equal(before, after) {
			t.Fatalf("%s=%s was refused but settings.json changed", key, value)
		}
	}
	if _, err := Body(settings.Load(), "port", json.RawMessage(`1`)); err == nil {
		t.Error("Body took port, which has a route of its own")
	}
}

// "Don't ask again" on the Quit question (zhujiu on Discord) holds across
// a save from a page that doesn't send quitNoAsk (the tray panel, or a
// page from before it), and Settings › General's Ask before quitting
// sending it false turns the question back on.
func TestQuitNoAskKeptUnlessSent(t *testing.T) {
	cur := settings.Defaults()
	cur.QuitNoAsk = true
	in, err := Merge([]byte(`{"theme":"dark"}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if !in.QuitNoAsk {
		t.Error("a save that didn't send quitNoAsk turned the Quit question back on")
	}
	in, err = Merge([]byte(`{"theme":"dark","quitNoAsk":false}`), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if in.QuitNoAsk {
		t.Error("quitNoAsk sent false left the Quit question off")
	}
}

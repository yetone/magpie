package catalog

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// A model is named as most of the providers serving it name it, matched
// without a vendor's prefix and in any case; a name that is only the id
// casts no vote.
func TestNameOf(t *testing.T) {
	writeCatalog(t, `{
	  "zai": {"models": {"glm-5-turbo": {"id":"glm-5-turbo","name":"GLM-5-Turbo"}}},
	  "zai-coding-plan": {"models": {"glm-5-turbo": {"id":"glm-5-turbo","name":"GLM-5-Turbo"}}},
	  "a": {"models": {"z-ai/glm-5-turbo": {"id":"z-ai/glm-5-turbo","name":"GLM 5 Turbo"}}},
	  "b": {"models": {"glm-5-turbo": {"id":"glm-5-turbo","name":"glm-5-turbo"}},
	        "plain": {"id":"plain","name":"plain"}}
	}`)
	for id, want := range map[string]string{
		"glm-5-turbo":      "GLM-5-Turbo",
		"GLM-5-Turbo":      "GLM-5-Turbo",
		"z-ai/glm-5-turbo": "GLM-5-Turbo",
		"plain":            "",
		"unknown":          "",
	} {
		if got := NameOf(id); got != want {
			t.Errorf("NameOf(%q) = %q, want %q", id, got, want)
		}
	}
	in := []Model{
		{ID: "glm-5-turbo", Name: "glm-5-turbo"},
		{ID: "GLM-5-Turbo"},
		{ID: "glm-5-turbo", Name: "Turbo"}, // the list's own name stays
		{ID: "plain", Name: "plain"},
	}
	var got []string
	for _, m := range Named(in) {
		got = append(got, m.ID+"|"+m.Name)
	}
	// These exact ids differ, so the shared name would hide that distinction.
	// NameOf still matches either case; Named keeps each id in this list.
	want := "glm-5-turbo|glm-5-turbo\nGLM-5-Turbo|GLM-5-Turbo\nglm-5-turbo|Turbo\nplain|plain"
	if strings.Join(got, "\n") != want {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
	if in[0].Name != "glm-5-turbo" {
		t.Fatalf("the list given was changed: %q", in[0].Name)
	}
}

func TestNamedAvoidsCollisions(t *testing.T) {
	writeCatalog(t, `{"vendor":{"models":{
	  "flash":{"id":"flash","name":"Flash"},
	  "fast":{"id":"fast","name":"Flash"},
	  "other":{"id":"other","name":"Other"},
	  "alias":{"id":"alias","name":"fast"}
	}}}`)
	for _, c := range []struct {
		name     string
		in, want []Model
	}{
		{"supplemented", []Model{{ID: "flash", Name: "flash"}, {ID: "fast"}}, []Model{{ID: "flash", Name: "flash"}, {ID: "fast", Name: "fast"}}},
		{"explicit", []Model{{ID: "flash", Name: "Flash"}, {ID: "fast", Name: "fast"}, {ID: "other"}}, []Model{{ID: "flash", Name: "Flash"}, {ID: "fast", Name: "fast"}, {ID: "other", Name: "Other"}}},
		{"explicit duplicates", []Model{{ID: "flash", Name: "Vendor"}, {ID: "fast", Name: "Vendor"}}, []Model{{ID: "flash", Name: "Vendor"}, {ID: "fast", Name: "Vendor"}}},
		{"fallback id", []Model{{ID: "alias"}, {ID: "fast"}}, []Model{{ID: "alias", Name: "alias"}, {ID: "fast", Name: "Flash"}}},
		{"same id", []Model{{ID: "flash"}, {ID: "flash", Name: "flash"}}, []Model{{ID: "flash", Name: "Flash"}, {ID: "flash", Name: "Flash"}}},
		{"unknown", []Model{{ID: "unknown", Name: "unknown"}}, []Model{{ID: "unknown", Name: "unknown"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				in, want := slices.Clone(c.in), slices.Clone(c.want)
				if reverse {
					slices.Reverse(in)
					slices.Reverse(want)
				}
				before := slices.Clone(in)
				if got := Named(in); !reflect.DeepEqual(got, want) {
					t.Errorf("Named(%+v) = %+v, want %+v", in, got, want)
				}
				if !reflect.DeepEqual(in, before) {
					t.Errorf("input changed: %+v", in)
				}
			}
		})
	}
}

func TestDecorateAvoidsNameCollisions(t *testing.T) {
	known := []Model{{ID: "flash", Name: "Flash", Context: 1000000}, {ID: "fast", Name: "Flash", Context: 1000000}}
	for _, explicit := range []bool{false, true} {
		in := []Model{{ID: "vendor/flash", Name: "vendor/flash"}, {ID: "vendor/fast", Name: "vendor/fast"}}
		want := slices.Clone(in)
		if explicit {
			in[0].Name, want[0].Name = "Flash", "Flash"
		}
		for i := range want {
			want[i].Context = 1000000
		}
		for _, reverse := range []bool{false, true} {
			if reverse {
				slices.Reverse(in)
				slices.Reverse(want)
			}
			before := slices.Clone(in)
			if got := Decorate(in, known); !reflect.DeepEqual(got, want) {
				t.Errorf("Decorate(%+v) = %+v, want %+v", in, got, want)
			}
			if !reflect.DeepEqual(in, before) {
				t.Errorf("input changed: %+v", in)
			}
		}
	}
}

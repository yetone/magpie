package fonts

import (
	"math"
	"reflect"
	"testing"
)

func TestGroupInstalledStyles(t *testing.T) {
	regular := Face{Family: "HarmonyOS Sans SC", Name: "Regular", Weight: 400, Style: "normal", Stretch: 100}
	medium := regular
	medium.Name, medium.Weight = "Medium", 500
	italic := regular
	italic.Name, italic.Style = "Italic", "italic"
	chinese := Face{Family: "思源黑体", Name: "Regular", Weight: 400, Style: "normal", Stretch: 100}
	got := group([]Face{chinese, medium, italic, regular, medium, {Family: "invalid"}})
	want := []Family{{Name: regular.Family, Styles: []Face{regular, italic, medium}}, {Name: chinese.Family, Styles: []Face{chinese}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installed families and styles = %#v, want %#v", got, want)
	}
	if got := group(nil); got == nil || len(got) != 0 {
		t.Fatalf("an empty successful collection must be [], got %#v", got)
	}
}

func TestGroupMenloStyleOrder(t *testing.T) {
	faces := []Face{
		{Family: "Menlo", Name: "Bold Italic", Weight: 700, Style: "italic", Stretch: 100},
		{Family: "Menlo", Name: "Italic", Weight: 400, Style: "italic", Stretch: 100},
		{Family: "Menlo", Name: "Bold", Weight: 700, Style: "normal", Stretch: 100},
		{Family: "Menlo", Name: "Regular", Weight: 400, Style: "normal", Stretch: 100},
	}
	styles := group(faces)[0].Styles
	want := []Face{faces[3], faces[1], faces[2], faces[0]}
	if !reflect.DeepEqual(styles, want) {
		t.Fatalf("Menlo style menu = %v, want Regular, Italic, Bold, Bold Italic", styles)
	}
}

func TestGroupStyleOrderKeepsWeightWidthAndName(t *testing.T) {
	faces := []Face{
		{Family: "Ordering fixture", Name: "Oblique", Weight: 400, Style: "oblique", Stretch: 50},
		{Family: "Ordering fixture", Name: "Italic", Weight: 400, Style: "italic", Stretch: 75},
		{Family: "Ordering fixture", Name: "Regular", Weight: 400, Style: "normal", Stretch: 100},
		{Family: "Ordering fixture", Name: "Book", Weight: 400, Style: "normal", Stretch: 100},
		{Family: "Ordering fixture", Name: "Condensed", Weight: 400, Style: "normal", Stretch: 75},
		{Family: "Ordering fixture", Name: "Light Oblique", Weight: 300, Style: "oblique", Stretch: 100},
	}
	styles := group(faces)[0].Styles
	want := []Face{faces[5], faces[4], faces[3], faces[2], faces[1], faces[0]}
	if !reflect.DeepEqual(styles, want) {
		t.Fatalf("style menu = %v, want %v", styles, want)
	}
}

func TestValidateChoice(t *testing.T) {
	f := Face{Family: `Reader's "中文"`, Name: "Medium", Weight: 500, Style: "normal", Stretch: 100}
	if err := Validate(&f); err != nil {
		t.Fatal(err)
	}
	if err := Validate(nil); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Face){
		func(f *Face) { f.Family = "" }, func(f *Face) { f.Name = "\x00" },
		func(f *Face) { f.Weight = 0 }, func(f *Face) { f.Weight = 1001 },
		func(f *Face) { f.Style = "bold" }, func(f *Face) { f.Stretch = -1 },
		func(f *Face) { f.Weight = math.NaN() }, func(f *Face) { f.Stretch = math.Inf(1) },
	} {
		bad := f
		change(&bad)
		if Validate(&bad) == nil {
			t.Fatalf("accepted invalid font %#v", bad)
		}
	}
}

func TestVariableTraitsKeepCSSPrecision(t *testing.T) {
	for _, width := range []float64{0, 25, 87.5, 250} {
		f := Face{Family: "Variable Sans", Name: "Book", Weight: 425.5, Style: "normal", Stretch: width}
		if err := Validate(&f); err != nil {
			t.Fatal(err)
		}
		got := group([]Face{f})
		if len(got) != 1 || len(got[0].Styles) != 1 || got[0].Styles[0] != f {
			t.Fatalf("lost variable traits: %#v", got)
		}
	}
}

func TestInstalledFonts(t *testing.T) {
	if !Available {
		t.Skip("this build has no desktop font API")
	}
	families, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) == 0 {
		t.Fatal("the system font collection is empty")
	}
	count := 0
	for _, family := range families {
		for _, face := range family.Styles {
			if face.Family != family.Name {
				t.Fatalf("wrong family: %#v", face)
			}
			if err := Validate(&face); err != nil {
				t.Fatalf("%#v: %v", face, err)
			}
			count++
		}
	}
	t.Logf("read %d installed families and %d styles", len(families), count)
}

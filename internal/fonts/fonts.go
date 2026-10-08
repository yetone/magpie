// Package fonts describes the installed faces a desktop page can choose.
// It returns names and CSS traits, never font files or their paths.
package fonts

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
)

// Face is one installed style. Name is the font's own style name; Style is
// its CSS slope. Stretch is a percentage, and Weight is an OpenType weight.
type Face struct {
	Family  string  `json:"family"`
	Name    string  `json:"name"`
	Weight  float64 `json:"weight"`
	Style   string  `json:"style"`
	Stretch float64 `json:"stretch"`
}

type Family struct {
	Name   string `json:"name"`
	Styles []Face `json:"styles"`
}

// Validate also accepts an absent choice, which keeps the platform's stack.
// Availability is separate: a font removed since it was chosen stays saved.
func Validate(f *Face) error {
	if f == nil {
		return nil
	}
	for _, s := range []string{f.Family, f.Name} {
		if strings.TrimSpace(s) == "" || len(s) > 512 || strings.ContainsFunc(s, unicode.IsControl) {
			return fmt.Errorf("font family and style must be nonempty names without control characters")
		}
	}
	if math.IsNaN(f.Weight) || math.IsInf(f.Weight, 0) || f.Weight < 1 || f.Weight > 1000 {
		return fmt.Errorf("font weight must be between 1 and 1000")
	}
	if f.Style != "normal" && f.Style != "italic" && f.Style != "oblique" {
		return fmt.Errorf("font style must be normal, italic or oblique")
	}
	// The nine traditional width classes span 50–200%, but named
	// variable instances can use other nonnegative CSS percentages.
	if math.IsNaN(f.Stretch) || math.IsInf(f.Stretch, 0) || f.Stretch < 0 {
		return fmt.Errorf("font stretch must be a finite nonnegative percentage")
	}
	return nil
}

// List reads a fresh system collection, including fonts installed for just
// this user. Callers may cache a successful result until the user refreshes.
func List() ([]Family, error) {
	faces, err := installed()
	if err != nil {
		return nil, err
	}
	return group(faces), nil
}

func group(faces []Face) []Family {
	byName := map[string][]Face{}
	seen := map[Face]bool{}
	for _, f := range faces {
		f.Family, f.Name = strings.TrimSpace(f.Family), strings.TrimSpace(f.Name)
		if Validate(&f) != nil || seen[f] {
			continue
		}
		seen[f] = true
		byName[f.Family] = append(byName[f.Family], f)
	}
	out := make([]Family, 0, len(byName))
	for name, styles := range byName {
		slices.SortFunc(styles, func(a, b Face) int {
			return cmp.Or(cmp.Compare(a.Weight, b.Weight), cmp.Compare(styleRank(a.Style), styleRank(b.Style)), cmp.Compare(a.Stretch, b.Stretch), strings.Compare(a.Name, b.Name))
		})
		out = append(out, Family{Name: name, Styles: styles})
	}
	slices.SortFunc(out, func(a, b Family) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.Name, b.Name))
	})
	return out
}

// At the same weight, upright faces precede slanted ones in the style menu.
func styleRank(style string) int {
	switch style {
	case "normal":
		return 0
	case "italic":
		return 1
	default: // group has already validated the remaining style as oblique.
		return 2
	}
}

// OpenType and DirectWrite share these width classes.
func width(n int) float64 {
	if n < 1 || n > 9 {
		return 100
	}
	return [...]float64{50, 62.5, 75, 87.5, 100, 112.5, 125, 150, 200}[n-1]
}

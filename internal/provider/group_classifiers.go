package provider

import (
	"slices"
	"strings"
)

// Classifiers are the models (or groups) the group asks which intent a
// turn is, in order: Classifier, then ClassifierMore. The next is asked
// only when the one before couldn't answer. None when Classifier is "".
func (g Group) Classifiers() []string {
	if g.Classifier == "" {
		return nil
	}
	return append([]string{g.Classifier}, g.ClassifierMore...)
}

// SetClassifiers has the group ask list, in order, each once and none
// empty: the first is Classifier, the rest ClassifierMore. A list of one
// leaves ClassifierMore nil, so a group of one classifier saves as it
// always has.
func (g *Group) SetClassifiers(list []string) {
	var out []string
	for _, c := range list {
		if c = strings.TrimSpace(c); c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	g.Classifier, g.ClassifierMore = "", nil
	if len(out) > 0 {
		g.Classifier = out[0]
	}
	if len(out) > 1 {
		g.ClassifierMore = out[1:]
	}
}

// MapClassifiers has each of the group's classifiers go by what f makes
// of it: renamed, cleaned or taken out ("").
func (g *Group) MapClassifiers(f func(string) string) {
	list := g.Classifiers()
	if list == nil {
		list = slices.Clone(g.ClassifierMore) // a hand-edited file with no first
	}
	for i, c := range list {
		list[i] = f(c)
	}
	g.SetClassifiers(list)
}

// ClassifiesWith reports whether ref is one of the group's classifiers.
func (g Group) ClassifiesWith(ref string) bool {
	return ref != "" && (g.Classifier == ref || slices.Contains(g.ClassifierMore, ref))
}

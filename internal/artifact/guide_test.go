package artifact

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The guide covers every namespace claude.use gives a page here, and
// each call the user namespace has: an agent told of a capability the
// runtime lacks, or not told of one it has, writes a page that fails.
func TestGuideNamesWhatRuntimeServes(t *testing.T) {
	js := string(runtimeJS)
	m := regexp.MustCompile(`var impls = \{([^}]*)\}`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("no impls in runtime.js")
	}
	var spaces []string
	for _, kv := range regexp.MustCompile(`(\w+):`).FindAllStringSubmatch(m[1], -1) {
		if kv[1] != "self" {
			spaces = append(spaces, kv[1])
		}
	}
	if got := slices.Sorted(slices.Values(GuideNames())); !slices.Equal(got, slices.Sorted(slices.Values(spaces))) {
		t.Fatalf("the guide covers %v, the runtime serves %v", got, spaces)
	}
	all, err := Guide(nil)
	if err != nil {
		t.Fatal(err)
	}
	u := regexp.MustCompile(`(?s)var user = freeze\(\{(.*?)\n  \}\);`).FindStringSubmatch(js)
	if u == nil {
		t.Fatal("no user namespace in runtime.js")
	}
	for _, f := range regexp.MustCompile(`(?m)^    (\w+): function`).FindAllStringSubmatch(u[1], -1) {
		if f[1] != "name" && f[1] != "avatarUrl" && f[1] != "email" && !strings.Contains(all, "user."+f[1]+"(") {
			t.Errorf("the guide has no user.%s", f[1])
		}
	}
	for _, s := range []string{`db.doc(`, `.onSnapshot(`, `artifact.publish(`, `assets.upload(`, `sample.json(`, `downloads.save(`, `t.speak(`, `p.state(`, "view", "interact", "admin", "owner", "self"} {
		if !strings.Contains(all, s) {
			t.Errorf("the guide has no %s", s)
		}
	}
}

// Asked for some, the guide gives the introduction and those alone;
// self is artifact's former name, and a name it doesn't know is refused.
func TestGuideForSome(t *testing.T) {
	g, err := Guide([]string{" DB ", "self", "db"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(g, guideIntro) || !strings.Contains(g, "\ndb: ") || !strings.Contains(g, "\nartifact: ") || strings.Contains(g, "\nsample: ") || strings.Count(g, "\ndb: ") != 1 {
		t.Fatalf("db and self:\n%s", g)
	}
	if _, err := Guide([]string{"speech"}); err == nil || !strings.Contains(err.Error(), "db, user, artifact") {
		t.Fatalf("speech: %v", err)
	}
}

// A db declaration that doesn't read names the levels a rule takes: told
// only "read: …", Pi had to guess them (11-24).
func TestDBConfigErrorNamesLevels(t *testing.T) {
	_, err := ParseDBConfig([]byte(`{"rules": {"path": "votes"}}`))
	if err == nil || !strings.Contains(err.Error(), "view, interact, admin, owner or self") {
		t.Fatalf("%v", err)
	}
}

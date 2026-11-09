package plugin

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// mahiro233 on Discord: the Kiro plugin's sign-in methods read in English
// in a Chinese window. A plugin may give its sign-in words by language,
// beside OpenCode's English ones, as a page gives its titles: a method's
// labels, a question's messages, a choice's labels and hints. They reach
// magpie as given, words that aren't a string, empty ones and a list left
// out; a plugin that gives none is read as before.
func TestPluginSignInWordsByLanguage(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/langwords")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Provider{}
	for _, p := range ps {
		by[p.ID] = p
	}
	quince, pear := by["quince"], by["pear"]
	if len(quince.Methods) != 2 {
		t.Fatalf("quince's methods = %+v", quince.Methods)
	}
	if want := map[string]string{"zh": "Quince 登录", "zh-TW": "Quince 登入"}; !reflect.DeepEqual(quince.Methods[0].Labels, want) {
		t.Errorf("way 0's labels = %v, want %v", quince.Methods[0].Labels, want)
	}
	if want := map[string]string{"zh": "Quince API Key"}; !reflect.DeepEqual(quince.Methods[1].Labels, want) {
		t.Errorf("way 1's labels = %v, want %v", quince.Methods[1].Labels, want)
	}
	if len(pear.Methods) != 1 || pear.Methods[0].Labels != nil {
		t.Errorf("pear's methods = %+v, want no labels", pear.Methods)
	}

	q, err := NextPrompt(ctx, "quince", 0, map[string]string{})
	if err != nil || q == nil {
		t.Fatalf("NextPrompt = %+v, %v", q, err)
	}
	if want := map[string]string{"zh": "选择登录方式", "ja": "サインイン方法を選択"}; q.Message != "Choose a sign-in method" || !reflect.DeepEqual(q.Messages, want) {
		t.Errorf("message = %q, %v, want %v", q.Message, q.Messages, want)
	}
	if len(q.Options) != 2 {
		t.Fatalf("options = %+v", q.Options)
	}
	o := q.Options[0]
	if o.Label != "Builder ID" || o.Hint != "AWS sign-in" || !reflect.DeepEqual(o.Labels, map[string]string{"zh": "Builder ID（个人）"}) || !reflect.DeepEqual(o.Hints, map[string]string{"zh": "AWS 登录"}) {
		t.Errorf("option 0 = %+v", o)
	}
	if o := q.Options[1]; o.Label != "Sign-in page" || o.Labels != nil || o.Hints != nil {
		t.Errorf("option 1 = %+v, want no words by language", o)
	}
}

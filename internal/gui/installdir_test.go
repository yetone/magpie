//go:build !nogui

package gui

import (
	"errors"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/update"
)

// TestInstallWordsEveryLanguage: each language the dialog speaks has every
// word, with the folder (and the error) where the English has them.
func TestInstallWordsEveryLanguage(t *testing.T) {
	for _, lang := range []string{"en", "zh", "zh-TW", "ja", "de"} {
		w, ok := installText[lang]
		if !ok {
			t.Fatalf("%s: no words", lang)
		}
		for name, s := range map[string]string{"title": w.title, "ask": w.ask, "install": w.install, "change": w.change, "pick": w.pick} {
			if s == "" || strings.Contains(s, "%") {
				t.Errorf("%s %s = %q", lang, name, s)
			}
		}
		dir := `D:\Apps\magpie`
		for _, c := range []struct {
			err  error
			want []string
		}{
			{update.ErrNeedsAdmin, []string{dir}},
			{update.ErrDownloadFolder, []string{dir}},
			{errors.New("disk full"), []string{dir, "disk full"}},
		} {
			got := w.refusal(dir, c.err)
			if strings.Contains(got, "%!") {
				t.Errorf("%s %v: %q", lang, c.err, got)
			}
			for _, s := range c.want {
				if !strings.Contains(got, s) {
					t.Errorf("%s %v: %q lacks %q", lang, c.err, got, s)
				}
			}
		}
	}
	if installWordsFor("fr") != installText["en"] {
		t.Error("a language without words isn't English")
	}
}

// TestInstallQuestionNeedsSomeoneToAnswer: a CI runner's start (any CI
// service's CI variable) or one on a desktop no one sees is never asked
// where to install, so nothing waits on a dialog with the gateway not
// up yet (v0.1.4160's release check); a person's start is asked.
func TestInstallQuestionNeedsSomeoneToAnswer(t *testing.T) {
	for _, c := range []struct {
		ci      string
		visible bool
		asked   bool
	}{
		{"", true, true},
		{"false", true, true},
		{"0", true, true},
		{"true", true, false},
		{"True", true, false},
		{"1", true, false},
		{"", false, false},
	} {
		if why := unanswerable(c.ci, c.visible); (why == "") != c.asked {
			t.Errorf("CI=%q visible=%v: unanswerable = %q, want asked %v", c.ci, c.visible, why, c.asked)
		}
	}
}

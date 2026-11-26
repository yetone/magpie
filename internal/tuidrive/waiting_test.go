package tuidrive

import "testing"

func TestProgramNamesNoArguments(t *testing.T) {
	for args, want := range map[string]string{
		"/usr/bin/git config --get user.token=SECRET": "git",
		"/bin/zsh -c echo SECRET":                     "zsh",
		"/usr/bin/python3 /opt/helper.py SECRET":      "python3 helper.py",
		"node /x/apiKeyHelper.js --key SECRET":        "node apiKeyHelper.js",
		"":                                            "a command",
	} {
		if got := Program(args); got != want {
			t.Errorf("%q: %q", args, got)
		}
	}
}

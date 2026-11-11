package library

import (
	"strings"
	"testing"
)

// #6111 (Abandon332 on X): env=, header= and timeout= on a library mcp line
// were handed to the program as arguments. They are settings now, an
// unknown setting-looking word is refused, and -- hands words to the program.
func TestSplitServerWords(t *testing.T) {
	cmd, o, err := SplitServerWords([]string{"uvx", "foo", "env=A=B=C", "timeout=30", "agents=alma", "--local"})
	if err != nil || strings.Join(cmd, " ") != "uvx foo" || o.Env["A"] != "B=C" || *o.Timeout != 30 || !o.HasAgent || !*o.Local {
		t.Fatalf("%v %+v %v", cmd, o, err)
	}
	_, o, err = SplitServerWords([]string{"https://x/mcp", "header=Authorization=Bearer abc", "header=Accept: application/json, text/event-stream"})
	if err != nil || o.Headers["Authorization"] != "Bearer abc" || o.Headers["Accept"] != "application/json, text/event-stream" {
		t.Fatalf("%+v %v", o, err)
	}
	cmd, o, err = SplitServerWords([]string{"uvx", "foo", "--", "env=A=B", "timeout=3"})
	if err != nil || strings.Join(cmd, " ") != "uvx foo env=A=B timeout=3" || o.Env != nil || o.Timeout != nil {
		t.Fatalf("after --: %v %+v %v", cmd, o, err)
	}
	for _, bad := range []string{"headers=A=b", "envs=A=B", "timeout=soon", "env=NOVALUE", "header=nocolon", "timeout=-1"} {
		if _, _, err := SplitServerWords([]string{"uvx", bad}); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
	// a program's own key=value that isn't a setting of ours stays an argument
	if cmd, _, err := SplitServerWords([]string{"srv", "mode=fast", "--port=3"}); err != nil || len(cmd) != 3 {
		t.Fatalf("%v %v", cmd, err)
	}
}

func TestSetServerKeepsTheRest(t *testing.T) {
	sandbox(t)
	s, err := ServerOf("gh", []string{"https://api.githubcopilot.com/mcp/"})
	if err != nil {
		t.Fatal(err)
	}
	s.Headers = map[string]string{"Authorization": "Bearer old"}
	s.Note = "mine"
	if _, err := SaveServer("", s); err != nil {
		t.Fatal(err)
	}
	_, o, _ := SplitServerWords([]string{"header=authorization=Bearer new", "header=Accept=application/json, text/event-stream", "timeout=60"})
	if _, err := SetServer("gh", o); err != nil {
		t.Fatal(err)
	}
	got := readServer(t, "gh")
	if got.Headers["Authorization"] != "Bearer new" || len(got.Headers) != 2 || got.StartupTimeout != 60 || got.Note != "mine" {
		t.Fatalf("%+v", got)
	}
	_, o, _ = SplitServerWords([]string{"header-=accept"})
	if _, err := SetServer("gh", o); err != nil {
		t.Fatal(err)
	}
	if h := readServer(t, "gh").Headers; len(h) != 1 {
		t.Fatalf("%v", h)
	}
	_, o, _ = SplitServerWords([]string{"env=A=B"})
	if _, err := SetServer("gh", o); err == nil {
		t.Fatal("env on a URL server")
	}
}

func readServer(t *testing.T, name string) Server {
	t.Helper()
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Servers {
		if s.Name == name {
			return *s.Server
		}
	}
	t.Fatalf("no %s", name)
	return Server{}
}

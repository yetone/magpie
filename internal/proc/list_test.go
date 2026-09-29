package proc

import (
	"reflect"
	"testing"
)

func TestParseList(t *testing.T) {
	out := "  PID ARGS\n" +
		"    1 /sbin/launchd\n" +
		"  812 /Users/me/.codex/packages/standalone/current/bin/codex app-server --managed-daemon --listen unix://\n" +
		"\n" +
		"9001\r\n" +
		"x 1 junk\n"
	want := []Process{
		{1, "/sbin/launchd"},
		{812, "/Users/me/.codex/packages/standalone/current/bin/codex app-server --managed-daemon --listen unix://"},
		{9001, ""},
	}
	if got := parseList(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseList = %#v, want %#v", got, want)
	}
}

package cli

import (
	"strings"
	"testing"
)

// A routing group's note (ConatusMoon2222 on X) is set from the terminal
// with note=, given at add or set, shown by magpie group <id> and under its
// row in magpie groups, and cleared with note= empty, when nothing shows.
func TestGroupNoteCLI(t *testing.T) {
	groupsHome(t)
	if _, err := addGroup("Work", []string{"models=a/m", "note=for the office key"}); err != nil {
		t.Fatal(err)
	}
	if gs := storedGroups(t); len(gs) != 1 || gs[0]["note"] != "for the office key" {
		t.Fatalf("stored: %v", gs)
	}
	if err := groupCmd([]string{"group", "set", "work", "note=  weekdays\n only "}); err != nil {
		t.Fatal(err)
	}
	if gs := storedGroups(t); gs[0]["note"] != "weekdays only" {
		t.Fatalf("set: %v", gs)
	}
	out, err := stdoutOf(t, func() error { return groupCmd([]string{"group", "work"}) })
	if err != nil || !strings.Contains(out, "note") || !strings.Contains(out, "weekdays only") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	out, err = stdoutOf(t, groups)
	if err != nil || !strings.Contains(out, "weekdays only") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if err := groupCmd([]string{"group", "set", "work", "note="}); err != nil {
		t.Fatal(err)
	}
	if gs := storedGroups(t); gs[0]["note"] != nil {
		t.Fatalf("cleared: %v", gs)
	}
	out, _ = stdoutOf(t, func() error { return groupCmd([]string{"group", "work"}) })
	if strings.Contains(out, "note") {
		t.Fatalf("a group without a note shows one:\n%s", out)
	}
}

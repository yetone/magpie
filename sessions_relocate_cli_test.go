package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRelocateClaudeCLIArguments(t *testing.T) {
	for _, args := range [][]string{
		nil, {"--from"}, {"--from", "/old"}, {"--to", "/new"},
		{"--from", "/old", "--to", "/new", "--confirm", ""},
		{"--unknown", "x"},
	} {
		var out bytes.Buffer
		err := relocateClaudeTo(&out, args)
		if err == nil || !strings.Contains(err.Error(), "usage:") || out.Len() != 0 {
			t.Fatalf("%v: %v, %s", args, err, out.String())
		}
	}
}

package settings

import (
	"strings"
	"testing"
)

func TestCheckBark(t *testing.T) {
	for _, c := range []struct {
		server, key, daily string
		ok                 bool
	}{
		{"", "", "", true},
		{"", "AbC123", "21:00", true},
		{"https://bark.example.com/", "k", "9:05", true},
		{"http://192.168.1.5:8080", "k", "", true},
		{"bark.example.com", "k", "", false},
		{"ftp://x.example.com", "k", "", false},
		{"https://user:pw@x.example.com", "k", "", false},
		{"https://x.example.com/?a=1", "k", "", false},
		{"", "https://api.day.app/AbC123", "", false},
		{"", "two words", "", false},
		{"", "k", "25:00", false},
		{"", "k", "evening", false},
	} {
		err := CheckBark(c.server, c.key, c.daily)
		if (err == nil) != c.ok {
			t.Errorf("%+v: %v", c, err)
		}
		if err != nil && c.key != "" && strings.Contains(err.Error(), c.key) && len(c.key) > 3 {
			t.Errorf("the error names the key: %v", err)
		}
	}
	s, k, d := normalBark(" https://bark.example.com/ ", " k ", "9:05")
	if s != "https://bark.example.com" || k != "k" || d != "09:05" {
		t.Errorf("normal: %q %q %q", s, k, d)
	}
}

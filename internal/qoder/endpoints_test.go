package qoder

import "testing"

func TestNormalizeVPCEndpoint(t *testing.T) {
	tests := []struct{ in, want string; bad bool }{
		{"", "", false},
		{"tenant.vpc.qoder.com.cn", "https://tenant.vpc.qoder.com.cn", false},
		{"https://tenant.vpc.qoder.com.cn/", "https://tenant.vpc.qoder.com.cn", false},
		{"ftp://tenant.example", "", true},
		{"https://tenant.example/account/usage", "", true},
		{"https://user@tenant.example", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeVPCEndpoint(tt.in)
		if (err != nil) != tt.bad || got != tt.want {
			t.Fatalf("NormalizeVPCEndpoint(%q) = %q, %v; want %q, bad=%v", tt.in, got, err, tt.want, tt.bad)
		}
	}
}

func TestChatURLFor(t *testing.T) {
	if got := ChatURLFor("https://tenant.example"); got != "https://tenant.example"+ChatPath {
		t.Fatalf("ChatURLFor = %q", got)
	}
}
package settings

import "testing"

func TestUsageBucket(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, bucket := range []string{"", "hour", "10m"} {
		if err := Save(Settings{UsageBucket: bucket}); err != nil {
			t.Fatal(err)
		}
		if got := Load().UsageBucket; got != bucket {
			t.Fatalf("saved %q, loaded %q", bucket, got)
		}
	}
	if err := Save(Settings{UsageBucket: "minute"}); err == nil {
		t.Fatal("unsupported interval accepted")
	}
}

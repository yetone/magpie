package gui

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Alibaba Cloud's two Token Plan presets at aliyuncs.com ask for the
// account's AccessKey (#2940); the Qwen AI platform's and Qwen Cloud's
// plans have no such read and ask for nothing.
func TestPresetAccessKeyAlibaba(t *testing.T) {
	for id, want := range map[string]string{"bailian-token-plan": "alibaba", "alibaba-token-plan": "alibaba", "qwen-token-plan": "", "qwencloud-token-plan": "", "qwen": ""} {
		pr := provider.Preset(id)
		if pr == nil {
			t.Fatalf("no preset %s", id)
		}
		if v, ids := presetAccessKey(*pr); v != want || ids != nil {
			t.Errorf("%s: %q %v", id, v, ids)
		}
	}
}

package provider

import (
	"encoding/json"
	"testing"
)

// A model WorkBuddy's picker shows at x0.00 credits is marked free; any
// other rate, or none, is not.
func TestWBFreeCredits(t *testing.T) {
	var cfg wbProductConfig
	if err := json.Unmarshal([]byte(`{"agents":[{"name":"cli","models":["deepseek-v4.1-flash","deepseek-v4.1-flash-sg","gpt-5.5","glm-5.3","hy3"]}],
	 "models":[{"id":"deepseek-v4.1-flash","name":"Deepseek-V4.1-Flash","credits":"x0.00"},
	  {"id":"deepseek-v4.1-flash-sg","name":"Deepseek-V4.1-Flash","credits":"x0.03"},
	  {"id":"gpt-5.5","name":"GPT-5.5","credits":"x1.00"},
	  {"id":"glm-5.3","name":"GLM-5.3","credits":0},
	  {"id":"hy3","name":"Hy3"}]}`), &cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"deepseek-v4.1-flash": true, "glm-5.3": true}
	for _, m := range cfg.cliModels() {
		if m.Free != want[m.ID] {
			t.Errorf("%s free = %v", m.ID, m.Free)
		}
	}
}

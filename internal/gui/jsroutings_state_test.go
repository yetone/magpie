package gui

import (
	"context"
	"encoding/json"
	"testing"
)

// The routings JS gateway plugins serve come with the state, the Routing
// page's groups and the Plugins page's list, each a list: only Settings'
// read carried them, so a picker drawn from the state read after a change
// offered none (guhailin3 on X, #6026).
func TestJSRoutingsComeWithEveryRead(t *testing.T) {
	sandboxHome(t)
	for name, v := range map[string]any{"state": state(), "groups": groupsState(), "plugins": pluginsState(context.Background(), nil)} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(name, err)
		}
		var got struct {
			JSRoutings *[]json.RawMessage `json:"jsRoutings"`
		}
		if err := json.Unmarshal(b, &got); err != nil || got.JSRoutings == nil {
			t.Errorf("%s has no jsRoutings list (%v): %.300s", name, err, b)
		}
	}
}

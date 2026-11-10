package provider

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// DeepSeek Harness compacts its context on the model of the session's
// latest request (compaction-basic's summarizationProvider and
// summarizationModel unset), so a switch away from a model whose provider
// has run out of its limit compacts on that model first, and the switch
// fails with it (#5395). Its own setting for the model lives in each
// agent preset's whole config, which a profile patch can only replace
// whole, and a copy of the preset would go stale with the next dsh.
// settings.DshCompactModel is the model the gateway puts each of its
// compactions on instead, read on each request, so dsh needn't restart.

// DshCompactModel is the model DSH's compactions are put on, "" for the
// one each asks for. A provider renamed since it was set is named by its
// new id (RenamedRef).
func DshCompactModel() string {
	id := strings.TrimSpace(heldSettings().DshCompactModel)
	if id == "" {
		return ""
	}
	return RenamedRef(id)
}

// SetDshCompactModel puts DSH's compactions on id, a model or routing
// group of magpie's (provider/model, group/<id>); "" leaves each on the
// model it asks for. One magpie doesn't serve now is kept all the same,
// as Desktop's subagent model is: the gateway leaves compactions where
// they are until it is served.
func SetDshCompactModel(id string) error {
	id = strings.TrimSpace(id)
	if pid, model, ok := strings.Cut(id, "/"); id != "" && (!ok || pid == "" || model == "") {
		return fmt.Errorf("DeepSeek Harness's compaction can't be put on %q: pick one of magpie's models (provider/model)", id)
	}
	s := settings.Load()
	if s.DshCompactModel == id {
		return nil
	}
	s.DshCompactModel = id
	if err := settings.Save(s); err != nil {
		return err
	}
	Changed()
	return nil
}

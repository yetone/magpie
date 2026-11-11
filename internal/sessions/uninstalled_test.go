package sessions

import (
	"path/filepath"
	"testing"
)

// _SekiBetu_ on X: OpenCode uninstalled, its sessions stayed listed with a
// Resume and no Delete. With its command gone Resume is not offered, Delete
// takes the row off the list (its database is not touched), and Restore
// lists it again.
func TestUninstalledAgentSessionsAreForgotten(t *testing.T) {
	inZone(t, 0)
	data, _ := setupMore(t)
	ocMakeDB(t, filepath.Join(data, "opencode", "opencode.db"))
	t.Cleanup(ForceCLIGone())

	row := func() Managed {
		t.Helper()
		for _, m := range ListAgent("opencode") {
			if m.ID == ocMain {
				return m
			}
		}
		t.Fatal("no OpenCode session")
		return Managed{}
	}
	if m := row(); m.Resume == "" || m.Deletable {
		t.Fatalf("installed: %+v", m)
	}
	if _, err := Forget("opencode", ocMain); err == nil {
		t.Fatal("forgot a session of an installed agent")
	}

	defer ForceCLIGone("opencode")()
	if oc := find(t, List(0), "opencode", ocMain); oc.Resume != "" {
		t.Fatalf("resume offered with opencode gone: %q", oc.Resume)
	}
	if m := row(); m.Resume != "" || !m.Deletable || !m.Forget {
		t.Fatalf("uninstalled: resume %q deletable %v forget %v", m.Resume, m.Deletable, m.Forget)
	}
	m := row()
	tr, err := Forget("opencode", ocMain)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Forgot || !GatewayHidden()("opencode", ocMain, m.Last) {
		t.Fatalf("not hidden after Forget: %+v", tr)
	}
	if got := Trash(); len(got) != 1 || !got[0].Forgot {
		t.Fatalf("trash %+v", got)
	}
	// the Usage page's list (ForgottenOut) leaves it out, and only while the
	// command is gone
	has := func() bool {
		for _, x := range ForgottenOut(List(0)) {
			if x.Agent == "opencode" && x.ID == ocMain {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("a forgotten session is still in the Usage list")
	}
	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	if GatewayHidden()("opencode", ocMain, m.Last) {
		t.Fatal("still hidden after Restore")
	}
	if !has() {
		t.Fatal("a restored session is not in the Usage list")
	}
}

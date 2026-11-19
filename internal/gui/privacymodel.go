package gui

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/pii"
	"github.com/yetone/magpie/internal/settings"
)

// privacyModelJSON is Settings → Privacy's local masking model: whether it
// is on (masking.ModelWanted: the user's, or the default), the model masking uses (or would, turned on) with its own status
// (downloaded, downloading, where it runs) and threshold (the one the
// user set, else its default), and every model there is to pick, with
// theirs.
type privacyModelJSON struct {
	On bool `json:"on"`
	// Chosen says the user turned it on, not the default: only then is
	// a request held back for it (masking.ModelOn)
	Chosen bool `json:"chosen,omitempty"`
	pii.Status
	// Folder is where the models are kept (pii.Root, as ~ for the home),
	// FolderSet that the user picked it
	Folder    string `json:"folder"`
	FolderSet bool   `json:"folderSet,omitempty"`
	pii.Moving
	MinScore     float64      `json:"minScore"`
	DefaultScore float64      `json:"defaultScore"`
	Models       []pii.Status `json:"models"`
}

// modelOf and models are pii's; vars for tests.
var modelOf, models = pii.Of, pii.Models

func privacyModelState() privacyModelJSON {
	st := settings.Load()
	on := masking.ModelWanted(st)
	m := modelOf(pii.Choose(st.RedactModelName, on))
	out := privacyModelJSON{On: on, Chosen: st.RedactModel, Status: m.Now(), MinScore: pii.Threshold(st.RedactModelScore, m), DefaultScore: m.MinScore()}
	for _, m := range models() {
		out.Models = append(out.Models, m.Now())
	}
	out.Folder, out.FolderSet, out.Moving = tilde(pii.Root()), pii.Custom(), pii.MoveState()
	return out
}

// privacyModelRoutes: GET /api/privacy-model is its state; POST with
// {"do": "on" | "off" | "cancel" | "remove" | "pick" | "score", "model": id,
// "score": s} turns
// it on (as model, or the one it would use: downloading it first, and
// saving it on once that is done), off (a download is stopped, the model
// let go and kept), stops a download, turns it off and deletes every
// model downloaded, or picks model: while it is on, that is turning it on
// as model (the one before masks until model is downloaded); while off,
// the pick is kept for turning it on; score keeps s as the threshold of
// model (the one masking uses, with none), 0 for its default; folder moves
// the downloaded models to dir (the system's picker for "", magpie's cache
// for "default") in the background, which the state's moving says.
func privacyModelRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/privacy-model", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, privacyModelState())
	})
	mux.HandleFunc("POST /api/privacy-model", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Do    string  `json:"do"`
			Model string  `json:"model"`
			Score float64 `json:"score"`
			Dir   string  `json:"dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Model != "" && in.Model != pii.PrivacyFilter && in.Model != pii.AstrLinkGuard {
			fail(rw, fmt.Errorf("model is %s or %s, not %q", pii.PrivacyFilter, pii.AstrLinkGuard, in.Model))
			return
		}
		st := settings.Load()
		on := masking.ModelWanted(st)
		was := pii.Choose(st.RedactModelName, on)
		var err error
		switch in.Do {
		case "pick":
			switch {
			case in.Model == "":
				err = fmt.Errorf("pick needs a model")
			case on:
				err = turnOn(in.Model, was)
			default:
				err = settings.SetRedactModelName(in.Model)
			}
		case "folder":
			// "" asks with the system's picker; "default" is magpie's cache
			dir := in.Dir
			if dir == "" {
				if dir, err = w.ChooseFolder("Choose a folder for the local models"); err != nil || dir == "" {
					break // cancelled: nothing moved
				}
			} else if dir == "default" {
				dir = ""
			}
			err = pii.MoveTo(dir)
		case "score":
			id := in.Model
			if id == "" {
				id = was
			}
			if in.Score != 0 && !(in.Score >= pii.Floor && in.Score < 1) {
				err = fmt.Errorf("score is 0 (the model's own) or from %g to under 1, not %g", pii.Floor, in.Score)
			} else {
				err = settings.SetRedactModelScore(id, in.Score)
			}
		case "on":
			id := in.Model
			if id == "" {
				id = was
			}
			err = turnOn(id, was)
		case "off":
			for _, m := range models() {
				m.Cancel()
			}
			err = settings.SetRedactModel(false)
			for _, m := range models() {
				m.Stop()
			}
		case "cancel":
			for _, m := range models() {
				m.Cancel()
			}
			// the download on by default, cancelled, is the model
			// turned off: it would start again on the next request
			if on && !st.RedactModel && !modelOf(was).Installed() {
				err = settings.SetRedactModel(false)
			}
		case "remove":
			if err = settings.SetRedactModel(false); err == nil {
				for _, m := range models() {
					if e := m.Remove(); e != nil && err == nil {
						err = e
					}
				}
			}
		default:
			err = fmt.Errorf("do is on, off, cancel, remove, pick, folder or score, not %q", in.Do)
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, privacyModelState())
	})
}

// turnOn turns the local model on as id, once it is on disk: a request
// in between is masked as before (by the rules, or by was, the model on
// till then), and not held back. A download of another model under way
// is stopped; was is let go once id masks.
func turnOn(id, was string) error {
	m := modelOf(id)
	if !m.Supported() {
		return fmt.Errorf("the local masking model doesn't run on this system yet")
	}
	for _, o := range models() {
		if o.ID() != id {
			o.Cancel()
		}
	}
	on := func() {
		if settings.UseRedactModel(id) == nil {
			if was != id {
				modelOf(was).Stop()
			}
			m.Warm()
		}
	}
	if m.Installed() {
		on()
		return nil
	}
	return m.Download(on)
}

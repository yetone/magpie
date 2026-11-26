package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/yetone/magpie/internal/groupshare"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/stats"
)

// Sharing a routing group on usemagpie.ai (internal/groupshare): the
// Routing page's Share… sheet, its import preview, its gallery and the
// author's newer versions, as actions of POST /api/groups/{action}.

// groupShareBody is what those actions read of the request, beside the
// group's id (Group.ID).
type groupShareBody struct {
	// publish: what the user says of the group shared
	Meta groupshare.Meta `json:"meta"`
	// preview, import: the members the user sent elsewhere ("" left out),
	// by the member the shared group names (provider.ImportShared)
	Use map[string]string `json:"use"`
	// import: the shared group's id on usemagpie.ai, fetched again rather
	// than taken from the page; like: the one liked
	Source string `json:"source"`
	// preview, import: the group of the user's it replaces, by id: the
	// one imported from Source, taking the author's newer version
	Into string `json:"into"`
	// community: the gallery's order ("top", "new"), search and page
	Sort string `json:"sort"`
	Q    string `json:"q"`
	Page int    `json:"page"`
	// passover: the version of the source the user passed over
	Version int `json:"version"`
}

// previewJSON is preview's answer: the shared group read (Text, as
// import takes it), where it came from, and what importing it would make
// here.
type previewJSON struct {
	Text   string                `json:"text"`
	Source *groupshare.Page      `json:"source,omitempty"`
	Plan   provider.ImportPlan   `json:"plan"`
	Into   string                `json:"into,omitempty"`
	Shared *provider.SharedGroup `json:"shared"`
}

// sharedFrom is the shared group text names: a link to one on
// usemagpie.ai, fetched, or the group as text.
func sharedFrom(ctx context.Context, text string) (provider.SharedGroup, *groupshare.Page, error) {
	if id := groupshare.LinkID(text); id != "" {
		p, err := groupshare.Fetch(ctx, id)
		if err != nil {
			return provider.SharedGroup{}, nil, err
		}
		s, err := provider.ReadShared(mustJSON(p.Group))
		return s, &p, err
	}
	s, err := provider.ReadShared(text)
	return s, nil, err
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// groupShareAction answers the sharing actions; false for any other.
func groupShareAction(rw http.ResponseWriter, r *http.Request, action, gid, text string, replace, on bool, b groupShareBody) bool {
	ctx := r.Context()
	switch action {
	case "publish":
		m, err := groupshare.Publish(ctx, gid, b.Meta)
		if err != nil {
			fail(rw, err)
			return true
		}
		st := groupsState()
		st.Published = &m
		writeJSON(rw, st)
	case "unpublish":
		if err := groupshare.Unpublish(ctx, gid); err != nil {
			fail(rw, err)
			return true
		}
		writeJSON(rw, groupsState())
	case "public":
		// what Share… would send, for the user to see before it goes
		s, err := provider.PublicGroup(gid)
		if err != nil {
			fail(rw, err)
			return true
		}
		writeJSON(rw, map[string]any{"shared": s})
	case "preview":
		s, page, err := sharedFrom(ctx, text)
		if b.Source != "" && page == nil {
			s, page, err = sharedFrom(ctx, b.Source)
		}
		if err != nil {
			fail(rw, err)
			return true
		}
		over := replace
		if b.Into != "" {
			s.Group.ID, over = b.Into, true
		}
		out := previewJSON{Text: mustJSON(s), Source: page, Plan: provider.PlanImport(s, b.Use, over), Into: b.Into, Shared: &s}
		if page != nil {
			out.Source.Group = provider.SharedGroup{} // it is Shared
		}
		writeJSON(rw, out)
	case "import":
		s, page, err := sharedFrom(ctx, text)
		if b.Source != "" {
			// the page as usemagpie.ai has it now, never as the GUI says
			s, page, err = sharedFrom(ctx, b.Source)
		}
		if err != nil {
			fail(rw, err)
			return true
		}
		over := replace
		if b.Into != "" {
			s.Group.ID, over = b.Into, true
		}
		res, err := provider.ImportShared(s, b.Use, over)
		if err != nil {
			fail(rw, err)
			return true
		}
		if page != nil {
			if err := groupshare.Imported(res.ID, *page); err != nil {
				fail(rw, err)
				return true
			}
		}
		st := groupsState()
		st.Imported = &res
		writeJSON(rw, st)
	case "community":
		g, err := groupshare.List(ctx, b.Sort, b.Q, b.Page)
		if err != nil {
			fail(rw, err)
			return true
		}
		writeJSON(rw, g)
	case "like":
		install, err := stats.InstallID()
		if err != nil {
			fail(rw, err)
			return true
		}
		n, err := groupshare.Like(ctx, install, b.Source, on)
		if err != nil {
			fail(rw, err)
			return true
		}
		writeJSON(rw, map[string]any{"likes": n, "liked": groupshare.Liked()})
	case "updates":
		u, err := groupshare.Updates(ctx)
		if err != nil {
			// offline is no newer version, said nowhere
			u = map[string]groupshare.Update{}
		}
		writeJSON(rw, map[string]any{"updates": u})
	case "passover":
		if err := groupshare.PassOver(gid, b.Version); err != nil {
			fail(rw, err)
			return true
		}
		writeJSON(rw, groupsState())
	default:
		return false
	}
	return true
}

// groupGone is a shared group usemagpie.ai doesn't have, or sharing off
// here, as fail says it: a code, and the words the page says it in, an
// i18n.js key.
func groupGone(err error) (code, said string) {
	switch {
	case errors.Is(err, groupshare.ErrGone):
		return "group_gone", "This group is no longer shared: its author stopped sharing it, or it was taken down"
	case errors.Is(err, groupshare.ErrOff):
		return "share_off", "Sharing isn't available in this magpie"
	}
	return "", ""
}

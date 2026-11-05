package provider

import "strings"

// A group of one kind is the models of one API other than chat (an
// embedding, rerank, speech, transcription or video model) that more than
// one provider serves under exactly the same id (#5339, XYenon on
// Discord). Unlike a chat model's found group (autoGroups) it is in no
// list of groups: agents, gateway keys and Routing offer chat models, and
// these are none. It is made when its API's route is asked for one, by its
// id there (KindGroup).

// kindPrefix starts a group of one kind's id: "auto-embedding:<model>".
// A chat model's found group is "auto-" and a slug, which has no ":", so
// the two never name the same group.
const kindPrefix = "auto-"

// KindGroupID is the id of the group of the API kind's model: its exact
// id, as the providers list it.
func KindGroupID(kind, model string) string {
	return kindPrefix + kind + ":" + model
}

// kindModelOf is the model a group of kind's ref names: "group/" and
// KindGroupID's id, or the model's own id. ok is false for a group ref of
// another kind or of no kind.
func kindModelOf(kind, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	gid, isGroup := GroupIDOf(ref)
	if !isGroup {
		return ref, ref != ""
	}
	model, ok := strings.CutPrefix(gid, kindPrefix+kind+":")
	return model, ok && model != ""
}

// KindGroup is the group of the API kind's model ref names, with its
// members ready now: each provider has serves say serves the model under
// exactly that id, in the order the providers were added, none switched
// off and none the user disabled the model on (#1867). ref is the group's
// id ("group/auto-embedding:embed-1") or the model's own ("embed-1", which
// the route asks for: GroupFor by route). ok is false when no provider
// serves it; a group of one member is the model of that one.
func KindGroup(kind, ref string, serves func(p Provider, model string) bool) (Group, []Member, bool) {
	model, ok := kindModelOf(kind, ref)
	if !ok {
		return Group{}, nil, false
	}
	off := disabledNow()
	g := Group{ID: KindGroupID(kind, model), Name: model, Auto: true}
	var ms []Member
	for _, p := range All() {
		if !p.On() || p.DecideOnly() || p.ServesNone() || off(p.ID, model) || !serves(p, model) {
			continue
		}
		id := p.ID + "/" + model
		g.Members = append(g.Members, id)
		ms = append(ms, Member{ID: id, Path: []string{id}, Provider: p, Model: model})
	}
	return g, ms, len(ms) > 0
}

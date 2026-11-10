package provider

import (
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// Claude Code takes a Claude model by its id's version written with dashes
// (claude-opus-4-6). Many providers and relays name one with a dot
// (claude-opus-4.6, relay/claude-opus-4.6-us), which Claude Code 2.1.296
// reads as Claude Opus 4: it warns the model is retired, asks with budget
// thinking and 32000 output tokens, and offers no effort, whether its
// settings.json names the model by id or by "opus" (misteer_ti. on
// Discord). So a served Claude model with a dotted version is given to
// Claude Code with dashes (ClaudeSpelled), and the gateway takes that
// spelling back to the served id (ClaudeUndashed). Mythos too: Claude Code
// matches claude-mythos-5-1 by substring, so claude-mythos-5.1 would read
// as Mythos 5. Other vendors' ids are never respelled.

// claudeDotted is a Claude model's version written with a dot.
var claudeDotted = lazyre.New(`(?i)claude-(?:opus|sonnet|haiku|fable|mythos)-\d+(?:\.\d+)+`)

// claudeDashedish is an id that could be a dotted one respelled.
var claudeDashedish = lazyre.New(`(?i)claude-(?:opus|sonnet|haiku|fable|mythos)-\d+-\d`)

// ClaudeDashed is id with each dot of a Claude model's version a dash:
// relay/claude-opus-4.6-us[1m] is relay/claude-opus-4-6-us[1m]. Any other
// id is as it is.
func ClaudeDashed(id string) string {
	if !strings.Contains(id, ".") {
		return id
	}
	return claudeDotted.ReplaceAllStringFunc(id, func(s string) string { return strings.ReplaceAll(s, ".", "-") })
}

// claudeSplit splits a model as an agent is given it into the id and what
// follows it: an effort fixed on it (":high", MemberEfforts) and Claude
// Code's [1m] mark.
func claudeSplit(ref string) (id, rest string) {
	const mark = "[1m]"
	id, marked := strings.CutSuffix(ref, mark)
	if i := strings.LastIndex(id, ":"); i > 0 && slices.Contains(MemberEfforts, strings.ToLower(id[i+1:])) {
		id, rest = id[:i], id[i:]
	}
	if marked {
		rest += mark
	}
	return id, rest
}

// ClaudeUndashed is the served id (a provider's model or a routing group)
// whose dashed spelling (ClaudeDashed) ref is, its effort and [1m] mark
// kept, when ref is no served id as it is and exactly one served id is so
// spelled. An id served as it is spelled always wins.
func ClaudeUndashed(ref string) (string, bool) {
	id, rest := claudeSplit(ref)
	if !claudeDashedish.MatchString(id) {
		return "", false
	}
	if real, ok := undashIn(Served(), id); ok {
		return real + rest, true
	}
	return "", false
}

func undashIn(entries []Entry, id string) (string, bool) {
	found := ""
	for _, e := range entries {
		if e.ID == id {
			return "", false // served as it is spelled
		}
		if e.ID == found || ClaudeDashed(e.ID) != id {
			continue
		}
		if found != "" {
			return "", false // two ids dash alike: neither is meant
		}
		found = e.ID
	}
	return found, found != ""
}

// ClaudeSpelled is ref (a served id, with an effort and [1m] mark or not)
// as Claude Code is given it: dashed (ClaudeDashed) when that spelling
// comes back to this id (ClaudeUndashed), else as it is.
func ClaudeSpelled(ref string) string {
	id, rest := claudeSplit(ref)
	d := ClaudeDashed(id)
	if d == id {
		return ref
	}
	if real, ok := undashIn(Served(), d); ok && real == id {
		return d + rest
	}
	return ref
}

// ClaudeRead is a model read back from Claude Code's settings as magpie
// serves it: a dashed spelling ClaudeSpelled wrote taken back to the
// served id, anything else as it is. A model of a provider switched off
// since is taken back too, so magpie still knows it for its own and moves
// the agent off it (Reseat).
func ClaudeRead(ref string) string {
	if real, ok := ClaudeUndashed(ref); ok {
		return real
	}
	id, rest := claudeSplit(ref)
	if !claudeDashedish.MatchString(id) {
		return ref
	}
	if real, ok := undashIn(offEntries(), id); ok {
		return real + rest
	}
	return ref
}

// offEntries are the models of the providers switched off, by id alone.
func offEntries() []Entry {
	var out []Entry
	for _, p := range All() {
		if p.On() {
			continue
		}
		for _, m := range p.Exposed() {
			out = append(out, Entry{ID: p.ID + "/" + m.ID})
		}
	}
	return out
}

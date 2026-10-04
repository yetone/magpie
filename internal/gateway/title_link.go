package gateway

import (
	"container/list"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/sessions"
)

// TitleLink contains only fingerprints. Native request IDs and account affinity
// are never changed by an inferred display association.
type TitleLink struct {
	Scope  string `json:"scope"`
	Prompt string `json:"prompt"`
	Reply  string `json:"reply,omitempty"`
}

type titlePrompt struct {
	Session string
	Time    time.Time
	Link    TitleLink
}
type titlePrompts struct {
	sync.Mutex
	first    map[string]*list.Element
	byPrompt map[string][]titlePrompt
	recent   list.List // oldest first; repeated turns keep a chat active
}

const titlePromptLimit = 4096

func promptDigest(s string) string {
	return sessions.CodexPromptDigest(strings.TrimSpace(s))
}

// Inspect only the first real user message, before any assistant reply. A
// multimodal or unfamiliar title template stays unassociated. Walk the input
// once; callers do this outside the shared prompt-cache lock.
func titlePromptDigest(body []byte, title bool) string {
	var digest string
	i := 0
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return ""
	}
	input.ForEach(func(_, item gjson.Result) bool {
		i++
		if i > 32 {
			return false
		}
		if item.Get("role").String() == "assistant" {
			return false
		}
		if item.Get("role").String() != "user" {
			return true
		}
		content := item.Get("content")
		text := ""
		if content.Type == gjson.String {
			text = content.String()
		} else if content.IsArray() {
			valid := true
			content.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() != "input_text" {
					valid = false
					return false
				}
				text += part.Get("text").String()
				return true
			})
			if !valid {
				return false
			}
		}
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "<environment_context>") || strings.HasPrefix(text, "<user_instructions>") || strings.HasPrefix(text, "# AGENTS.md instructions for ") || strings.HasPrefix(text, "<external_codex_apps_open_page>") {
			return true
		}
		if title {
			const prefix = "You are a helpful assistant. You will be presented with a user prompt,"
			const marker = "\n\nUser prompt:\n"
			if !strings.HasPrefix(text, prefix) {
				return false
			}
			_, tail, ok := strings.Cut(text, marker)
			if !ok {
				return false
			}
			text = strings.TrimSpace(tail)
		}
		if text == "" {
			return false
		}
		digest = promptDigest(text)
		return false
	})
	return digest
}

func (p *titlePrompts) observe(r *http.Request, body []byte, m sessionMetadata, kind string, at time.Time) *TitleLink {
	if agentOf(r) != "codex" || !local(r) || callerOf(r).via != "" || m.Installation == "" || len(m.Installation) > 128 || m.Source == "subagent" || (kind != "" && !isTitleKind(kind)) {
		return nil
	}
	id := sessionOf(r.Header)
	if id == "" {
		return nil
	}
	scope := promptDigest(m.Installation)
	if scope == "" {
		return nil
	}
	if isTitleKind(kind) {
		if key := titlePromptDigest(body, true); key != "" {
			return &TitleLink{Scope: scope, Prompt: key}
		}
		return nil
	}
	p.Lock()
	key := scope + ":" + id
	if e := p.first[key]; e != nil {
		p.recent.MoveToBack(e)
		p.Unlock()
		return nil
	}
	p.Unlock()
	link := TitleLink{Scope: scope, Prompt: titlePromptDigest(body, false)}
	p.Lock()
	defer p.Unlock()
	// Another request for this chat may have arrived while its body was parsed.
	if e := p.first[key]; e != nil {
		p.recent.MoveToBack(e)
		return nil
	}
	if len(p.first) >= titlePromptLimit {
		e := p.recent.Front()
		old := e.Value.(titlePrompt)
		delete(p.first, old.Link.Scope+":"+old.Session)
		promptKey := old.Link.Scope + ":" + old.Link.Prompt
		candidates := p.byPrompt[promptKey]
		for i, candidate := range candidates {
			if candidate.Session == old.Session {
				candidates = append(candidates[:i], candidates[i+1:]...)
				break
			}
		}
		if len(candidates) == 0 {
			delete(p.byPrompt, promptKey)
		} else {
			p.byPrompt[promptKey] = candidates
		}
		p.recent.Remove(e)
	}
	if p.first == nil {
		p.first = make(map[string]*list.Element)
	}
	entry := titlePrompt{Session: id, Time: at, Link: link}
	p.first[key] = p.recent.PushBack(entry)
	if link.Prompt == "" {
		return nil
	}
	if p.byPrompt == nil {
		p.byPrompt = make(map[string][]titlePrompt)
	}
	p.byPrompt[scope+":"+link.Prompt] = append(p.byPrompt[scope+":"+link.Prompt], entry)
	return &link
}

func titleReplyDigest(body []byte, shape *titleShape) string {
	if !gjson.ValidBytes(body) {
		completed := false
		readSSE(strings.NewReader(string(body)), func(_, data string) error {
			switch gjson.Get(data, "type").String() {
			case "response.completed":
				completed = true
			case "response.incomplete", "response.failed", "error":
				completed = false
			}
			return nil
		})
		if !completed {
			return ""
		}
	} else if status := gjson.GetBytes(body, "status").String(); status != "" && status != "completed" {
		return ""
	}
	res, err := compactReply(body)
	if err != nil || res.Error != nil {
		return ""
	}
	text := messageText(res)
	if shape != nil {
		text = titleJSON(text, *shape)
	} // exactly the title handed to Codex

	// A native reply must really contain the structured title Codex accepts.
	if !gjson.Valid(text) {
		return ""
	}
	title := gjson.Get(text, "title")
	if title.Type != gjson.String {
		return ""
	}
	return sessions.CodexTitleDigest(title.String())
}

// ResolveTitleParents checks completed title replies against actual Codex name
// writes. It operates on copies for the GUI only, never on gateway routing.
// An explicit parent wins. Missing, conflicting or old evidence fails closed.
func (s *Server) ResolveTitleParents(rows []Route) []Route {
	needed := false
	for _, r := range rows {
		if isTitleKind(r.Kind) && r.TitleLink != nil {
			needed = true
			break
		}
	}
	if !needed {
		return rows
	}
	s.titlePrompts.Lock()
	first := []titlePrompt{}
	seen := map[string]bool{}
	for _, r := range rows {
		if !isTitleKind(r.Kind) || r.TitleLink == nil {
			continue
		}
		key := r.TitleLink.Scope + ":" + r.TitleLink.Prompt
		if !seen[key] {
			first = append(first, s.titlePrompts.byPrompt[key]...)
			seen[key] = true
		}
	}
	s.titlePrompts.Unlock()
	return resolveTitleParents(rows, first, sessions.CodexTitleRecipients)
}

func resolveTitleParents(rows []Route, first []titlePrompt, applications func([]string) map[string][]sessions.TitleApplication) []Route {
	out := clearInferredParents(rows)
	byPrompt := map[string]map[string]time.Time{}
	add := func(p titlePrompt) {
		if p.Link.Prompt == "" || p.Link.Scope == "" || p.Session == "" {
			return
		}
		key := p.Link.Scope + ":" + p.Link.Prompt
		if byPrompt[key] == nil {
			byPrompt[key] = map[string]time.Time{}
		}
		old, ok := byPrompt[key][p.Session]
		if !ok || p.Time.Before(old) {
			byPrompt[key][p.Session] = p.Time
		}
	}
	for _, p := range first {
		add(p)
	}
	for _, r := range rows {
		if r.Agent == "codex" && r.Kind == "" && r.TitleLink != nil {
			add(titlePrompt{r.Session, r.Time, *r.TitleLink})
		}
	}
	digests := map[string]bool{}
	for _, r := range out {
		if r.Agent != "codex" || !isTitleKind(r.Kind) || r.TitleLink == nil || r.TitleLink.Reply == "" || !r.Done || r.Status >= 400 || r.Error != "" {
			continue
		}
		digests[r.TitleLink.Reply] = true
	}
	if len(digests) == 0 {
		return out
	}
	want := make([]string, 0, len(digests))
	for digest := range digests {
		want = append(want, digest)
	}
	applied := applications(want)
	for i := range out {
		r := &out[i]
		if r.ParentSession != "" || r.Agent != "codex" || !isTitleKind(r.Kind) || r.TitleLink == nil || r.TitleLink.Reply == "" || !r.Done || r.Status >= 400 || r.Error != "" {
			continue
		}
		parent := ""
		// The name write must follow this request and promptly follow its reply.
		// Time is corroborating evidence, never the sole matching key.
		end := r.Time.Add(time.Duration(r.Millis)*time.Millisecond + 2*time.Minute)
		for id, writes := range applied {
			for _, a := range writes {
				if a.Digest != r.TitleLink.Reply || a.Updated.Before(r.Time) || a.Updated.After(end) {
					continue
				}
				if parent != "" && parent != id {
					parent = "!ambiguous"
					break
				}
				parent = id
				break
			}
			if parent == "!ambiguous" {
				break
			}
		}
		at, known := byPrompt[r.TitleLink.Scope+":"+r.TitleLink.Prompt][parent]
		if parent != "" && parent != "!ambiguous" && parent != r.Session && known && !at.After(end) {
			r.ParentSession, r.ParentMatched = parent, true
		}
	}
	return out
}

func clearInferredParents(rows []Route) []Route {
	out := append([]Route{}, rows...)
	for i := range out {
		if out[i].ParentMatched {
			out[i].ParentSession, out[i].ParentMatched = "", false
		}
	}
	return out
}

package sessions

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/dirent"
	"github.com/yetone/magpie/internal/provider"
)

// FindOne is Find for a caller that asks for one session and goes: the
// CLI's show, whose every run is a new process with no listing kept
// (keptList). Find lists every agent's sessions first, every file of
// every agent stat()ed and the newest 200 put together, and over ~3000
// sessions each `magpie sessions show agent:id --from N` took 4-5s
// (hdhfhf_hdh219 on X). A Claude Code or Codex session is read from its
// agent's files alone: Claude Code's sessions' own files (what tells a
// copy or a branch, ccFamily) and this one's subagents', or Codex's
// rollouts (whose identities each thread's needs), on this computer: no
// other agent's folders, no WSL distro (whose first listing a new process
// waits up to wslFirstWait for), no 200 sessions put together. What it
// gives is what Find gives; any other agent, or a session not found so,
// is Find's.
func FindOne(agent, id string) (Session, bool) {
	alias := provider.UsageAlias()
	if s, found := findOne(agent, id); found {
		if alias != nil {
			aliasSession(&s, alias)
		}
		return s, true
	}
	return Find(agent, id)
}

func findOne(agent, id string) (Session, bool) {
	if id == "" || !Reads(agent) {
		return Session{}, false
	}
	key := agent + ":" + id
	storeMu.RLock()
	defer storeMu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	switch agent {
	case "claude":
		if bridged(agent, id) {
			return Session{}, false
		}
		loadCache()
		mains, own := claudeOwnFiles(id)
		if len(own) == 0 {
			return Session{}, false
		}
		// the other sessions' own files are read as the listing reads
		// them: what tells this one a copy or a branch is in them. Kept
		// parses of files not listed here stay (no all-set).
		want := slices.Clip(mains)
		for _, f := range own {
			if !f.main {
				want = append(want, f)
			}
		}
		refresh(want, nil)
		groups := byKey(mains, nil)
		copies, branches := ccFamily(mains)
		s, ok := assemble(own, pricer(), codexTitleOf())
		if !ok {
			listed(&s)
		} else if copies[key] == "" {
			// as the listing has it; a copy itself is Get's, with none
			s.Copies = copiesByKeeper(copies, groups)[key]
		}
		setBranch(&s, branches[key], groups)
		return s, s.ID == id
	case "codex":
		loadCache()
		// this computer's: a thread's parent and the spawns that name it
		// are where it is, and WSL's are Find's
		files := allowed(codexFiles())
		var fs []file
		for _, f := range files {
			if f.key == key {
				fs = append(fs, f)
			}
		}
		if len(fs) == 0 {
			return Session{}, false
		}
		refresh(files, nil)
		s, ok := assemble(fs, pricer(), codexTitleOf())
		if !ok {
			listed(&s)
		}
		setSessionCodexIdentity(&s, codexIdentitySnapshot(files))
		setSessionCodexTaskTitle(&s, codexSubagentTaskTitles(files))
		return s, s.ID == id
	}
	return Session{}, false
}

// claudeOwnFiles are, of Claude Code's folders (ClaudeDirs), every
// session's own file, stat()ed, and session id's files: its own and its
// subagents', as listCCFiles lists them, from the first folder that has
// any (claudeFiles). The other sessions' subagents aren't looked for.
func claudeOwnFiles(id string) (mains, own []file) {
	key := "claude:" + id
	owner := map[string]bool{} // the keys a folder before has
	for _, dir := range ClaudeDirs() {
		projects := filepath.Join(dir, "projects")
		var here, mine []file
		for _, project := range readDirectory(projects) {
			if !dirent.IsFolder(projects, project) {
				continue
			}
			root := filepath.Join(projects, project.Name())
			for _, e := range readDirectory(root) {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
					continue
				}
				sid := strings.TrimSuffix(name, ".jsonl")
				if bridged("claude", sid) {
					continue
				}
				f := file{agent: "claude", key: "claude:" + sid, path: filepath.Join(root, name), main: true}
				here = append(here, f)
				if sid == id {
					mine = append(mine, f)
				}
			}
			if len(own) == 0 {
				mine = append(mine, ccSubagentFiles(root, id)...)
			}
		}
		here = statFiles(here, true)
		for _, f := range here {
			if !owner[f.key] {
				mains = append(mains, f)
			}
		}
		for _, f := range here {
			owner[f.key] = true
		}
		if len(own) == 0 && len(mine) > 0 {
			own = statFiles(mine, true)
			owner[key] = len(own) > 0
		}
	}
	return mains, own
}

// ccSubagentFiles are the subagents' files of session id in the project
// folder root, as listCCFiles lists them: <id>/subagents/*.jsonl, and its
// workflows' agents, <id>/subagents/workflows/<run>/agent-*.jsonl.
func ccSubagentFiles(root, id string) []file {
	key := "claude:" + id
	subs := filepath.Join(root, id, "subagents")
	var out []file
	for _, sub := range readDirectory(subs) {
		if sub.IsDir() || !strings.HasSuffix(sub.Name(), ".jsonl") {
			continue
		}
		out = append(out, file{agent: "claude", key: key, path: filepath.Join(subs, sub.Name())})
	}
	flows := filepath.Join(subs, "workflows")
	for _, run := range readDirectory(flows) {
		if !dirent.IsFolder(flows, run) {
			continue
		}
		dir := filepath.Join(flows, run.Name())
		for _, sub := range readDirectory(dir) {
			if sub.IsDir() || !strings.HasPrefix(sub.Name(), "agent-") || !strings.HasSuffix(sub.Name(), ".jsonl") {
				continue
			}
			out = append(out, file{agent: "claude", key: key, path: filepath.Join(dir, sub.Name())})
		}
	}
	return out
}

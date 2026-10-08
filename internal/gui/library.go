package gui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/mcpauth"
)

// The problems of the last change to the library stay on the page until the
// next one, so a reload still says what an agent couldn't be given.
var lastProblems struct {
	sync.Mutex
	p []library.Problem
}

type libraryJSON struct {
	*library.View
	Result *library.Result `json:"result,omitempty"`
	Home   string          `json:"home"` // for the page to show paths under it as ~
	// Problems are every one still standing, the page's list of what an
	// agent couldn't be given: not only those a chip can carry
	Problems []library.Problem `json:"problems,omitempty"`
}

func libraryView(res *library.Result) (libraryJSON, error) {
	lastProblems.Lock()
	if res != nil {
		lastProblems.p = res.Problems
	}
	p := lastProblems.p
	lastProblems.Unlock()
	v, err := library.Read(p)
	home, _ := os.UserHomeDir()
	return libraryJSON{View: v, Result: res, Home: home, Problems: p}, err
}

// marketJSON is a market's list, and why it may be short: a search that
// couldn't reach the registry still shows what magpie has of its own.
type marketJSON struct {
	Items any    `json:"items"`
	Error string `json:"error,omitempty"`
	// Custom is a server to add by hand, for a search that is an address
	// nothing listed is at
	Custom *library.Custom `json:"custom,omitempty"`
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// revealable is every path the Library page shows: the only ones it may
// ask to be shown in the file manager.
func revealable(v *library.View) []string {
	out := []string{v.Dir, v.Backups}
	for _, a := range v.Agents {
		out = append(out, a.Instructions, a.MCP, a.Skills)
	}
	for _, s := range v.Skills {
		out = append(out, library.SkillPath(s.Name))
		if s.Kind == "folder" {
			out = append(out, s.Source)
		}
	}
	for _, s := range v.FoundSkills {
		out = append(out, s.Link, s.Shared)
	}
	for _, p := range v.Projects {
		out = append(out, p.Dir)
		for _, e := range p.Placed {
			out = append(out, filepath.Join(p.Dir, filepath.FromSlash(e)))
		}
		for f := range p.Wrote {
			out = append(out, filepath.Join(p.Dir, filepath.FromSlash(f)))
		}
	}
	return out
}

func libraryRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/library", func(rw http.ResponseWriter, r *http.Request) {
		v, err := libraryView(nil)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("GET /api/library/skill", func(rw http.ResponseWriter, r *http.Request) {
		text, err := library.SkillText(r.URL.Query().Get("name"))
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"text": text, "path": library.SkillPath(r.URL.Query().Get("name"))})
	})
	// the market: what it offers for a search, and each skill's description
	// once asked for, which skills.sh gives one at a time
	mux.HandleFunc("GET /api/library/market/servers", func(rw http.ResponseWriter, r *http.Request) {
		list, err := library.MarketServers(r.URL.Query().Get("q"))
		writeJSON(rw, marketJSON{Items: list, Error: errText(err), Custom: library.CustomAt(r.URL.Query().Get("q"), list)})
	})
	mux.HandleFunc("GET /api/library/market/skills", func(rw http.ResponseWriter, r *http.Request) {
		list, err := library.MarketSkills(r.URL.Query().Get("q"))
		writeJSON(rw, marketJSON{Items: list, Error: errText(err)})
	})
	mux.HandleFunc("POST /api/library/market/about", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ IDs []string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if len(in.IDs) > 60 {
			in.IDs = in.IDs[:60]
		}
		writeJSON(rw, library.SkillsAbout(in.IDs))
	})
	mux.HandleFunc("GET /api/library/icon", func(rw http.ResponseWriter, r *http.Request) {
		b, ct, err := library.Icon(r.URL.Query().Get("u"))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		rw.Header().Set("Content-Type", ct)
		rw.Header().Set("Cache-Control", "max-age=604800")
		rw.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'") // an SVG opened on its own runs nothing
		rw.Write(b)
	})
	mux.HandleFunc("POST /api/library/skills/probe", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Source string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		p, err := library.ProbeSkills(in.Source)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, p)
	})
	// which skills GitHub has changed: the page as it is, each skill from
	// there carrying what the check found of it
	mux.HandleFunc("POST /api/library/skills/check", func(rw http.ResponseWriter, r *http.Request) {
		if _, err := library.CheckSkills(); err != nil {
			fail(rw, err)
			return
		}
		v, err := libraryView(nil)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// RTK:which agents have its hook, switching one on or off, and
	// installing rtk when the page is asked to
	mux.HandleFunc("GET /api/library/rtk", func(rw http.ResponseWriter, r *http.Request) {
		v := library.ReadRTK()
		// its latest release, and whether winget or Homebrew has it yet,
		// when they answer in time: the page is drawn without them
		// otherwise, and has them next time
		if v.Path != "" {
			checked := make(chan *library.RTKView, 1)
			go func() { c := *v; c.CheckLatest(); checked <- &c }()
			select {
			case v = <-checked:
			case <-time.After(3 * time.Second):
			}
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/library/rtk", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string
			On    bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v, err := library.SetRTK(in.Agent, in.On)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/library/rtk/upgrade", func(rw http.ResponseWriter, r *http.Request) {
		v, err := library.UpgradeRTK()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// rtk put on the PATH the agents get, when magpie found it off it
	mux.HandleFunc("POST /api/library/rtk/path", func(rw http.ResponseWriter, r *http.Request) {
		v, err := library.PathRTK()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/library/rtk/install", func(rw http.ResponseWriter, r *http.Request) {
		v, err := library.InstallRTK()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// a project's folder, from the system's picker; "" when it's cancelled
	mux.HandleFunc("POST /api/library/projects/choose", func(rw http.ResponseWriter, r *http.Request) {
		dir, err := w.ChooseFolder("Choose a project")
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]string{"dir": dir})
	})
	mux.HandleFunc("POST /api/library/reveal", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Path string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Path == "" {
			http.Error(rw, "no path", http.StatusBadRequest)
			return
		}
		v, err := library.Read(nil)
		if err != nil {
			fail(rw, err)
			return
		}
		if !slices.Contains(revealable(v), in.Path) {
			http.Error(rw, "not a path the library shows", http.StatusForbidden)
			return
		}
		// a file is shown in its folder; one not written yet, the folder it will be in
		p := in.Path
		for {
			fi, err := os.Stat(p)
			if err == nil && fi.IsDir() {
				break
			}
			up := filepath.Dir(p)
			if up == p {
				break
			}
			p = up
		}
		if err := w.OpenFolder(p); err != nil {
			fail(rw, err)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
	// magpie signs in to a remote server once, for every agent given it (#615)
	mux.HandleFunc("POST /api/library/mcp-signin", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			http.Error(rw, "no server", http.StatusBadRequest)
			return
		}
		st, err := library.SignInServer(r.Context(), in.Name)
		if err != nil {
			fail(rw, err)
			return
		}
		w.OpenURL(st.URL)
		writeJSON(rw, st)
	})
	mux.HandleFunc("GET /api/library/mcp-signin/{id}", func(rw http.ResponseWriter, r *http.Request) {
		st, ok := mcpauth.Progress(r.PathValue("id"))
		if !ok {
			http.NotFound(rw, r)
			return
		}
		writeJSON(rw, st)
	})
	mux.HandleFunc("POST /api/library/mcp-signin/{id}/cancel", func(rw http.ResponseWriter, r *http.Request) {
		mcpauth.Cancel(r.PathValue("id"))
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/library/mcp-signout", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			http.Error(rw, "no server", http.StatusBadRequest)
			return
		}
		if err := library.SignOutServer(in.Name); err != nil {
			fail(rw, err)
			return
		}
		v, err := libraryView(nil)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// whether the servers work, by connecting to them: those named, or every
	// one; a server checked this session as it is now is answered from then
	mux.HandleFunc("POST /api/library/mcp/check", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Names []string
			Fresh bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		m, err := library.CheckServers(r.Context(), in.Names, in.Fresh)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"servers": m})
	})
	// every change answers with the page as it is after it, and what it did
	mux.HandleFunc("POST /api/library/{what}/{action}", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Name string
			Old  string
			// Agents is named as the page sends it, so that it is this
			// and not the instructions' own Agents that "agents" fills
			Agents []string `json:"agents"`
			Source string
			Paths  []string
			Names  []string // the skills to update, of those a check found changed; to bring in, of those found in the agents
			Server library.Server
			Agent  string            // the agent whose own skill is in the library's way
			ID     string            // a market server's, or a market skill's in its repository
			Values map[string]string // what a market server needs
			Dir    string            // a project's folder
			Copy   bool              // a project gets copies, not links
			Keep   bool              // a project removed keeps what magpie put in it
			On     bool              // every skill or server given to the agents, or taken from them
			How    string            // link or copy: how skills are given to Agent, or to every agent with none (#896)
			library.InstructionsChange
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var res *library.Result
		var err error
		switch r.PathValue("what") + "/" + r.PathValue("action") {
		case "instructions/save":
			c := in.InstructionsChange
			c.Agents = in.Agents
			res, err = library.SaveInstructions(c)
		case "instructions/import":
			res, err = library.ImportInstructions(in.Name)
		case "servers/save":
			res, err = library.SaveServer(in.Old, in.Server)
		case "servers/agents":
			res, err = library.ServerAgents(in.Name, in.Agents)
		case "servers/agents-all":
			res, err = library.EveryServerAgents(in.Agents, in.On)
		case "servers/agents-some":
			res, err = library.SomeServersAgents(in.Names, in.Agents, in.On)
		case "servers/remove":
			res, err = library.RemoveServer(in.Name)
		case "servers/remove-all":
			res, err = library.RemoveServers(in.Names)
		case "servers/import":
			res, err = library.ImportServer(in.Name)
		case "skills/install":
			res, err = library.InstallSkills(in.Source, in.Paths, in.Agents)
		case "skills/update":
			res, err = library.UpdateSkill(in.Name)
		case "skills/update-all":
			res, err = library.UpdateSkills()
		case "skills/update-some":
			res, err = library.UpdateSomeSkills(in.Names)
		case "skills/agents":
			res, err = library.SkillAgents(in.Name, in.Agents)
		case "skills/agents-all":
			res, err = library.EverySkillAgents(in.Agents, in.On)
		case "skills/agents-some":
			res, err = library.SomeSkillsAgents(in.Names, in.Agents, in.On)
		case "skills/remove":
			res, err = library.RemoveSkill(in.Name)
		case "skills/remove-all":
			res, err = library.RemoveSkills(in.Names)
		case "skills/group":
			res, err = library.GroupSkills(in.Old, in.Name, in.Names)
		case "skills/ungroup":
			res, err = library.UngroupSkills(in.Name, in.Names)
		case "skills/add-new":
			res, err = library.AddNewSkills(in.Names)
		case "skills/ignore-new":
			err = library.IgnoreNewSkills(in.Names)
		case "skills/import":
			res, err = library.ImportSkill(in.Name)
		case "skills/import-all":
			res, err = library.ImportSkills(in.Names)
		case "skills/remove-found":
			res, err = library.RemoveFoundSkill(in.Name)
		case "skills/use-library":
			res, err = library.UseLibrarySkill(in.Name, in.Agent)
		case "skills/how":
			res, err = library.SetSkillHow(in.Agent, in.How)
		case "skills/keep-own":
			res, err = library.KeepAgentSkill(in.Name, in.Agent)
		case "market/server":
			res, err = library.InstallServer(in.ID, in.Values, in.Agents)
		case "market/skill":
			res, err = library.InstallMarketSkill(in.Source, in.ID, in.Agents)
		case "projects/add":
			res, err = library.AddProject(in.Dir)
		case "projects/remove":
			res, err = library.RemoveProject(in.Dir, in.Keep)
		case "projects/skill":
			res, err = library.ProjectSkill(in.Dir, in.Name, in.Agents)
		case "projects/server":
			res, err = library.ProjectServer(in.Dir, in.Name, in.Agents)
		case "projects/copy":
			res, err = library.ProjectCopy(in.Dir, in.Copy)
		case "all/sync":
			res, err = library.Sync()
		default:
			http.NotFound(rw, r)
			return
		}
		if err != nil {
			fail(rw, err)
			return
		}
		v, err := libraryView(res)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
}

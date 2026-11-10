package gui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
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
	// SubagentModels are what a subagent's model may be set to: the models
	// and routing groups Codex's lists show, the only agent that takes one
	// (library.Subagent.Model). CodexSubagentModel is the model the
	// gateway puts every Codex subagent on when set on the Agents page,
	// which a subagent's own then gives way to.
	SubagentModels     []subagentModel `json:"subagentModels"`
	CodexSubagentModel string          `json:"codexSubagentModel,omitempty"`
}

// subagentModel is a model a subagent may be set to run on.
type subagentModel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Note  string `json:"note,omitempty"`
	Group bool   `json:"group,omitempty"`
}

func subagentModels() []subagentModel {
	out := []subagentModel{}
	for _, e := range provider.CodexSubagentModels() {
		m := subagentModel{ID: e.ID, Name: e.Name, Note: e.Provider.Name, Group: e.Group != ""}
		if a := e.Provider.Account; a != nil && a.User != "" {
			m.Note = a.User
		}
		if m.Group {
			m.Note = ""
		}
		out = append(out, m)
	}
	return out
}

func libraryView(res *library.Result) (libraryJSON, error) { return libraryRead(res, false) }

// libraryRead is libraryView, or with glance the page but for what takes
// reading the agents' skill folders through (library.Glance), for the
// page to show at once while the rest is read (#541).
func libraryRead(res *library.Result, glance bool) (libraryJSON, error) {
	lastProblems.Lock()
	if res != nil {
		lastProblems.p = res.Problems
	}
	p := lastProblems.p
	lastProblems.Unlock()
	read := library.Read
	if glance {
		read = library.Glance
	}
	v, err := read(p)
	home, _ := os.UserHomeDir()
	return libraryJSON{View: v, Result: res, Home: home, Problems: p,
		SubagentModels: subagentModels(), CodexSubagentModel: provider.CodexSubagentModel()}, err
}

// instructionsJSON answers a change to the instructions: what the page's
// Instructions tab shows, without reading the rest of the library again.
// That read goes through every agent's skill folders, which on Windows made
// each instruction made, saved or removed take up to a minute (#1624).
type instructionsJSON struct {
	Part         string                    `json:"part"` // "instructions": the page keeps the rest it has
	Instructions *library.InstructionsView `json:"instructions"`
	Result       *library.Result           `json:"result,omitempty"`
	Problems     []library.Problem         `json:"problems,omitempty"`
}

func instructionsView(res *library.Result) (instructionsJSON, error) {
	lastProblems.Lock()
	// an instruction change wrote only the instructions: the problems the
	// last change had with servers and skills still stand
	p := []library.Problem{}
	for _, x := range lastProblems.p {
		if x.What != "instructions" {
			p = append(p, x)
		}
	}
	if res != nil {
		p = append(p, res.Problems...)
	}
	lastProblems.p = p
	lastProblems.Unlock()
	iv, err := library.ReadInstructions()
	return instructionsJSON{Part: "instructions", Instructions: iv, Result: res, Problems: p}, err
}

// marketJSON is a market's list, and why it may be short: a search that
// couldn't reach the registry still shows what magpie has of its own.
type marketJSON struct {
	Items any    `json:"items"`
	Error string `json:"error,omitempty"`
	// Custom is a server to add by hand, for a search that is an address
	// nothing listed is at
	Custom *library.Custom `json:"custom,omitempty"`
	// Failed is the source the search couldn't reach, and Cause why; the
	// items are then what the others found (#1589)
	Failed string `json:"failed,omitempty"`
	Cause  string `json:"cause,omitempty"`
}

func marketOf(items any, err error) marketJSON {
	m := marketJSON{Items: items, Error: errText(err)}
	if se := (*library.SearchError)(nil); errors.As(err, &se) {
		m.Failed, m.Cause = se.Source, se.Err.Error()
	}
	return m
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
	browserRoutes(mux, w)
	memoryRoutes(mux)
	memoryDistillRoutes(mux)
	mux.HandleFunc("GET /api/library", func(rw http.ResponseWriter, r *http.Request) {
		v, err := libraryRead(nil, r.URL.Query().Get("glance") == "1")
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("GET /api/library/skill", func(rw http.ResponseWriter, r *http.Request) {
		f, err := library.ReadSkillFile(r.URL.Query().Get("name"), r.URL.Query().Get("file"))
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, f)
	})
	// the market: what it offers for a search, and each skill's description
	// once asked for, which skills.sh gives one at a time
	mux.HandleFunc("GET /api/library/market/servers", func(rw http.ResponseWriter, r *http.Request) {
		list, err := library.MarketServers(r.URL.Query().Get("q"))
		for i := range list {
			// magpie's own that can't answer yet says why on its row
			list[i].Unready = gateway.OwnUnready(list[i].ID)
		}
		m := marketOf(list, err)
		m.Custom = library.CustomAt(r.URL.Query().Get("q"), list)
		writeJSON(rw, m)
	})
	mux.HandleFunc("GET /api/library/market/skills", func(rw http.ResponseWriter, r *http.Request) {
		list, err := library.MarketSkills(r.URL.Query().Get("q"))
		writeJSON(rw, marketOf(list, err))
	})
	mux.HandleFunc("GET /api/library/market/pi", func(rw http.ResponseWriter, r *http.Request) {
		list, err := library.MarketPiPackages(r.URL.Query().Get("q"))
		writeJSON(rw, marketOf(list, err))
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
	// Group again: the skills traced to their repositories once more, the
	// page as it is then with how many joined a group
	mux.HandleFunc("POST /api/library/skills/regroup", func(rw http.ResponseWriter, r *http.Request) {
		n, err := library.RegroupSkills()
		if err != nil {
			fail(rw, err)
			return
		}
		v, err := libraryView(nil)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, struct {
			libraryJSON
			Regrouped int `json:"regrouped"`
		}{v, n})
	})
	// Pi's extensions and packages, for each Pi here (this computer's and
	// each WSL distro's): switched on or off as Pi itself does it, and
	// copied from one Pi to another
	mux.HandleFunc("GET /api/library/pi", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, library.ReadPiExtensions())
	})
	mux.HandleFunc("POST /api/library/pi", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Pi, Ext, Pkg string
			On           bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var v *library.PiExtensions
		var err error
		switch {
		case in.Ext != "" && in.Pkg == "":
			v, err = library.SetPiExtension(in.Pi, in.Ext, in.On)
		case in.Pkg != "" && in.Ext == "":
			v, err = library.SetPiPackage(in.Pi, in.Pkg, in.On)
		default:
			err = errors.New("name one extension or one package")
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/library/pi/copy", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ From, To, Ext, Pkg string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v, err := library.CopyPi(in.From, in.To, in.Ext, in.Pkg)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// a package from Pi's market installed into a Pi, or one of a Pi's
	// packages removed, by Pi's own install and remove
	mux.HandleFunc("POST /api/library/pi/install", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Pi, Source string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v, err := library.InstallPiPackage(in.Pi, in.Source)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/library/pi/remove", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Pi, Source string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v, err := library.RemovePiPackage(in.Pi, in.Source)
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
		// its latest release, and what the package manager it was installed
		// with has of that release, when GitHub answers in time: the page is
		// drawn without them otherwise, and has them next time
		if v.Path != "" {
			checked := make(chan *library.RTKView, 1)
			go func() { c := *v; c.CheckLatest(); checked <- &c }()
			select {
			case v = <-checked:
			case <-time.After(3 * time.Second):
			}
			// what the package manager has is asked after the answer goes out:
			// `winget show` refreshes its sources and takes seconds, which is
			// longer than the wait above, so asking it there held the whole tab
			// for a tag that then never arrived (#1025). It is one read of the
			// tab away, and the answer is kept for hours either way.
			go library.CheckChannel(v)
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
	// distro: in that WSL distro, by rtk's own Linux installer run there
	mux.HandleFunc("POST /api/library/rtk/install", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Distro string }
		// {} or no body: this computer; a body that can't be read, nowhere
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			http.Error(rw, "bad body", http.StatusBadRequest)
			return
		}
		var v *library.RTKView
		var err error
		if in.Distro != "" {
			v, err = library.InstallRTKIn(in.Distro)
		} else {
			v, err = library.InstallRTK()
		}
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// billion-context: which agents have its plugin, switching one on or
	// off, and installing bili when the page is asked to
	mux.HandleFunc("GET /api/library/bili", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, library.ReadBili())
	})
	mux.HandleFunc("POST /api/library/bili", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string
			On    bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		v, err := library.SetBili(in.Agent, in.On)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/library/bili/install", func(rw http.ResponseWriter, r *http.Request) {
		v, err := library.InstallBili()
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
		writeJSON(rw, mcpSignInOpened{st, openError(w, st.URL)})
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
	// the servers as an agent's project file has them, for the user to
	// paste into a project magpie doesn't keep (#1478); nothing is written
	mux.HandleFunc("POST /api/library/mcp/config", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent string
			Names []string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		c, err := library.ConfigOfServers(in.Agent, in.Names)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, c)
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
			Hook   library.Hook // a hook saved (#1790)
			// a subagent saved (#2283)
			Subagent library.Subagent
			Agent    string            // the agent whose own skill is in the library's way
			App      string            // the app the MCP hub is added to (servers/hub-open)
			Note     string            // what the MCP router says of itself (servers/hub-note), or a skill's note (skills/note)
			ID       string            // a market server's, or a market skill's in its repository
			Values   map[string]string // what a market server needs
			Dir      string            // a project's folder
			Copy     bool              // a project gets copies, not links
			Keep     bool              // a project removed keeps what magpie put in it
			On       bool              // every skill or server given to the agents, or taken from them
			How      string            // link or copy: how skills are given to Agent, or to every agent with none (#896)
			// Replace updates a skill changed here all the same, its
			// changed version kept with the backups (#1449)
			Replace bool
			// a skill's file saved: its place in the skill, its new text,
			// and what it held when the page opened it
			File, Text, Was string
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
		case "servers/hub":
			// magpie as the agents' one MCP server (#2679)
			res, err = library.SetMCPHub(in.On)
		case "servers/hub-note":
			// what the router says of itself to the agents, this
			// computer's own (settings.RouterNote)
			err = settings.SetRouterNote(in.Note)
		case "servers/hub-tools":
			// every tool given whole, not on demand (settings.RouterAllTools)
			err = settings.SetRouterAllTools(in.On)
		case "servers/hub-open":
			// the hub added to another app through its install link,
			// built here: the page names the app, never the link
			var link string
			if link, err = library.HubLink(in.App); err == nil {
				err = w.OpenURL(link)
			}
		case "servers/tools":
			// the tools its agents leave out (nf1qaz2w on Discord)
			res, err = library.ServerTools(in.Name, in.Names)
		case "servers/plugin-on":
			// a plugin's tools off for every agent, or on again, from its
			// row on the Plugins page (Name is the plugin's)
			res, err = library.PluginServerOn(in.Name, in.On)
		case "servers/on":
			// off for every agent, project and app at the router's
			// address, kept with its settings, or on again as it was
			// (swang20201 on X)
			res, err = library.ServerOn(in.Name, in.On)
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
		case "servers/remove-found":
			res, err = library.RemoveFoundServer(in.Name)
		case "hooks/save":
			res, err = library.SaveHook(in.Old, in.Hook)
		case "hooks/agents":
			res, err = library.HookAgents(in.Name, in.Agents)
		case "hooks/agents-some":
			res, err = library.SomeHooksAgents(in.Names, in.Agents, in.On)
		case "hooks/remove":
			res, err = library.RemoveHooks([]string{in.Name})
		case "hooks/remove-all":
			res, err = library.RemoveHooks(in.Names)
		case "subagents/save":
			res, err = library.SaveSubagent(in.Old, in.Subagent)
		case "subagents/agents":
			res, err = library.SubagentAgentsSet(in.Name, in.Agents)
		case "subagents/remove":
			res, err = library.RemoveSubagents([]string{in.Name})
		case "skills/install":
			res, err = library.InstallSkills(in.Source, in.Paths, in.Agents)
		case "skills/update":
			res, err = library.UpdateSkill(in.Name, in.Replace)
			var gone *library.GoneError
			if errors.As(err, &gone) {
				// removed from its source: the page shows it so, not a failure
				res, err = &library.Result{Changed: []string{}, Problems: []library.Problem{}, Gone: []string{gone.Name}}, nil
			}
		case "skills/remove-gone":
			res, err = library.RemoveGoneSkills(in.Names)
		case "skills/update-all":
			res, err = library.UpdateSkills()
		case "skills/update-some":
			res, err = library.UpdateSomeSkills(in.Names)
		case "skills/agents":
			res, err = library.SkillAgents(in.Name, in.Agents)
		case "skills/save":
			// a skill's file edited in the page, given to its agents again
			res, err = library.SaveSkillFile(in.Name, in.File, in.Text, in.Was)
		case "skills/here":
			res, err = library.SkillHere(in.Name, in.On)
		case "skills/note":
			// the user's own note on it, which no agent is given
			res, err = library.SkillNote(in.Name, in.Note)
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
		case "skills/remove-found-some":
			res, err = library.RemoveFoundSkills(in.Names)
		case "skills/hand-over":
			// out of ~/.agents/skills, into the library's own folder (tnt1223)
			res, err = library.HandOverSkills(oneOrMore(in.Name, in.Names))
		case "skills/use-library":
			res, err = library.UseLibrarySkills(oneOrMore(in.Name, in.Names), in.Agent)
		case "skills/how":
			res, err = library.SetSkillHow(in.Agent, in.How)
		case "skills/where":
			// each agent's own folder, or ~/.agents/skills too (skills_shared.go)
			res, err = library.SetSkillsWhere(in.How)
		case "skills/router":
			// magpie's one skill listing the rest (skill_router.go)
			res, err = library.SetSkillRouter(in.On)
		case "skills/router-direct":
			// a skill kept out of the router, each agent's own link
			res, err = library.SkillRouterHow(in.Name, in.On)
		case "skills/keep-own":
			res, err = library.KeepAgentSkills(oneOrMore(in.Name, in.Names), in.Agent)
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
		case "projects/skills":
			res, err = library.ProjectSkills(in.Dir, in.Names, in.Agents)
		case "projects/server":
			res, err = library.ProjectServer(in.Dir, in.Name, in.Agents)
		case "projects/server-off":
			// a Library server turned off in this project only
			res, err = library.ProjectServerOff(in.Dir, in.Name, in.Agents)
		case "projects/skill-off":
			res, err = library.ProjectSkillsOff(in.Dir, oneOrMore(in.Name, in.Names), in.Agents)
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
		if r.PathValue("what") == "instructions" {
			v, err := instructionsView(res)
			if err != nil {
				fail(rw, err)
				return
			}
			writeJSON(rw, v)
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

// oneOrMore is the skills an agent's own are settled for: the several the
// Library's warning sends for one agent together (#1896), or the one.
func oneOrMore(name string, names []string) []string {
	if len(names) > 0 {
		return names
	}
	return []string{name}
}

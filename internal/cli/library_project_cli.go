package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/library"
)

const libraryProjectUsage = `magpie library project [--json]      the projects (Library › Projects) and the skills each has
  magpie library project add <dir>         add a folder whose agents can be given library skills
  magpie library project rm <dir> [--keep-files]   take it off; --keep-files leaves what magpie put there, yours from then on
  magpie library project skills <dir> [--json]     its skills, for which agents, and where each is placed
  magpie library project skill add <dir> <skill…> [agents=a,b…]   give it skills: by default for the agents its skills
                                     are for already, or, with none yet, every agent here that reads a project's skills
  magpie library project skill rm <dir> <skill…>   take skills out of it
  magpie library project skill agents <dir> <skill> <a,b…|none>   the agents it has the skill for
  magpie library project copy <dir> on|off copies of its skills in place of links to the library's
                                     <dir> may be relative, or "." for the folder you're in. A skill that couldn't be
                                     placed (a folder there that isn't magpie's is left as it is) is named, and the
                                     command exits with status 1.
`

// libraryProjectCmd is magpie library project …: the window's Library ›
// Projects page from a terminal (#5193), through the same library calls
// its page makes.
func libraryProjectCmd(args []string) error {
	usage := func() error { return fmt.Errorf("usage:\n  %s", libraryProjectUsage) }
	if len(args) == 0 || len(args) == 1 && (args[0] == "list" || args[0] == "--json") || len(args) == 2 && args[0] == "list" && args[1] == "--json" {
		return listProjects("", slices.Contains(args, "--json"))
	}
	switch sub, rest := args[0], args[1:]; sub {
	case "help", "-h", "--help":
		fmt.Print("  " + libraryProjectUsage)
		return nil
	case "add":
		if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
			return usage()
		}
		dir, err := library.ProjectPath(rest[0])
		if err != nil {
			return err
		}
		if _, err := library.AddProject(dir); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), dir, "is a project")
		fmt.Println(muted.Render("  magpie library project skill add " + quoteArg(dir) + " <skill…> gives it skills"))
		return nil
	case "rm", "remove":
		keep := false
		var dirs []string
		for _, a := range rest {
			switch {
			case a == "--keep-files":
				keep = true
			case strings.HasPrefix(a, "-"):
				return usage()
			default:
				dirs = append(dirs, a)
			}
		}
		if len(dirs) != 1 {
			return usage()
		}
		dir, err := projectDir(dirs[0])
		if err != nil {
			return err
		}
		if _, err := library.RemoveProject(dir, keep); err != nil {
			return err
		}
		// what couldn't be taken out keeps the project listed
		if p, _ := library.ProjectAt(dir); p != "" {
			return fmt.Errorf("%s stays a project: magpie couldn't take everything it placed out of it (magpie library project skills %s)", dir, quoteArg(dir))
		}
		if keep {
			fmt.Println(green.Render("✓"), dir, "is no project now; what magpie put there is left as it is")
		} else {
			fmt.Println(green.Render("✓"), dir, "is no project now; what magpie put there is taken out")
		}
		return nil
	case "skills":
		asJSON := slices.Contains(rest, "--json")
		rest = slices.DeleteFunc(slices.Clone(rest), func(a string) bool { return a == "--json" })
		if len(rest) != 1 || strings.HasPrefix(rest[0], "-") {
			return usage()
		}
		dir, err := projectDir(rest[0])
		if err != nil {
			return err
		}
		return listProjects(dir, asJSON)
	case "copy":
		if len(rest) != 2 || strings.HasPrefix(rest[0], "-") || rest[1] != "on" && rest[1] != "off" {
			return usage()
		}
		dir, err := projectDir(rest[0])
		if err != nil {
			return err
		}
		res, err := library.ProjectCopy(dir, rest[1] == "on")
		if err != nil {
			return err
		}
		if err := projectOutcome(dir, nil, res); err != nil {
			return err
		}
		if rest[1] == "on" {
			fmt.Println(green.Render("✓"), dir, "gets copies of its skills, made again whenever the library's skill changes")
		} else {
			fmt.Println(green.Render("✓"), dir, "gets links to the library's skills")
		}
		return nil
	case "skill":
		if len(rest) == 0 {
			return usage()
		}
		return projectSkillCmd(rest[0], rest[1:], usage)
	}
	return fmt.Errorf("no library project command %q\n  %s", args[0], libraryProjectUsage)
}

func projectSkillCmd(verb string, rest []string, usage func() error) error {
	if len(rest) < 2 || strings.HasPrefix(rest[0], "-") {
		return usage()
	}
	dir, err := projectDir(rest[0])
	if err != nil {
		return err
	}
	v, err := library.Read(nil)
	if err != nil {
		return err
	}
	p := projectView(v, dir)
	if p == nil {
		return fmt.Errorf("%s isn't a project", dir)
	}
	switch verb {
	case "add":
		var names, agents []string
		given := false
		for _, a := range rest[1:] {
			if s, ok := strings.CutPrefix(a, "agents="); ok {
				if agents, err = projectAgents(s); err != nil {
					return err
				}
				given = true
				continue
			}
			if strings.HasPrefix(a, "-") {
				return usage()
			}
			names = addOnce(names, a)
		}
		if len(names) == 0 {
			return usage()
		}
		if !given {
			agents = defaultProjectAgents(v, p)
		}
		if len(agents) == 0 {
			return fmt.Errorf("no agent here reads a project's skills: name them with agents=a,b…")
		}
		res, err := library.ProjectSkills(dir, names, agents)
		if err != nil {
			return err
		}
		return projectOutcome(dir, names, res)
	case "rm", "remove":
		var names []string
		for _, a := range rest[1:] {
			if strings.HasPrefix(a, "-") {
				return usage()
			}
			if _, ok := p.Skills[a]; !ok {
				return fmt.Errorf("%s has no skill %s", dir, a)
			}
			names = addOnce(names, a)
		}
		res, err := library.ProjectSkills(dir, names, nil)
		if err != nil {
			return err
		}
		if err := projectOutcome(dir, nil, res); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), strings.Join(names, ", "), "taken out of", dir)
		return nil
	case "agents":
		if len(rest) != 3 || strings.HasPrefix(rest[1], "-") || strings.HasPrefix(rest[2], "-") {
			return usage()
		}
		agents, err := projectAgents(rest[2])
		if err != nil {
			return err
		}
		res, err := library.ProjectSkill(dir, rest[1], agents)
		if err != nil {
			return err
		}
		if len(agents) == 0 {
			if err := projectOutcome(dir, nil, res); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), rest[1], "taken out of", dir)
			return nil
		}
		return projectOutcome(dir, []string{rest[1]}, res)
	}
	return fmt.Errorf("no library project skill command %q\n  %s", verb, libraryProjectUsage)
}

// addOnce adds s to a list once, in the order given.
func addOnce(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// projectDir is the listed folder of the project typed, or why there's none.
func projectDir(typed string) (string, error) {
	dir, err := library.ProjectAt(typed)
	if err != nil {
		return "", err
	}
	if dir == "" {
		abs, _ := library.ProjectPath(typed)
		return "", fmt.Errorf("%s isn't a project: magpie library project add %s adds it", abs, quoteArg(abs))
	}
	return dir, nil
}

// projectAgents is a comma list of agents (none for no agent), each by its
// id, refused when magpie knows of no folder it reads a project's skills
// from.
func projectAgents(s string) ([]string, error) {
	ids, err := libraryAgents(s, "skills")
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if library.ProjectSkillsDir(id) == "" {
			return nil, fmt.Errorf("magpie knows of no folder %s reads a project's skills from", id)
		}
	}
	return ids, nil
}

// defaultProjectAgents is who a skill added with no agents= is for, as on
// the page's Skills menu: the agents the project's skills are for already,
// or, in a project with none yet, every agent here that reads a project's
// skills.
func defaultProjectAgents(v *library.View, p *library.ProjectView) []string {
	var all []string
	for _, a := range v.Agents {
		if a.ProjectSkills != "" && !a.Gone && !a.Unseen {
			all = append(all, a.ID)
		}
	}
	var used []string
	for _, ids := range p.Skills {
		for _, id := range ids {
			if slices.Contains(all, id) && !slices.Contains(used, id) {
				used = append(used, id)
			}
		}
	}
	if len(used) > 0 {
		sort.Strings(used)
		return used
	}
	var out []string
	for _, a := range v.Agents {
		if slices.Contains(all, a.ID) && a.Asleep == "" {
			out = append(out, a.ID)
		}
	}
	return out
}

func projectView(v *library.View, dir string) *library.ProjectView {
	for i := range v.Projects {
		if v.Projects[i].Dir == dir {
			return &v.Projects[i]
		}
	}
	return nil
}

// projectOutcome says, for each skill named, where it was placed or why
// it wasn't, and the project's own problem; one not placed is an error,
// for a script to tell "installed" from "left as it is" by the status.
func projectOutcome(dir string, names []string, res *library.Result) error {
	v, err := library.Read(res.Problems)
	if err != nil {
		return err
	}
	p := projectView(v, dir)
	if p == nil {
		return fmt.Errorf("%s isn't a project", dir)
	}
	if why := p.Problems[""]; why != "" {
		fmt.Println(amber.Render("!"), dir+":", why)
	}
	var unplaced []string
	for _, n := range names {
		placed, why := skillPlaces(p, n)
		if why != "" {
			unplaced = append(unplaced, n)
			fmt.Println(amber.Render("!"), n, muted.Render("not placed:"), why)
			continue
		}
		fmt.Println(green.Render("✓"), n, "→", strings.Join(p.Skills[n], ", "), muted.Render(strings.Join(placed, " ")))
	}
	if len(unplaced) > 0 {
		return fmt.Errorf("%s not placed in %s", strings.Join(unplaced, ", "), dir)
	}
	return nil
}

// skillPlaces are the entries of a project's skill magpie placed, and,
// for one its agents should have and don't, why.
func skillPlaces(p *library.ProjectView, name string) (placed []string, why string) {
	want := map[string]bool{}
	for _, id := range p.Skills[name] {
		if d := library.ProjectSkillsDir(id); d != "" {
			want[d+"/"+name] = true
		}
	}
	for e := range want {
		if slices.Contains(p.Placed, e) {
			placed = append(placed, e)
		}
	}
	sort.Strings(placed)
	if why = p.Problems[name]; why == "" && len(placed) < len(want) {
		why = "not in the project (magpie library sync says why)"
	}
	return placed, why
}

// listProjects prints the projects, or one, with their skills.
func listProjects(dir string, asJSON bool) error {
	v, err := library.Read(nil)
	if err != nil {
		return err
	}
	ps := v.Projects
	if dir != "" {
		p := projectView(v, dir)
		if p == nil {
			return fmt.Errorf("%s isn't a project", dir)
		}
		ps = []library.ProjectView{*p}
	}
	if asJSON {
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		if dir != "" {
			return e.Encode(ps[0])
		}
		return e.Encode(ps)
	}
	if len(ps) == 0 {
		fmt.Println(muted.Render("No projects yet · magpie library project add <dir>"))
		return nil
	}
	for _, p := range ps {
		head := bold.Render(p.Name) + " " + muted.Render(p.Dir)
		if p.Missing {
			head += " " + amber.Render("(the folder is gone)")
		} else if p.Copy {
			head += " " + muted.Render("(copies)")
		}
		fmt.Println(head)
		names := make([]string, 0, len(p.Skills))
		for n := range p.Skills {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Println(" ", muted.Render("no skills yet · magpie library project skill add "+quoteArg(p.Dir)+" <skill…>"))
		}
		for _, n := range names {
			placed, why := skillPlaces(&p, n)
			if why != "" {
				fmt.Printf("  %s %s → %s %s\n", amber.Render("!"), bold.Render(n), strings.Join(p.Skills[n], ", "), muted.Render(why))
				continue
			}
			fmt.Printf("  %s → %s %s\n", bold.Render(n), strings.Join(p.Skills[n], ", "), muted.Render(strings.Join(placed, " ")))
		}
	}
	return nil
}

// quoteArg is a path as a shell takes it back.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()&;|<>#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

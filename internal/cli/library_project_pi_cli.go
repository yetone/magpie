package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/library"
)

// projectPiCmd is magpie library project pi …: the packages of a project's
// .pi/settings.json (#7249), through the calls the Library page makes.
func projectPiCmd(rest []string, usage func() error) error {
	if len(rest) == 0 {
		return usage()
	}
	verb, args := rest[0], rest[1:]
	if !slices.Contains([]string{"add", "on", "off", "rm", "remove", "adopt", "list"}, verb) {
		verb, args = "list", rest
	}
	if verb == "list" {
		asJSON := slices.Contains(args, "--json")
		args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--json" })
		if len(args) != 1 || strings.HasPrefix(args[0], "-") {
			return usage()
		}
		dir, err := projectDir(args[0])
		if err != nil {
			return err
		}
		return listProjectPi(dir, asJSON)
	}
	if len(args) < 2 || strings.HasPrefix(args[0], "-") {
		return usage()
	}
	dir, err := projectDir(args[0])
	if err != nil {
		return err
	}
	var bad []string
	for _, src := range args[1:] {
		var res *library.Result
		switch verb {
		case "add", "on":
			res, err = library.ProjectPi(dir, src, "on")
		case "off":
			res, err = library.ProjectPi(dir, src, "off")
		case "rm", "remove":
			res, err = library.ProjectPi(dir, src, "removed")
		case "adopt":
			res, err = library.ProjectPiAdopt(dir, src)
		}
		if err != nil {
			fmt.Println(amber.Render("!"), src, muted.Render(err.Error()))
			bad = append(bad, src)
			continue
		}
		v, err := library.Read(res.Problems)
		if err != nil {
			return err
		}
		p := projectView(v, dir)
		if p == nil {
			return fmt.Errorf("%s isn't a project", dir)
		}
		if why := p.Problems["pi:"+src]; why != "" {
			fmt.Println(amber.Render("!"), src, muted.Render(why))
			bad = append(bad, src)
			continue
		}
		state := "removed"
		for _, x := range p.Pi {
			if x.Source == src {
				state = x.State
			}
		}
		fmt.Println(green.Render("✓"), src, muted.Render(state))
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s not changed in %s", strings.Join(bad, ", "), dir)
	}
	return nil
}

func listProjectPi(dir string, asJSON bool) error {
	v, err := library.Read(nil)
	if err != nil {
		return err
	}
	p := projectView(v, dir)
	if p == nil {
		return fmt.Errorf("%s isn't a project", dir)
	}
	type item struct {
		Source  string `json:"source"`
		State   string `json:"state"`
		Adopted bool   `json:"adopted,omitempty"`
		By      string `json:"by"` // "magpie" or "project"
	}
	items := []item{}
	for _, x := range p.Pi {
		items = append(items, item{x.Source, x.State, x.Adopted, "magpie"})
	}
	for _, o := range p.Own["pi"] {
		st := "on"
		if o.Event == "off" {
			st = "off"
		}
		items = append(items, item{o.Name, st, false, "project"})
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"dir": dir, "items": items})
	}
	if len(items) == 0 {
		fmt.Println(muted.Render("nothing for Pi in " + dir))
		return nil
	}
	for _, it := range items {
		who := it.By
		if it.Adopted {
			who = "adopted"
		}
		fmt.Println(it.Source, muted.Render(it.State+" · "+who))
	}
	return nil
}

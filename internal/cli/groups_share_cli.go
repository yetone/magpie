package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/groupshare"
	"github.com/yetone/magpie/internal/provider"
)

// groupShareCmd: `magpie group share <id> [--title …] [--about …] [--by …]
// [--unlisted] | --stop | --text`, the Routing page's Share… sheet.
func groupShareCmd(rest []string) error {
	usage := "magpie group share <id> [--title <title>] [--about <text>] [--by <name>] [--unlisted] | --stop | --text\n       magpie group share <link> --stop   (a link a removed group was shared at)"
	var id string
	meta := groupshare.Meta{Public: true}
	stop, text := false, false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		val := func() (string, error) {
			if i+1 >= len(rest) {
				return "", fmt.Errorf("%s needs a value\n%s", a, usage)
			}
			i++
			return rest[i], nil
		}
		var err error
		switch a {
		case "--title":
			meta.Title, err = val()
		case "--about", "--description":
			meta.Description, err = val()
		case "--by", "--author":
			meta.Author, err = val()
		case "--unlisted":
			meta.Public = false
		case "--stop":
			stop = true
		case "--text":
			text = true
		case "-h", "--help":
			fmt.Println(usage)
			return nil
		default:
			if strings.HasPrefix(a, "-") || id != "" {
				return fmt.Errorf("magpie group share has no %q\n%s", a, usage)
			}
			id = a
		}
		if err != nil {
			return err
		}
	}
	if id == "" {
		return errors.New(usage)
	}
	g, err := findGroup(id)
	if err != nil {
		// a link a removed group was shared at can still be taken down
		if old, ok := groupshare.RetiredLink(id); ok && stop {
			if err := groupshare.Unpublish(context.Background(), old.ID); err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), bold.Render(old.URL), "is no longer shared")
			return nil
		}
		return err
	}
	switch {
	case text:
		// the group as text, to paste into another magpie: nothing goes
		// to usemagpie.ai
		s, err := provider.ShareGroup(g.ID)
		if err != nil {
			return err
		}
		fmt.Println(s)
		return nil
	case stop:
		if err := groupshare.Unpublish(context.Background(), g.ID); err != nil {
			return err
		}
		fmt.Println(green.Render("✓"), bold.Render(g.Name), "is no longer shared")
		return nil
	}
	if old, ok := groupshare.Shared()[g.ID]; ok {
		meta = mergeMeta(old.Meta, meta, rest)
	}
	if strings.TrimSpace(meta.Title) == "" {
		meta.Title = g.Name
	}
	m, err := groupshare.Publish(context.Background(), g.ID, meta)
	if err != nil {
		return err
	}
	how := "public: listed on usemagpie.ai/groups"
	if !m.Public {
		how = "unlisted: only its link opens it"
	}
	fmt.Println(green.Render("✓"), bold.Render(g.Name), "is shared at", bold.Render(m.URL), muted.Render("· "+how+" · version "+fmt.Sprint(m.Version)))
	fmt.Println(muted.Render("  no key, account or folder goes with it; a provider you added yourself is named custom"))
	return nil
}

// mergeMeta is what a group shared already is shared again with: the
// words it had, but for those flags named anew.
func mergeMeta(old, now groupshare.Meta, args []string) groupshare.Meta {
	named := func(fs ...string) bool {
		for _, a := range args {
			for _, f := range fs {
				if a == f {
					return true
				}
			}
		}
		return false
	}
	if !named("--about", "--description") {
		now.Description = old.Description
	}
	if !named("--by", "--author") {
		now.Author = old.Author
	}
	if !named("--title") {
		now.Title = old.Title
	}
	if !named("--unlisted") {
		now.Public = old.Public
	}
	return now
}

// groupImportCmd: `magpie group import <link|text|-> [--over <id>]`: a
// group shared on usemagpie.ai, or as text, added as a group of the
// user's; --over replaces group <id> with it (the author's newer version).
func groupImportCmd(rest []string) error {
	usage := "magpie group import <usemagpie.ai/g/… link | shared text | -> [--over <id>]"
	var src, into string
	for i := 0; i < len(rest); i++ {
		switch a := rest[i]; a {
		case "--over":
			if i+1 >= len(rest) {
				return errors.New(usage)
			}
			i++
			into = rest[i]
		case "-h", "--help":
			fmt.Println(usage)
			return nil
		default:
			// a flag it doesn't know is never taken as the text
			if strings.HasPrefix(a, "-") && a != "-" {
				return fmt.Errorf("magpie group import has no %q\n%s", a, usage)
			}
			if src != "" {
				src += " "
			}
			src += a
		}
	}
	if src == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return err
		}
		src = string(b)
	}
	if strings.TrimSpace(src) == "" {
		return errors.New(usage)
	}
	ctx := context.Background()
	var page *groupshare.Page
	var s provider.SharedGroup
	if id := groupshare.LinkID(src); id != "" {
		p, err := groupshare.Fetch(ctx, id)
		if err != nil {
			return err
		}
		page, s = &p, p.Group
	} else {
		var err error
		if s, err = provider.ReadShared(src); err != nil {
			return err
		}
	}
	over := false
	if into != "" {
		g, err := findGroup(into)
		if err != nil {
			return err
		}
		s.Group.ID, over = g.ID, true
	}
	res, err := provider.ImportShared(s, nil, over)
	if err != nil {
		return err
	}
	if page != nil {
		if err := groupshare.Imported(res.ID, *page); err != nil {
			return err
		}
	}
	verb := "added"
	if res.Replaced {
		verb = "replaced"
	}
	fmt.Println(green.Render("✓"), verb, bold.Render(res.Name), muted.Render("· agents pick it as group/"+res.ID))
	for _, i := range res.Instead {
		fmt.Println(muted.Render("  " + i))
	}
	if len(res.Missing) > 0 {
		fmt.Println(amber.Render("!"), "left out, not in your magpie:", strings.Join(res.Missing, ", "))
	}
	if res.Rules > 0 {
		fmt.Println(amber.Render("!"), res.Rules, "rules left out with them")
	}
	g, err := findGroup(res.ID)
	if err != nil {
		return err
	}
	return showGroup(g)
}

package cli

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// magpie model spec: a model's context window, reply limit, vision and
// reasoning levels at once, over what the vendor's list and models.dev say
// (Dani Ariyanto on Discord: a custom provider's models had none known).

const specUsage = `usage:
  magpie model spec <provider/model>             its context window, max output, vision and reasoning levels,
                                                 and whether each is yours or the vendor's list's / models.dev's
  magpie model spec <provider/model> [--context <n>] [--output <n>] [--vision on|off] [--reasoning <levels>]
                                                 say any of them: --context 200000 or 1m, --output 128k,
                                                 --reasoning low-high (a range), low,medium,high (a list) or off
                                                 (send it no reasoning field); auto as any value takes yours off
                                                 that one and goes back to the vendor's list and models.dev
  magpie model spec '<provider>/*' --context <n> --output <n>
                                                 a window and max output for every model of that provider that
                                                 has none of its own; vision and reasoning are set per model
  magpie model spec <provider/model> --reset     take every value you set off this model
  magpie model specs                             the models you gave a spec

  What you set wins over the vendor's list and models.dev everywhere magpie reads them: the app's
  badges, the gateway's model list and routing, max_tokens, image handling, and the model lists
  magpie writes into the agents. A model you set nothing on is as it was. A catalog refresh keeps them.
  Reasoning levels narrowed from the model's own only narrow what agents are offered; the vendor is
  still asked at any level it has. Levels given a model with none known are what it is asked at.

  e.g. magpie model spec kenari/glm-5:free --context 128k --output 16k --vision off --reasoning low-high`

func modelSpec(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", specUsage)
	}
	if isHelp(args[0]) {
		fmt.Println(specUsage)
		return nil
	}
	ref, rest := args[0], args[1:]
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("name the model first, as provider/model, not %q\n\n%s", ref, specUsage)
	}
	if isReset(rest) {
		gone, err := provider.ResetModelSpec(ref)
		if err != nil {
			return err
		}
		if len(gone) == 0 {
			return fmt.Errorf("%s has no spec of yours to reset — magpie model spec %s says what it has now", ref, typedRef(ref))
		}
		fmt.Println(green.Render("✓"), ref, muted.Render("has no "+strings.Join(gone, ", ")+" of yours · it goes by the vendor's list and models.dev"))
		return nil
	}
	var c provider.SpecChange
	for i := 0; i < len(rest); i++ {
		flag, val, inline := strings.Cut(rest[i], "=")
		if isHelp(flag) {
			fmt.Println(specUsage)
			return nil
		}
		if !strings.HasPrefix(flag, "--") {
			return fmt.Errorf("%q is not one of --context, --output, --vision, --reasoning or --reset\n\n%s", rest[i], specUsage)
		}
		if !inline {
			if i+1 >= len(rest) || strings.HasPrefix(rest[i+1], "-") {
				if flag == "--reset" || flag == "-r" {
					return fmt.Errorf("--reset takes every value off and goes alone; to take one part off, give it auto (--vision auto)")
				}
				return fmt.Errorf("%s needs a value\n\n%s", flag, specUsage)
			}
			i++
			val = rest[i]
		}
		switch flag {
		case "--context", "--ctx", "--window":
			n, err := specTokens("a context window", val)
			if err != nil {
				return err
			}
			c.Context = &n
		case "--output", "--max-output":
			n, err := specTokens("a max output", val)
			if err != nil {
				return err
			}
			c.Output = &n
		case "--vision", "--images", "--multimodal":
			if _, err := provider.ParseVision(val); err != nil {
				return err
			}
			c.Vision = &val
		case "--reasoning", "--efforts", "--effort", "--levels":
			c.Reasoning = &val
		default:
			return fmt.Errorf("unknown flag %s: it takes --context, --output, --vision, --reasoning or --reset\n\n%s", flag, specUsage)
		}
	}
	if c.Empty() {
		return showSpec(ref)
	}
	changed, err := provider.ChangeModelSpec(ref, c)
	if err != nil {
		return err
	}
	if changed {
		fmt.Println(green.Render("✓"), muted.Render("saved"))
	} else {
		// it goes by these already: a tick over nothing saved read as a
		// set that took (@czhhcz on X)
		fmt.Println(muted.Render("nothing changed"), faint.Render("· it goes by these already"))
	}
	return showSpec(ref)
}

func isHelp(a string) bool { return a == "--help" || a == "-h" || a == "help" }

// specTokens is a typed number of tokens for a spec: 0 for auto. A 0 typed
// as a number is refused, as no window at all is not what anyone means.
func specTokens(what, v string) (int, error) {
	if strings.EqualFold(strings.TrimSpace(v), provider.Auto) {
		return 0, nil
	}
	n, err := parseTokens(v)
	if err != nil {
		return 0, fmt.Errorf("%s is a number of tokens, like 200000, 128k or 1m, or auto, not %q", what, v)
	}
	if n == 0 {
		return 0, fmt.Errorf("%s of 0 tokens is none at all — auto takes yours off and goes back to the vendor's list", what)
	}
	return n, nil
}

// showSpec prints a model's spec as magpie goes by it, and where each part
// came from.
func showSpec(ref string) error {
	p, model, err := modelRef(ref)
	if err != nil {
		return err
	}
	id := p.ID + "/" + model
	if model == "*" {
		fmt.Println(bold.Render(id), muted.Render("· every model of "+p.ID+" that has none of its own"))
		out := provider.OutputsOf(p.ID)["*"]
		fmt.Println("  " + pad("context", 11) + bold.Render(tokenCount(p.Contexts["*"])))
		fmt.Println("  " + pad("max output", 11) + bold.Render(tokenCount(out)))
		return nil
	}
	sp, ok := provider.SpecOf(id)
	if !ok {
		fmt.Println(muted.Render(id), faint.Render("· is not a model this provider serves"))
		return nil
	}
	from := func(part provider.SpecPart, flag string) string {
		switch part.From {
		case "model":
			return "yours · --" + flag + " auto goes back to the vendor's list"
		case "provider":
			return "yours for every model of " + p.ID
		}
		if part.Value == "unknown" {
			return "neither the vendor's list nor models.dev says · --" + flag + " sets it"
		}
		return "the vendor's list / models.dev"
	}
	fmt.Println(bold.Render(id))
	for _, l := range []struct {
		name, flag string
		part       provider.SpecPart
	}{
		{"context", "context", sp.Context},
		{"max output", "output", sp.Output},
		{"vision", "vision", sp.Vision},
		{"reasoning", "reasoning", sp.Reasoning},
	} {
		fmt.Println("  "+pad(l.name, 11)+bold.Render(pad(l.part.Value, 18)), faint.Render(from(l.part, l.flag)))
	}
	return nil
}

// modelSpecs lists the models the user gave a spec.
func modelSpecs() error {
	rows := provider.ModelSpecs()
	if len(rows) == 0 {
		fmt.Println(muted.Render("no model has a spec of yours yet · magpie model spec <provider/model> --context <n> …"))
		return nil
	}
	w := 0
	for _, r := range rows {
		w = max(w, len(r.Key))
	}
	for _, r := range rows {
		var parts []string
		for _, p := range [][2]string{{"context", r.Context}, {"output", r.Output}, {"vision", r.Vision}, {"reasoning", r.Reasoning}} {
			if p[1] != "" {
				parts = append(parts, p[0]+" "+p[1])
			}
		}
		line := "  " + pad(r.Key, w) + "  " + strings.Join(parts, " · ")
		if r.Gone {
			line += faint.Render("  (its provider is gone)")
		}
		fmt.Println(line)
	}
	return nil
}

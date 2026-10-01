package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// A group's rules from the terminal: magpie group rule add|rm|mv …, as the
// Routing view's Rules section makes them.

const ruleUsage = `usage:
  magpie group rule <group>               the group's rules
  magpie group rule add <group> use=<model> [tokens=<n>] [images] [effort=on|low|medium|high|xhigh|max] [agents=a,b…]
                        [intent="<what the message asks for>"] [compact] [time=HH:MM-HH:MM] [days=mon-fri]
                        [classifier=<model>] [at=<n>]
                                          a rule: a turn that matches it goes to <model>, one of the group's
                                          (or group/<id>, a group in it), first; a model in the group at an
                                          effort of its own is named with it (use=glm/glm-5.3-flash:high)
  magpie group rule rm <group> <n>        remove rule n
  magpie group rule mv <group> <n> <to>   move rule n to place <to>
  magpie group rule classifier <group> <model>
                                          the model that tells which intent a message is, or
                                          group/<id>: another group, its models tried in turn

  Rules are looked at top first when you send a message (a new turn); the first that
  matches puts its model first, and the group's others stay behind it if it fails.
  The agent's tool results within the turn stay with the model the turn began on.
  Every condition given must hold:
  tokens   the request is at least this long (200000, 200k, 1m): estimated from its size, or
           what the vendor counted the conversation's last request as, whichever is more
  images   it carries an image, now or earlier in the conversation
  effort   the agent asked for reasoning: on (any), or at least this level
  agents   it comes from one of these agents (claude, codex, opencode, … as magpie usage names them)
  intent   the user's message is of this kind, in your words ("writing or fixing tests", "a quick
           question"): as the turn begins, the group's classifier — any model magpie has, best a small
           fast one without reasoning, or another group of them to fail over — is asked which of the intents that may match the message is,
           once; if it fails or can't say, no intent matches. Its call shows in the usage as magpie's own
  compact  the agent is compacting the conversation (Claude Code's /compact or auto-compact, Codex's,
           OpenCode's, Pi's, Gemini CLI's, Qwen Code's, Kimi's): a cheaper, faster model can write the
           summary. The request is as long as the conversation, so a model known to take less is passed
           over; it is not the turn's model, and the requests after it go on as before
  time     the turn begins within these hours, on this computer's clock (09:00-18:00; 22:00-08:00
           runs past midnight) — a provider's peak-price hours sent to another, say
  days     only on these days (mon-fri, sat,sun); with time, a window past midnight is of the day it
           begins on. Days alone hold all day

  e.g. magpie group rule add opus-anywhere use=openrouter/google/gemini-3-pro tokens=200k
       magpie group rule add opus-anywhere use=a/vision-model images
       magpie group rule add fast use=codex/gpt-5.6-sol:xhigh effort=high
       magpie group rule add opus-anywhere use=deepseek/deepseek-v4-flash compact
       magpie group rule add cheap use=glm/glm-5.3 time=14:00-18:00 days=mon-fri
       magpie group rule add opus-anywhere use=deepseek/deepseek-v4-flash intent="a quick question" classifier=groq/llama-3.1-8b-instant`

// parseTokens reads 200000, 200k, 1.5m.
func parseTokens(v string) (int, error) { return provider.ParseTokens(v) }

// parseRule makes a rule of k=v words (provider.ParseRule), an unknown
// word answered with the usage.
func parseRule(g provider.Group, words []string) (provider.Rule, int, string, error) {
	r, at, classifier, err := provider.ParseRule(g, words)
	if err != nil && strings.HasPrefix(err.Error(), provider.UnknownRuleWord) {
		err = fmt.Errorf("%w\n\n%s", err, ruleUsage)
	}
	return r, at, classifier, err
}

// groupMember is the group's member a typed model names.
func groupMember(g provider.Group, in string) (string, error) { return provider.GroupMember(g, in) }

// ruleCmd: magpie group rule …
func ruleCmd(args []string) error {
	if len(args) == 0 || slices.Contains([]string{"help", "-h", "--help"}, args[0]) {
		fmt.Println(ruleUsage)
		return nil
	}
	verb, rest := args[0], args[1:]
	if len(rest) == 0 {
		// magpie group rule <group>: the group, its rules with it
		if g, err := findGroup(verb); err == nil {
			if len(g.Rules) == 0 {
				fmt.Println(muted.Render("  " + g.ID + " has no rules · magpie group rule add " + g.ID + " use=<model> …"))
				return nil
			}
			return showGroup(g)
		}
		return fmt.Errorf("%s", ruleUsage)
	}
	g, err := findGroup(rest[0])
	if err != nil {
		return err
	}
	if g.Hidden {
		return fmt.Errorf("%s was removed: magpie group restore %s brings it back first", g.ID, g.ID)
	}
	place := func(s string) (int, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > len(g.Rules) {
			if len(g.Rules) == 0 {
				return 0, fmt.Errorf("%s has no rules", g.ID)
			}
			return 0, fmt.Errorf("rule %s: %s has rules 1 to %d", s, g.ID, len(g.Rules))
		}
		return n - 1, nil
	}
	switch verb {
	case "add", "new":
		r, at, classifier, err := parseRule(g, rest[1:])
		if err != nil {
			return err
		}
		if classifier != "" {
			g.Classifier = classifier
		}
		if at == 0 || at > len(g.Rules) {
			g.Rules = append(g.Rules, r)
		} else {
			g.Rules = slices.Insert(g.Rules, at-1, r)
		}
	case "rm", "remove", "delete":
		if len(rest) != 2 {
			return fmt.Errorf("magpie group rule rm <group> <n>")
		}
		i, err := place(rest[1])
		if err != nil {
			return err
		}
		g.Rules = slices.Delete(g.Rules, i, i+1)
	case "mv", "move":
		if len(rest) != 3 {
			return fmt.Errorf("magpie group rule mv <group> <n> <to>")
		}
		i, err := place(rest[1])
		if err != nil {
			return err
		}
		j, err := place(rest[2])
		if err != nil {
			return err
		}
		r := g.Rules[i]
		g.Rules = slices.Insert(slices.Delete(g.Rules, i, i+1), j, r)
	case "classifier", "classify":
		if len(rest) != 2 {
			return fmt.Errorf("magpie group rule classifier <group> <model>")
		}
		if !slices.ContainsFunc(g.Rules, func(r provider.Rule) bool { return r.Intent != "" }) {
			return fmt.Errorf("%s has no rule with an intent to classify for", g.ID)
		}
		g.Classifier = rest[1]
	default:
		return fmt.Errorf("magpie group rule has no %q\n\n%s", verb, ruleUsage)
	}
	if err := provider.SaveGroup(g); err != nil {
		return err
	}
	g, err = findGroup(g.ID)
	if err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), "saved", bold.Render(g.Name))
	return showGroup(g)
}

// ruleLine is a rule as magpie group shows it.
func ruleLine(r provider.Rule) string {
	return strings.Join(r.Conditions(), " · ") + muted.Render(" → ") + r.Use
}

// pruneRules drops the rules for models no longer in the group, saying so.
func pruneRules(g *provider.Group) {
	var keep []provider.Rule
	for i, r := range g.Rules {
		if slices.Contains(g.Members, r.Use) {
			keep = append(keep, r)
		} else {
			fmt.Println(amber.Render("!"), fmt.Sprintf("rule %d is removed with %s", i+1, r.Use))
		}
	}
	g.Rules = keep
}

package provider

// A rule as it is typed: use=<model> tokens=200k images effort=high
// agents=a,b intent="…" — by magpie group rule and the TUI's routing page.

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseTokens reads 200000, 200k, 1.5m.
func ParseTokens(v string) (int, error) {
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(v, "_", "")))
	mul := 1.0
	switch {
	case strings.HasSuffix(s, "k"):
		mul, s = 1e3, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mul, s = 1e6, strings.TrimSuffix(s, "m")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("tokens %q is not a length (200000, 200k, 1m)", v)
	}
	return int(f * mul), nil
}

// UnknownRuleWord starts the error for a word ParseRule doesn't know.
const UnknownRuleWord = "unknown "

// ParseRule makes a rule of k=v words; the model is resolved among the
// group's members. classifier is the group's classifier when one was
// given, at the place asked for (from 1) when one was.
func ParseRule(g Group, words []string) (r Rule, at int, classifier string, err error) {
	for _, w := range words {
		k, v, hasV := strings.Cut(w, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		switch k {
		case "use", "model", "to":
			id, err := GroupMember(g, v)
			if err != nil {
				return r, 0, "", err
			}
			r.Use = id
		case "tokens", "context", "longer":
			n, err := ParseTokens(v)
			if err != nil {
				return r, 0, "", err
			}
			r.Tokens = n
		case "images", "image":
			switch strings.ToLower(v) {
			case "", "yes", "true", "on", "1":
				r.Images = true
			case "no", "false", "off", "0":
				r.Images = false
			default:
				return r, 0, "", fmt.Errorf("images takes no value (or yes/no), not %q", v)
			}
		case "effort", "reasoning", "thinking":
			if !hasV {
				v = "on"
			}
			r.Effort = strings.ToLower(strings.TrimSpace(v))
		case "agents", "agent":
			r.Agents = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
		case "intent", "asks", "about":
			r.Intent = strings.TrimSpace(v)
			if r.Intent == "" {
				return r, 0, "", fmt.Errorf(`intent says what the message asks for: intent="writing or fixing tests"`)
			}
		case "classifier", "classify", "by":
			if classifier = strings.TrimSpace(v); classifier == "" {
				return r, 0, "", fmt.Errorf("classifier=<model>: the model that tells which intent a message is")
			}
		case "at":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return r, 0, "", fmt.Errorf("at is a place from 1, not %q", v)
			}
			at = n
		default:
			return r, 0, "", fmt.Errorf(UnknownRuleWord+"%q (use, tokens, images, effort, agents, intent, classifier, at)", w)
		}
	}
	if r.Use == "" {
		return r, 0, "", fmt.Errorf("use=<model> is missing: one of %s", strings.Join(g.Members, ", "))
	}
	if r.Intent != "" && classifier == "" && g.Classifier == "" {
		return r, 0, "", fmt.Errorf("a rule with an intent needs the group's classifier, the model that tells which intent a message is: add classifier=<model>, best a small fast one")
	}
	return r, at, classifier, nil
}

// GroupMember is the group's member a typed model names: its id, its
// model id without the provider, or the model's last name — each with the
// effort fixed on the member ("m:high") or, when only one member is of
// that model, without it.
func GroupMember(g Group, in string) (string, error) {
	in = strings.TrimPrefix(strings.TrimSpace(in), "magpie/")
	plain := func(m string) string { model, _ := MemberEffort(m); return model }
	for _, match := range []func(string) bool{
		func(m string) bool { return m == in },
		func(m string) bool { return strings.EqualFold(m, in) },
		func(m string) bool { _, bare, _ := strings.Cut(m, "/"); return strings.EqualFold(bare, in) },
		func(m string) bool { return strings.EqualFold(m[strings.LastIndex(m, "/")+1:], in) },
		func(m string) bool { return strings.EqualFold(plain(m), in) },
		func(m string) bool { _, bare, _ := strings.Cut(plain(m), "/"); return strings.EqualFold(bare, in) },
	} {
		var hits []string
		for _, m := range g.Members {
			if match(m) {
				hits = append(hits, m)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			return "", fmt.Errorf("%s is %s: name one", in, strings.Join(hits, " and "))
		}
	}
	return "", fmt.Errorf("%s is not in %s (its models: %s)", in, g.ID, strings.Join(g.Members, ", "))
}

// RuleWords splits a typed line into its words, a "quoted" part in one:
// intent="a quick question".
func RuleWords(line string) []string {
	var out []string
	var w strings.Builder
	quoted, some := false, false
	for _, c := range line {
		switch {
		case c == '"':
			quoted, some = !quoted, true
		case !quoted && (c == ' ' || c == '\t'):
			if some {
				out = append(out, w.String())
			}
			w.Reset()
			some = false
		default:
			w.WriteRune(c)
			some = true
		}
	}
	if some {
		out = append(out, w.String())
	}
	return out
}

// Line is the rule as ParseRule reads it back.
func (r Rule) Line() string {
	out := []string{"use=" + r.Use}
	if r.Tokens > 0 {
		out = append(out, fmt.Sprintf("tokens=%d", r.Tokens))
	}
	if r.Images {
		out = append(out, "images")
	}
	if r.Effort != "" {
		out = append(out, "effort="+r.Effort)
	}
	if len(r.Agents) > 0 {
		out = append(out, "agents="+strings.Join(r.Agents, ","))
	}
	if r.Intent != "" {
		out = append(out, `intent="`+strings.ReplaceAll(r.Intent, `"`, "")+`"`)
	}
	return strings.Join(out, " ")
}

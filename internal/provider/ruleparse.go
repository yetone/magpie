package provider

// A rule as it is typed: use=<model>[,<model>…] use-effort=low tokens=200k images effort=high
// agents=a,b intent="…" compact time=09:00-18:00 days=mon-fri tz=Asia/Shanghai
// folders=~/work,"~/My Project" except-folders=~/work/old, or
// when="effort is not off" when="message contains run the tests" (rulecond.go),
// pause=<model> time=… days=… agents=… folders=… except-folders=… — by
// magpie group rule and the
// TUI's routing page.

import (
	"fmt"
	"math"
	"slices"
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
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("tokens %q is not a length (200000, 200k, 1m)", v)
	}
	n := f * mul
	if n >= float64(math.MaxInt) {
		return 0, fmt.Errorf("tokens %q is not a length (200000, 200k, 1m)", v)
	}
	return int(n), nil
}

// ParseDays reads days of the week: "mon-fri", "sat,sun", "mon,wed-fri";
// a range may run past Sunday (fri-mon).
func ParseDays(v string) ([]string, error) {
	var out []string
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		a, b, isRange := strings.Cut(strings.ReplaceAll(part, "–", "-"), "-")
		from, ok := ParseDay(a)
		if !ok {
			return nil, fmt.Errorf("days: %q is not a day of the week (mon … sun)", a)
		}
		to := from
		if isRange {
			if to, ok = ParseDay(b); !ok {
				return nil, fmt.Errorf("days: %q is not a day of the week (mon … sun)", b)
			}
		}
		for i := slices.Index(Weekdays, from); ; i = (i + 1) % 7 {
			if !slices.Contains(out, Weekdays[i]) {
				out = append(out, Weekdays[i])
			}
			if Weekdays[i] == to {
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("days names the days of the week it holds on: days=mon-fri")
	}
	return out, nil
}

// ParseTimeWindow reads hours of the day: 09:00-18:00, 22:00-08:00, and
// the time zone they are in after an @: 09:00-18:00@Asia/Shanghai.
func ParseTimeWindow(v string) (*TimeWindow, error) {
	hours, zone, _ := strings.Cut(v, "@")
	from, to, ok := strings.Cut(strings.NewReplacer("–", "-", "~", "-", "～", "-").Replace(strings.TrimSpace(hours)), "-")
	if !ok {
		return nil, fmt.Errorf("time is hours of the day: time=09:00-18:00 (or 22:00-08:00, past midnight; 09:00-18:00@Asia/Shanghai in a time zone), not %q", v)
	}
	w := &TimeWindow{From: strings.TrimSpace(from), To: strings.TrimSpace(to), Zone: strings.TrimSpace(zone)}
	if err := w.clean(); err != nil {
		return nil, err
	}
	return w, nil
}

// InZoneHere puts a rule's window typed with no time zone in this
// computer's (LocalZone), as magpie group rule and the TUI read it: the
// hours are the ones the person typing them means. It stays without one
// when this computer's zone has no IANA name.
func (r *Rule) InZoneHere() {
	if r.Time != nil && r.Time.Zone == "" {
		r.Time.Zone = LocalZone()
	}
}

// zoneErr is what a time zone magpie doesn't know is answered with.
func zoneErr(v string) error {
	return fmt.Errorf("tz: %q is not a time zone magpie knows: an IANA name such as Asia/Shanghai, Europe/Berlin or America/New_York", v)
}

// UnknownRuleWord starts the error for a word ParseRule doesn't know.
const UnknownRuleWord = "unknown "

// ParseRule makes a rule of k=v words; the model is resolved among the
// group's members. classifier is the group's classifier when one was
// given, at the place asked for (from 1) when one was.
func ParseRule(g Group, words []string) (r Rule, at int, classifier string, err error) {
	zone, zoned := "", false // tz=, or time=…@zone
	for _, w := range words {
		k, v, hasV := strings.Cut(w, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		switch k {
		case "use", "model", "to":
			// use=a,b: a first, then b when a can't take the request
			var uses []string
			for _, m := range strings.Split(v, ",") {
				if m = strings.TrimSpace(m); m == "" {
					continue
				}
				id, err := GroupMember(g, m)
				if err != nil {
					return r, 0, "", err
				}
				uses = append(uses, id)
			}
			if len(uses) == 0 {
				return r, 0, "", fmt.Errorf("use=<model> names the model it sends to, or several in order: use=a,b")
			}
			r.SetUses(uses)
		case "pause", "skip":
			id, err := GroupMember(g, v)
			if err != nil {
				return r, 0, "", err
			}
			r.Use, r.More, r.Pause = id, nil, true
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
		case "compact", "compacting", "compaction":
			switch strings.ToLower(v) {
			case "", "yes", "true", "on", "1":
				r.Compact = true
			case "no", "false", "off", "0":
				r.Compact = false
			default:
				return r, 0, "", fmt.Errorf("compact takes no value (or yes/no), not %q", v)
			}
		case "effort", "reasoning", "thinking":
			if !hasV {
				v = "on"
			}
			r.Effort = strings.ToLower(strings.TrimSpace(v))
		case "use-effort", "useeffort", "set-effort", "seteffort":
			r.UseEffort = strings.ToLower(strings.TrimSpace(v))
			if !slices.Contains(Efforts, r.UseEffort) {
				return r, 0, "", fmt.Errorf("use-effort is the reasoning the rule's model is asked for: one of %s, not %q", strings.Join(Efforts, ", "), v)
			}
		case "agents", "agent":
			r.Agents = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
		case "intent", "asks", "about":
			r.Intent = strings.TrimSpace(v)
			if r.Intent == "" {
				return r, 0, "", fmt.Errorf(`intent says what the message asks for: intent="writing or fixing tests"`)
			}
		case "time", "hours", "between":
			w, err := ParseTimeWindow(v)
			if err != nil {
				return r, 0, "", err
			}
			if w.Zone != "" {
				zone, zoned = w.Zone, true
			}
			if r.Time != nil {
				w.Days = r.Time.Days
			}
			r.Time = w
		case "tz", "zone", "timezone", "time-zone":
			zone, zoned = strings.TrimSpace(v), true
			if zone == "" || !ValidZone(zone) {
				return r, 0, "", zoneErr(v)
			}
		case "days", "day":
			days, err := ParseDays(v)
			if err != nil {
				return r, 0, "", err
			}
			if r.Time == nil {
				r.Time = &TimeWindow{From: "00:00", To: "00:00"} // the whole day, unless time= says
			}
			r.Time.Days = days
		case "folders", "folder", "in", "project", "projects":
			fs, err := parseFolders(v)
			if err != nil {
				return r, 0, "", err
			}
			if len(fs) == 0 {
				return r, 0, "", fmt.Errorf("folders names the working folders it holds in: folders=~/work/acme,~/oss")
			}
			r.Folders = append(r.Folders, fs...)
		case "except-folders", "except-folder", "exceptfolders", "not-in", "outside":
			fs, err := parseFolders(v)
			if err != nil {
				return r, 0, "", err
			}
			if len(fs) == 0 {
				return r, 0, "", fmt.Errorf("except-folders names the working folders it doesn't hold in: except-folders=~/big-task")
			}
			r.ExceptFolders = append(r.ExceptFolders, fs...)
		case "when", "where", "if":
			c, err := ParseCond(v)
			if err != nil {
				return r, 0, "", err
			}
			r.Where = append(r.Where, c)
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
			return r, 0, "", fmt.Errorf(UnknownRuleWord+"%q (use, use-effort, pause, tokens, images, effort, agents, intent, compact, time, days, tz, folders, except-folders, when, classifier, at)", w)
		}
	}
	if zoned {
		if r.Time == nil {
			return r, 0, "", fmt.Errorf("tz= is the time zone of time= or days=: give one of them too")
		}
		r.Time.Zone = zone
	}
	if r.Use == "" {
		return r, 0, "", fmt.Errorf("use=<model> (or pause=<model>) is missing: one of %s", strings.Join(g.Members, ", "))
	}
	if r.Pause && len(r.More) > 0 {
		return r, 0, "", fmt.Errorf("pause=<model> names one model: give each its own rule")
	}
	if r.Pause && r.UseEffort != "" {
		return r, 0, "", fmt.Errorf("pause=<model> sends nothing, so it takes no use-effort=")
	}
	if r.Pause && (r.Tokens > 0 || r.Images || r.Effort != "" || r.Intent != "" || r.Compact ||
		slices.ContainsFunc(r.Where, func(c Cond) bool { return c.Field != "agent" })) {
		return r, 0, "", fmt.Errorf("pause=<model> holds by time=, days=, agents=, folders=, except-folders= or when=\"agent …\" only")
	}
	if r.Pause && r.Time == nil && len(r.Agents) == 0 && len(r.Where) == 0 && !r.ByFolder() {
		return r, 0, "", fmt.Errorf("pause=<model> needs time=, days=, agents=, folders= or except-folders=: when the model is left out")
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
	out := []string{"use=" + strings.Join(r.Uses(), ",")}
	if r.Pause {
		out[0] = "pause=" + r.Use
	}
	if r.UseEffort != "" {
		out = append(out, "use-effort="+r.UseEffort)
	}
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
	if r.Compact {
		out = append(out, "compact")
	}
	if r.Time != nil {
		if r.Time.From != r.Time.To || len(r.Time.Days) == 0 {
			out = append(out, "time="+r.Time.From+"-"+r.Time.To)
		}
		if len(r.Time.Days) > 0 {
			out = append(out, "days="+strings.ReplaceAll(strings.ToLower(r.Time.DaysText()), "–", "-"))
		}
		if r.Time.Zone != "" {
			out = append(out, "tz="+r.Time.Zone)
		}
	}
	folders := func(k string, fs []string) {
		if len(fs) == 0 {
			return
		}
		v := strings.ReplaceAll(strings.Join(fs, ","), `"`, "")
		if strings.ContainsAny(v, " \t") {
			v = `"` + v + `"`
		}
		out = append(out, k+"="+v)
	}
	folders("folders", r.Folders)
	folders("except-folders", r.ExceptFolders)
	for _, c := range r.Where {
		v := strings.ReplaceAll(c.Words(), `"`, "")
		if strings.ContainsAny(v, " \t") {
			v = `"` + v + `"`
		}
		out = append(out, "when="+v)
	}
	return strings.Join(out, " ")
}

// parseFolders reads a typed list of folders, comma separated.
func parseFolders(v string) ([]string, error) {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		c, err := cleanFolder(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

package provider

// A rule's conditions as a field, a way to compare and a value
// (aipha2025 on X): "effort is not off", "tokens < 20000", "agent is not
// codex", "subagent", "message contains run the tests". They sit beside
// the rule's own fields (Tokens, Effort, Agents…), which keep meaning
// what they always did, so a rule saved before reads and routes the same.
// Every condition must hold, as the fields' must.

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Cond is one condition of a rule's Where.
type Cond struct {
	// Field is what the condition looks at: one of CondFields.
	Field string `json:"field"`
	// Op is how: one of the field's CondOps.
	Op string `json:"op"`
	// Value is what it is compared with: a length in tokens; off, on or a
	// reasoning level; agents, comma separated; the text to find in the
	// message. A subagent condition has none.
	Value string `json:"value,omitempty"`
}

// CondFields are the fields a condition can look at. system and last
// are the request's texts as the agent sent them (SucroseMoE on X: a
// harness's title or compaction call told apart by its prompt, with no
// model asked): system, each of its system prompt's blocks; last, each
// text of its last user message.
var CondFields = []string{"tokens", "effort", "agent", "subagent", "message", "system", "last"}

// CondOps are each field's ways to compare.
var CondOps = map[string][]string{
	"tokens":   {"gte", "gt", "lt", "lte"},
	"effort":   {"is", "not", "gte", "lte"},
	"agent":    {"is", "not"},
	"subagent": {"is", "not"},
	"message":  {"contains", "excludes", "starts", "matches"},
	"system":   {"contains", "excludes", "starts", "matches"},
	"last":     {"contains", "excludes", "starts", "matches"},
}

// textField reports whether a condition's field is a text it looks for
// words in.
func textField(f string) bool { return f == "message" || f == "system" || f == "last" }

// MaxCondText is how long a message condition's text may be, in
// characters.
const MaxCondText = 500

// condRes are the message patterns read so far, each compiled once: a
// rule is looked at on every turn.
var condRes sync.Map // pattern → *regexp.Regexp

func condRe(p string) (*regexp.Regexp, error) {
	if re, ok := condRes.Load(p); ok {
		return re.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile("(?i)" + p)
	if err != nil {
		return nil, err
	}
	condRes.Store(p, re)
	return re, nil
}

// clean checks a condition and puts it the one way.
func (c *Cond) clean() error {
	c.Field = strings.ToLower(strings.TrimSpace(c.Field))
	c.Op = strings.ToLower(strings.TrimSpace(c.Op))
	ops, ok := CondOps[c.Field]
	if !ok {
		return fmt.Errorf("a condition looks at one of %s, not %q", strings.Join(CondFields, ", "), c.Field)
	}
	if !slices.Contains(ops, c.Op) {
		return fmt.Errorf("a %s condition is %s, not %q", c.Field, strings.Join(ops, ", "), c.Op)
	}
	switch c.Field {
	case "tokens":
		n, err := ParseTokens(c.Value)
		if err != nil {
			return err
		}
		c.Value = strconv.Itoa(n)
	case "effort":
		c.Value = strings.ToLower(strings.TrimSpace(c.Value))
		level := slices.Contains(Efforts, c.Value)
		if c.Op == "gte" || c.Op == "lte" {
			if !level {
				return fmt.Errorf("effort at least or at most is one of %s, not %q", strings.Join(Efforts, ", "), c.Value)
			}
		} else if !level && c.Value != "on" && c.Value != "off" {
			return fmt.Errorf("effort is off, on or one of %s, not %q", strings.Join(Efforts, ", "), c.Value)
		}
	case "agent":
		var agents []string
		for _, a := range strings.FieldsFunc(c.Value, func(r rune) bool { return r == ',' || r == '|' || r == ' ' }) {
			if a = strings.ToLower(a); !slices.Contains(agents, a) {
				agents = append(agents, a)
			}
		}
		if len(agents) == 0 {
			return fmt.Errorf("an agent condition names the agents: agent is not codex")
		}
		c.Value = strings.Join(agents, ",")
	case "subagent":
		c.Value = ""
	case "message", "system", "last":
		if c.Value = strings.TrimSpace(c.Value); c.Value == "" {
			return fmt.Errorf("a %s condition needs the text it looks for", c.Field)
		}
		if len([]rune(c.Value)) > MaxCondText {
			return fmt.Errorf("a %s condition's text is at most %d characters", c.Field, MaxCondText)
		}
		if c.Op == "matches" {
			// written as /…/, as in JavaScript
			if v := c.Value; len(v) > 2 && v[0] == '/' && v[len(v)-1] == '/' {
				c.Value = v[1 : len(v)-1]
			}
			if _, err := condRe(c.Value); err != nil {
				return fmt.Errorf("%s matches %q: not a regular expression: %v", c.Field, c.Value, err)
			}
		}
	}
	return nil
}

// condEffort reads the request's reasoning: off, or the level it asked
// for ("" when it asked for reasoning at no stated level).
func condEffort(q RuleRequest) (off bool, level string) {
	if q.Off || !q.Thinking && q.Effort == "" {
		return true, ""
	}
	return false, q.Effort
}

// Holds reports whether the request is as the condition says.
func (c Cond) Holds(q RuleRequest) bool {
	switch c.Field {
	case "tokens":
		n, err := strconv.Atoi(c.Value)
		if err != nil {
			return false
		}
		switch c.Op {
		case "gte":
			return q.Tokens >= n
		case "gt":
			return q.Tokens > n
		case "lt":
			return q.Tokens < n
		case "lte":
			return q.Tokens <= n
		}
	case "effort":
		off, level := condEffort(q)
		is := func() bool {
			switch c.Value {
			case "off":
				return off
			case "on":
				return !off
			}
			return !off && level == c.Value
		}
		want, have := slices.Index(Efforts, c.Value), slices.Index(Efforts, level)
		switch c.Op {
		case "is":
			return is()
		case "not":
			return !is()
		case "gte":
			return !off && want >= 0 && have >= want
		case "lte":
			// reasoning off is below every level; reasoning at a level not
			// stated is not known to be at most one
			return want >= 0 && (off || have >= 0 && have <= want)
		}
	case "agent":
		in := slices.Contains(strings.Split(c.Value, ","), strings.ToLower(q.Agent))
		switch c.Op {
		case "is":
			return in
		case "not":
			return !in
		}
	case "subagent":
		switch c.Op {
		case "is":
			return q.Subagent
		case "not":
			return !q.Subagent
		}
	case "message":
		return c.holdsIn([]string{q.Message})
	case "system":
		return c.holdsIn(q.System)
	case "last":
		return c.holdsIn(q.Last)
	}
	return false // a condition this magpie doesn't know never holds
}

// holdsIn is a text condition over texts: it holds when one of them has
// (starts with, matches) the words, and for excludes when none has them.
func (c Cond) holdsIn(texts []string) bool {
	v := strings.ToLower(c.Value)
	var re *regexp.Regexp
	if c.Op == "matches" {
		var err error
		if re, err = condRe(c.Value); err != nil {
			return false
		}
	}
	for _, text := range texts {
		switch c.Op {
		case "contains", "excludes":
			if strings.Contains(strings.ToLower(text), v) {
				return c.Op == "contains"
			}
		case "starts":
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), strings.TrimSpace(v)) {
				return true
			}
		case "matches":
			if re.MatchString(text) {
				return true
			}
		}
	}
	return c.Op == "excludes"
}

// textOps are a text condition's ops as Text writes them.
var textOps = map[string]string{"contains": "contains", "excludes": "doesn't contain", "starts": "starts with", "matches": "matches"}

// Text is the condition as a rule's conditions show it.
func (c Cond) Text() string {
	switch c.Field {
	case "tokens":
		return "tokens " + map[string]string{"gte": "≥", "gt": ">", "lt": "<", "lte": "≤"}[c.Op] + " " + c.Value
	case "effort":
		return "effort " + map[string]string{"is": "is", "not": "is not", "gte": "≥", "lte": "≤"}[c.Op] + " " + c.Value
	case "agent":
		return "agent " + map[string]string{"is": "is", "not": "is not"}[c.Op] + " " + strings.ReplaceAll(c.Value, ",", "|")
	case "subagent":
		if c.Op == "not" {
			return "not subagent"
		}
		return "subagent"
	case "message", "system", "last":
		return c.Field + " " + textOps[c.Op] + " " + strconv.Quote(c.Value)
	}
	return strings.TrimSpace(c.Field + " " + c.Op + " " + c.Value)
}

// Words is the condition as ParseCond reads it back: "effort is not
// off", "tokens < 20000", "not subagent".
func (c Cond) Words() string {
	if c.Field == "subagent" {
		if c.Op == "not" {
			return "not subagent"
		}
		return "subagent"
	}
	op := map[string]string{"not": "is not", "gte": ">=", "gt": ">", "lt": "<", "lte": "<=", "excludes": "does not contain", "starts": "starts with"}[c.Op]
	if op == "" {
		op = c.Op
	}
	return strings.TrimSpace(c.Field + " " + op + " " + c.Value)
}

// condOpWords are the ways a typed condition may say its op, longest
// first so "is not" is read before "is".
var condOpWords = []struct{ word, op string }{
	{"does not contain", "excludes"}, {"doesn't contain", "excludes"}, {"not contains", "excludes"},
	{"starts with", "starts"}, {"at least", "gte"}, {"at most", "lte"}, {"is not", "not"},
	{"contains", "contains"}, {"contain", "contains"}, {"excludes", "excludes"}, {"starts", "starts"},
	{"matches", "matches"}, {"match", "matches"}, {"~", "matches"},
	{"isnt", "not"}, {"not", "not"}, {"!=", "not"}, {"≠", "not"},
	{"is", "is"}, {"==", "is"}, {"=", "is"},
	{">=", "gte"}, {"≥", "gte"}, {"gte", "gte"}, {"<=", "lte"}, {"≤", "lte"}, {"lte", "lte"},
	{">", "gt"}, {"gt", "gt"}, {"<", "lt"}, {"lt", "lt"},
}

// ParseCond reads a typed condition: "effort is not off", "tokens <
// 20k", "agent is not codex,opencode", "subagent", "not subagent",
// "message contains run the tests".
func ParseCond(s string) (Cond, error) {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	switch low {
	case "subagent", "subagent is", "subagent is yes", "is subagent":
		return Cond{Field: "subagent", Op: "is"}, nil
	case "not subagent", "subagent not", "subagent is not", "no subagent":
		return Cond{Field: "subagent", Op: "not"}, nil
	}
	field, rest, _ := strings.Cut(s, " ")
	field = strings.ToLower(field)
	switch field {
	case "agents":
		field = "agent"
	case "reasoning", "thinking":
		field = "effort"
	case "text", "content", "prompt":
		field = "message"
	case "system[0]", "sys", "instructions", "system-prompt":
		field = "system"
	case "user[-1]", "last-user", "lastuser", "last-message":
		field = "last"
	case "length", "context":
		field = "tokens"
	}
	if _, ok := CondOps[field]; !ok {
		return Cond{}, fmt.Errorf("when=%q: a condition starts with one of %s", s, strings.Join(CondFields, ", "))
	}
	rest = strings.TrimSpace(rest)
	lowRest := strings.ToLower(rest)
	for _, w := range condOpWords {
		if !strings.HasPrefix(lowRest, w.word) {
			continue
		}
		v := rest[len(w.word):]
		// a word op ends at a space; a sign may sit against its value
		if len(v) > 0 && v[0] != ' ' && w.word[len(w.word)-1] >= 'a' && w.word[len(w.word)-1] <= 'z' {
			continue
		}
		c := Cond{Field: field, Op: w.op, Value: strings.TrimSpace(v)}
		if field == "tokens" && c.Op == "is" {
			continue
		}
		if err := c.clean(); err != nil {
			return Cond{}, fmt.Errorf("when=%q: %w", s, err)
		}
		return c, nil
	}
	return Cond{}, fmt.Errorf("when=%q: say how it compares: %s", s, strings.Join(CondOps[field], ", "))
}

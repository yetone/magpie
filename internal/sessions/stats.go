package sessions

import (
	"cmp"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// Stats is what every session spent, day by day: all of them, not only the
// latest List reads.
type Stats struct {
	From string `json:"from"` // the first date, local: the range's, or for all time the first with anything
	To   string `json:"to"`   // today
	Days []Day  `json:"days"` // the dates with anything, in order
	// Sessions are the sessions at work in the range, each with what it
	// spent in it, the most recently active first. Too many to send the
	// page over a long range: Overview sums them up.
	Sessions []Summary `json:"-"`
}

// Summary is one session's share of a range.
type Summary struct {
	Key             string    `json:"key"` // agent:id as its files are grouped, for Get
	Agent           string    `json:"agent"`
	ID              string    `json:"id"`
	Cwd             string    `json:"cwd"`
	Title           string    `json:"title"`
	UsageIncomplete bool      `json:"usage_incomplete,omitempty"`
	Last            time.Time `json:"last"`
	Tokens
	Cost   float64  `json:"cost"`
	Priced bool     `json:"priced"` // every model it used has a price
	Active int64    `json:"active"` // seconds
	Models []string `json:"models"` // the models it used in the range, the most used first
	Days   []int    `json:"days"`   // the dates it was at work on, as days after From
	// Prompts and Replies are the messages each way, ToolCalls the tools
	// it called, in the range (Claude Code's, Codex's, Pi's and omp's sessions)
	Prompts   int `json:"prompts"`
	Replies   int `json:"replies"`
	ToolCalls int `json:"tool_calls"`

	tools     map[string]int      // calls by tool
	skills    map[string]int      // calls by skill
	skillLast map[string]string   // the last date each skill was called
	perDay    map[int]*summaryDay // by day after From
}

// summaryDay is a session's day, for the overview's calendar and trends.
type summaryDay struct {
	messages, output int
	tools            map[string]int // calls by tool category
}

// Day is one local date's usage, cut fine enough for the page to filter
// it by agent, model and folder itself.
type Day struct {
	Date   string   `json:"date"`
	Usage  []Usage  `json:"usage"`  // by agent, folder and model
	Active []Active `json:"active"` // by agent and folder
}

// Usage is what one model spent for one agent in one folder on a day.
type Usage struct {
	Agent string `json:"agent"`
	Cwd   string `json:"cwd"`
	Model string `json:"model"`
	Tokens
	Cost   float64 `json:"cost"` // USD at the effective price, when priced
	Priced bool    `json:"priced"`
}

// Active is the time an agent's sessions in one folder were at work on a
// day, in seconds (see day.Active for how it is told).
type Active struct {
	Agent   string `json:"agent"`
	Cwd     string `json:"cwd"`
	Seconds int64  `json:"seconds"`
	// Hours is Seconds again by the local hour of the day (24, or none)
	Hours []int64 `json:"hours,omitempty"`
}

// StatsFor reads the usage of the last days days up to today, today
// included (every day when 0), from every session file there is. Only the
// files written to since the range began are looked at; what is read is
// kept, as List's is.
func StatsFor(days int) Stats {
	return statsAt(days, time.Now())
}

// StatsAt is StatsFor as if it were now.
func StatsAt(days int, now time.Time) Stats { return statsAt(days, now) }

func statsAt(days int, now time.Time) Stats {
	now = now.In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	var since time.Time
	from := ""
	if days > 0 {
		since = today.AddDate(0, 0, 1-days)
		from = since.Format(time.DateOnly)
	}
	out := Stats{From: from, To: today.Format(time.DateOnly), Days: []Day{}, Sessions: []Summary{}}

	dbReadMu.Lock()
	defer dbReadMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	loadCache()
	defer closeDBs()
	files := allFiles()
	// every changed file, whatever the range, so a wider range picked later
	// finds them read rather than starting another run
	refresh(files, files)
	var want []file
	for _, f := range files {
		if !f.mod.Before(since) {
			want = append(want, f)
		}
	}

	// a subagent's file counts in the folder of its session's own file
	folder := map[string]string{}
	for _, f := range files {
		if st := cache[f.path]; f.main && st != nil && st.Cwd != "" && folder[f.key] == "" {
			folder[f.key] = st.Cwd
		}
	}
	type uk struct{ date, agent, cwd, model string }
	type ak struct{ date, agent, cwd string }
	type act0 struct {
		ms    int64
		hours []int64
	}
	use := map[uk]Tokens{}
	act := map[ak]*act0{}
	dates := map[string]bool{}
	// each session's share: its files' models and dates in range
	type share struct {
		models           map[string]Tokens
		active           int64
		dates            map[string]bool
		prompts, replies int
		tools, skills    map[string]int
		skillLast        map[string]string
		perDate          map[string]*summaryDay
	}
	shares := map[string]*share{}
	for _, f := range want {
		st := cache[f.path]
		if st == nil {
			continue
		}
		cwd := folder[f.key]
		if cwd == "" {
			cwd = st.Cwd
		}
		for date, d := range st.Days {
			if date == "" || date < from || date > out.To {
				continue
			}
			for model, t := range d.Models {
				if t.zero() {
					continue
				}
				k := uk{date, f.agent, cwd, model}
				u := use[k]
				u.add(t)
				use[k] = u
				dates[date] = true
			}
			if d.Active > 0 {
				k := ak{date, f.agent, cwd}
				a := act[k]
				if a == nil {
					a = &act0{}
					act[k] = a
				}
				a.ms += d.Active
				if len(d.Hours) == 24 {
					if a.hours == nil {
						a.hours = make([]int64, 24)
					}
					for h, ms := range d.Hours {
						a.hours[h] += ms
					}
				}
				dates[date] = true
			}
			sh := shares[f.key]
			if sh == nil {
				sh = &share{models: map[string]Tokens{}, dates: map[string]bool{}, tools: map[string]int{},
					skills: map[string]int{}, skillLast: map[string]string{}, perDate: map[string]*summaryDay{}}
				shares[f.key] = sh
			}
			pd := sh.perDate[date]
			if pd == nil {
				pd = &summaryDay{tools: map[string]int{}}
				sh.perDate[date] = pd
			}
			for model, t := range d.Models {
				// Reasonix 2.x records model presence without session token counts.
				if !t.zero() || f.agent == "reasonix" {
					u := sh.models[model]
					u.add(t)
					sh.models[model] = u
					sh.dates[date] = true
					pd.output += t.Output
				}
			}
			sh.prompts += d.Prompts
			sh.replies += d.Replies
			pd.messages += d.Prompts + d.Replies
			// A Reasonix history can retain authored turns without a token
			// receipt or per-message model. Presence is still known.
			if f.agent == "reasonix" && d.Prompts+d.Replies > 0 {
				sh.dates[date] = true
			}
			for name, n := range d.Tools {
				sh.tools[name] += n
				pd.tools[ToolCategory(name)] += n
			}
			for name, n := range d.Skills {
				sh.skills[name] += n
				if date > sh.skillLast[name] {
					sh.skillLast[name] = date
				}
			}
			if d.Active > 0 {
				sh.active += d.Active
				sh.dates[date] = true
			}
		}
	}

	byDate := map[string]*Day{}
	for date := range dates {
		byDate[date] = &Day{Date: date, Usage: []Usage{}, Active: []Active{}}
	}
	price := pricer()
	for k, t := range use {
		u := Usage{Agent: k.agent, Cwd: k.cwd, Model: k.model, Tokens: t}
		if p := price(k.model); p != nil {
			u.Cost, u.Priced = p.At(0).CostSplit(t.Input, t.Output, t.CacheRead, t.CacheWrite, t.CacheWrite1h), true
		}
		byDate[k.date].Usage = append(byDate[k.date].Usage, u)
	}
	for k, a := range act {
		v := Active{Agent: k.agent, Cwd: k.cwd, Seconds: a.ms / 1000}
		if a.hours != nil {
			v.Hours = make([]int64, 24)
			for h, ms := range a.hours {
				v.Hours[h] = ms / 1000
			}
		}
		byDate[k.date].Active = append(byDate[k.date].Active, v)
	}
	for _, d := range byDate {
		sort.Slice(d.Usage, func(i, j int) bool {
			a, b := d.Usage[i], d.Usage[j]
			if a.Agent != b.Agent {
				return a.Agent < b.Agent
			}
			if a.Cwd != b.Cwd {
				return a.Cwd < b.Cwd
			}
			return a.Model < b.Model
		})
		sort.Slice(d.Active, func(i, j int) bool {
			a, b := d.Active[i], d.Active[j]
			if a.Agent != b.Agent {
				return a.Agent < b.Agent
			}
			return a.Cwd < b.Cwd
		})
		out.Days = append(out.Days, *d)
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Date < out.Days[j].Date })
	if out.From == "" {
		out.From = out.To
		if len(out.Days) > 0 && out.Days[0].Date < out.To {
			out.From = out.Days[0].Date
		}
	}

	groups := map[string][]file{}
	for _, f := range files {
		if shares[f.key] != nil {
			groups[f.key] = append(groups[f.key], f)
		}
	}
	first, _ := time.ParseInLocation(time.DateOnly, out.From, time.Local)
	for key, sh := range shares {
		if len(sh.dates) == 0 {
			continue
		}
		s, ok := assemble(groups[key], price)
		if !ok {
			continue
		}
		sum := Summary{Key: key, Agent: s.Agent, ID: s.ID, Cwd: cmp.Or(folder[key], s.Cwd), Title: clip(s.Title, 160),
			Last: s.Last, Priced: true, Active: sh.active / 1000, Models: []string{}, Days: []int{}, UsageIncomplete: s.UsageIncomplete,
			Prompts: sh.prompts, Replies: sh.replies, tools: sh.tools, skills: sh.skills, skillLast: sh.skillLast,
			perDay: map[int]*summaryDay{}}
		for _, n := range sh.tools {
			sum.ToolCalls += n
		}
		for date, pd := range sh.perDate {
			if t, err := time.ParseInLocation(time.DateOnly, date, time.Local); err == nil {
				sum.perDay[int(math.Round(t.Sub(first).Hours()/24))] = pd
			}
		}
		for model, t := range sh.models {
			sum.Tokens.add(t)
			sum.Models = append(sum.Models, model)
			if p := price(model); p != nil {
				sum.Cost += p.At(0).CostSplit(t.Input, t.Output, t.CacheRead, t.CacheWrite, t.CacheWrite1h)
			} else {
				sum.Priced = false
			}
		}
		sort.Slice(sum.Models, func(i, j int) bool {
			a, b := sh.models[sum.Models[i]], sh.models[sum.Models[j]]
			if a.Input+a.Output != b.Input+b.Output {
				return a.Input+a.Output > b.Input+b.Output
			}
			return sum.Models[i] < sum.Models[j]
		})
		for date := range sh.dates {
			if t, err := time.ParseInLocation(time.DateOnly, date, time.Local); err == nil {
				// whole days, across a change of the clocks too
				sum.Days = append(sum.Days, int(math.Round(t.Sub(first).Hours()/24)))
			}
		}
		sort.Ints(sum.Days)
		out.Sessions = append(out.Sessions, sum)
	}
	sort.Slice(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		if !a.Last.Equal(b.Last) {
			return a.Last.After(b.Last)
		}
		return a.Key < b.Key
	})
	return out
}

// clip cuts s to at most n characters, with an ellipsis.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// Overview is what the sessions of a range come to under the page's
// filters.
type Overview struct {
	Count  int   `json:"count"`
	Median int   `json:"median"` // tokens (in and out) of the middle session
	P90    int   `json:"p90"`    // … of the one nine in ten spent no more than
	Days   []int `json:"days"`   // sessions at work each day, from Stats.From to To
	// Top are the sessions that spent the most, by tokens, cost and
	// active time, n of each
	Top map[string][]Summary `json:"top"`
	// Messages and Output are the messages and output tokens each day,
	// as Days
	Messages []int `json:"messages"`
	Output   []int `json:"output"`
	// Shape is how many sessions came to how much: by messages, by
	// minutes at work and by tool calls a prompt (autonomy)
	Shape  map[string]Shape `json:"shape"`
	Tools  ToolUse          `json:"tools"`
	Skills SkillUse         `json:"skills"`
}

// Shape counts sessions into buckets: Counts[i] those from Edges[i] up to
// the next edge, the last one open.
type Shape struct {
	Edges  []int `json:"edges"`
	Counts []int `json:"counts"`
	Total  int   `json:"total"`
}

// shapeEdges are the buckets' lower edges for each way of telling.
var shapeEdges = map[string][]int{
	"messages": {1, 6, 16, 31, 61, 121},
	"minutes":  {1, 6, 16, 31, 61, 121},
	"autonomy": {0, 1, 3, 6, 11, 21},
}

// ToolUse is what the sessions called their tools for.
type ToolUse struct {
	Calls      int        `json:"calls"`
	Sessions   int        `json:"sessions"`
	Top        []ToolStat `json:"top"`        // the most called
	Categories []ToolStat `json:"categories"` // by category, the most called first
	// Weeks are the calls week by week, from Monday, by category
	Weeks []ToolWeek `json:"weeks"`
}

// ToolStat is one tool's (or category's) calls.
type ToolStat struct {
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
	Calls    int    `json:"calls"`
	Sessions int    `json:"sessions"`
}

// ToolWeek is one week's calls by category.
type ToolWeek struct {
	Start string         `json:"start"` // its Monday
	Calls map[string]int `json:"calls"`
}

// SkillUse is the skills the sessions called up.
type SkillUse struct {
	Calls int     `json:"calls"`
	Top   []Skill `json:"top"`
	Count int     `json:"count"` // skills called at all
}

// Skill is one skill's calls.
type Skill struct {
	Name     string         `json:"name"`
	Calls    int            `json:"calls"`
	Sessions int            `json:"sessions"`
	Last     string         `json:"last"`     // the date it was last called
	Agents   map[string]int `json:"agents"`   // calls by agent
	Projects []ToolStat     `json:"projects"` // the folders that called it most, by name
}

// Overview sums up st's sessions of agent, with model and in folder cwd
// ("" for any), with the n that spent the most each way.
func (st Stats) Overview(agent, model, cwd string, n int) Overview {
	out := Overview{Days: []int{}, Top: map[string][]Summary{}}
	first, err1 := time.ParseInLocation(time.DateOnly, st.From, time.Local)
	last, err2 := time.ParseInLocation(time.DateOnly, st.To, time.Local)
	if err1 == nil && err2 == nil && !last.Before(first) {
		out.Days = make([]int, int(math.Round(last.Sub(first).Hours()/24))+1)
	}
	out.Messages, out.Output = make([]int, len(out.Days)), make([]int, len(out.Days))
	var in []Summary
	var spent []int
	for _, s := range st.Sessions {
		if agent != "" && s.Agent != agent || cwd != "" && s.Cwd != cwd || model != "" && !slices.Contains(s.Models, model) {
			continue
		}
		in = append(in, s)
		spent = append(spent, s.Input+s.Output)
		for _, d := range s.Days {
			if d >= 0 && d < len(out.Days) {
				out.Days[d]++
			}
		}
		for d, pd := range s.perDay {
			if d >= 0 && d < len(out.Days) {
				out.Messages[d] += pd.messages
				out.Output[d] += pd.output
			}
		}
	}
	out.Shape = shapes(in)
	out.Tools = toolUse(in, first, n)
	out.Skills = skillUse(in, n)
	out.Count = len(in)
	if len(spent) > 0 {
		sort.Ints(spent)
		// the nearest rank
		rank := func(p float64) int { return spent[max(0, int(math.Ceil(p*float64(len(spent))))-1)] }
		out.Median, out.P90 = rank(0.5), rank(0.9)
	}
	for by, v := range map[string]func(Summary) float64{
		"tokens": func(s Summary) float64 { return float64(s.Input + s.Output) },
		"cost":   func(s Summary) float64 { return s.Cost },
		"active": func(s Summary) float64 { return float64(s.Active) },
	} {
		top := slices.Clone(in)
		sort.SliceStable(top, func(i, j int) bool { return v(top[i]) > v(top[j]) })
		k := 0
		for k < len(top) && k < n && v(top[k]) > 0 {
			k++
		}
		out.Top[by] = top[:k]
	}
	return out
}

// shapes buckets the sessions that told their messages, or for minutes
// those that were at work.
func shapes(in []Summary) map[string]Shape {
	out := map[string]Shape{}
	for by, edges := range shapeEdges {
		sh := Shape{Edges: edges, Counts: make([]int, len(edges))}
		for _, s := range in {
			v := s.Prompts + s.Replies
			switch by {
			case "minutes":
				// every agent's sessions tell their time, not all their
				// messages
				if s.Active <= 0 {
					continue
				}
				v = int(max(1, (s.Active+30)/60))
			case "messages", "autonomy":
				if v == 0 {
					continue
				}
			}
			if by == "autonomy" {
				v = s.ToolCalls / max(1, s.Prompts)
			}
			i := len(edges) - 1
			for i > 0 && v < edges[i] {
				i--
			}
			sh.Counts[i]++
			sh.Total++
		}
		out[by] = sh
	}
	return out
}

// toolUse sums up the sessions' tool calls, the n most called.
func toolUse(in []Summary, first time.Time, n int) ToolUse {
	out := ToolUse{Top: []ToolStat{}, Categories: []ToolStat{}, Weeks: []ToolWeek{}}
	tools, cats := map[string]*ToolStat{}, map[string]*ToolStat{}
	weeks := map[string]map[string]int{}
	for _, s := range in {
		if s.ToolCalls == 0 {
			continue
		}
		out.Sessions++
		out.Calls += s.ToolCalls
		seen := map[string]bool{}
		for name, c := range s.tools {
			t := tools[name]
			if t == nil {
				t = &ToolStat{Name: name, Category: ToolCategory(name)}
				tools[name] = t
			}
			t.Calls += c
			t.Sessions++
			cat := cats[t.Category]
			if cat == nil {
				cat = &ToolStat{Name: t.Category}
				cats[t.Category] = cat
			}
			cat.Calls += c
			if !seen[t.Category] {
				seen[t.Category] = true
				cat.Sessions++
			}
		}
		if first.IsZero() {
			continue
		}
		for d, pd := range s.perDay {
			day := first.AddDate(0, 0, d)
			mon := day.AddDate(0, 0, -(int(day.Weekday())+6)%7).Format(time.DateOnly)
			w := weeks[mon]
			if w == nil {
				w = map[string]int{}
				weeks[mon] = w
			}
			for cat, c := range pd.tools {
				w[cat] += c
			}
		}
	}
	for _, t := range tools {
		out.Top = append(out.Top, *t)
	}
	for _, c := range cats {
		out.Categories = append(out.Categories, *c)
	}
	byCalls := func(a, b ToolStat) int { return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.Name, b.Name)) }
	slices.SortFunc(out.Top, byCalls)
	slices.SortFunc(out.Categories, byCalls)
	out.Top = out.Top[:min(len(out.Top), n)]
	for mon, calls := range weeks {
		out.Weeks = append(out.Weeks, ToolWeek{Start: mon, Calls: calls})
	}
	slices.SortFunc(out.Weeks, func(a, b ToolWeek) int { return cmp.Compare(a.Start, b.Start) })
	return out
}

// skillUse sums up the skills the sessions called up, the n most called.
func skillUse(in []Summary, n int) SkillUse {
	out := SkillUse{Top: []Skill{}}
	skills := map[string]*Skill{}
	projects := map[string]map[string]int{}
	for _, s := range in {
		for name, c := range s.skills {
			k := skills[name]
			if k == nil {
				k = &Skill{Name: name, Agents: map[string]int{}, Projects: []ToolStat{}}
				skills[name] = k
				projects[name] = map[string]int{}
			}
			k.Calls += c
			k.Sessions++
			k.Agents[s.Agent] += c
			k.Last = max(k.Last, s.skillLast[name])
			projects[name][s.Cwd] += c
			out.Calls += c
		}
	}
	out.Count = len(skills)
	for name, k := range skills {
		for cwd, c := range projects[name] {
			k.Projects = append(k.Projects, ToolStat{Name: cwd, Calls: c})
		}
		slices.SortFunc(k.Projects, func(a, b ToolStat) int { return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.Name, b.Name)) })
		k.Projects = k.Projects[:min(len(k.Projects), 3)]
		out.Top = append(out.Top, *k)
	}
	slices.SortFunc(out.Top, func(a, b Skill) int { return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.Name, b.Name)) })
	out.Top = out.Top[:min(len(out.Top), n)]
	return out
}

// ToolCategory is the kind of tool a name is, Claude Code's and Codex's
// alike: Bash, Edit, Read, Write, Glob, Grep, Task (subagents and plans),
// Tool (an MCP server's) or Other.
func ToolCategory(name string) string {
	switch name {
	case "Bash", "BashOutput", "KillShell", "KillBash", "exec_command", "shell", "shell_command", "local_shell_call", "write_stdin", "unified_exec", "bash", "powershell":
		return "Bash"
	case "Edit", "MultiEdit", "NotebookEdit", "apply_patch", "edit", "str_replace":
		return "Edit"
	case "Read", "NotebookRead", "view_image", "read_file", "view", "read":
		return "Read"
	case "Write", "write_file", "create", "write":
		return "Write"
	case "Glob", "LS", "list_dir", "glob", "find", "ls", "fffind":
		return "Glob"
	case "Grep", "grep", "grep_files", "search", "ffgrep":
		return "Grep"
	case "Task", "Agent", "TodoWrite", "update_plan", "spawn_agent", "send_input", "wait", "close_agent", "Workflow":
		return "Task"
	}
	if strings.HasPrefix(name, "mcp__") || strings.HasPrefix(name, "mcp_") {
		return "Tool"
	}
	return "Other"
}

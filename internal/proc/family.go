package proc

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// A Kin is a running process with its parent's id, as a listing of every
// process saw it.
type Kin struct {
	PID, PPID int
	Args      string
}

// Family lists every process this user can see, with its parent's id
// and its command line (#7812: what holds magpie's folders).
func Family(ctx context.Context) ([]Kin, error) { return family(ctx) }

// EndTrees ends the processes match picks from every process this user
// can see, each with all it started, and says how many match picked. A
// process under one already picked goes with that one; magpie and what it
// runs under are never picked. It is how a turned-off MCP server's
// processes go when the agent that started them keeps running (#3885).
func EndTrees(ctx context.Context, match func(args string) bool) (int, error) {
	ps, err := family(ctx)
	if err != nil {
		return 0, err
	}
	picked := picks(ps, match, os.Getpid())
	for _, pid := range picked {
		endFamily(pid, under(ps, pid))
	}
	return len(picked), nil
}

// parseFamily reads lines of "pid ppid command line", as ps prints them.
func parseFamily(out string) []Kin {
	var ps []Kin
	for _, line := range strings.Split(out, "\n") {
		id, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		parent, args, _ := strings.Cut(strings.TrimSpace(rest), " ")
		pid, err1 := strconv.Atoi(id)
		ppid, err2 := strconv.Atoi(parent)
		if err1 != nil || err2 != nil || pid <= 0 {
			continue
		}
		ps = append(ps, Kin{PID: pid, PPID: ppid, Args: strings.TrimSpace(args)})
	}
	return ps
}

// picks are the processes match picks, but those under another picked one
// and self with what it runs under.
func picks(ps []Kin, match func(string) bool, self int) []int {
	parent := map[int]int{}
	for _, p := range ps {
		if p.PID != p.PPID {
			parent[p.PID] = p.PPID
		}
	}
	mine := map[int]bool{}
	for p, n := self, 0; p > 0 && n < len(ps)+1; p, n = parent[p], n+1 {
		mine[p] = true
	}
	hit := map[int]bool{}
	for _, p := range ps {
		if !mine[p.PID] && p.Args != "" && match(p.Args) {
			hit[p.PID] = true
		}
	}
	var out []int
	for _, p := range ps {
		if !hit[p.PID] {
			continue
		}
		top := true
		for q, n := parent[p.PID], 0; q > 0 && n < len(ps); q, n = parent[q], n+1 {
			if hit[q] {
				top = false
				break
			}
		}
		if top {
			out = append(out, p.PID)
		}
	}
	return out
}

// under are the processes started under pid, at any depth, nearest first.
func under(ps []Kin, pid int) []int {
	kids := map[int][]int{}
	for _, p := range ps {
		if p.PID != p.PPID {
			kids[p.PPID] = append(kids[p.PPID], p.PID)
		}
	}
	var out []int
	seen := map[int]bool{pid: true}
	for queue := []int{pid}; len(queue) > 0; queue = queue[1:] {
		for _, k := range kids[queue[0]] {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
				queue = append(queue, k)
			}
		}
	}
	return out
}

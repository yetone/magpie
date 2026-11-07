//go:build darwin || linux

package claudetui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// waiting is what Claude Code is doing while it has drawn nothing: the
// commands it runs (its descendants, the longest-running first) and how
// much memory it holds, read from ps. ok is false when ps can't say.
func (s *shim) waiting() (cmds []running, rssKiB int64, ok bool) {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil, 0, false
	}
	out, err := proc.Command("ps", "-A", "-o", "pid=,ppid=,rss=,etime=,command=").Output()
	if err != nil {
		return nil, 0, false
	}
	return descendants(string(out), s.cmd.Process.Pid)
}

// descendants reads ps's lines (pid, ppid, rss, etime, command) for root's
// descendants and root's own rss.
func descendants(ps string, root int) (cmds []running, rssKiB int64, ok bool) {
	type row struct {
		pid, ppid int
		rss       int64
		age       time.Duration
		args      string
	}
	var rows []row
	for _, line := range strings.Split(ps, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		rss, err3 := strconv.ParseInt(f[2], 10, 64)
		age, err4 := etime(f[3])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		rows = append(rows, row{pid, ppid, rss, age, strings.Join(f[4:], " ")})
		if pid == root {
			rssKiB, ok = rss, true
		}
	}
	if !ok {
		return nil, 0, false
	}
	in := map[int]bool{root: true}
	for grew := true; grew; {
		grew = false
		for _, r := range rows {
			if in[r.ppid] && !in[r.pid] {
				in[r.pid] = true
				cmds = append(cmds, running{r.age, r.args})
				grew = true
			}
		}
	}
	slices.SortStableFunc(cmds, func(a, b running) int { return int(b.age - a.age) })
	return cmds, rssKiB, true
}

// etime reads ps's elapsed time, [[dd-]hh:]mm:ss.
func etime(v string) (time.Duration, error) {
	days := 0
	if d, rest, ok := strings.Cut(v, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, err
		}
		days, v = n, rest
	}
	parts := strings.Split(v, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("elapsed time %q", v)
	}
	secs := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, err
		}
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second, nil
}

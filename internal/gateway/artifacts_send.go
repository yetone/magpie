package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/sessions"
)

// Send to agent (docs/subsystems/artifacts.md, "Comments"): a comment
// sent to the agent goes to the session that published the page, picked
// up again without a window (claude -p --resume, codex exec resume,
// opencode run --session, pi -p --session), in the folder it worked in.
// It changes the page and publishes it, and what it says at the end is
// posted in the thread. With no such session, or its agent's command
// gone, a new one of the first agent installed takes it, in the same
// folder. One run at a time on an artifact; a thread sent while one is
// at work waits for it.

// sendAgents are the agents a comment can go to, in the order a new
// session is started with.
var sendAgents = []string{"claude", "codex", "opencode", "pi"}

// sendAgentNames are their names, as the shell shows them.
var sendAgentNames = map[string]string{"claude": "Claude Code", "codex": "Codex", "opencode": "OpenCode", "pi": "Pi"}

// sendFor is the longest a run may take.
const sendFor = 20 * time.Minute

// sendLook says where an agent's command is; a var so tests can fake it.
var sendLook = func(agent string) string {
	p, err := proc.LookPath(agent)
	if err != nil {
		return ""
	}
	return p
}

// sendSessions are the sessions a publisher's is looked for in; a var for
// tests.
var sendSessions = func() []sessions.Session { return sessions.List(0) }

// sendRun starts an agent's command; a var so tests can fake the agent.
var sendRun = func(ctx context.Context, dir string, stdin string, name string, args ...string) ([]byte, []byte, error) {
	cmd := proc.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// commentAgent is who a comment on a goes to, as the shell is told.
type commentAgentInfo struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// Can is the contract's canSendToClaude: available, or no_session
	Can string `json:"can"`
}

// publisher is the version of a an agent published last (not the page
// itself), nil when none did.
func publisher(a *artifact.Artifact) *artifact.Version {
	for i := len(a.Versions) - 1; i >= 0; i-- {
		v := &a.Versions[i]
		if v.Agent != "" && v.Agent != artifact.PageAgent {
			return v
		}
	}
	return nil
}

// sendAgentOf is the agent a version's publisher was, by sendAgents'
// ids: Claude Code names itself claude-cli or claude-code by the route it
// came in on, OpenCode and Pi by a version or a client's name.
func sendAgentOf(name string) string {
	ag := memoryAgent(name)
	if _, ok := sendAgentNames[ag]; ok {
		return ag
	}
	l := strings.ToLower(ag)
	for _, p := range []struct{ prefix, id string }{{"claude-cli", "claude"}, {"claude-code", "claude"}, {"claude_code", "claude"}, {"opencode", "opencode"}, {"pi-", "pi"}, {"pi/", "pi"}} {
		if strings.HasPrefix(l, p.prefix) {
			return p.id
		}
	}
	return ""
}

// sendAgentFor is the agent a comment on a goes to: the publisher's when
// its command is here, else the first installed.
func sendAgentFor(a *artifact.Artifact) string {
	if v := publisher(a); v != nil {
		if ag := sendAgentOf(v.Agent); ag != "" && sendLook(ag) != "" {
			return ag
		}
	}
	for _, ag := range sendAgents {
		if sendLook(ag) != "" {
			return ag
		}
	}
	return ""
}

func (s *Server) commentAgent(a *artifact.Artifact) commentAgentInfo {
	ag := sendAgentFor(a)
	if ag == "" {
		return commentAgentInfo{Can: "no_session"}
	}
	return commentAgentInfo{ID: ag, Name: sendAgentNames[ag], Can: "available"}
}

// sends are the runs at work, by artifact; each artifact's queue goes one
// at a time.
var sends = struct {
	sync.Mutex
	busy   map[string]bool
	queue  map[string][]string
	cancel map[string]context.CancelFunc // by artifact\x00thread
}{busy: map[string]bool{}, queue: map[string][]string{}, cancel: map[string]context.CancelFunc{}}

// sendThread sends thread tid of a to its agent, now or once the run at
// work on a is done.
func (s *Server) sendThread(a *artifact.Artifact, tid string) error {
	ag := sendAgentFor(a)
	if ag == "" {
		return &artifact.CommentError{Code: "claude_unavailable", Message: "no agent can take it on this computer"}
	}
	sends.Lock()
	for _, q := range sends.queue[a.ID] {
		if q == tid {
			sends.Unlock()
			return nil
		}
	}
	if _, at := sends.cancel[a.ID+"\x00"+tid]; at {
		sends.Unlock()
		return nil
	}
	sends.queue[a.ID] = append(sends.queue[a.ID], tid)
	start := !sends.busy[a.ID]
	if start {
		sends.busy[a.ID] = true
	}
	sends.Unlock()
	state := "waiting"
	if start {
		state = "working"
	}
	if err := artifact.SetRun(a.ID, tid, &artifact.Run{Agent: ag, State: state, At: time.Now().UTC()}); err != nil {
		return err
	}
	if start {
		go s.sendQueue(a.ID)
	}
	return nil
}

// stopThread stops thread tid's run, or takes it off the queue.
func (s *Server) stopThread(id, tid string) error {
	sends.Lock()
	q := sends.queue[id]
	for i, t := range q {
		if t == tid {
			sends.queue[id] = append(q[:i:i], q[i+1:]...)
			sends.Unlock()
			return artifact.SetRun(id, tid, nil)
		}
	}
	cancel := sends.cancel[id+"\x00"+tid]
	sends.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *Server) sendQueue(id string) {
	for {
		sends.Lock()
		q := sends.queue[id]
		if len(q) == 0 {
			delete(sends.queue, id)
			delete(sends.busy, id)
			sends.Unlock()
			return
		}
		tid := q[0]
		sends.queue[id] = q[1:]
		ctx, cancel := context.WithTimeout(context.Background(), sendFor)
		sends.cancel[id+"\x00"+tid] = cancel
		sends.Unlock()
		s.sendOne(ctx, id, tid)
		cancel()
		sends.Lock()
		delete(sends.cancel, id+"\x00"+tid)
		sends.Unlock()
	}
}

// sendOne runs the agent on thread tid, and posts its answer there.
func (s *Server) sendOne(ctx context.Context, id, tid string) {
	a, err := artifact.Get(id)
	if err != nil {
		return
	}
	t, err := artifact.GetThread(id, tid)
	if err != nil {
		return
	}
	ag := sendAgentFor(a)
	run := &artifact.Run{Agent: ag, State: "working", At: time.Now().UTC()}
	artifact.SetRun(id, tid, run)
	before := a.Latest().N
	answer, err := sendAgent(ctx, a, t, ag)
	after := before
	if b, gerr := artifact.Get(id); gerr == nil {
		after = b.Latest().N
	}
	run = &artifact.Run{Agent: ag, State: "done", At: time.Now().UTC()}
	if after > before {
		run.Version = after
	}
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		artifact.SetRun(id, tid, nil)
		return
	case err != nil:
		run.State, run.Error = "failed", oneLineErr(err)
	}
	if answer = strings.TrimSpace(answer); answer != "" {
		if len(answer) > artifact.MaxCommentText {
			answer = strings.ToValidUTF8(answer[:artifact.MaxCommentText-3], "") + "…"
		}
		artifact.Reply(id, tid, "agent:"+ag, ag, answer, false)
	}
	artifact.SetRun(id, tid, run)
}

func oneLineErr(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "") + "…"
	}
	return s
}

// sendPrompt is what the agent is asked. It goes to the agent's model,
// so it names no product, the agent's or this one.
func sendPrompt(a *artifact.Artifact, t artifact.Thread) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A comment on the page %q (artifact %s, version %d)", a.Title, a.ID, t.Version)
	switch {
	case t.Label != "" && t.Anchor != nil:
		fmt.Fprintf(&b, ", at %q (element %s)", t.Label, t.Anchor.Path+t.Anchor.Name)
	case t.Anchor != nil:
		fmt.Fprintf(&b, ", at element %s", t.Anchor.Path+t.Anchor.Name)
	case t.Label != "":
		fmt.Fprintf(&b, ", at %q", t.Label)
	}
	b.WriteString(":\n")
	if t.Quote != "" {
		fmt.Fprintf(&b, "\nSelected text there: %q\n", t.Quote)
	}
	if t.Detail != "" {
		fmt.Fprintf(&b, "About the spot: %q\n", t.Detail)
	}
	if len(t.Comments) > 1 {
		b.WriteString("\nThe thread so far:\n")
		for _, c := range t.Comments[:len(t.Comments)-1] {
			who := "Person"
			if c.Agent != "" {
				who = "You"
			}
			fmt.Fprintf(&b, "- %s: %s\n", who, c.Text)
		}
		b.WriteString("\nThe latest comment:\n")
	}
	b.WriteString("\n" + t.Comments[len(t.Comments)-1].Text + "\n\n")
	fmt.Fprintf(&b, "Change the page as the comment asks, and publish it again as the same artifact (id %s): read its current version first. If the comment only asks a question, answer it and publish nothing. End with one or two plain sentences saying what you did; they are posted in reply to the comment.", a.ID)
	return b.String()
}

// publisherSession is the session that published a's newest agent
// version: of its agent, in its folder, active when it was published,
// with the artifact's id in its file. "" when none is found.
func publisherSession(a *artifact.Artifact, agent string) (id, cwd string) {
	v := publisher(a)
	if v == nil || sendAgentOf(v.Agent) != agent {
		return "", ""
	}
	var found []sessions.Session
	for _, se := range sendSessions() {
		if se.Agent != agent || se.WSL != "" || se.ReadOnly || se.Gateway {
			continue
		}
		if v.Cwd != "" && filepath.Clean(se.Cwd) != filepath.Clean(v.Cwd) {
			continue
		}
		if se.Last.Before(v.At.Add(-time.Minute)) || se.Start.After(v.At.Add(time.Minute)) {
			continue
		}
		found = append(found, se)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Last.After(found[j].Last) })
	for _, se := range found {
		if fileNames(se.Path, a.ID) {
			return se.ID, se.Cwd
		}
	}
	return "", ""
}

// fileNames is whether the session file at p names the artifact id. A
// file it can't read, or a database many sessions share, names nothing.
func fileNames(p, id string) bool {
	if p == "" || strings.HasSuffix(p, ".db") || strings.HasSuffix(p, ".sqlite") {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	tail := []byte{}
	for {
		n, err := f.Read(buf)
		chunk := append(tail, buf[:n]...)
		if bytes.Contains(chunk, []byte(id)) {
			return true
		}
		if len(chunk) > len(id) {
			tail = append([]byte{}, chunk[len(chunk)-len(id):]...)
		}
		if err != nil {
			return false
		}
	}
}

// sendTools are the tools a resumed Claude Code may use without asking:
// the artifacts server's, under its own name and behind the router, and
// reading and editing in its folder.
var sendTools = []string{
	"mcp__magpie-artifacts", "mcp__magpie__publish_artifact", "mcp__magpie__read_artifact", "mcp__magpie__list_artifacts",
	"mcp__magpie__artifact_guide", "mcp__magpie__artifact_data", "Read", "Glob", "Grep", "Edit", "Write",
}

// sendAgent runs agent on thread t, and is what it said at the end.
func sendAgent(ctx context.Context, a *artifact.Artifact, t artifact.Thread, agent string) (string, error) {
	bin := sendLook(agent)
	if bin == "" {
		return "", fmt.Errorf("%s isn't installed", sendAgentNames[agent])
	}
	sid, dir := publisherSession(a, agent)
	if dir == "" {
		if v := publisher(a); v != nil && v.Cwd != "" {
			dir = v.Cwd
		}
	}
	if fi, err := os.Stat(dir); dir == "" || err != nil || !fi.IsDir() {
		// no folder of its own: one for the artifact's runs
		dir = filepath.Join(artifact.Dir(), ".runs", a.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	prompt := sendPrompt(a, t)
	var args []string
	switch agent {
	case "claude":
		args = []string{"-p", "--output-format", "json", "--permission-mode", "acceptEdits", "--allowedTools", strings.Join(sendTools, ",")}
		if sid != "" {
			args = append(args, "--resume", sid)
		}
		out, errb, err := sendRun(ctx, dir, prompt, bin, args...)
		var res struct {
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
		}
		if jerr := json.Unmarshal(lastJSONLine(out), &res); jerr != nil {
			return "", runError(err, errb, out)
		}
		if res.IsError {
			return "", errors.New(res.Result)
		}
		return res.Result, nil
	case "codex":
		last, err := os.CreateTemp("", "magpie-send-*.txt")
		if err != nil {
			return "", err
		}
		last.Close()
		defer os.Remove(last.Name())
		args = []string{"exec"}
		if sid != "" {
			args = append(args, "resume", sid)
		}
		args = append(args, "--skip-git-repo-check", "-c", `sandbox_mode="workspace-write"`, "-o", last.Name(), "-")
		out, errb, err := sendRun(ctx, dir, prompt, bin, args...)
		said, _ := os.ReadFile(last.Name())
		if len(bytes.TrimSpace(said)) == 0 {
			return "", runError(err, errb, out)
		}
		return string(said), nil
	case "opencode":
		args = []string{"run", "--format", "json"}
		if sid != "" {
			args = append(args, "--session", sid)
		}
		args = append(args, prompt)
		out, errb, err := sendRun(ctx, dir, "", bin, args...)
		said := opencodeText(out)
		if said == "" {
			return "", runError(err, errb, out)
		}
		return said, nil
	case "pi":
		args = []string{"-p"}
		if sid != "" {
			args = append(args, "--session", sid)
		}
		args = append(args, prompt)
		out, errb, err := sendRun(ctx, dir, "", bin, args...)
		if err != nil || len(bytes.TrimSpace(out)) == 0 {
			return "", runError(err, errb, out)
		}
		return string(out), nil
	}
	return "", fmt.Errorf("%s can't take a comment", agent)
}

// lastJSONLine is the last line of out that is a JSON object.
func lastJSONLine(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); len(l) > 0 && l[0] == '{' {
			return l
		}
	}
	return out
}

// opencodeText is the text of the last step of opencode run's JSON
// events: its text parts after the last tool call.
func opencodeText(out []byte) string {
	var said []string
	for _, l := range bytes.Split(out, []byte("\n")) {
		var e struct {
			Type string `json:"type"`
			Part struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"part"`
		}
		if json.Unmarshal(l, &e) != nil {
			continue
		}
		switch {
		case e.Type == "text" || e.Part.Type == "text" && e.Part.Text != "":
			said = append(said, e.Part.Text)
		case e.Type == "tool_use" || e.Part.Type == "tool":
			said = nil
		}
	}
	return strings.TrimSpace(strings.Join(said, "\n"))
}

// runError is why a run gave no answer: its error and the end of what
// it wrote.
func runError(err error, errb, out []byte) error {
	tail := bytes.TrimSpace(errb)
	if len(tail) == 0 {
		tail = bytes.TrimSpace(out)
	}
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee) && len(tail) > 0:
		return fmt.Errorf("%s", tail)
	case err != nil:
		return err
	case len(tail) > 0:
		return fmt.Errorf("%s", tail)
	}
	return errors.New("the agent said nothing")
}

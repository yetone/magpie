package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/artifact"
)

// The comments API (docs/subsystems/artifacts.md, "Comments"): what the
// viewer shell (shell.js) and a page that declares comments call, at
// <page>/_api/comments. The shell holds the shell's token and does all
// of it; the page holds its own and only writes, as the contract has it:
// nothing lists threads but the shell, and a page declared
// {comments: {composer_only: true}} writes nothing.

// artifactCommentCall is one comments call.
type artifactCommentCall struct {
	Token    string           `json:"t"`
	Op       string           `json:"op"`
	Thread   string           `json:"thread"`
	Comment  string           `json:"comment"`
	Anchor   *artifact.Anchor `json:"anchor"`
	Label    string           `json:"label"`
	Detail   string           `json:"detail"`
	Quote    string           `json:"quote"`
	Text     string           `json:"text"`
	Resolved bool             `json:"resolved"`
	// Send: the comment goes to the agent too
	Send bool `json:"send"`
	// Version is the version the comment was made on
	Version int `json:"version"`
}

// commentPageOps are what a page's own token may do, with the full
// declaration: ask whether the agent can take a comment. Its writes go
// through the viewer's shell, which asks the viewer first and writes
// with the shell's token.
var commentPageOps = []string{"can"}

// commentWrites are the calls that write as the viewer.
var commentWrites = []string{"create", "reply", "resolve", "delete"}

func (s *Server) artifactComments(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		artifactAPIError(w, http.StatusBadRequest, "invalid", "a comments call is at most 64 KiB")
		return
	}
	var c artifactCommentCall
	if err := json.Unmarshal(body, &c); err != nil {
		artifactAPIError(w, http.StatusBadRequest, "transform_error", "the call isn't JSON: "+err.Error())
		return
	}
	if !local(r) || !loopbackHost(r.Host) {
		artifactAPIError(w, http.StatusForbidden, "unavailable", "an artifact's comments are reached from a browser on this computer, at "+URL())
		return
	}
	id, n, ok := ArtifactRef(r.PathValue("ref"))
	if !ok || strings.Contains(r.PathValue("ref"), "/") {
		artifactAPIError(w, http.StatusNotFound, "not_found", "no such artifact here")
		return
	}
	a, err := artifact.Get(id)
	if err != nil {
		artifactAPIError(w, http.StatusNotFound, "unavailable", "the artifact is gone")
		return
	}
	shell := artifact.ShellTokenOK(id, c.Token)
	if !shell {
		// the page's own: from a version declaring comments in full
		h.Set("Access-Control-Allow-Origin", "*")
		if !artifact.PageTokenOK(id, c.Token) {
			artifactAPIError(w, http.StatusForbidden, "not_granted", "this isn't the artifact's own page")
			return
		}
		v := a.Version(n)
		if v == nil || !v.Capabilities.Has("comments") {
			artifactAPIError(w, http.StatusForbidden, "not_granted", "this version of the page doesn't declare comments")
			return
		}
		if artifact.CommentsComposerOnly(v.Capabilities) {
			artifactAPIError(w, http.StatusForbidden, "not_granted", "the page declares comments as composer_only: it opens the composer, and writes nothing")
			return
		}
		if slices.Contains(commentWrites, c.Op) {
			artifactAPIError(w, http.StatusForbidden, "consent_required", "a page's comments are written through magpie's viewer, which asks the viewer first")
			return
		}
		if !slices.Contains(commentPageOps, c.Op) {
			artifactAPIError(w, http.StatusForbidden, "not_granted", "a page writes comments; the shell lists them")
			return
		}
	}
	if c.Version <= 0 {
		c.Version = a.Latest().N
		if n > 0 {
			c.Version = n
		}
	}
	who, err := artifact.LocalViewer()
	if err != nil {
		artifactAPIError(w, http.StatusInternalServerError, "unavailable", err.Error())
		return
	}
	var out any = map[string]any{}
	switch c.Op {
	case "list":
		var ts []artifact.Thread
		if ts, err = artifact.Threads(id); err == nil {
			if ts == nil {
				ts = []artifact.Thread{}
			}
			out = map[string]any{"threads": ts, "version": a.Latest().N, "agent": s.commentAgent(a), "me": who.ID}
		}
	case "can":
		out = map[string]any{"can": s.commentAgent(a).Can}
	case "create":
		if c.Send && s.commentAgent(a).Can != "available" {
			err = &artifact.CommentError{Code: "claude_unavailable", Message: "no agent can take it now; nothing was posted"}
			break
		}
		var t artifact.Thread
		t, err = artifact.StartThread(id, artifact.NewThread{Anchor: c.Anchor, Label: c.Label, Detail: c.Detail, Quote: c.Quote, Version: c.Version, Author: who.ID, Text: c.Text, Sent: c.Send})
		if err == nil {
			if c.Send {
				err = s.sendThread(a, t.ID)
			}
			out = map[string]any{"threadId": t.ID, "commentId": t.Comments[0].ID}
		}
	case "reply":
		if c.Send && s.commentAgent(a).Can != "available" {
			err = &artifact.CommentError{Code: "claude_unavailable", Message: "no agent can take it now; nothing was posted"}
			break
		}
		var cm artifact.Comment
		if cm, err = artifact.Reply(id, c.Thread, who.ID, "", c.Text, c.Send); err == nil {
			if c.Send {
				err = s.sendThread(a, c.Thread)
			}
			out = map[string]any{"threadId": c.Thread, "commentId": cm.ID}
		}
	case "send":
		// a thread already written, to the agent
		if s.commentAgent(a).Can != "available" {
			err = &artifact.CommentError{Code: "claude_unavailable", Message: "no agent can take it now"}
			break
		}
		if _, err = artifact.GetThread(id, c.Thread); err == nil {
			err = s.sendThread(a, c.Thread)
		}
	case "stop":
		err = s.stopThread(id, c.Thread)
	case "resolve":
		err = artifact.Resolve(id, c.Thread, c.Resolved)
	case "delete":
		if c.Comment != "" {
			err = artifact.DeleteComment(id, c.Thread, c.Comment)
		} else {
			s.stopThread(id, c.Thread)
			err = artifact.DeleteThread(id, c.Thread)
		}
	default:
		err = &artifact.CommentError{Code: "capability_removed", Message: c.Op + " isn't a comments call"}
	}
	if err != nil {
		var ce *artifact.CommentError
		switch {
		case errors.As(err, &ce):
			status := http.StatusBadRequest
			if ce.Code == "not_found" {
				status = http.StatusNotFound
			}
			artifactAPIError(w, status, ce.Code, ce.Message)
		case errors.Is(err, artifact.ErrNotFound):
			artifactAPIError(w, http.StatusNotFound, "unavailable", "the artifact is gone")
		default:
			artifactAPIError(w, http.StatusInternalServerError, "upstream_error", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// artifactShellEvents is the viewer shell's stream, at
// <page>/_api/shell?t=<shell token>: {"comments":true} when the threads
// change, here or in another process, and {"version":n} when a version
// is published, so a shell showing the newest loads it (a comment sent
// to the agent ends in one).
func (s *Server) artifactShellEvents(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if !local(r) || !loopbackHost(r.Host) {
		artifactAPIError(w, http.StatusForbidden, "unavailable", "an artifact's comments are reached from a browser on this computer, at "+URL())
		return
	}
	id, _, ok := ArtifactRef(r.PathValue("ref"))
	if !ok || strings.Contains(r.PathValue("ref"), "/") || !artifact.ShellTokenOK(id, r.URL.Query().Get("t")) {
		artifactAPIError(w, http.StatusForbidden, "not_granted", "this isn't the artifact's viewer")
		return
	}
	a, err := artifact.Get(id)
	if err != nil {
		artifactAPIError(w, http.StatusNotFound, "unavailable", "the artifact is gone")
		return
	}
	changes, stop := artifact.Watch(id)
	defer stop()
	rc := http.NewResponseController(w)
	defer rc.SetWriteDeadline(time.Time{})
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(line string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(w, line); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("retry: 2000\n: magpie\n\n") {
		return
	}
	newest, stamp := a.Latest().N, artifact.CommentsStamp(id)
	poll := time.NewTicker(artifactPollEvery)
	defer poll.Stop()
	beat := time.NewTicker(artifactBeat)
	defer beat.Stop()
	const comments = "data: {\"comments\":true}\n\n"
	for {
		var line string
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsEnding():
			return
		case c := <-changes:
			switch {
			case c.Comments:
				stamp = artifact.CommentsStamp(id)
				line = comments
			case c.Version > newest:
				newest = c.Version
				line = fmt.Sprintf("data: {\"version\":%d}\n\n", newest)
			case c.Version == 0 && c.Paths == nil:
				// fell behind: the threads again, and the newest version
				line = comments
				if a, err := artifact.Get(id); err == nil && a.Latest().N > newest {
					newest = a.Latest().N
					line += fmt.Sprintf("data: {\"version\":%d}\n\n", newest)
				}
			}
		case <-poll.C:
			a, err := artifact.Get(id)
			if errors.Is(err, artifact.ErrNotFound) {
				return
			}
			// another process's writes (`magpie mcp artifacts`)
			if st := artifact.CommentsStamp(id); st != stamp {
				stamp = st
				line = comments
			}
			if err == nil && a.Latest().N > newest {
				newest = a.Latest().N
				line += fmt.Sprintf("data: {\"version\":%d}\n\n", newest)
			}
		case <-beat.C:
			line = ": beat\n\n"
		}
		if line != "" && !send(line) {
			return
		}
	}
}

package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// ClaudeRelocation moves an entire project within one Claude Code config
// directory. Token is the preview's fingerprint; an empty token only previews.
type ClaudeRelocation struct {
	From  string `json:"from"`
	To    string `json:"to"`
	WSL   string `json:"wsl,omitempty"`
	Token string `json:"token,omitempty"`
}

type ClaudeRelocationResult struct {
	From     string `json:"from"`
	To       string `json:"to"`
	WSL      string `json:"wsl,omitempty"`
	Sessions int    `json:"sessions"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Token    string `json:"token"`
	Backup   string `json:"backup,omitempty"`
}

// RelocateClaudeProject does not touch Desktop's stores, global history, or
// session-ID-keyed files (file-history, todos and session-env). IDs stay intact.
// Clients must be closed: the in-process lock cannot exclude Claude itself.
func RelocateClaudeProject(in ClaudeRelocation) (ClaudeRelocationResult, error) {
	dbReadMu.Lock()
	defer dbReadMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	root, target, err := claudeRelocationRoot(in)
	if err != nil {
		return ClaudeRelocationResult{}, err
	}
	out, err := relocateClaudeProject(root, target, in)
	if err == nil && out.Backup != "" {
		// Discard both parses and directory listings after their paths change.
		cache, loaded = nil, false
		cacheGeneration++
		directoryCache.Lock()
		directoryCache.entries = map[string]directoryEntry{}
		directoryCache.Unlock()
		resetCalls()
		wslReset()
	}
	return out, err
}

func claudeRelocationRoot(in ClaudeRelocation) (string, string, error) {
	if in.WSL == "" {
		return ClaudeDir(), in.To, nil
	}
	if WSLHomes != nil {
		for _, h := range WSLHomes() {
			if h.Distro != in.WSL {
				continue
			}
			if !h.Running {
				return "", "", errors.New("start the WSL distribution before relocating sessions")
			}
			// Windows opens the Linux path through the selected distro, never
			// through the host's current drive or another distro.
			volume := filepath.VolumeName(h.Home)
			if volume == "" || !strings.HasPrefix(volume, `\\`) {
				return "", "", errors.New("WSL filesystem root is unavailable")
			}
			return filepath.Join(h.Home, ".claude"), filepath.Join(volume, filepath.FromSlash(in.To)), nil
		}
	}
	return "", "", errors.New("unknown WSL distribution")
}

func claudeProjectName(p string) (string, error) {
	var b strings.Builder
	for _, c := range utf16.Encode([]rune(p)) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteByte(byte(c))
		} else {
			b.WriteByte('-')
		}
	}
	if b.Len() > 200 {
		return "", errors.New("long hashed Claude project paths are not supported yet")
	}
	return b.String(), nil
}

func relocationPaths(in ClaudeRelocation) (string, string, error) {
	for _, p := range []string{in.From, in.To} {
		valid := filepath.IsAbs(p) && filepath.Clean(p) == p
		if in.WSL != "" {
			valid = path.IsAbs(p) && path.Clean(p) == p && !strings.Contains(p, `\`)
		}
		if !valid || strings.ContainsAny(p, "\x00\r\n") {
			return "", "", errors.New("use a clean absolute project path, without a trailing separator")
		}
	}
	from, err := claudeProjectName(in.From)
	if err != nil {
		return "", "", err
	}
	to, err := claudeProjectName(in.To)
	if err != nil {
		return "", "", err
	}
	if strings.EqualFold(from, to) {
		return "", "", errors.New("the project paths resolve to the same Claude storage directory")
	}
	return from, to, nil
}

type relocationFile struct {
	name string
	mod  time.Time
	mode fs.FileMode
}

func relocationCwd(cwd, from, to string) (string, bool) {
	if cwd == from {
		return to, true
	}
	for _, sep := range []string{"/", `\`} {
		if strings.HasPrefix(cwd, from+sep) {
			return to + strings.TrimPrefix(cwd, from), true
		}
	}
	return cwd, false
}

// Only top-level cwd metadata changes. Message/tool content and UUID links
// remain untouched, including historical absolute paths in tool arguments.
func relocateClaudeJSONL(b []byte, from, to string, main bool) ([]byte, bool, error) {
	lines := bytes.Split(b, []byte("\n"))
	found := false
	sawCwd := false
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row map[string]json.RawMessage
		if err := json.Unmarshal(line, &row); err != nil || row == nil {
			return nil, false, errors.New("invalid transcript JSON; repair it before relocating")
		}
		raw, ok := row["cwd"]
		if !ok {
			continue
		}
		var cwd string
		if err := json.Unmarshal(raw, &cwd); err != nil {
			return nil, false, err
		}
		next, ok := relocationCwd(cwd, from, to)
		if main && !sawCwd && !ok {
			return nil, false, fmt.Errorf("transcript contains a different project cwd %q", cwd)
		}
		sawCwd = true
		if !ok {
			// A session may have visited other projects or a subagent worktree.
			continue
		}
		found = true
		row["cwd"], _ = json.Marshal(next)
		lines[i], _ = json.Marshal(row)
	}
	return bytes.Join(lines, []byte("\n")), found, nil
}

func relocateClaudeIndex(b []byte, source, dest, from, to string) ([]byte, error) {
	var index map[string]json.RawMessage
	if err := json.Unmarshal(b, &index); err != nil || index == nil {
		return nil, errors.New("invalid Claude sessions index")
	}
	if raw, ok := index["originalPath"]; ok {
		var p string
		if json.Unmarshal(raw, &p) != nil || p != from {
			return nil, errors.New("Claude sessions index belongs to another project")
		}
		index["originalPath"], _ = json.Marshal(to)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(index["entries"], &entries); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		for key, pair := range map[string][2]string{
			"projectPath": {from, to}, "fullPath": {source, dest},
		} {
			raw, ok := entry[key]
			if !ok {
				continue
			}
			var p string
			if json.Unmarshal(raw, &p) != nil {
				return nil, fmt.Errorf("invalid index %s", key)
			}
			next, ok := relocationCwd(p, pair[0], pair[1])
			if !ok {
				return nil, fmt.Errorf("index %s is outside the source project", key)
			}
			entry[key], _ = json.Marshal(next)
		}
	}
	index["entries"], _ = json.Marshal(entries)
	return json.Marshal(index)
}

func relocateClaudeProject(root, target string, in ClaudeRelocation) (ClaudeRelocationResult, error) {
	out := ClaudeRelocationResult{From: in.From, To: in.To, WSL: in.WSL}
	from, to, err := relocationPaths(in)
	if err != nil {
		return out, err
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return out, errors.New("the target project directory does not exist")
	}
	projects := filepath.Join(root, "projects")
	source, dest := filepath.Join(projects, from), filepath.Join(projects, to)
	for _, dir := range []string{projects, source} {
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return out, errors.New("source project storage is missing or is a symbolic link")
		}
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return out, errors.New("target Claude project storage already exists; nothing was changed")
	}
	transform := func(rel string, b []byte) ([]byte, bool, error) {
		main := filepath.Dir(rel) == "." && strings.HasSuffix(rel, ".jsonl")
		sub := filepath.Base(filepath.Dir(rel)) == "subagents" && strings.HasSuffix(rel, ".jsonl")
		if main || sub {
			next, found, err := relocateClaudeJSONL(b, in.From, in.To, main)
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", rel, err)
			}
			if main && !found {
				return nil, false, fmt.Errorf("%s has no project cwd; cannot safely relocate it", rel)
			}
			return next, main, nil
		}
		if rel == "sessions-index.json" {
			// WSL indexes contain Linux paths, not the host's UNC paths.
			indexSource, indexDest := source, dest
			if in.WSL != "" {
				volume := filepath.VolumeName(root)
				if volume == "" {
					return nil, false, errors.New("cannot resolve the WSL index's Linux path")
				}
				linuxRoot := filepath.ToSlash(strings.TrimPrefix(root, volume))
				indexSource = path.Join(linuxRoot, "projects", from)
				indexDest = path.Join(linuxRoot, "projects", to)
			}
			next, err := relocateClaudeIndex(b, indexSource, indexDest, in.From, in.To)
			return next, false, err
		}
		return b, false, nil
	}
	var files []relocationFile
	hash := sha256.New()
	fmt.Fprintf(hash, "%q %q %q %q\n", root, in.From, in.To, in.WSL)
	err = filepath.WalkDir(source, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() && !fi.Mode().IsRegular() {
			return errors.New("project storage contains a symbolic link or special file")
		}
		if time.Since(fi.ModTime()) < ActiveWindow {
			return ErrActive
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%q %d %d %d\n", rel, fi.Size(), fi.ModTime().UnixNano(), fi.Mode())
		if fi.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		hash.Write(b)
		out.Files++
		out.Bytes += int64(len(b))
		_, main, err := transform(rel, b)
		if err != nil {
			return err
		}
		if main {
			out.Sessions++
		}
		if in.Token != "" {
			files = append(files, relocationFile{rel, fi.ModTime(), fi.Mode().Perm()})
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if out.Sessions == 0 {
		return out, errors.New("no Claude Code sessions found in this project")
	}
	out.Token = fmt.Sprintf("%x", hash.Sum(nil))
	if in.Token == "" {
		return out, nil
	}
	if in.Token != out.Token {
		return out, errors.New("project files changed; preview the relocation again")
	}
	// Stage beside the config's projects directory so publishing and backing
	// up use same-filesystem renames. Backups are outside the session scanner.
	backupRoot := filepath.Join(root, "magpie-relocations")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return out, err
	}
	if fi, err := os.Lstat(backupRoot); err != nil || !fi.IsDir() {
		return out, errors.New("backup location is not a regular directory")
	}
	backup, err := os.MkdirTemp(backupRoot, "project-")
	if err != nil {
		return out, err
	}
	stage := filepath.Join(backup, "staged")
	if err := os.Mkdir(stage, 0o700); err != nil {
		return out, err
	}
	for _, f := range files {
		p := filepath.Join(stage, f.name)
		b, err := os.ReadFile(filepath.Join(source, f.name))
		if err != nil {
			return out, err
		}
		b, _, err = transform(f.name, b)
		if err != nil {
			return out, err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return out, err
		}
		if err := os.WriteFile(p, b, f.mode); err != nil {
			return out, err
		}
		if err := os.Chtimes(p, f.mod, f.mod); err != nil {
			return out, err
		}
	}
	// Recheck after staging, before touching any originals.
	check := in
	check.Token = ""
	fresh, err := relocateClaudeProject(root, target, check)
	if err != nil {
		return out, err
	}
	if fresh.Token != out.Token {
		return out, errors.New("project files changed during staging; originals were not moved")
	}
	manifest, _ := json.MarshalIndent(map[string]any{
		"from": source, "to": dest, "projectFrom": in.From, "projectTo": in.To,
		"original": filepath.Join(backup, "original"), "preview": out,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(backup, "manifest.json"), manifest, 0o600); err != nil {
		return out, err
	}
	if err := publishClaudeRelocation(source, dest, backup); err != nil {
		return out, err
	}
	out.Backup = backup
	return out, nil
}

func publishClaudeRelocation(source, dest, backup string) error {
	original, stage := filepath.Join(backup, "original"), filepath.Join(backup, "staged")
	if err := os.Rename(source, original); err != nil {
		return err
	}
	// Rename would replace an empty directory on Unix; refuse it explicitly.
	_, err := os.Lstat(dest)
	if errors.Is(err, os.ErrNotExist) {
		err = os.Rename(stage, dest)
	} else if err == nil {
		err = errors.New("target storage appeared during relocation")
	}
	if err != nil {
		restoreErr := os.Rename(original, source)
		return fmt.Errorf("relocation failed (backup: %s): %w", backup, errors.Join(err, restoreErr))
	}
	return nil
}

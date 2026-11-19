package library

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/edit"
)

// A subagent's own file (JonahFu on X: show where a subagent is, to edit
// it with an LLM, or keep it in a git repository). Each subagent of the
// library is kept in a Markdown file on this computer as well, in Claude
// Code's shape with its model: <library>/subagents/<name>.md, or a file
// of the user's own (File), in a repository say. An edit made there, by
// hand, by an agent or by a git pull, is taken into the library at the
// next sync (followSubagentFiles), and every agent ticked for it gets it;
// one made on the page is written there (writeSubagentFiles). FileHash is
// what the file said when magpie last wrote or took it in: a file that
// says anything else is the user's edit, to take in, never to write over.
// One that can't be read as a subagent stays as it is, the agents keep
// what it said before, and the page says why (FileError).
//
// The file is this computer's, as Applied is: a bundle carries neither
// File nor FileHash, and Put keeps this computer's. A plugin's subagent
// (Bundle) has none: the plugin's update would undo an edit.

// subagentsDir is where the library keeps its subagents' files.
func subagentsDir() string { return filepath.Join(Dir(), "subagents") }

// file is where s is kept on this computer, "" for a plugin's.
func (s *Subagent) file() string {
	switch {
	case s.Bundle != "":
		return ""
	case s.File != "":
		return s.File
	}
	return filepath.Join(subagentsDir(), s.Name+".md")
}

// ownSubagentFile is s as its own file says it.
func ownSubagentFile(s *Subagent) []byte {
	kv := [][2]string{{"name", yamlStr(s.Name)}, {"description", yamlStr(s.Description)}}
	if s.Model != "" {
		kv = append(kv, [2]string{"model", yamlStr(s.Model)})
	}
	if len(s.Skills) > 0 {
		kv = append(kv, [2]string{"skills", yamlList(s.Skills)})
	}
	if len(s.MCP) > 0 {
		kv = append(kv, [2]string{"mcpServers", yamlList(s.MCP)})
	}
	return frontmatter(kv, s.Prompt)
}

// readOwnSubagent is the subagent a file of its own says, named name, or
// why it can't be one.
func readOwnSubagent(name string, b []byte) (*Subagent, error) {
	if len(b) > maxSubagentFile {
		return nil, errors.New("it is over 1 MB")
	}
	front, body := splitFront(b)
	if front == nil {
		return nil, errors.New("it has no front matter (--- name, description --- above the prompt)")
	}
	if n := plStr(front["name"]); n != "" && n != name {
		return nil, fmt.Errorf("it names it %s: rename it in magpie", n)
	}
	s := &Subagent{
		Name:        name,
		Description: strings.Join(strings.Fields(plStr(front["description"])), " "),
		Prompt:      strings.TrimRight(strings.TrimLeft(body, "\n"), " \n\t"),
		Model:       plStr(front["model"]),
	}
	for _, k := range []struct {
		key string
		to  *[]string
	}{{"skills", &s.Skills}, {"mcpServers", &s.MCP}} {
		named := pathsOf(front[k.key])
		if len(named) == 1 {
			named = strings.Split(named[0], ",") // Claude Code's comma list
		}
		for _, x := range named {
			if x = strings.TrimSpace(x); x != "" {
				*k.to = append(*k.to, x)
			}
		}
		*k.to = slices.Compact(slices.Sorted(slices.Values(*k.to)))
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	return s, nil
}

// saysSame is whether s and f say the same, the agents aside.
func (s *Subagent) saysSame(f *Subagent) bool {
	return s.Description == f.Description && s.Prompt == f.Prompt && s.Model == f.Model &&
		slices.Equal(s.Skills, f.Skills) && slices.Equal(s.MCP, f.MCP)
}

// readSteady reads f once it holds still: a file read while another
// process writes it in place may be half there. ok false: it changed
// while being read, to look at again later.
func readSteady(f string) (b []byte, ok bool, err error) {
	before, err := os.Stat(f)
	if err != nil {
		return nil, false, err
	}
	if b, err = os.ReadFile(f); err != nil {
		return nil, false, err
	}
	after, err := os.Stat(f)
	if err != nil {
		return nil, false, err
	}
	return b, before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && int64(len(b)) == after.Size(), nil
}

// takeFile takes what s's file says, when it changed since magpie last
// wrote or took it in: true when s changed. A file that can't be read as
// a subagent is left for writeSubagentFiles to leave too.
func (s *Subagent) takeFile() bool {
	f := s.file()
	if f == "" {
		return false
	}
	b, ok, err := readSteady(f)
	if err != nil || !ok || subagentHash(b) == s.FileHash {
		return false
	}
	x, err := readOwnSubagent(s.Name, b)
	if err != nil {
		return false
	}
	changed := !s.saysSame(x)
	s.Description, s.Prompt, s.Model, s.Skills, s.MCP = x.Description, x.Prompt, x.Model, x.Skills, x.MCP
	s.FileHash = subagentHash(b)
	return changed
}

// followSubagentFiles takes into the library every edit made in a
// subagent's file, before the agents are written from it.
func (l *Library) followSubagentFiles() {
	for _, s := range l.Subagents {
		s.takeFile()
	}
}

// writeSubagentFiles writes each subagent's file where it says other than
// the library: one missing, or one as magpie last wrote or took it in. An
// edit not taken in (it can't be read) stays.
func (l *Library) writeSubagentFiles() {
	for _, s := range l.Subagents {
		f := s.file()
		if f == "" {
			continue
		}
		b, err := os.ReadFile(f)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			continue // unknown: left as it is
		case subagentHash(b) != s.FileHash:
			continue
		default:
			if x, err := readOwnSubagent(s.Name, b); err == nil && s.saysSame(x) {
				continue
			}
		}
		body := ownSubagentFile(s)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			log.Printf("library: subagent %s's file: %v", s.Name, err)
			continue
		}
		if err := edit.WriteAtomic(f, body); err != nil {
			log.Printf("library: subagent %s's file: %v", s.Name, err)
			continue
		}
		s.FileHash = subagentHash(body)
	}
}

// dropSubagentFile takes away the library's own file of s, still as magpie
// wrote it, when s goes or is renamed. A file of the user's own (File)
// always stays, as does one edited since.
func dropSubagentFile(s *Subagent) {
	if s.File != "" || s.Bundle != "" {
		return
	}
	f := s.file()
	if b, err := os.ReadFile(f); err == nil && subagentHash(b) == s.FileHash {
		os.Remove(f)
	}
}

// subagentFileError is why s's file isn't followed, or "".
func subagentFileError(s *Subagent) string {
	f := s.file()
	if f == "" {
		return ""
	}
	b, err := os.ReadFile(f)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ""
	case err != nil:
		return err.Error()
	case subagentHash(b) == s.FileHash:
		return ""
	}
	if _, err := readOwnSubagent(s.Name, b); err != nil {
		return "magpie can't read " + f + ": " + err.Error() + ". The agents keep what it said before."
	}
	return ""
}

// ownSubagentPath is where the user asked a subagent kept, made absolute,
// or why it can't be: a file magpie writes for an agent, one of the
// library's own, a folder, or one another subagent is kept in.
func (l *Library) ownSubagentPath(name, file string) (string, error) {
	file = strings.TrimSpace(file)
	if file == "" {
		return "", nil
	}
	if rest, ok := strings.CutPrefix(file, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
		file = home() + rest
	}
	if !filepath.IsAbs(file) {
		return "", fmt.Errorf("%s: give the file's full path", file)
	}
	file = filepath.Clean(file)
	if !strings.EqualFold(filepath.Ext(file), ".md") {
		return "", fmt.Errorf("%s: give a Markdown file (.md)", file)
	}
	if st, err := os.Stat(file); err == nil && st.IsDir() {
		return "", fmt.Errorf("%s is a folder: give a file", file)
	}
	real := realBelow(file)
	if lib := realBelow(Dir()); strings.HasPrefix(real, lib+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is the library's own", file)
	}
	for _, s := range l.Subagents {
		if s.Name != name && s.File != "" && realBelow(s.File) == real {
			return "", fmt.Errorf("the subagent %s is kept in %s already", s.Name, file)
		}
	}
	for _, a := range l.Applied {
		for _, fs := range [][]SubagentFile{a.Subagents, a.Commands, a.Rules} {
			for _, f := range fs {
				if realBelow(f.Path) == real {
					return "", fmt.Errorf("magpie writes %s for an agent: a subagent can't be kept there", file)
				}
			}
		}
	}
	return file, nil
}

// subagentFilesTried is, for each file, the edit keepSubagentFiles last
// took a sync for: one that can't be read is tried once, not every tick.
var subagentFilesTried struct {
	sync.Mutex
	at map[string]string
}

// keepSubagentFiles writes the agents again when a subagent's file says
// other than magpie last wrote or took in: edited by an agent, or pulled
// by git, while magpie runs.
func keepSubagentFiles() {
	mu.Lock()
	l, err := load()
	edited := map[string]string{}
	if err == nil {
		for _, s := range l.Subagents {
			if f := s.file(); f != "" {
				if b, err := os.ReadFile(f); err == nil && subagentHash(b) != s.FileHash {
					edited[f] = subagentHash(b)
				}
			}
		}
	}
	mu.Unlock()
	if err != nil {
		return
	}
	subagentFilesTried.Lock()
	changed := false
	for f, h := range edited {
		if subagentFilesTried.at[f] != h {
			changed = true
		}
	}
	subagentFilesTried.at = edited
	subagentFilesTried.Unlock()
	if !changed {
		return
	}
	if _, err := change(func(*Library) error { return nil }); err != nil {
		log.Println("library: subagent files:", err)
	}
}

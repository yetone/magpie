package library

// A skill's files read and edited in the page (refeccd on Discord: skills
// handed over to magpie from ~/.agents/skills could no longer be edited
// there, where the user used to). What is saved goes into the library's
// folder of the skill, so every agent linked to it reads it at once, and
// the sync that follows makes each agent's copy again. A copy edited in an
// agent meanwhile loses to the newer save and is kept aside with the
// backups (takeEdits). A skill from GitHub saved here is changed here
// (editState): an update asks before it replaces the edit.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/edit"
)

const (
	skillFileMax  = 1 << 20 // a text file larger than this is not opened in the page
	skillFilesMax = 200     // the files listed of one skill
)

// SkillFile is one file of a library skill as the page reads it.
type SkillFile struct {
	File  string   `json:"file"`  // its place in the skill's folder, with /
	Text  string   `json:"text"`  // what it holds
	Path  string   `json:"path"`  // the skill's folder
	Files []string `json:"files"` // the skill's text files, SKILL.md first
}

// ReadSkillFile is the file of a library skill at file (SKILL.md when
// empty), with the skill's other text files listed.
func ReadSkillFile(name, file string) (*SkillFile, error) {
	if err := checkName("skill", name); err != nil {
		return nil, err
	}
	if file == "" {
		file = "SKILL.md"
	}
	root := realDir(skillDir(name))
	p, err := skillFilePath(root, file)
	if err != nil {
		return nil, err
	}
	text, err := readSkillText(p)
	if err != nil {
		if file == "SKILL.md" && errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s has no SKILL.md in the library", name)
		}
		return nil, err
	}
	return &SkillFile{File: filepath.ToSlash(filepath.Clean(file)), Text: text, Path: skillDir(name), Files: skillTextFiles(root)}, nil
}

// SaveSkillFile writes text into the file of a library skill at file, and
// gives the skill to its agents again. was is the text the page opened:
// when the file holds something else now (an agent, an update or the user
// changed it since), nothing is written, and the error says so.
func SaveSkillFile(name, file, text, was string) (*Result, error) {
	if err := checkName("skill", name); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if l.skill(name) == nil {
			return fmt.Errorf("no skill called %s", name)
		}
		p, err := skillFilePath(realDir(skillDir(name)), file)
		if err != nil {
			return err
		}
		cur, err := readSkillText(p)
		if err != nil {
			return err
		}
		if cur != was {
			return fmt.Errorf("%s was changed since it was opened; open it again to see what it holds now", file)
		}
		// the line ends the file was written with: the page's text has \n
		if strings.Contains(cur, "\r\n") {
			text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
		}
		if text == cur {
			return nil
		}
		if len(text) > skillFileMax {
			return fmt.Errorf("%s would be over %d KB", file, skillFileMax>>10)
		}
		return edit.WriteAtomic(p, []byte(text))
	})
}

// skillFilePath is the file at rel in the skill's folder root: a place in
// it, not a link, and none of the folders magpie leaves aside.
func skillFilePath(root, rel string) (string, error) {
	rel = filepath.FromSlash(rel)
	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%q is not a file of the skill", rel)
	}
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if skipInSkill(part) || installedDirs[part] {
			return "", fmt.Errorf("%q is not a file of the skill", rel)
		}
	}
	p := filepath.Join(root, rel)
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file magpie opens: it is a link or a folder", filepath.ToSlash(rel))
	}
	// a folder in it that is a link leads out of the skill
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err != nil {
		return "", err
	} else if r, err := filepath.EvalSymlinks(root); err != nil {
		return "", err
	} else if dir != r && !strings.HasPrefix(dir, r+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the skill's folder", filepath.ToSlash(rel))
	}
	return p, nil
}

// readSkillText is the file at p as text: one too large, or not text, is
// refused rather than shown garbled or written back changed.
func readSkillText(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, skillFileMax+1))
	if err != nil {
		return "", err
	}
	if len(b) > skillFileMax {
		return "", fmt.Errorf("%s is over %d KB, too large to open here", filepath.Base(p), skillFileMax>>10)
	}
	if !isText(b) {
		return "", fmt.Errorf("%s is not a text file", filepath.Base(p))
	}
	return string(b), nil
}

func isText(b []byte) bool { return utf8.Valid(b) && !bytes.Contains(b, []byte{0}) }

// skillTextFiles lists the text files in the skill's folder, SKILL.md
// first, then by place; the folders installing it makes, version
// control's and links are left out.
func skillTextFiles(root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if skipInSkill(name) || installedDirs[name] || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) >= skillFilesMax {
			return filepath.SkipAll
		}
		if !d.Type().IsRegular() || skipInSkill(name) || fromInstall(name, false, true) {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.Size() > skillFileMax {
			return nil
		}
		if !headIsText(p) {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	slices.SortStableFunc(out, func(a, b string) int {
		if (a == "SKILL.md") != (b == "SKILL.md") {
			if a == "SKILL.md" {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return out
}

// headIsText is whether the file's first 8 KB read as text, a rune cut at
// the end allowed.
func headIsText(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 8<<10)
	n, _ := io.ReadFull(f, b)
	b = b[:n]
	if bytes.Contains(b, []byte{0}) {
		return false
	}
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return utf8.Valid(b)
}

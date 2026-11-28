package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// The pull script (Michaelfpqc on X): the router's get_skill gives an
// agent on another computer a skill's words, but a skill that runs a
// script, and an agent that reads skills only from its own folder, need
// them on that computer's disk. pull.sh, run there, copies the Library's
// skills into its agents' folders: ~/.agents/skills, and Claude Code's
// skills folder when Claude Code is there (MAGPIE_SKILLS_DIRS says
// others). It is asked for, and asks for the skills, with a gateway key,
// and is given them as get_skill is (remoteSkillsFor), the router on or
// not.
//
// A folder of the user's own is never written over: each skill pulled
// gets a .magpie-pulled beside its SKILL.md with where it came from and a
// sum of its files, and a folder is replaced, or removed when its skill
// is gone from the Library, only when it has that mark, came from the
// same magpie and is as it was pulled.

// SkillsArchivePath is where the Library's skills are given as one
// .tar.gz, each skill a folder by its name, beside skillsListFile.
// SkillsPullPath is the script that pulls them.
const (
	SkillsArchivePath = "/v1/magpie/skills.tar.gz"
	SkillsPullPath    = "/v1/magpie/skills/pull.sh"
)

// skillsListFile names, one a line, every skill the Library gives, those
// left out of the archive for its size too: pull.sh removes a skill it
// pulled only when this doesn't name it, so one it isn't sent this time
// is kept.
const skillsListFile = ".magpie-skills"

// skillsArchiveMax is how many bytes of files the archive holds at most;
// a skill past it is named in skillsListFile and the X-Magpie-Skills-Left
// header, not sent.
var skillsArchiveMax = 64 << 20

// skillsPullRefused is what a request not given the skills is told, when
// it is no held key's.
const skillsPullRefused = "magpie's skills are given only to another computer, with a gateway key of this magpie (Authorization: Bearer <key>), while it is shared (Settings → Share on local network); this computer's agents have them in their own folders"

//go:embed skills_pull.sh
var skillsPullScript string

// skillsBaseOK is the address pull.sh is written with: a URL's characters
// only, nothing a shell's single quotes would end on.
var skillsBaseOK = lazyre.New(`^https?://[A-Za-z0-9.\-_~:\[\]%/@]+$`)

// skillsPullAllowed answers w for r when it is not given the skills, and
// says whether it is.
func skillsPullAllowed(w http.ResponseWriter, r *http.Request) bool {
	ok, why := remoteSkillsFor(r)
	if !ok {
		if why == "" {
			why = skillsPullRefused
		}
		writeError(w, provider.Chat, http.StatusForbidden, why)
	}
	return ok
}

// skillsPull is pull.sh, written for the address r asked this magpie by.
func (s *Server) skillsPull(w http.ResponseWriter, r *http.Request) {
	if !skillsPullAllowed(w, r) {
		return
	}
	base := strings.TrimRight(imageMCPBase(r), "/")
	if !skillsBaseOK.MatchString(base) {
		writeError(w, provider.Chat, http.StatusBadRequest, "this magpie's address can't go in a script: "+base)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(strings.ReplaceAll(skillsPullScript, "@BASE@", base)))
}

// skillsArchive is the Library's skills as pull.sh takes them.
func (s *Server) skillsArchive(w http.ResponseWriter, r *http.Request) {
	if !skillsPullAllowed(w, r) {
		return
	}
	h := hubRemoteSkills()
	ks, err := h.RemoteSkills("")
	if err != nil {
		writeError(w, provider.Chat, http.StatusServiceUnavailable, "magpie couldn't read its Library: "+err.Error())
		return
	}
	b, left, err := skillsTar(h, ks)
	if err != nil {
		writeError(w, provider.Chat, http.StatusServiceUnavailable, err.Error())
		return
	}
	if len(left) > 0 {
		w.Header().Set("X-Magpie-Skills-Left", strings.Join(left, ","))
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

// skillsTarFile is whether f, a file of a skill's folder, goes in the
// archive by that path: one below the folder, never out of it.
func skillsTarFile(f string) bool {
	return f != "" && !path.IsAbs(f) && path.Clean(f) == f && f != ".." && !strings.HasPrefix(f, "../") &&
		!strings.Contains(f, "\\") && path.Base(f) != ".magpie-pulled"
}

// skillsTar is ks's folders as a .tar.gz, and the skills left out for
// skillsArchiveMax. A skill listed that can't be read now is an error:
// left out, pull.sh would have it gone.
func skillsTar(h HubRemoteSkills, ks []RemoteSkill) ([]byte, []string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	when := time.Now().Truncate(time.Second)
	put := func(name string, mode int64, b []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(b)), ModTime: when, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		_, err := tw.Write(b)
		return err
	}
	var names, left []string
	total := 0
	for _, k := range ks {
		if k.Name == "" || strings.ContainsAny(k.Name, `/\`) || k.Name == "." || k.Name == ".." || strings.HasPrefix(k.Name, ".") {
			continue
		}
		_, fs, ok := h.RemoteSkill("", k.Name)
		if !ok {
			return nil, nil, fmt.Errorf("magpie couldn't read the skill %s; try again", k.Name)
		}
		names = append(names, k.Name)
		size := 0
		for _, b := range fs.Files {
			size += len(b)
		}
		if total+size > skillsArchiveMax {
			left = append(left, k.Name)
			continue
		}
		total += size
		if err := tw.WriteHeader(&tar.Header{Name: k.Name + "/", Mode: 0o755, ModTime: when, Typeflag: tar.TypeDir, Format: tar.FormatPAX}); err != nil {
			return nil, nil, err
		}
		for _, f := range slices.Sorted(maps.Keys(fs.Files)) {
			if !skillsTarFile(f) {
				continue
			}
			mode := int64(0o644)
			if slices.Contains(fs.Exec, f) {
				mode = 0o755
			}
			if err := put(k.Name+"/"+f, mode, fs.Files[f]); err != nil {
				return nil, nil, err
			}
		}
	}
	list := strings.Join(names, "\n")
	if list != "" {
		list += "\n"
	}
	if err := put(skillsListFile, 0o644, []byte(list)); err != nil {
		return nil, nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), left, nil
}

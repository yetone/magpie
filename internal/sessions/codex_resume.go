package sessions

import (
	"database/sql"
	"log"
	"net/url"
	"path/filepath"
	"strings"
)

// Codex 0.166 resumes a thread only on the provider it was started on:
// the provider its row in the state database names, else its rollout's
// session_meta. On any other, `codex resume <id>` stops at "This chat uses
// custom, but your current provider is magpie. Switch to custom to
// continue." (modelProviderMismatch, codex-rs app-server
// config_manager.rs load_for_resume), and nothing lets a resume go on
// another provider: the check compares the provider the thread was saved
// with against the one config.toml (with -c) selects. So a session started
// through CC Switch's "custom" couldn't be opened from Sessions once Codex
// was on magpie (mrten_95615 on Discord). Codex 0.165 and before resumed it
// on its own provider without asking.

// codexBuiltinProviders are the providers Codex knows without a table of
// config.toml (model-provider-info built_in_model_providers).
var codexBuiltinProviders = []string{"openai", "amazon-bedrock", "amazon-bedrock-runtime", "gcp", "ollama", "lmstudio"}

// codexSavedProvider is the provider Codex resumes a thread of a Codex
// folder on: its row's in the state databases, else its rollout's
// session_meta's, as Codex's thread/resume reads them; "" when neither
// names one.
func codexSavedProvider(home, id, path string) string {
	for _, p := range codexStateDBs(home) {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(p), RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}
		if len(u.Path) >= 2 && u.Path[1] == ':' {
			u.Path = "/" + u.Path
		}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			continue
		}
		var s sql.NullString
		err = db.QueryRow(`SELECT model_provider FROM threads WHERE id = ?`, id).Scan(&s)
		db.Close()
		if err == nil && s.String != "" {
			return s.String
		}
	}
	return codexMetaProvider(path)
}

// codexProviderKnown says whether the Codex of a folder can run on the
// provider p: a table of config.toml's [model_providers], or a built-in.
func codexProviderKnown(home, p string) bool {
	if contains(codexBuiltinProviders, p) {
		return true
	}
	_, ok := readCodexConfig(home).Providers[p]
	return ok
}

// ResumeNow is the line that resumes a session as its agent takes it now,
// made when the user opens it, not on each listing. For a Codex session
// started on a provider other than the one Codex is on now, that is
// `codex resume -c model_provider=<its provider> <id>` while config.toml
// still has that provider, so it goes on where it was made; when the
// provider is gone the thread is moved to the one Codex is on
// (MoveCodexProvider, its files kept in magpie's trash first) and resumed
// there, the only way Codex resumes it at all. Any other session, and a
// Codex one in a WSL distro, is its Resume as listed.
func ResumeNow(s Session) string {
	if s.Agent != "codex" || s.Resume == "" || s.WSL != "" || s.Path == "" || !safeID.MatchString(s.ID) {
		return s.Resume
	}
	home := codexHomeOf(s.Path)
	if home == "" {
		return s.Resume
	}
	now := CodexProviderIn(home)
	was := codexSavedProvider(home, s.ID, s.Path)
	if was == "" || was == now || !codexProviderRe.MatchString(was) {
		return s.Resume
	}
	if codexProviderKnown(home, was) {
		run := "codex resume " + s.ID
		if i := strings.LastIndex(s.Resume, run); i >= 0 {
			return s.Resume[:i] + "codex resume -c model_provider=" + was + " " + s.ID + s.Resume[i+len(run):]
		}
		return s.Resume
	}
	if _, err := MoveCodexProvider(s.ID, now); err != nil {
		log.Printf("sessions: resuming Codex thread %s, started on %s, which config.toml no longer has: moving it to %s: %v", s.ID, was, now, err)
	}
	return s.Resume
}

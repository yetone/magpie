package library

import (
	"fmt"
	"maps"
	"strconv"
	"strings"
)

// ServerOpts are the settings a server takes beside its command or URL,
// typed as key=value words on the command line (#6111, Abandon332 on X):
// header=K=V (or K: V), env=K=V, timeout=SECS, agents=a,b, --local. A word
// the line means as the child's own argument comes after "--".
type ServerOpts struct {
	Headers  map[string]string
	Env      map[string]string
	NoHeader []string // header-=K
	NoEnv    []string // env-=K
	Timeout  *int
	Agents   []string // the words of agents=, for the caller to resolve
	HasAgent bool
	Local    *bool
}

// optionNames are the words that, as key=value, are never a child's
// argument in a line that has no "--": they were meant as magpie's own
// setting, and passing them on to the program silently was #6111's footgun.
var optionNames = map[string]bool{
	"header": true, "headers": true, "env": true, "envs": true, "environment": true,
	"timeout": true, "timeouts": true, "startup-timeout": true, "startup_timeout": true,
	"agent": true, "agents": true, "local": true, "hub": true, "note": true,
	"transport": true, "url": true, "oauth": true, "name": true,
}

// SplitServerWords takes the options out of the words typed after a
// server's name and leaves its command or URL. Words after a "--" are the
// command's, however they look. A word that looks like a setting of
// magpie's but isn't one it knows is refused rather than handed to the
// program.
func SplitServerWords(words []string) (cmd []string, o ServerOpts, err error) {
	for i, w := range words {
		if w == "--" {
			cmd = append(cmd, words[i+1:]...)
			break
		}
		if w == "--local" {
			t := true
			o.Local = &t
			continue
		}
		k, v, ok := strings.Cut(w, "=")
		if !ok || strings.HasPrefix(k, "-") {
			cmd = append(cmd, w)
			continue
		}
		key := strings.ToLower(k)
		switch key {
		case "agents":
			o.Agents, o.HasAgent = append(o.Agents, v), true
		case "header", "env":
			var n, val string
			if key == "header" {
				n, val, err = headerPair(v)
			} else {
				n, val, err = envPair(v)
			}
			if err != nil {
				return nil, o, err
			}
			if key == "header" {
				if o.Headers == nil {
					o.Headers = map[string]string{}
				}
				o.Headers[n] = val
			} else {
				if o.Env == nil {
					o.Env = map[string]string{}
				}
				o.Env[n] = val
			}
		case "header-", "env-":
			if strings.TrimSpace(v) == "" {
				return nil, o, fmt.Errorf("%s= needs the name to take away", key)
			}
			if key == "header-" {
				o.NoHeader = append(o.NoHeader, strings.TrimSpace(v))
			} else {
				o.NoEnv = append(o.NoEnv, strings.TrimSpace(v))
			}
		case "timeout", "startup-timeout", "startup_timeout":
			n, e := strconv.Atoi(strings.TrimSpace(v))
			if e != nil || n < 0 || n > maxStartupTimeout {
				return nil, o, fmt.Errorf("%s= takes seconds from 0 (the agent's own) to %d, not %q", key, maxStartupTimeout, v)
			}
			o.Timeout = &n
		default:
			if optionNames[key] {
				return nil, o, fmt.Errorf("%s= isn't a setting magpie knows here: header=K=V, env=K=V, timeout=SECS, agents=a,b (put it after -- to hand it to the program)", k)
			}
			cmd = append(cmd, w)
		}
	}
	return cmd, o, nil
}

func headerPair(v string) (string, string, error) {
	i := strings.IndexAny(v, "=:")
	if i <= 0 || strings.TrimSpace(v[:i]) == "" {
		return "", "", fmt.Errorf("header=%s: a header is header=Name=value (or header=Name: value)", v)
	}
	return strings.TrimSpace(v[:i]), strings.TrimSpace(v[i+1:]), nil
}

func envPair(v string) (string, string, error) {
	k, val, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return "", "", fmt.Errorf("env=%s: a variable is env=NAME=value", v)
	}
	return strings.TrimSpace(k), val, nil
}

// Apply puts the options on s, which is a command or a URL server: a
// header on a command, or an env on a URL, is an error rather than
// dropped.
func (o ServerOpts) Apply(s *Server) error {
	if (len(o.Headers) > 0 || len(o.NoHeader) > 0) && !s.Remote() {
		return fmt.Errorf("%s: header= is for a server by URL; a command takes env=", s.Name)
	}
	if (len(o.Env) > 0 || len(o.NoEnv) > 0) && s.Remote() {
		return fmt.Errorf("%s: env= is for a command; a server by URL takes header=", s.Name)
	}
	if len(o.Headers) > 0 {
		h := maps.Clone(s.Headers)
		if h == nil {
			h = map[string]string{}
		}
		for k, v := range o.Headers {
			// the header is the same one whatever its case
			for e := range h {
				if strings.EqualFold(e, k) {
					k = e
				}
			}
			h[k] = v
		}
		s.Headers = h
	}
	for _, k := range o.NoHeader {
		for e := range s.Headers {
			if strings.EqualFold(e, k) {
				s.Headers = without(s.Headers, e)
			}
		}
	}
	if len(o.Env) > 0 {
		e := maps.Clone(s.Env)
		if e == nil {
			e = map[string]string{}
		}
		maps.Copy(e, o.Env)
		s.Env = e
	}
	for _, k := range o.NoEnv {
		s.Env = without(s.Env, k)
	}
	if o.Timeout != nil {
		s.StartupTimeout = *o.Timeout
	}
	if o.Local != nil {
		s.Local = *o.Local
	}
	return nil
}

func without(m map[string]string, k string) map[string]string {
	m = maps.Clone(m)
	delete(m, k)
	if len(m) == 0 {
		return nil
	}
	return m
}

// SetServer changes settings of a server already in the library, keeping
// the rest of it as it is.
func SetServer(name string, o ServerOpts) (*Result, error) {
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		n := *s
		if err := o.Apply(&n); err != nil {
			return err
		}
		if err := n.check(); err != nil {
			return err
		}
		*s = n
		return nil
	})
}

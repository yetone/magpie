package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/artifact"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
)

const artifactsUsage = `usage: magpie artifacts [--json]          the pages agents published (Magpie Artifacts), newest first
       magpie artifacts open <id>[@<n>]    open one in the browser: the newest version, or version n
       magpie artifacts delete <id>        delete one with every version, for good`

// artifactsCmd: `magpie artifacts` — Work › Artifacts in a terminal
// (docs/subsystems/artifacts.md): the list, opening one and deleting one.
func artifactsCmd(args []string) error {
	return artifactsTo(os.Stdout, args[1:], time.Now())
}

// openArtifact opens an address in the browser; tests stand in.
var openArtifact = proc.OpenBrowser

func artifactsTo(w io.Writer, args []string, now time.Time) error {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" || args[0] == "--json" {
		asJSON := false
		for _, a := range args {
			switch a {
			case "list", "ls":
			case "--json":
				asJSON = true
			default:
				return fmt.Errorf("unknown argument %q\n%s", a, artifactsUsage)
			}
		}
		return artifactsListTo(w, asJSON, now)
	}
	switch args[0] {
	case "open", "delete", "rm", "remove":
		if len(args) != 2 {
			return fmt.Errorf("%s takes one artifact\n%s", args[0], artifactsUsage)
		}
		id, n, ok := gateway.ArtifactRef(args[1])
		if !ok {
			return fmt.Errorf("%q isn't an artifact: give its id or address, as magpie artifacts lists it", args[1])
		}
		a, err := artifact.Get(id)
		if errors.Is(err, artifact.ErrNotFound) {
			return fmt.Errorf("no artifact %s", id)
		}
		if err != nil {
			return err
		}
		if args[0] == "open" {
			if a.Version(n) == nil {
				return fmt.Errorf("%s has no version %d: it has 1 to %d", a.Title, n, a.Latest().N)
			}
			url := gateway.ArtifactURL(id, n)
			if err := openArtifact(url); err != nil {
				return fmt.Errorf("couldn't open %s: %w", url, err)
			}
			fmt.Fprintln(w, url)
			return nil
		}
		if n != 0 {
			return fmt.Errorf("delete takes the whole artifact, every version: give %s without @%d", id, n)
		}
		if err := artifact.Delete(id); err != nil {
			return err
		}
		fmt.Fprintf(w, "✓ deleted %s, with its %s\n", a.Title, plural(len(a.Versions), "version"))
		return nil
	}
	return fmt.Errorf("unknown argument %q\n%s", args[0], artifactsUsage)
}

func artifactsListTo(w io.Writer, asJSON bool, now time.Time) error {
	list, err := artifact.List()
	if list == nil {
		return err
	}
	if err != nil {
		// one that can't be read is named, and the rest still listed
		fmt.Fprintln(os.Stderr, "!", err)
	}
	names := map[string]string{}
	for _, g := range agent.All() {
		names[g.ID] = g.Name
	}
	if asJSON {
		type ver struct {
			N     int       `json:"n"`
			At    time.Time `json:"at"`
			Label string    `json:"label,omitempty"`
			Agent string    `json:"agent,omitempty"`
			URL   string    `json:"url"`
		}
		type row struct {
			ID          string    `json:"id"`
			Title       string    `json:"title"`
			Description string    `json:"description,omitempty"`
			Updated     time.Time `json:"updated"`
			URL         string    `json:"url"`
			Versions    []ver     `json:"versions"` // newest first
		}
		out := make([]row, 0, len(list))
		for _, a := range list {
			r := row{ID: a.ID, Title: a.Title, Description: a.Description, Updated: a.Updated, URL: gateway.ArtifactURL(a.ID, 0)}
			for i := len(a.Versions) - 1; i >= 0; i-- {
				v := a.Versions[i]
				r.Versions = append(r.Versions, ver{N: v.N, At: v.At, Label: v.Label, Agent: v.Agent, URL: gateway.ArtifactURL(a.ID, v.N)})
			}
			out = append(out, r)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "Nothing published yet: an agent with Magpie Artifacts publishes a page when one says it better than text.")
		return nil
	}
	for _, a := range list {
		v := a.Latest()
		bits := []string{}
		if v != nil && v.Agent != "" {
			bits = append(bits, nameOf(names, v.Agent))
		}
		bits = append(bits, agoAt(a.Updated, now))
		if len(a.Versions) > 1 {
			bits = append(bits, plural(len(a.Versions), "version"))
		}
		fmt.Fprintf(w, "%s  %s\n  %s\n", a.Title, strings.Join(bits, " · "), gateway.ArtifactURL(a.ID, 0))
	}
	return nil
}

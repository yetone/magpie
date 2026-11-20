package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/library"
)

// projectExportCmd is magpie library project export <dir> [file] (#7278).
func projectExportCmd(rest []string, usage func() error) error {
	if len(rest) < 1 || len(rest) > 2 || strings.HasPrefix(rest[0], "-") {
		return usage()
	}
	dir, err := projectDir(rest[0])
	if err != nil {
		return err
	}
	e, err := library.ExportProject(dir)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(rest) == 1 || rest[1] == "-" {
		_, err = os.Stdout.Write(b)
		return err
	}
	if err := os.WriteFile(rest[1], b, 0o600); err != nil {
		return err
	}
	fmt.Println(green.Render("✓"), dir, "→", rest[1])
	return nil
}

// projectApplyCmd is magpie library project apply <file|-> <dir>.
func projectApplyCmd(rest []string, usage func() error) error {
	if len(rest) != 2 || rest[0] != "-" && strings.HasPrefix(rest[0], "-") || strings.HasPrefix(rest[1], "-") {
		return usage()
	}
	var (
		b   []byte
		err error
	)
	if rest[0] == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(rest[0])
	}
	if err != nil {
		return err
	}
	var e library.ProjectExport
	if err := json.Unmarshal(b, &e); err != nil {
		return fmt.Errorf("%s: %w", rest[0], err)
	}
	dir, err := library.ProjectPath(rest[1])
	if err != nil {
		return err
	}
	out, err := library.ApplyProject(dir, &e)
	if err != nil {
		return err
	}
	var problems []library.Problem
	for _, r := range out.Res {
		problems = append(problems, r.Problems...)
	}
	for _, s := range out.Saved {
		fmt.Println(green.Render("+"), s, muted.Render("saved in the library, for no agent of its own"))
	}
	if err := projectOutcome(dir, nil, &library.Result{Problems: problems}); err != nil {
		return err
	}
	for _, m := range out.Missing {
		fmt.Println(amber.Render("!"), m)
	}
	if len(out.Missing) > 0 {
		return fmt.Errorf("%d of the export's choices left out of %s", len(out.Missing), dir)
	}
	fmt.Println(green.Render("✓"), dir, "has what the export has")
	return nil
}

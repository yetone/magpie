package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const keysUsage = "magpie provider keys <id> [add [key… | -]]"

// keysCmd prints every key a provider holds, one a line and nothing else,
// or adds keys: given as words, or one a line on stdin (-, or none given).
// So one provider's keys go from one magpie to another (@emilochen on X:
// this computer's to a magpie in the cloud):
//
//	magpie provider keys relay | ssh cloud magpie provider keys relay add
//
// What is printed is the keys themselves, asked for by name; they go to
// stdout alone, never to a log.
func keysCmd(rest []string, in io.Reader, out io.Writer) error {
	if len(rest) < 1 || strings.HasPrefix(rest[0], "-") {
		return fmt.Errorf("%s", keysUsage)
	}
	if len(rest) == 1 {
		keys, err := provider.AllKeys(rest[0])
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Fprintln(out, k)
		}
		return nil
	}
	if rest[1] != "add" {
		return fmt.Errorf("%s", keysUsage)
	}
	words := rest[2:]
	var text string
	if len(words) == 0 || len(words) == 1 && words[0] == "-" {
		b, err := io.ReadAll(io.LimitReader(in, 8<<20))
		if err != nil {
			return err
		}
		text = string(b)
	} else {
		for _, w := range words {
			// a flag is never a key (--help)
			if strings.HasPrefix(w, "-") {
				return fmt.Errorf("%s", keysUsage)
			}
		}
		text = strings.Join(words, "\n")
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	added, had, err := provider.AddKeys(p.ID, provider.SplitKeys(text), "")
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("%d keys added to %s", added, p.Name)
	if had > 0 {
		msg += muted.Render(fmt.Sprintf(" · %d already there", had))
	}
	fmt.Fprintln(os.Stderr, green.Render("✓"), msg)
	return nil
}

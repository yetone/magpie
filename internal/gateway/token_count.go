package gateway

import (
	"encoding/json"
	"unicode"
)

// localTokenCount is a deterministic, offline estimate used while Cursor's
// agent is still running. Cursor supplies the authoritative cumulative usage
// only in its final result event.
func localTokenCount(s string) int {
	count := 0
	asciiRun := 0
	flushASCII := func() {
		if asciiRun > 0 {
			count += (asciiRun + 3) / 4
			asciiRun = 0
		}
	}
	for _, r := range s {
		switch {
		case r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r)):
			asciiRun++
		case unicode.Is(unicode.Han, r):
			flushASCII()
			count++
		default:
			flushASCII()
			count++
		}
	}
	flushASCII()
	return count
}

func localRequestTokenCount(req *Request) int {
	count := localTokenCount(req.System)
	for _, m := range req.Messages {
		count += localTokenCount(m.Role)
		for _, p := range m.Parts {
			count += localTokenCount(p.Text)
			count += localTokenCount(p.Name)
			count += localTokenCount(p.CallID)
			if len(p.Args) > 0 {
				count += localTokenCount(string(p.Args))
			}
		}
	}
	for _, t := range req.Tools {
		count += localTokenCount(t.Name)
		count += localTokenCount(t.Description)
		count += localTokenCount(string(t.Schema))
	}
	return count
}

func localToolArgsTokenCount(name string, args json.RawMessage) int {
	return localTokenCount(name) + localTokenCount(string(args))
}

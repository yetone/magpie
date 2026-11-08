package sessions

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
)

func dshUserTurn(seq int, text string) string {
	return `{"type":"user/message","seq":` + strconv.Itoa(seq) + `,"time":1000,"data":{"source":{"kind":"user"},"content":[{"type":"text","text":"` + text + `"}]}}`
}

func dshTurnStart(seq, turn int) string {
	return `{"type":"turn/start","seq":` + strconv.Itoa(seq) + `,"time":1000,"data":{"turn":` + strconv.Itoa(turn) + `}}`
}

// dshTurnID is the id a session keeps of a message of its turn: the one
// spelling the gateway writes a request's message in (dshLastConvs).
func dshTurnID(text string) string {
	return dshConvs(json.RawMessage(`[{"type":"text","text":"` + text + `"}]`))[0]
}

// A session's TurnConvs are the ids of its newest turn's user messages: a
// request still answering carries them and has no reply id yet.
func TestDshTurnConvsAreTheNewestTurns(t *testing.T) {
	head := `{"type":"session","version":4,"id":"session-one","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":false}`
	s := dshSessionFile(t, []string{
		head,
		dshTurnStart(1, 1),
		dshUserTurn(2, "first"),
		dshTurnStart(3, 2),
		dshUserTurn(4, "second"),
		dshUserTurn(5, "context"),
	})
	if want := []string{dshTurnID("second"), dshTurnID("context")}; !slices.Equal(s.TurnConvs, want) {
		t.Fatalf("newest turn's messages %v, want %v", s.TurnConvs, want)
	}
}

// The seeded turn is its source's: a fork's TurnConvs are its own turn's
// messages, which its source's file does not have.
func TestDshTurnConvsSkipTheSeed(t *testing.T) {
	head := `{"type":"session","version":4,"id":"session-child","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":true}`
	s := dshSessionFile(t, []string{
		head,
		dshTurnStart(1, 1),
		dshUserTurn(2, "seed"),
		`{"type":"session/end-seed","seq":3,"time":1000,"data":{"inherited":true}}`,
		dshTurnStart(4, 2),
		dshUserTurn(5, "own"),
	})
	if want := []string{dshTurnID("own")}; !slices.Equal(s.TurnConvs, want) {
		t.Fatalf("fork's newest turn %v, want %v", s.TurnConvs, want)
	}
}

// A file whose dsh writes no turn/start (version 3) keeps only its newest
// few messages, not one id for every message it was ever sent.
func TestDshTurnConvsKeepTheNewestFew(t *testing.T) {
	lines := []string{`{"type":"session","version":3,"id":"session-old","cwd":"/w","createdAt":1000}`}
	var want []string
	for i := range 10 {
		text := "m" + strconv.Itoa(i)
		lines = append(lines, dshUserTurn(i+1, text))
		want = append(want, dshTurnID(text))
	}
	s := dshSessionFile(t, lines)
	if want = want[len(want)-dshTurnKept:]; !slices.Equal(s.TurnConvs, want) {
		t.Fatalf("turn messages %v, want the newest %d %v", s.TurnConvs, dshTurnKept, want)
	}
}

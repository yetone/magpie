package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// The request index's rows hold what the catalog said of each call: its
// price, and whether the reply's model is the one the provider lists under
// the name asked (ListedAs). Its meta named the catalog by the bytes of
// models.json, so every change models.dev made, most of them to aggregators'
// lists no call of the user's names, read every session file again: ~15 s
// of CPU on a long history, several times a day.
//
// So the index is named by catalogBase instead: the catalog it was built
// under, kept while every question its rows asked of the catalog is
// answered the same by the one on disk now. Each question and its answer
// is noted as it is first asked (noteAsk); a new catalog asks them all
// again, and one answered otherwise names the index anew, which builds it
// again, as any change did before.

// catalogAsks is the questions asked under base, and their answers.
type catalogAsks struct {
	// Base names the catalog the index is built under; Checked, the one
	// the asks were last answered the same by.
	Base, Checked string
	Asks          map[string]string
}

var asks struct {
	sync.Mutex
	path  string
	state catalogAsks
	// dirty: asks noted since the file was written; suspect: a question
	// answered otherwise than first, or one past asksMax not kept
	dirty, suspect bool
	// resets is catalog.Resets when the asks were last checked: the file
	// is written before the catalog is read again (Sync), so a check
	// between the two asked the old one
	resets int64
}

// checking holds one check of the asks at a time.
var checking sync.Mutex

// asksMax bounds the questions kept: past it the index is named anew
// with the next catalog, as before, rather than every one asked again.
const asksMax = 50000

func catalogAsksPath() string {
	return filepath.Join(filepath.Dir(sessions.CachePath()), "catalog-asks-v1.json")
}

// loadAsks reads the asks of this home's file, once per home; asks is
// held. A file that can't be read or parsed is no asks: the index is named
// anew (the current catalog), so its rows are all asked again.
func loadAsks() {
	p := catalogAsksPath()
	if asks.path == p {
		return
	}
	asks.path, asks.state, asks.dirty, asks.suspect = p, catalogAsks{}, false, false
	asks.resets = catalog.Resets() // this process reads the file as it is now
	if b, err := os.ReadFile(p); err == nil {
		var st catalogAsks
		if json.Unmarshal(b, &st) == nil && st.Base != "" && st.Checked != "" {
			asks.state = st
		}
	}
	if asks.state.Asks == nil {
		asks.state.Asks = map[string]string{}
	}
}

// noteAsk keeps a question asked of the catalog with its answer, the
// first time it is asked. The rows made of the answer hold it, so it is
// the answer a later catalog is held to, whichever catalog gave it. A
// question answered otherwise later means rows of both answers may be
// held: the next check names the index anew.
func noteAsk(q, answer string) {
	asks.Lock()
	defer asks.Unlock()
	loadAsks()
	if was, ok := asks.state.Asks[q]; ok {
		if was != answer {
			asks.suspect = true
		}
		return
	}
	if len(asks.state.Asks) >= asksMax {
		asks.suspect = true
		return
	}
	asks.state.Asks[q] = answer
	asks.dirty = true
}

// catalogBase names the catalog the request index's rows were made under.
// It is the catalog on disk while the asks are unchanged by it.
func catalogBase() string {
	checking.Lock()
	defer checking.Unlock()
	rs := catalog.Resets()
	cur := catalog.CacheKey()
	asks.Lock()
	loadAsks()
	st := asks.state
	if st.Base == "" {
		// no asks on record: the rows made now are named by this catalog
		asks.state = catalogAsks{Base: cur, Checked: cur, Asks: map[string]string{}}
		asks.dirty, asks.suspect, asks.resets = true, false, rs
		asks.Unlock()
		saveAsks()
		return cur
	}
	if cur == st.Checked && rs == asks.resets && !asks.suspect {
		asks.Unlock()
		return st.Base
	}
	suspect := asks.suspect
	qs := make(map[string]string, len(st.Asks))
	for q, a := range st.Asks {
		qs[q] = a
	}
	asks.suspect = false
	asks.Unlock()

	same := !suspect && answersSame(qs)
	asks.Lock()
	if same {
		asks.state.Checked = cur
	} else {
		// asking again noted the new answers as differing too: a new
		// base holds none of the old rows
		asks.state = catalogAsks{Base: cur, Checked: cur, Asks: map[string]string{}}
		asks.suspect = false
	}
	asks.dirty, asks.resets = true, rs
	base := asks.state.Base
	asks.Unlock()
	saveAsks()
	return base
}

// answersSame asks qs again of the catalog now, and reports whether each
// is answered as it was.
func answersSame(qs map[string]string) bool {
	forgetListed() // a provider's list as the catalog has it now
	s := settings.Load()
	wires := s.Wires()
	for q, was := range qs {
		now, ok := answerAsk(s, wires, q)
		if !ok || now != was {
			return false
		}
	}
	return true
}

// saveAsks writes the asks when some were noted since they were last
// written. A cache written of rows names the asks they were made of, so
// it is called before one is (toDisk and the rest): a file of rows whose
// asks were lost would keep their answers through a catalog that changes
// them.
func saveAsks() {
	asks.Lock()
	if !asks.dirty || asks.path == "" {
		asks.Unlock()
		return
	}
	b, err := json.Marshal(asks.state)
	p := asks.path
	asks.dirty = false
	asks.Unlock()
	if err != nil {
		return
	}
	if err := writeAsks(p, b); err != nil {
		asks.Lock()
		asks.dirty = true
		asks.Unlock()
	}
}

func writeAsks(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(p), ".catalog-asks-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	_, err = out.Write(b)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(out.Name(), p)
}

// The questions: a price (p) and a listed name (l), each its fields
// joined by a byte no name holds.
const askSep = "\x00"

func (a priceAsk) ask() string {
	u := "0"
	if a.Unknown {
		u = "1"
	}
	return strings.Join([]string{"p", a.ID, a.Model, a.Served, a.SessionProvider, a.Effort, a.Key, u}, askSep)
}

func listedAsk(prov, model, served string) string {
	return strings.Join([]string{"l", prov, model, served}, askSep)
}

// priceAnswer is a base price and its windows as an answer.
func priceAnswer(pr *catalog.Price, ws []provider.RateWindow) string {
	b, _ := json.Marshal(struct {
		P *catalog.Price        `json:"p"`
		W []provider.RateWindow `json:"w,omitempty"`
	}{pr, ws})
	return string(b)
}

func boolAnswer(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// answerAsk answers q of the catalog now; false for a question it doesn't
// know, which counts as answered otherwise.
func answerAsk(s settings.Settings, wires map[string]string, q string) (string, bool) {
	f := strings.Split(q, askSep)
	switch {
	case f[0] == "p" && len(f) == 8:
		a := priceAsk{ID: f[1], Model: f[2], Served: f[3], SessionProvider: f[4], Effort: f[5], Key: f[6], Unknown: f[7] == "1"}
		pr, ws := basePriceIn(s, wires, a)
		return priceAnswer(pr, ws), true
	case f[0] == "l" && len(f) == 4:
		return boolAnswer(listedAsNow(f[1], f[2], f[3])), true
	}
	return "", false
}

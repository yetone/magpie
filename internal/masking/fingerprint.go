package masking

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/yetone/magpie/internal/pii"
	"github.com/yetone/magpie/internal/settings"
)

// Fingerprint names what st has a request masked by: every setting that
// changes what is masked (the kinds, the user's words and rules, what each
// kind does, what is let through, the local model and its threshold), as
// a hash, so two magpies can tell they mask alike without either telling
// the other its words. "" when st masks nothing.
//
// A magpie that passes a request on to another (a Remote magpie) after
// masking it says so with this; the other masks it again only when its
// own fingerprint differs: masked twice by the same settings, the second
// pass finds nothing the first left, and costs the request another run of
// the rules and of the local model (YeYaka88 on X). It starts no download.
func Fingerprint(st settings.Settings) string {
	if !Wanted(st) {
		return ""
	}
	type modelPart struct {
		ID         string  `json:"id"`
		Score      float64 `json:"score"`
		Generation string  `json:"generation,omitempty"`
	}
	f := struct {
		Secrets  bool              `json:"secrets"`
		Personal bool              `json:"personal"`
		Kinds    map[string]bool   `json:"kinds,omitempty"`
		Words    []string          `json:"words,omitempty"`
		AnyCase  bool              `json:"anyCase,omitempty"`
		Whole    bool              `json:"whole,omitempty"`
		Rules    any               `json:"rules,omitempty"`
		Actions  map[string]string `json:"actions,omitempty"`
		Allow    []string          `json:"allow,omitempty"`
		Shape    bool              `json:"shape,omitempty"`
		Model    *modelPart        `json:"model,omitempty"`
	}{
		Secrets: st.Redact, Personal: st.RedactPersonal, Kinds: st.RedactKinds, Words: st.RedactWords,
		AnyCase: st.RedactWordsAnyCase, Whole: st.RedactWordsWhole, Rules: st.RedactRules,
		Actions: st.RedactActions, Allow: st.RedactAllow, Shape: st.RedactShape,
	}
	if (st.Redact || st.RedactPersonal) && ModelWanted(st) {
		// the model optionsCtx asks, without picked's download: one not
		// on disk masks nothing yet, so it isn't part of it
		if m := chosen(st); m.Installed() {
			f.Model = &modelPart{ID: m.ID(), Score: pii.Threshold(st.RedactModelScore, m), Generation: m.Generation()}
		}
	}
	b, err := json.Marshal(f)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// chosen is the model optionsCtx asks under st, without picked's download;
// a var for tests.
var chosen = func(st settings.Settings) pii.Model { return pii.Of(pii.Choose(st.RedactModelName, true)) }

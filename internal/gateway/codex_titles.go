package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// codexTitlesTo is where a request Codex makes for a thread's title goes
// (#705): a hidden turn of its own (thread_title,
// thread_title_reconsideration), which Codex sends on a model it chooses
// itself — gpt-5.6-luna when signed in to ChatGPT, its provider list's
// default or first model when magpie is its provider. "" for a request
// that isn't one, or one left to go as Codex sent it; "off", or the model
// that writes the title: settings.CodexTitles, else the conversation's
// model (codexSideTo). Codex reads a title only as the {"title": …} its
// text.format asks for, which a model behind the Chat or Anthropic API is never shown, so
// a title on one of magpie's models goes through codexTitle whoever picked
// it (#743: Codex as magpie's provider had none at all, the model's plain
// answer turned down).
func codexTitlesTo(h http.Header, body []byte, ours bool, session func() string) string {
	if !isTitleKind(requestCallKind(h, requestSessionMetadata(h, body))) {
		return ""
	}
	if to := settings.Shared().CodexTitles; to != "" {
		return to
	}
	return codexSideTo(body, ours, session)
}

// CodexSideModel is the model this machine's Codex is set to when it is
// one of magpie's, picked beside Codex's ChatGPT sign-in, "" otherwise
// (agent.CodexSideModel).
var CodexSideModel func() string

// codexSideTo is the model that answers one of the Codex app's hidden
// side turns (a thread's title, description or one-line summary) when no
// setting names one: the model Codex asked for when it is one of magpie's
// (ours: the request came to magpie's own API, where every model is), "" to
// leave it as Codex sent it. The app asks for these on a model of its own
// (gpt-6-luna) whatever the conversation is on, so with one of magpie's
// models picked beside the ChatGPT sign-in every one of them went to
// ChatGPT and spent its allowance, and failed with it once it was used up
// (atom_Ray on X: an hour on a third-party model took 2% of the 5-hour
// window). Those go on the session's model (session: Server.codexSideSession),
// as the conversation's turns do; on Codex's own, as Codex sent them.
//
// With magpie as Codex's provider the model asked for is not the user's
// pick either: the app asks for a new conversation's title on
// ephemeralGenerationModel, and when the provider's list lacks it, on the
// list's default model or else its first (the app's oX), never on the
// model picked for the conversation (tenacious_eagle_96548 on Discord:
// the conversation on A/dsf4.1, its title on B/qwen3.7). Those go on the
// session's model too when magpie knows it, else on the model asked for.
func codexSideTo(body []byte, ours bool, session func() string) string {
	m := modelOf(body)
	if ours {
		if session != nil {
			if to := session(); to != "" {
				return to
			}
		}
		return m
	}
	if strings.Contains(m, "/") {
		return m
	}
	if session != nil {
		return session()
	}
	return ""
}

// codexDescriptionsTo is where a request Codex makes for a thread's
// description goes. "" leaves the request as Codex sent it; "off" answers
// it with no description, sent nowhere; a model's id writes it. A description is not a title: Codex asks for {"description"}
// alone, which the title wrapper cannot answer.
func codexDescriptionsTo(h http.Header, body []byte, ours bool, session func() string) string {
	if !isDescriptionKind(requestCallKind(h, requestSessionMetadata(h, body))) {
		return ""
	}
	if to := settings.Shared().CodexDescriptions; to != "" {
		return to
	}
	// Codex on a magpie model gets no schema upstream (#743), so an empty
	// setting still wraps the model it asked for, as titles do.
	return codexSideTo(body, ours, session)
}

// codexSummariesTo is where the Codex app's request for a thread's
// one-line activity summary goes (thread_summary, asked after each message
// of a thread): the model codexSideTo names, its answer handed back as the
// {"summary": …} the request's schema asks for; "" leaves it as Codex sent
// it.
func codexSummariesTo(h http.Header, body []byte, ours bool, session func() string) string {
	if !isSummaryKind(requestCallKind(h, requestSessionMetadata(h, body))) {
		return ""
	}
	return codexSideTo(body, ours, session)
}

// codexCommitsTo is where the Codex app's request to write a commit
// message or a pull request's text goes (makenxis on X): an ephemeral turn
// on a model it picks as it does for a title, with the text in a schema
// of its own ({"message"}, {"title","body"}). A model behind the Chat or
// Anthropic API never sees that schema and answers in plain text, which
// the app turns down, so on one of magpie's models the answer is handed
// back in the schema's shape (codexCommit). "" leaves the request as Codex
// sent it.
func codexCommitsTo(h http.Header, body []byte, ours bool, session func() string) string {
	if !isCommitKind(requestCallKind(h, requestSessionMetadata(h, body))) {
		return ""
	}
	return codexSideTo(body, ours, session)
}

// codexCommit answers a commit message request as codexCommitsTo says, by
// that model, its text handed back as the JSON the request's schema asks
// for.
func (s *Server) codexCommit(w http.ResponseWriter, r *http.Request, body []byte, to string) {
	shape := titleShapeOf(body)
	s.codexShaped(w, r, body, to, "commit message", false, func(said string) string { return commitJSON(said, shape) }, noCommit)
}

func noCommit(said string) string {
	said = strings.Join(strings.Fields(said), " ")
	if said == "" {
		return "commit message: the model answered with no text, so Codex got no message"
	}
	if r := []rune(said); len(r) > 80 {
		said = string(r[:80]) + "…"
	}
	return "commit message: no usable text in the model's answer, so Codex got none: " + said
}

// commitJSON is a model's answer to a commit message or pull request
// request as Codex reads one: the JSON object its schema asks for, the
// model's own when it gave every required string, else made from its text.
// message is the whole text, its subject (first line) cut to 72 characters,
// as the app's schema requires; title is the first line (a leading "#" or
// "Title:" dropped) and body the rest, or the whole text when that is
// short. "" when the answer holds no text.
func commitJSON(said string, shape titleShape) string {
	t := strings.TrimSpace(said)
	t = afterThought(t)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		if i := strings.IndexByte(t, '\n'); i >= 0 && !strings.Contains(t[:i], "{") {
			t = t[i+1:]
		}
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	fields := map[string]any{}
	if strings.HasPrefix(t, "{") {
		if json.NewDecoder(bytes.NewReader([]byte(t))).Decode(&fields) != nil {
			return ""
		}
	}
	names := shape.Required
	if len(names) == 0 {
		names = []string{"message"}
	}
	given := func(name string) string {
		v, _ := fields[name].(string)
		return strings.TrimSpace(v)
	}
	if len(fields) == 0 {
		t = strings.TrimSpace(strings.Trim(t, "`"))
		if low := strings.ToLower(t); strings.HasPrefix(low, "message:") {
			t = strings.TrimSpace(t[len("message:"):])
		}
	}
	whole := t
	if len(fields) > 0 {
		whole = given("message")
	}
	var title, body string
	if len(fields) > 0 {
		title, body = given("title"), given("body")
	} else {
		first, rest, _ := strings.Cut(t, "\n")
		title = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(first), "# "))
		if low := strings.ToLower(title); strings.HasPrefix(low, "title:") {
			title = strings.TrimSpace(title[len("title:"):])
		}
		title = strings.Trim(title, "\"'`*")
		body = strings.TrimSpace(rest)
		if low := strings.ToLower(body); strings.HasPrefix(low, "body:") {
			body = strings.TrimSpace(body[len("body:"):])
		}
	}
	clip := func(name, v string) string {
		if n := shape.Properties[name].MaxLength; n > 0 {
			if r := []rune(v); len(r) > n {
				v = strings.TrimSpace(string(r[:n]))
			}
		}
		return v
	}
	out := map[string]string{}
	for _, name := range names {
		if ty := shape.Properties[name].Type; ty != "" && ty != "string" {
			continue
		}
		var v string
		switch name {
		case "message":
			v = whole
			if v == "" {
				v = title
			}
			// the subject is the first line, 72 characters at most
			first, rest, more := strings.Cut(v, "\n")
			if r := []rune(strings.TrimSpace(first)); len(r) > 72 {
				first = strings.TrimSpace(string(r[:72]))
			}
			v = strings.TrimSpace(first)
			if more && strings.TrimSpace(rest) != "" {
				v += "\n\n" + strings.TrimSpace(rest)
			}
		case "title":
			v = title
		case "body":
			v = body
			if v == "" {
				v = title
			}
		default:
			v = given(name)
			if v == "" {
				v = title
			}
		}
		if v == "" {
			return ""
		}
		out[name] = clip(name, v)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// codexPredictionsTo is where the Codex app's request to predict the
// user's next message goes (#1970, pluo): a hidden turn on a fork of the
// whole conversation (composer_predictions), which the app sends on the
// thread's own model, so a long thread on an expensive model pays for
// every prediction. "" leaves the request as Codex sent it; "off", or a
// model's id that writes it: settings.CodexPredictions, else the model
// Codex asked for when it is one of magpie's, else the session's
// (codexSideTo), whose plain answer is wrapped as titles' and
// descriptions' are.
func codexPredictionsTo(h http.Header, body []byte, ours bool, session func() string) string {
	if !isPredictionKind(requestCallKind(h, requestSessionMetadata(h, body))) {
		return ""
	}
	if to := settings.Shared().CodexPredictions; to != "" {
		return to
	}
	// the app asks on the thread's model, but one of Codex's own when the
	// thread is new to it: the session's, as titles' (codexSideTo)
	return codexSideTo(body, ours, session)
}

// titleCheckKey holds, in a request's context, what serve asks of the
// reply it relayed before it records the call: the reason it fails its
// caller although the vendor answered, or "".
type titleCheckKey struct{}
type titleShapeKey struct{}

// replyTitleShape is the exact schema used by the title wrapper, or nil for a
// native reply. Inference must fingerprint the title the caller actually gets.
func replyTitleShape(ctx context.Context) *titleShape {
	shape, _ := ctx.Value(titleShapeKey{}).(*titleShape)
	return shape
}

// replyCheck is the check a request's context holds for serve, if any.
func replyCheck(ctx context.Context) func(reply string) string {
	f, _ := ctx.Value(titleCheckKey{}).(func(string) string)
	return f
}

// codexTitle answers a request for a thread's title as codexTitlesTo
// says: "off" here, with no title; a model's id by that model, its answer
// handed back as the {"title": …} Codex asked for. Either way the call is
// in the Usage and Routing views as the title request it is, and an answer
// with no title in it as the failure it is to Codex, which drops it
// without a word.
func (s *Server) codexTitle(w http.ResponseWriter, r *http.Request, body []byte, to string) {
	if to == "off" {
		s.codexTitleOff(w, r, body)
		return
	}
	shape := titleShapeOf(body)
	body, _ = codexInput(body, true)
	rec := &recorder{header: http.Header{}, status: 200}
	check := func(reply string) string {
		if res, err := compactReply([]byte(reply)); err == nil && titleJSON(messageText(res), shape) == "" {
			return noTitle(messageText(res))
		}
		return ""
	}
	ctx := context.WithValue(r.Context(), titleCheckKey{}, check)
	ctx = context.WithValue(ctx, titleShapeKey{}, &shape)
	if to == settings.Shared().CodexTitles {
		// the model picked for titles in magpie's settings, not by the
		// caller's key (#882)
		ctx = magpieChose(ctx)
	}
	s.serve(rec, r.WithContext(ctx), provider.Responses, codexSideEffort(withModel(body, to), to))
	if rec.status >= 400 {
		for k, vs := range rec.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.status)
		w.Write(rec.body.Bytes())
		return
	}
	res, err := compactReply(rec.body.Bytes())
	if err != nil {
		writeError(w, provider.Responses, 502, "title: "+err.Error())
		return
	}
	id := res.ID
	if id == "" {
		id = fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano())
	}
	var out []any
	if t := titleJSON(messageText(res), shape); t != "" {
		out = append(out, map[string]any{"type": "message", "id": "msg_" + strings.TrimPrefix(id, "resp_"), "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": t, "annotations": []any{}}}})
	} else if res.Status == "incomplete" {
		// cut short before any title (max_output_tokens): Codex is told so,
		// as the vendor told magpie, not that the model finished with
		// nothing to say (#1466). Codex takes it as a stream error and asks
		// again, as it does when its own backend answers so.
		writeTitleEnd(w, id, nil, res.Usage, "incomplete", res.IncompleteDetails)
		return
	}
	writeTitleReply(w, id, out, res.Usage)
}

// codexDescription answers a thread_description request as
// codexDescriptionsTo says: "off" here, with no description, as titles'
// Off; a model's id by that model. The model's text comes back as the
// {"description": ...} its schema asks for; a reply without one fails as a
// description, not as a title.
func (s *Server) codexDescription(w http.ResponseWriter, r *http.Request, body []byte, to string) {
	if to == "off" {
		// a finished reply with no message: the app's thread_description
		// helper reads no {"description"} in it as none (an "empty" result,
		// not an error) and leaves the thread's description as it was
		s.sentNowhere(w, r, body, "Descriptions off", "Codex descriptions are off in Codex's settings on magpie's Agents page: answered with no description, sent nowhere", nil)
		return
	}
	s.codexShaped(w, r, body, to, "description", true, descriptionJSON, noDescription)
}

// codexSummary answers a thread_summary request as codexSummariesTo says,
// by that model, its text handed back as the {"summary": …} the request's
// schema asks for (a compactSummary beside it when the schema asks for
// one too).
func (s *Server) codexSummary(w http.ResponseWriter, r *http.Request, body []byte, to string) {
	shape := titleShapeOf(body)
	s.codexShaped(w, r, body, to, "summary", false, func(said string) string { return summaryJSON(said, shape) }, noSummary)
}

// codexShaped answers one of Codex's hidden side turns by the model to,
// its answer handed back as shaped makes it the JSON the turn's schema
// asks for, which a model behind another API never sees; an answer
// shaped finds nothing in fails as missing says. chose: magpie picked the
// model in its settings rather than the caller (magpieChose).
func (s *Server) codexShaped(w http.ResponseWriter, r *http.Request, body []byte, to, what string, chose bool, shaped func(said string) string, missing func(said string) string) {
	body, _ = codexInput(body, true)
	rec := &recorder{header: http.Header{}, status: 200}
	check := func(reply string) string {
		if res, err := compactReply([]byte(reply)); err == nil && shaped(messageText(res)) == "" {
			return missing(messageText(res))
		}
		return ""
	}
	ctx := context.WithValue(r.Context(), titleCheckKey{}, check)
	if chose {
		ctx = magpieChose(ctx)
	}
	s.serve(rec, r.WithContext(ctx), provider.Responses, codexSideEffort(withModel(body, to), to))
	if rec.status >= 400 {
		for k, vs := range rec.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.status)
		w.Write(rec.body.Bytes())
		return
	}
	res, err := compactReply(rec.body.Bytes())
	if err != nil {
		writeError(w, provider.Responses, 502, what+": "+err.Error())
		return
	}
	id := res.ID
	if id == "" {
		id = fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano())
	}
	var out []any
	if d := shaped(messageText(res)); d != "" {
		out = append(out, map[string]any{"type": "message", "id": "msg_" + strings.TrimPrefix(id, "resp_"), "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": d, "annotations": []any{}}}})
	}
	writeTitleReply(w, id, out, res.Usage)
}

// codexPrediction answers a composer_predictions request as
// codexPredictionsTo says: "off" here, with no prediction and sent
// nowhere; a model's id by that model. Codex reads the reply only as the
// {"suggestion": string|null} its prompt and schema ask for, and takes a
// reply without one as a failed request, so an answer with no
// suggestion in it goes back as {"suggestion": null}, no prediction, and
// is in the Usage and Routing views as the failure it is.
func (s *Server) codexPrediction(w http.ResponseWriter, r *http.Request, body []byte, to string) {
	if to == "off" {
		s.sentNowhere(w, r, body, "Predictions off",
			"Codex composer predictions are off in Codex's settings on magpie's Agents page: answered with no prediction, sent nowhere", []any{predictionItem(fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano()), noSuggestion)})
		return
	}
	body, _ = codexInput(body, true)
	rec := &recorder{header: http.Header{}, status: 200}
	check := func(reply string) string {
		if res, err := compactReply([]byte(reply)); err == nil {
			if _, ok := predictionJSON(messageText(res)); !ok {
				return noPrediction(messageText(res))
			}
		}
		return ""
	}
	ctx := context.WithValue(r.Context(), titleCheckKey{}, check)
	if to == settings.Shared().CodexPredictions {
		ctx = magpieChose(ctx)
	}
	s.serve(rec, r.WithContext(ctx), provider.Responses, codexSideEffort(withModel(body, to), to))
	if rec.status >= 400 {
		for k, vs := range rec.header {
			w.Header()[k] = vs
		}
		w.WriteHeader(rec.status)
		w.Write(rec.body.Bytes())
		return
	}
	res, err := compactReply(rec.body.Bytes())
	if err != nil {
		writeError(w, provider.Responses, 502, "prediction: "+err.Error())
		return
	}
	id := res.ID
	if id == "" {
		id = fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano())
	}
	p, _ := predictionJSON(messageText(res))
	writeTitleReply(w, id, []any{predictionItem(id, p)}, res.Usage)
}

// noSuggestion is the reply that predicts nothing, which Codex shows as
// no prediction.
const noSuggestion = `{"suggestion":null}`

func predictionItem(id, text string) map[string]any {
	return map[string]any{"type": "message", "id": "msg_" + strings.TrimPrefix(id, "resp_"), "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func noPrediction(said string) string {
	said = strings.Join(strings.Fields(said), " ")
	if said == "" {
		return "prediction: the model answered with no text, so Codex got no prediction"
	}
	if r := []rune(said); len(r) > 80 {
		said = string(r[:80]) + "…"
	}
	return "prediction: no {\"suggestion\"} in the model's answer, so Codex got none: " + said
}

// predictionJSON is the model's answer as the {"suggestion": …} Codex
// asks for, and whether the answer was one: a JSON object with a
// suggestion, a string or null, its reasoning or a code fence around it
// left out. Plain text isn't taken as the suggestion, as it is for a
// description: a model that talks instead of answering would have its
// words put in the user's composer as their own next message. An empty
// or unusable answer is no prediction.
func predictionJSON(said string) (string, bool) {
	t := strings.TrimSpace(said)
	t = afterThought(t)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		if i := strings.IndexByte(t, '\n'); i >= 0 && !strings.Contains(t[:i], "{") {
			t = t[i+1:]
		}
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	var fields map[string]json.RawMessage
	if !strings.HasPrefix(t, "{") || json.NewDecoder(strings.NewReader(t)).Decode(&fields) != nil {
		return noSuggestion, false
	}
	raw, ok := fields["suggestion"]
	if !ok {
		return noSuggestion, false
	}
	var v *string
	if json.Unmarshal(raw, &v) != nil {
		return noSuggestion, false
	}
	if v == nil || strings.TrimSpace(*v) == "" {
		return noSuggestion, true
	}
	b, _ := json.Marshal(map[string]string{"suggestion": strings.TrimSpace(*v)})
	return string(b), true
}

func noSummary(said string) string {
	said = strings.Join(strings.Fields(said), " ")
	if said == "" {
		return "summary: the model answered with no text, so Codex got no summary"
	}
	if r := []rune(said); len(r) > 80 {
		said = string(r[:80]) + "…"
	}
	return "summary: no summary in the model's answer, so Codex got none: " + said
}

// summaryJSON is a model's answer to a thread_summary request as Codex
// reads one: {"summary": "…"} on one line, with each other string its
// shape requires (compactSummary) the model's own when its JSON gave one,
// else the summary, cut to the schema's length. "" when it gave none.
func summaryJSON(said string, shape titleShape) string {
	t := strings.TrimSpace(said)
	t = afterThought(t)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		if i := strings.IndexByte(t, '\n'); i >= 0 && !strings.Contains(t[:i], "{") {
			t = t[i+1:]
		}
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	fields := map[string]any{}
	if strings.HasPrefix(t, "{") {
		if json.NewDecoder(bytes.NewReader([]byte(t))).Decode(&fields) == nil {
			if v, ok := fields["summary"].(string); ok {
				t = v
			}
		}
	}
	t = strings.Join(strings.Fields(t), " ")
	if low := strings.ToLower(t); strings.HasPrefix(low, "summary:") {
		t = t[len("summary:"):]
	}
	t = strings.TrimSpace(strings.Trim(strings.TrimSpace(t), "\"'`*“”‘’"))
	if t == "" || strings.HasPrefix(t, "{") {
		return ""
	}
	clip := func(name, v string) string {
		if n := shape.Properties[name].MaxLength; n > 0 {
			if r := []rune(v); len(r) > n {
				v = strings.TrimSpace(string(r[:n]))
			}
		}
		return v
	}
	out := map[string]string{"summary": clip("summary", t)}
	for _, name := range shape.Required {
		if _, done := out[name]; done {
			continue
		}
		if ty := shape.Properties[name].Type; ty != "" && ty != "string" {
			continue
		}
		v, _ := fields[name].(string)
		if v = strings.Join(strings.Fields(v), " "); v == "" {
			v = t
		}
		out[name] = clip(name, v)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func noDescription(said string) string {
	said = strings.Join(strings.Fields(said), " ")
	if said == "" {
		return "description: the model answered with no text, so Codex got no description"
	}
	if r := []rune(said); len(r) > 80 {
		said = string(r[:80]) + "…"
	}
	return "description: no description in the model's answer, so Codex got none: " + said
}

// descriptionJSON returns the model's text as the nonempty description
// object Codex asks for. An unusable answer returns an empty string.
func descriptionJSON(said string) string {
	t := strings.TrimSpace(said)
	t = afterThought(t)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		if i := strings.IndexByte(t, '\n'); i >= 0 && !strings.Contains(t[:i], "{") {
			t = t[i+1:]
		}
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	if strings.HasPrefix(t, "{") {
		fields := map[string]any{}
		if json.NewDecoder(bytes.NewReader([]byte(t))).Decode(&fields) == nil {
			if v, ok := fields["description"].(string); ok {
				t = v
			}
		}
	}
	t = strings.TrimSpace(strings.Trim(strings.TrimSpace(t), "\"`'"))
	if t == "" || strings.HasPrefix(t, "{") {
		return ""
	}
	b, _ := json.Marshal(map[string]string{"description": t})
	return string(b)
}

// messageText is the text of a reply's messages, its reasoning left out.
func messageText(res compactResult) string {
	var said strings.Builder
	for _, o := range res.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			said.WriteString(c.Text)
		}
	}
	return said.String()
}

// noTitle is why an answer to a title request gave Codex no title: what
// the Routing and Usage views say of it, with the start of what the model
// said.
func noTitle(said string) string {
	said = strings.Join(strings.Fields(said), " ")
	if said == "" {
		return "title: the model answered with no text, so Codex got no title"
	}
	if r := []rune(said); len(r) > 80 {
		said = string(r[:80]) + "…"
	}
	return "title: no title in the model's answer, so Codex got none: " + said
}

// codexTitleOff answers a title request with no title, sending it nowhere:
// a finished reply with nothing in it, which Codex takes as no title and
// leaves the thread named as it was (its thread_title.rs: no message, or
// no {"title": …} in it, sets none). The Routing and Usage views have it
// as the title request it was, answered by magpie and sent to no one.
func (s *Server) codexTitleOff(w http.ResponseWriter, r *http.Request, body []byte) {
	s.sentNowhere(w, r, body, "Titles off", "Codex titles are off in Codex's settings on magpie's Agents page: answered with no title, sent nowhere", nil)
}

// sentNowhere answers a hidden turn of Codex's that a setting turned off
// with these output items, sending it nowhere: who and why are what the
// Routing and Usage views say of it, the request kept there as what it
// was, answered by magpie.
func (s *Server) sentNowhere(w http.ResponseWriter, r *http.Request, body []byte, who, why string, out []any) {
	s.answeredHere(r, provider.Responses, body, who, why, func() {
		writeTitleReply(w, fmt.Sprintf("resp_magpie_%d", time.Now().UnixNano()), out, nil)
	})
}

// answeredHere records a request magpie answered itself with write,
// sending it nowhere: the Routing view has it tried on magpie alone, who
// naming why, and the Recent calls and the usage ledger have it as
// answered by magpie, rejected, with why as its error, so Usage and the
// Sessions it belongs to say what became of it.
func (s *Server) answeredHere(r *http.Request, from provider.Protocol, body []byte, who, why string, write func()) {
	start := time.Now()
	usage.Saw(agentOf(r))
	metadata := requestSessionMetadata(r.Header, body)
	kind := requestCallKind(r.Header, metadata)
	model := modelOf(body)
	seat := Weighed{ID: "magpie", Provider: "magpie", Name: "magpie", Who: who, Kind: "provider", Model: model}
	by := callerOf(r) // as it is recorded: another magpie's agent by its own name
	tr := s.trace.beginFor(r, Route{Time: start, Agent: by.agent, Session: sessionOf(r.Header), ParentSession: titleParentSession(r.Header, metadata, kind),
		Kind: kind, Model: model, Provider: "magpie", Order: []Weighed{seat}, Tries: []Try{{ID: seat.ID, Model: model, Start: start}}})
	write()
	ms := time.Since(start).Milliseconds()
	s.trace.update(tr, func(t *Route) {
		t.Tries[0].Done, t.Tries[0].Status, t.Tries[0].Millis, t.Tries[0].Error = true, 200, ms, why
		t.Done, t.Status, t.Error, t.Millis = true, 200, why, ms
	})
	s.record(Call{Route: tr.ID, Time: start, From: from, To: from, Model: model, Provider: "magpie",
		Agent: by.agent, Via: by.via, Kind: kind, Status: 200, Millis: ms, Error: why})
	appendUsage(r, usage.Record{RouteID: tr.ID, Time: start, Agent: by.agent, Via: by.via, Provider: "magpie", Model: model, Requested: model,
		Millis: ms, Status: 200, Rejected: true, Error: why, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header),
		Kind: kind, Endpoint: r.URL.Path})
}

// writeTitleReply streams a finished Responses reply with these output
// items, as the ChatGPT backend streams one to Codex.
func writeTitleReply(w http.ResponseWriter, id string, out []any, used json.RawMessage) {
	writeTitleEnd(w, id, out, used, "completed", nil)
}

// writeTitleEnd streams a Responses reply that ended so: "completed", or
// "incomplete" for why in details, the one terminal event
// response.<status>.
func writeTitleEnd(w http.ResponseWriter, id string, out []any, used json.RawMessage, status string, details json.RawMessage) {
	if out == nil {
		out = []any{}
	}
	done := map[string]any{"id": id, "object": "response", "status": status, "output": out}
	if len(used) > 0 {
		done["usage"] = used
	}
	if status != "completed" {
		if len(details) == 0 || string(details) == "null" {
			details = json.RawMessage(`{"reason":"unknown"}`)
		}
		done["incomplete_details"] = details
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	evs := []map[string]any{{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}}}}
	for i, it := range out {
		evs = append(evs, map[string]any{"type": "response.output_item.added", "output_index": i, "item": it},
			map[string]any{"type": "response.output_item.done", "output_index": i, "item": it})
	}
	evs = append(evs, map[string]any{"type": "response." + status, "response": done})
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], b)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// titleShape is the object a title request's text.format asks for: its
// string properties' bounds and which are required. Codex 0.160's TUI asks
// for {"title"} alone; another of its builds for a description beside the
// title, both required and nothing else allowed (#743: magpie gave it the
// title alone, so it kept the first message as the thread's name).
type titleShape struct {
	Properties map[string]struct {
		Type      string `json:"type"`
		MaxLength int    `json:"maxLength"`
	} `json:"properties"`
	Required []string `json:"required"`
}

// titleShapeOf is the shape a Responses request's text.format.schema asks
// for, the zero shape (a title) when it names none.
func titleShapeOf(body []byte) titleShape {
	var q struct {
		Text struct {
			Format struct {
				Schema titleShape `json:"schema"`
			} `json:"format"`
		} `json:"text"`
	}
	json.Unmarshal(body, &q)
	return q.Text.Format.Schema
}

// titleJSON is a model's answer to a title request as Codex reads one:
// {"title": "…"}, with each other string its shape requires. Codex asks
// for that shape in the request's text.format, which a model behind
// another API never sees, and so may answer with the title alone, in
// quotes, or in a code fence. "" when it gave none.
func titleJSON(said string, shape titleShape) string {
	t := strings.TrimSpace(said)
	// a reasoning model's thoughts, where its API leaves them in the text
	t = afterThought(t)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimPrefix(t, "```")
		if i := strings.IndexByte(t, '\n'); i >= 0 && !strings.Contains(t[:i], "{") {
			t = t[i+1:]
		}
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
	}
	fields := map[string]any{}
	if strings.HasPrefix(t, "{") {
		if json.NewDecoder(bytes.NewReader([]byte(t))).Decode(&fields) == nil {
			if v, ok := fields["title"].(string); ok {
				t = v
			}
		}
	}
	// its first line, as a title is one
	for _, line := range strings.Split(t, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			t = line
			break
		}
	}
	if low := strings.ToLower(t); strings.HasPrefix(low, "title:") {
		t = t[len("title:"):]
	}
	t = strings.TrimSpace(strings.Trim(strings.TrimSpace(t), "\"'`*#“”‘’"))
	if t == "" || strings.HasPrefix(t, "{") {
		return ""
	}
	// each other string the schema requires, a description among them:
	// the model's own when its JSON gave one, else the title, every one
	// cut to the schema's length
	clip := func(name, v string) string {
		if n := shape.Properties[name].MaxLength; n > 0 {
			if r := []rune(v); len(r) > n {
				v = strings.TrimSpace(string(r[:n]))
			}
		}
		return v
	}
	out := map[string]string{"title": clip("title", t)}
	for _, name := range shape.Required {
		if _, done := out[name]; done {
			continue
		}
		if ty := shape.Properties[name].Type; ty != "" && ty != "string" {
			continue
		}
		v, _ := fields[name].(string)
		if v = strings.TrimSpace(v); v == "" {
			v = t
		}
		out[name] = clip(name, v)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// codexSideEffort is a Codex side call's body for to, one of magpie's
// models, asking a reasoning effort to takes. Codex asks low for a title
// only on its own title model, and none (no effort) on any other; its
// auto-review asks low only where the model lists low, else the model's
// default, which Codex has none of for a model of magpie's whose levels
// it isn't told. A model that needs an effort turned those away with 400
// (cybercaius on Discord). With the model's levels known, a call asking
// no effort, or an off one the model hasn't, is asked at its lowest on
// level, low where it has it; any other effort, a group, or a model whose
// levels aren't known goes as asked (serve's effortRequired retry then
// asks the last at low if it turns the request away).
func codexSideEffort(body []byte, to string) []byte {
	if provider.IsGroupRef(to) {
		return body
	}
	e := bodyEffort(provider.Responses, body)
	if e != "" && !offEffort(e) {
		return body
	}
	levels := ModelEfforts(to)
	if len(levels) == 0 || slices.Contains(levels, e) {
		return body
	}
	p, model, ok := provider.Resolve(to)
	if !ok {
		return body
	}
	return withFixedEffort(provider.Responses, body, onEffort(p, model))
}

package agent

// Shelley (boldsoftware/shelley, "a coding agent for exe.dev") is a web
// app `shelley serve` runs. Its models are in its database, not in a file
// magpie can edit: the database is wherever -db says (shelley.db in the
// folder it was started in, by default). magpie tells a running Shelley
// through the API its own Models and Profiles pages use, on the Unix
// socket it opens for its CLI (server/server.go: the whole API, no CSRF
// check): $SHELLEY_SOCKET, else $XDG_CONFIG_HOME/shelley/shelley.sock
// (~/.config/shelley), or shelley-2.sock … when a second server took the
// first (client.DefaultSocketPath, cmd/shelley/main.go).
//
//	GET    /api/custom-models       [{model_id, display_name, provider_type, endpoint, has_api_key, model_name, …}]
//	POST   /api/custom-models       {display_name, provider_type, endpoint, api_key, model_name, …} → the model (its id made from endpoint and name)
//	PUT    /api/custom-models/{id}  the same fields; an empty api_key keeps the one it has
//	DELETE /api/custom-models/{id}
//	GET    /api/models              every model Shelley can use, {id, display_name, ready, …}
//	GET    /api/profiles            [{name, default, …settings: model, thinking_level, …}]
//	PUT    /api/profiles/{name}     the profile back whole (strict: unknown fields refused)
//
// A model of magpie's is a custom model of type openai at the gateway's /v1
// (Shelley adds /chat/completions), with Shelley's own key, one per model
// picked in magpie; the ids magpie made are kept in the stash, so a model
// the user made by hand at the gateway stays theirs. Picking one makes it
// the default profile's model, which new conversations start on; what the
// profile had is stashed and put back on Disconnect, and magpie's models go.
// Shelley not running is no error to show: there is nothing to read.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
)

// shelleySockets are the sockets a running Shelley may answer on, in the
// order its CLI tries them. Under test there are none, so no test reaches a
// real Shelley; a test points it at a fake one.
var shelleySockets = func() []string {
	if testing.Testing() {
		return nil
	}
	if s := appdir.Getenv("SHELLEY_SOCKET"); s != "" {
		return []string{s}
	}
	dir := shelleyDir()
	out := []string{filepath.Join(dir, "shelley.sock")}
	for i := 2; i <= 5; i++ {
		out = append(out, filepath.Join(dir, fmt.Sprintf("shelley-%d.sock", i)))
	}
	return out
}

// shelleyDir is Shelley's config folder, where its socket is:
// $XDG_CONFIG_HOME/shelley, else ~/.config/shelley (on a Mac too).
func shelleyDir() string {
	if d := appdir.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "shelley")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "shelley")
}

// errShelleyDown is no Shelley answering.
var errShelleyDown = errors.New("Shelley isn't running — start it (shelley serve) and try again")

// shelleyStash keys: the ids of the models magpie made, JSON, and the
// default profile's model and thinking level before magpie's, JSON.
const (
	shelleyModelsKey = "shelley.models"
	shelleyWasKey    = "shelley.default"
	// shelleyTakeOut is magpie's models left in a Shelley that was gone
	// when it was turned off; they come out once it answers again.
	shelleyTakeOut = "shelley.takeout"
)

// shelleyCall sends one request to the Shelley answering on one of
// shelleySockets and decodes its reply into out.
func shelleyCall(method, path string, body, out any) error {
	var last error = errShelleyDown
	for _, sock := range shelleySockets() {
		if _, err := os.Stat(sock); err != nil {
			continue
		}
		b, err := shelleySend(sock, method, path, body)
		if errors.Is(err, errShelleyDown) {
			continue // a socket a stopped Shelley left behind
		}
		if err != nil {
			return err
		}
		if out == nil || len(bytes.TrimSpace(b)) == 0 {
			return nil
		}
		return json.Unmarshal(b, out)
	}
	return last
}

func shelleySend(sock, method, path string, body any) ([]byte, error) {
	c := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
	}
	defer c.CloseIdleConnections()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://shelley"+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, errShelleyDown
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Shelley: %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

// shelleyModel is one of Shelley's custom models.
type shelleyModel struct {
	ID        string `json:"model_id"`
	Name      string `json:"display_name"`
	Type      string `json:"provider_type"`
	Endpoint  string `json:"endpoint"`
	ModelName string `json:"model_name"`
}

func shelleyModels() ([]shelleyModel, error) {
	var ms []shelleyModel
	err := shelleyCall("GET", "/api/custom-models", nil, &ms)
	return ms, err
}

// shelleyOurs is the ids of the custom models magpie made.
func shelleyOurs() []string {
	var ids []string
	json.Unmarshal([]byte(stashShared()[shelleyModelsKey]), &ids)
	return ids
}

func shelleyKeepOurs(ids []string) {
	if len(ids) == 0 {
		forget(shelleyModelsKey)
		return
	}
	b, _ := json.Marshal(ids)
	stash(map[string]string{shelleyModelsKey: string(b)})
}

// shelleyMine is magpie's models among ms.
func shelleyMine(ms []shelleyModel) []shelleyModel {
	ours := shelleyOurs()
	var out []shelleyModel
	for _, m := range ms {
		if slices.Contains(ours, m.ID) {
			out = append(out, m)
		}
	}
	return out
}

// shelleyDefault is the default profile, whole, as Shelley gave it: its
// PUT takes it back only with every field it has.
func shelleyDefault() (map[string]any, error) {
	var ps []map[string]any
	if err := shelleyCall("GET", "/api/profiles", nil, &ps); err != nil {
		return nil, err
	}
	for _, p := range ps {
		if d, _ := p["default"].(bool); d {
			return p, nil
		}
	}
	return nil, errors.New("Shelley has no default profile")
}

func shelleyPutDefault(p map[string]any) error {
	name, _ := p["name"].(string)
	return shelleyCall("PUT", "/api/profiles/"+url.PathEscape(name), p, nil)
}

// shelleyStr is p's string k.
func shelleyStr(p map[string]any, k string) string { s, _ := p[k].(string); return s }

// shelleyBody is what magpie sends Shelley of model ref at the gateway.
func shelleyBody(at place, ref string) map[string]any {
	b := map[string]any{
		"display_name":  ref + " (magpie)",
		"provider_type": "openai",
		"endpoint":      at.v1(),
		"api_key":       agentKeyAt("shelley", at.gw()),
		"model_name":    ref,
	}
	// Shelley knows a model's reasoning and images from models.dev by its
	// endpoint and name, and magpie's are neither: say what magpie knows
	for _, m := range sharedModels("shelley") {
		if m.ID != ref {
			continue
		}
		b["reasoning_support"] = shelleyYes(m.Reasoning || len(m.Efforts) > 0)
		b["image_support"] = shelleyYes(m.Images)
		if n := maxTokens(m); n > 0 {
			b["max_tokens"] = n
		}
	}
	return b
}

func shelleyYes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// shelleyReasons: magpie's model ref reasons, as far as magpie knows.
func shelleyReasons(ref string) bool {
	i := slices.IndexFunc(sharedModels("shelley"), func(m catalog.Model) bool { return m.ID == ref })
	if i < 0 {
		return true // not known: Shelley's own "auto" decides
	}
	m := sharedModels("shelley")[i]
	return m.Reasoning || len(m.Efforts) > 0
}

// shelleyRewire brings magpie's models in Shelley to the gateway as it is
// now (its address, Shelley's key), and forgets those gone from Shelley.
func shelleyRewire(at place, ms []shelleyModel) ([]shelleyModel, error) {
	mine := shelleyMine(ms)
	var kept []string
	for _, m := range mine {
		if err := shelleyCall("PUT", "/api/custom-models/"+url.PathEscape(m.ID), shelleyBody(at, m.ModelName), nil); err != nil {
			return nil, err
		}
		kept = append(kept, m.ID)
	}
	if len(kept) != len(shelleyOurs()) {
		shelleyKeepOurs(kept)
	}
	return mine, nil
}

func shelleyGet() string {
	p, err := shelleyDefault()
	if err != nil {
		if stashShared()[shelleyTakeOut] != "" {
			return ""
		}
		return appliedOf("shelley").Fields["model"]
	}
	model := shelleyStr(p, "model")
	ms, err := shelleyModels()
	if err != nil {
		return appliedOf("shelley").Fields["model"]
	}
	for _, m := range shelleyMine(ms) {
		if m.ID == model {
			return magpieID + "/" + m.ModelName
		}
	}
	return model
}

func shelleySet(at place, v string) error {
	ref, onMagpie := strings.CutPrefix(v, magpieID+"/")
	p, err := shelleyDefault()
	if err != nil {
		if v == "" && errors.Is(err, errShelleyDown) && !shelleyHere() {
			// Shelley uninstalled: nothing of it to change now. Its
			// database, which only it changes, loses magpie's models if
			// it runs again (Sync), as Alma's does.
			stash(map[string]string{shelleyTakeOut: "1"})
			return nil
		}
		return err
	}
	ms, err := shelleyModels()
	if err != nil {
		return err
	}
	if !onMagpie {
		return shelleyOff(p, ms, v)
	}
	forget(shelleyTakeOut)
	mine, err := shelleyRewire(at, ms)
	if err != nil {
		return err
	}
	id := ""
	for _, m := range mine {
		if m.ModelName == ref {
			id = m.ID
		}
	}
	if id == "" {
		var made shelleyModel
		if err := shelleyCall("POST", "/api/custom-models", shelleyBody(at, ref), &made); err != nil {
			return err
		}
		if made.ID == "" {
			return errors.New("Shelley made the model but gave no id for it")
		}
		id = made.ID
		shelleyKeepOurs(append(shelleyOurs(), id))
	}
	// what the profile had, before magpie's first model
	if _, had := stashShared()[shelleyWasKey]; !had {
		was, _ := json.Marshal(map[string]string{"model": shelleyStr(p, "model"), "thinking_level": shelleyStr(p, "thinking_level")})
		stash(map[string]string{shelleyWasKey: string(was)})
	}
	p["model"] = id
	// a level Shelley would refuse for a model that doesn't reason
	if !shelleyReasons(ref) {
		p["thinking_level"] = ""
	}
	return shelleyPutDefault(p)
}

// shelleyOff puts the default profile on v, the user's own model ("" for
// the one they had before magpie), and takes magpie's models out.
func shelleyOff(p map[string]any, ms []shelleyModel, v string) error {
	mine := shelleyMine(ms)
	var was map[string]string
	if s := stashShared()[shelleyWasKey]; s != "" {
		json.Unmarshal([]byte(s), &was)
	}
	cur := shelleyStr(p, "model")
	onMine := slices.ContainsFunc(mine, func(m shelleyModel) bool { return m.ID == cur })
	switch {
	case v != "":
		p["model"] = v
	case onMine && was != nil:
		p["model"] = was["model"]
		if shelleyStr(p, "thinking_level") == "" {
			p["thinking_level"] = was["thinking_level"]
		}
		// the model they had may be gone since
		if was["model"] != "" && !shelleyReady(was["model"]) {
			p["model"] = ""
		}
	case onMine:
		p["model"] = "" // the server's default
	}
	if shelleyStr(p, "model") != cur || v != "" {
		if err := shelleyPutDefault(p); err != nil {
			return err
		}
	}
	for _, m := range mine {
		if err := shelleyCall("DELETE", "/api/custom-models/"+url.PathEscape(m.ID), nil, nil); err != nil {
			return err
		}
	}
	forget(shelleyModelsKey, shelleyWasKey, shelleyTakeOut)
	return nil
}

// shelleyReady: Shelley can use model id now. Not known is ready: Shelley
// itself refuses a model it can't use.
func shelleyReady(id string) bool {
	ms, err := shelleyAll()
	if err != nil {
		return true
	}
	return slices.ContainsFunc(ms, func(m shelleyInfo) bool { return m.ID == id && m.Ready })
}

// shelleyInfo is one model Shelley can offer (GET /api/models).
type shelleyInfo struct {
	ID    string `json:"id"`
	Name  string `json:"display_name"`
	Ready bool   `json:"ready"`
}

func shelleyAll() ([]shelleyInfo, error) {
	var ms []shelleyInfo
	err := shelleyCall("GET", "/api/models", nil, &ms)
	return ms, err
}

// shelleyOwn lists the models Shelley can use besides magpie's.
func shelleyOwn() []Option {
	ms, err := shelleyAll()
	if err != nil {
		return nil
	}
	ours := shelleyOurs()
	var out []Option
	for _, m := range ms {
		if !m.Ready || slices.Contains(ours, m.ID) {
			continue
		}
		out = append(out, Option{Value: m.ID, Label: cmp.Or(m.Name, m.ID), Icon: modelIcon("", m.ID)})
	}
	return out
}

// shelleyHere: Shelley is installed — its command on PATH, or its config
// folder, which it makes for its socket.
func shelleyHere() bool {
	if binAt("shelley") != "" {
		return true
	}
	st, err := os.Stat(shelleyDir())
	return err == nil && st.IsDir()
}

var noticeShelley = newNotice("New Shelley conversations start on this; an open one keeps its model until you pick another in its model menu.")

func shelley(home string) *Agent {
	at := here(home)
	return &Agent{
		ID: "shelley", Name: "Shelley", Icon: "shelley-color", Spelled: prefixed, NoNames: true,
		UA:  []string{"shelley"},
		Bin: "shelley", Dir: shelleyDir(), Path: filepath.Join(shelleyDir(), "shelley.sock"),
		Notice: func() string {
			if usesMagpie(shelleyGet()) {
				return noticeShelley.String()
			}
			return ""
		},
		Check: func() string {
			p, err := shelleyDefault()
			if err != nil {
				return ""
			}
			ms, err := shelleyModels()
			if err != nil {
				return ""
			}
			for _, m := range shelleyMine(ms) {
				if m.ID == shelleyStr(p, "model") {
					return wiringOff("Shelley", "custom models", func(string) (string, bool) { return m.Endpoint, m.Endpoint != "" },
						"endpoint", at.v1())
				}
			}
			return ""
		},
		Sync: func() error {
			ms, err := shelleyModels()
			if err != nil || len(shelleyMine(ms)) == 0 {
				return nil // not running, or nothing of magpie's: nothing to keep current
			}
			if stashShared()[shelleyTakeOut] != "" {
				// turned off while Shelley was gone, and here again
				return shelleySet(at, "")
			}
			_, err = shelleyRewire(at, ms)
			return err
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: shelleyGet,
			Set: func(v string) error { return shelleySet(at, v) },
			Options: func(map[string]string) []Option {
				return withVia(shelleyOwn(), "shelley", magpieID+"/")
			},
		}},
	}
}

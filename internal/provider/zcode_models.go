package provider

// A coding plan's models are the ones ZCode offers for it, from ZCode's
// built-in provider config: zcode.z.ai names the release to use
// (/api/v1/client/configs, its configs.builtin_provider_config_json), a
// JSON on cdn-zcode.z.ai that lists each plan's models (by providerId,
// "account:zai-individual-coding-plan") and what each model is, by rules
// matched in turn on its id. ZCode keeps whichever of that release and the
// one it ships with (or cached last) is the later revision, and so does
// magpie. Nothing here is the account's: neither is signed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

type zcodeBuiltin struct {
	Revision int `json:"revision"`
	Config   struct {
		ProviderConfigRules struct {
			ProviderRules []struct {
				ProviderID      string   `json:"providerId"`
				BuiltinModelIDs []string `json:"builtinModelIds"`
			} `json:"providerRules"`
		} `json:"providerConfigRules"`
		ModelConfigRules struct {
			ModelRules []struct {
				Match  string         `json:"modelMatch"`
				Config zcodeModelRule `json:"config"`
			} `json:"modelRules"`
			ProviderModels []struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
				Config     struct {
					Enabled *bool `json:"enabled"`
				} `json:"config"`
			} `json:"builtinProviderModelRules"`
		} `json:"modelConfigRules"`
	} `json:"config"`
}

type zcodeModelRule struct {
	Properties struct {
		ContextWindow int `json:"contextWindow"`
		InputFormat   struct {
			SupportsImage *bool `json:"supportsImage"`
		} `json:"inputFormat"`
	} `json:"properties"`
	OptionSpecs struct {
		MaxOutputTokens struct {
			Max int `json:"max"`
		} `json:"maxOutputTokens"`
		ReasoningLevel struct {
			Values []string `json:"values"`
		} `json:"reasoningLevel"`
	} `json:"optionSpecs"`
}

// zcodeEfforts are ZCode's reasoning levels for a model in magpie's words:
// "disabled" is none, and "enabled", a model's one level when it has no
// others (GLM-5-Turbo's), is high.
func zcodeEfforts(values []string) []string {
	var out []string
	for _, v := range values {
		switch v {
		case "disabled":
			v = "none"
		case "enabled":
			v = "high"
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// zcodePlanID is ZCode's providerId for the coding plan served at base.
// A team's plan has the individual one's models.
func zcodePlanID(base string) string {
	if strings.Contains(base, "/zcode-plan/") { // ZCode's Start Plan (zcode_start.go)
		return "account:zai-start-plan"
	}
	if strings.Contains(base, "bigmodel.cn") {
		return "account:bigmodel-individual-coding-plan"
	}
	return "account:zai-individual-coding-plan"
}

// zcodeFetchModels is the plan at base's models, as ZCode has them now.
func zcodeFetchModels(ctx context.Context, base string) ([]catalog.Model, error) {
	b, err := zcodeRemoteBuiltin(ctx)
	if l, ok := zcodeLocalBuiltin(); ok && (err != nil || l.Revision > b.Revision) {
		b, err = l, nil
	}
	if err != nil {
		return nil, err
	}
	ms := b.models(zcodePlanID(base))
	if len(ms) == 0 {
		return nil, fmt.Errorf("ZCode's config (revision %d) has no models for %s", b.Revision, zcodePlanID(base))
	}
	return ms, nil
}

func (b zcodeBuiltin) models(plan string) []catalog.Model {
	var ids []string
	on := map[string]bool{}
	add := func(id string) {
		if k := strings.ToLower(id); !on[k] {
			on[k] = true
			ids = append(ids, id)
		}
	}
	for _, r := range b.Config.ProviderConfigRules.ProviderRules {
		if r.ProviderID == plan {
			for _, id := range r.BuiltinModelIDs {
				add(id)
			}
		}
	}
	off := map[string]bool{}
	for _, r := range b.Config.ModelConfigRules.ProviderModels {
		if r.ProviderID != plan {
			continue
		}
		if r.Config.Enabled != nil && !*r.Config.Enabled {
			off[strings.ToLower(r.ModelID)] = true
			continue
		}
		add(r.ModelID)
	}
	var out []catalog.Model
	for _, id := range ids {
		if off[strings.ToLower(id)] {
			continue
		}
		m := catalog.Model{ID: id, Name: id}
		// every rule that matches the id, later ones over earlier ones
		for _, r := range b.Config.ModelConfigRules.ModelRules {
			re, err := regexp.Compile("^(?i:" + r.Match + ")$")
			if err != nil || !re.MatchString(id) {
				continue
			}
			c := r.Config
			if c.Properties.ContextWindow > 0 {
				m.Context = c.Properties.ContextWindow
			}
			if c.OptionSpecs.MaxOutputTokens.Max > 0 {
				m.Output = c.OptionSpecs.MaxOutputTokens.Max
			}
			if v := c.Properties.InputFormat.SupportsImage; v != nil {
				m.Images, m.ImageInput = *v, v
			}
			if vs := c.OptionSpecs.ReasoningLevel.Values; len(vs) > 0 {
				m.Efforts = zcodeEfforts(vs)
			}
		}
		out = append(out, m)
	}
	return out
}

// zcodeRemoteBuiltin is the release zcode.z.ai names now.
func zcodeRemoteBuiltin(ctx context.Context) (zcodeBuiltin, error) {
	var cfg struct {
		Configs struct {
			URL string `json:"builtin_provider_config_json"`
		} `json:"configs"`
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	u := zcodeAPI + "/api/v1/client/configs?app_version=" + zcodeAppVersion + "&platform=" + platform + "-" + arch
	if err := zcodeCall(ctx, http.MethodGet, u, "", nil, &cfg); err != nil {
		return zcodeBuiltin{}, fmt.Errorf("ZCode's configs: %w", err)
	}
	if !strings.HasPrefix(cfg.Configs.URL, "https://") {
		return zcodeBuiltin{}, errors.New("ZCode's configs name no provider config")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Configs.URL, nil)
	if err != nil {
		return zcodeBuiltin{}, err
	}
	req.Header.Set("User-Agent", "ZCode/"+zcodeAppVersion)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return zcodeBuiltin{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return zcodeBuiltin{}, fmt.Errorf("ZCode's provider config: %s", res.Status)
	}
	var b zcodeBuiltin
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&b); err != nil {
		return zcodeBuiltin{}, fmt.Errorf("ZCode's provider config: %w", err)
	}
	return b, nil
}

// zcodeBuiltinFiles are where an installed ZCode keeps a release: the one
// it ships with, and the one it last took, under ~/.zcode.
var zcodeBuiltinFiles = func() []string {
	home, _ := os.UserHomeDir()
	fs, _ := filepath.Glob(filepath.Join(home, ".zcode", "v2", "runtime", "provider", "*", "*", "*", "zcode-builtin.json"))
	if runtime.GOOS == "darwin" {
		fs = append(fs, "/Applications/ZCode.app/Contents/Resources/config/provider/zcode-builtin.json")
	}
	return fs
}

// zcodeLocalBuiltin is the latest release an installed ZCode has.
func zcodeLocalBuiltin() (zcodeBuiltin, bool) {
	var best zcodeBuiltin
	found := false
	for _, f := range zcodeBuiltinFiles() {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var b zcodeBuiltin
		if json.Unmarshal(raw, &b) == nil && (!found || b.Revision > best.Revision) {
			best, found = b, true
		}
	}
	return best, found
}

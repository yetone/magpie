package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
)

func codexConfigPath() string {
	dir := appdir.Getenv("CODEX_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".codex")
	}
	return filepath.Join(dir, "config.toml")
}

func readCodexConfig(path string) ([]AppImport, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	top, tables := parseTOML(string(b))
	var ids []string
	var profiles []string
	for table := range tables {
		if id, ok := codexTableID(table, "model_providers."); ok {
			ids = append(ids, id)
		} else if _, ok := codexTableID(table, "profiles."); ok {
			profiles = append(profiles, table)
		}
	}
	slices.Sort(ids)
	slices.Sort(profiles)
	var out []AppImport
	for _, id := range ids {
		table := "model_providers." + id
		mp := tables[table]
		if mp == nil {
			table = `model_providers."` + id + `"`
			mp = tables[table]
		}
		name := mp["name"]
		if name == "" {
			name = id
		}
		it := AppImport{Ref: id, From: "config.toml"}
		if id == "magpie" {
			it.Provider, it.Skip = Provider{Name: name}, "it points at magpie itself"
			out = append(out, it)
			continue
		}
		var models []string
		if top["model_provider"] == id {
			models = append(models, top["model"])
			models = append(models, codexImportModels(path, top["model_catalog_json"])...)
		}
		for _, table := range profiles {
			profile := tables[table]
			providerID := profile["model_provider"]
			if providerID == "" {
				providerID = top["model_provider"]
			}
			if providerID != id {
				continue
			}
			models = append(models, profile["model"])
			catalogPath := profile["model_catalog_json"]
			if catalogPath == "" && top["model_provider"] == id {
				catalogPath = top["model_catalog_json"]
			}
			models = append(models, codexImportModels(path, catalogPath)...)
		}
		var eps endpoints
		if mp["wire_api"] == "chat" {
			eps.chat = mp["base_url"]
		} else {
			eps.responses = mp["base_url"]
		}
		it.Provider, it.Skip = imported(name, mp["experimental_bearer_token"], eps, cleanList(models))
		if it.Skip != "" {
			it.Provider = Provider{Name: name}
		} else {
			it.Provider.Headers = tables[table+".http_headers"]
		}
		out = append(out, it)
	}
	return out, nil
}

func codexTableID(table, prefix string) (string, bool) {
	id, ok := strings.CutPrefix(table, prefix)
	if !ok || id == "" {
		return "", false
	}
	if strings.HasPrefix(id, `"`) {
		if !strings.HasSuffix(id, `"`) {
			return "", false
		}
		id = strings.Trim(id, `"`)
		return id, id != ""
	}
	return id, !strings.Contains(id, ".")
}

func codexImportModels(configPath, catalogPath string) []string {
	if catalogPath == "" {
		return nil
	}
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(catalogPath, "~/") {
		catalogPath = filepath.Join(home, strings.TrimPrefix(catalogPath, "~/"))
	} else if !filepath.IsAbs(catalogPath) {
		catalogPath = filepath.Join(filepath.Dir(configPath), catalogPath)
	}
	if filepath.Clean(catalogPath) == filepath.Join(home, ".codex", "magpie-models.json") {
		return nil
	}
	b, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil
	}
	var list struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	var models []string
	for _, m := range list.Models {
		if m.Slug != "" && m.Visibility != "hide" {
			models = append(models, m.Slug)
		}
	}
	return models
}

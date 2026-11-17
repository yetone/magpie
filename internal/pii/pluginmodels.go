package pii

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

const pluginModelPrefix = externalPrefix + "plugin/"

var pluginModels struct {
	sync.Mutex
	stamp string
	list  []*externalModel
}

func init() {
	plugin.OnChange(func() { registeredPluginModels() })
	plugin.RegisterOptionsValidator(func(target string, options map[string]any) error {
		if p, _ := plugin.PrivacyModels(target); p != nil {
			_, err := parsePrivacyVersions(options)
			return err
		}
		return nil
	})
}

// registeredPluginModels keeps a helper and its cache while its launch
// configuration stays the same. Install, options, updates and disable are
// picked up across Magpie processes through the plugin store's stamp.
func registeredPluginModels() []*externalModel {
	pluginModels.Lock()
	stamp := plugin.ListStamp()
	if pluginModels.stamp == stamp {
		list := pluginModels.list
		pluginModels.Unlock()
		return list
	}
	previous := make(map[string]*externalModel, len(pluginModels.list))
	for _, m := range pluginModels.list {
		previous[m.id] = m
	}
	var list []*externalModel
	for _, entry := range plugin.Load().Plugins {
		if entry.Off {
			continue
		}
		list = append(list, modelsFromPlugin(entry, previous)...)
	}
	for _, old := range previous {
		old.retired.Store(true)
	}
	pluginModels.stamp, pluginModels.list = stamp, list
	pluginModels.Unlock()
	for _, old := range previous {
		old.Stop()
	}
	return list
}

func modelsFromPlugin(entry plugin.Entry, previous map[string]*externalModel) []*externalModel {
	dir := plugin.Target(entry.Spec)
	launcher, _ := plugin.PrivacyModels(dir)
	if launcher == nil {
		return nil
	}
	versions, err := parsePrivacyVersions(entry.Options)
	if err != nil {
		log.Printf("privacy models from %s: %v", entry.Spec, err)
		return nil
	}
	command := launcher.Command
	if !filepath.IsAbs(command) && strings.ContainsAny(command, `/\`) {
		var ok bool
		if command, ok = plugin.FileIn(dir, filepath.ToSlash(command)); !ok {
			log.Printf("privacy models from %s: launcher must be a file in the plugin", entry.Spec)
			return nil
		}
	}
	owner := sha256.Sum256([]byte(plugin.Name(entry.Spec)))
	packageRevision := launcher.Version
	if plugin.IsGit(entry.Spec) {
		packageRevision += "@" + plugin.GitCommit(entry.Spec)
	}
	var list []*externalModel
	for _, v := range versions {
		id := fmt.Sprintf("%s%x/%s", pluginModelPrefix, owner[:8], v.ID)
		launch := externalLaunch{
			Command: command, Args: launcher.Args, Dir: dir, Weights: v.Path,
			WeightsStamp: weightsStamp(v.Path), WeightsRevision: v.Revision, PackageRevision: packageRevision,
		}
		identity := launch.generation(id)
		m := previous[id]
		if m != nil && m.identity == identity {
			delete(previous, id)
		} else {
			m = newExternalModel(id, identity, launch)
		}
		score := v.MinScore
		if score == 0 {
			score = Floor
		}
		m.setMetadata(v.Name, score)
		list = append(list, m)
	}
	return list
}

func (l externalLaunch) generation(id string) string {
	data, _ := json.Marshal(l)
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte(id+"\x00"), data...)))
}

// refreshWeights replaces the worker and its caches when the selected model's
// files change without a plugin configuration edit.
// Of and Models ask it on every request, so the files are looked at once
// a second at most.
func refreshWeights(m *externalModel) *externalModel {
	now := time.Now().UnixNano()
	if t := m.stamped.Load(); !m.retired.Load() && t != 0 && now-t < int64(time.Second) {
		return m
	}
	m.stamped.Store(now)
	stamp := weightsStamp(m.launch.Weights)
	if stamp == m.launch.WeightsStamp && !m.retired.Load() {
		return m
	}
	pluginModels.Lock()
	i := slices.IndexFunc(pluginModels.list, func(other *externalModel) bool { return other.id == m.id })
	if i < 0 {
		pluginModels.Unlock()
		return m
	}
	current := pluginModels.list[i]
	if current != m {
		pluginModels.Unlock()
		return current
	}
	launch := m.launch
	launch.WeightsStamp = stamp
	next := newExternalModel(m.id, launch.generation(m.id), launch)
	next.name, next.minScore = m.name, m.minScore // metadata updates hold the registry lock
	m.retired.Store(true)
	pluginModels.list = slices.Clone(pluginModels.list)
	pluginModels.list[i] = next
	pluginModels.Unlock()
	m.Stop()
	return next
}

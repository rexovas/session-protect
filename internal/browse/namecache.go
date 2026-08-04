package browse

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/rexovas/session-protect/internal/config"
)

type nameEntry struct {
	Name  string `json:"name"`
	Model string `json:"model"`
	Mod   int64  `json:"mod"` // session file mtime when the entry was read
}

// ScanNamed is Scan plus custom names for every session, backed by an
// mtime-keyed cache so only new or changed session files are read.
func ScanNamed(cfg config.Config) []*Project {
	projects := Scan(cfg)
	applyNames(cfg, projects)
	return projects
}

// metaCacheVersion invalidates the cache when the extractor learns new
// tricks (v2: codex models from turn_context).
const metaCacheVersion = 2

type metaCache struct {
	Version int                  `json:"version"`
	Entries map[string]nameEntry `json:"entries"`
}

func applyNames(cfg config.Config, projects []*Project) {
	path := filepath.Join(cfg.BackupRoot, ".session-meta.json")
	stored := metaCache{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &stored)
	}
	cache := stored.Entries
	if stored.Version != metaCacheVersion || cache == nil {
		cache = map[string]nameEntry{}
	}

	codexNames := codexThreadNames()
	dirty := false
	for _, project := range projects {
		for i := range project.Sessions {
			session := &project.Sessions[i]
			if session.Target == "codex" {
				if name, ok := codexNames[session.ID]; ok {
					session.CustomName = name
				}
			}
			file := session.SourcePath
			if file == "" {
				file = session.BackupPath
			}
			if file == "" {
				continue
			}
			mod := newest(*session).Unix()
			if entry, ok := cache[session.ID]; ok && entry.Mod == mod {
				session.CustomName = entry.Name
				session.LastModel = entry.Model
				continue
			}
			name, model := scanFileMeta(file)
			session.CustomName = name
			session.LastModel = model
			cache[session.ID] = nameEntry{Name: name, Model: model, Mod: mod}
			dirty = true
		}
		project.NamesLoaded = true
	}

	if !dirty {
		return
	}
	data, err := json.Marshal(metaCache{Version: metaCacheVersion, Entries: cache})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

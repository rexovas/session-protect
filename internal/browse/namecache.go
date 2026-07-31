package browse

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/rexovas/session-protect/internal/config"
)

type nameEntry struct {
	Name string `json:"name"`
	Mod  int64  `json:"mod"` // session file mtime when the name was read
}

// ScanNamed is Scan plus custom names for every session, backed by an
// mtime-keyed cache so only new or changed session files are read.
func ScanNamed(cfg config.Config) []*Project {
	projects := Scan(cfg)
	applyNames(cfg, projects)
	return projects
}

func applyNames(cfg config.Config, projects []*Project) {
	path := filepath.Join(cfg.BackupRoot, ".session-names.json")
	cache := map[string]nameEntry{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cache)
	}

	dirty := false
	for _, project := range projects {
		for i := range project.Sessions {
			session := &project.Sessions[i]
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
				continue
			}
			session.CustomName = customTitle(file)
			cache[session.ID] = nameEntry{Name: session.CustomName, Mod: mod}
			dirty = true
		}
		project.NamesLoaded = true
	}

	if !dirty {
		return
	}
	data, err := json.Marshal(cache)
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

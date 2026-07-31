package browse

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/lock"
	"github.com/rexovas/session-protect/internal/restore"
)

// restoreDest resolves where a session's backup copy would land in the live
// source tree, and whether that would overwrite an existing live file.
func restoreDest(cfg config.Config, session Session) (dest string, overwriting bool, err error) {
	if session.BackupPath == "" {
		return "", false, fmt.Errorf("no backup copy exists for this session")
	}
	if session.SourcePath != "" {
		return session.SourcePath, true, nil
	}
	var sourceRoot string
	for _, target := range cfg.ResolveTargets() {
		if target.Name == session.Target {
			sourceRoot = target.Source
		}
	}
	if sourceRoot == "" {
		return "", false, fmt.Errorf("target %s is not configured", session.Target)
	}
	repo, prefix := cfg.RepoFor(session.Target)
	base := filepath.Join(repo, prefix)
	rel, relErr := filepath.Rel(base, session.BackupPath)
	if relErr != nil || strings.HasPrefix(rel, "..") {
		return "", false, fmt.Errorf("backup path %s escapes %s", session.BackupPath, base)
	}
	return filepath.Join(sourceRoot, rel), false, nil
}

// RestoreSession copies a backed-up session file back into the live source
// tree through the restore engine, so browser restores get the same safety
// copies and audit log as the CLI command.
func RestoreSession(cfg config.Config, session Session) (string, error) {
	dest, overwriting, err := restoreDest(cfg, session)
	if err != nil {
		return "", err
	}
	release, err := lock.Acquire(cfg.BackupRoot)
	if err != nil {
		return "", err
	}
	defer release()
	_, err = restore.Apply(cfg, []restore.Item{{
		Target:      session.Target,
		SessionID:   session.ID,
		State:       session.State,
		From:        session.BackupPath,
		To:          dest,
		Overwriting: overwriting,
	}})
	if err != nil {
		return "", err
	}
	return dest, nil
}

package encryption

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// attributes encrypts every tracked file while keeping the attribute file
// itself readable so the repository stays self-describing. File names and
// commit metadata remain visible; contents are ciphertext at rest.
const attributes = `* filter=git-crypt diff=git-crypt
.gitattributes !filter !diff
.git-crypt/** !filter !diff
`

func Installed() bool {
	_, err := exec.LookPath("git-crypt")
	return err == nil
}

// Unlocked reports whether the repository's git-crypt key is present locally,
// which is true after init or unlock.
func Unlocked(repo string) bool {
	_, err := os.Stat(filepath.Join(repo, ".git", "git-crypt", "keys", "default"))
	return err == nil
}

// Setup initializes git-crypt in an existing repository, writes the
// encryption attributes, and exports the recovery key to keyPath. It fails
// closed rather than overwrite an existing key file.
func Setup(repo string, keyPath string) error {
	if !Installed() {
		return fmt.Errorf("git-crypt is not installed")
	}
	if _, err := os.Stat(keyPath); err == nil {
		return fmt.Errorf("key file %s already exists; refusing to overwrite it", keyPath)
	}

	if err := run(repo, "git-crypt", "init"); err != nil {
		return err
	}
	attributesPath := filepath.Join(repo, ".gitattributes")
	if _, err := os.Stat(attributesPath); os.IsNotExist(err) {
		if err := os.WriteFile(attributesPath, []byte(attributes), 0o600); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	if err := run(repo, "git-crypt", "export-key", keyPath); err != nil {
		return err
	}
	return os.Chmod(keyPath, 0o600)
}

func run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

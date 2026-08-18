package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/version"
)

// The self-updater serves release-channel installs: it asks GitHub for
// the latest release, downloads this platform's archive, verifies it
// against checksums.txt, and atomically swaps the running binary.
// Source-channel installs keep the rebuild-from-checkout path.

const repoSlug = "rexovas/session-protect"

// Bases are injectable so tests run against a local server.
var (
	apiBase      = "https://api.github.com"
	downloadBase = "https://github.com"
	httpClient   = &http.Client{Timeout: 30 * time.Second}
)

// LatestRelease returns the newest release tag, e.g. "v1.2.0".
func LatestRelease() (string, error) {
	resp, err := httpClient.Get(apiBase + "/repos/" + repoSlug + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup: HTTP %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", fmt.Errorf("release lookup: empty tag")
	}
	return release.TagName, nil
}

// NewerAvailable compares the running version against the latest release.
func NewerAvailable() (current string, latest string, newer bool, err error) {
	latest, err = LatestRelease()
	if err != nil {
		return version.Version, "", false, err
	}
	return version.Version, latest, semverLess(version.Version, latest), nil
}

// semverLess reports a < b for vX.Y.Z-style versions. Non-semver
// (dev/pseudo-version) currents always count as older than a release.
func semverLess(a string, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !okb {
		return false
	}
	if !oka {
		return true
	}
	for i := range 3 {
		if pa.nums[i] != pb.nums[i] {
			return pa.nums[i] < pb.nums[i]
		}
	}
	// Equal triples: a prerelease is older than the release proper.
	return pa.pre != "" && pb.pre == ""
}

type semver struct {
	nums [3]int
	pre  string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	var out semver
	if idx := strings.IndexAny(s, "-+"); idx >= 0 {
		out.pre = s[idx:]
		s = s[:idx]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}
		out.nums[i] = n
	}
	return out, true
}

// assetName is the GoReleaser name_template contract.
func assetName(tag string) string {
	base := fmt.Sprintf("session-protect_%s_%s_%s", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		return base + ".zip"
	}
	return base + ".tar.gz"
}

// Apply downloads the tagged release for this platform, verifies its
// checksum, and swaps the running binary in place. Returns the path of
// the updated executable.
// executablePath is injectable so tests swap a scratch file, not the
// test binary itself.
var executablePath = os.Executable

// ErrBrewManaged means the binary lives in Homebrew's Cellar; swapping it
// underneath brew desyncs its bookkeeping, so updates route to brew.
var ErrBrewManaged = errors.New("installed via Homebrew — update with: brew upgrade session-protect")

// brewManaged reports whether path is inside a Homebrew Cellar.
func brewManaged(path string) bool {
	return strings.Contains(path, string(os.PathSeparator)+"Cellar"+string(os.PathSeparator))
}

func Apply(tag string) (string, error) {
	exe, err := executablePath()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if brewManaged(exe) {
		return "", ErrBrewManaged
	}

	base := downloadBase + "/" + repoSlug + "/releases/download/" + tag + "/"
	sums, err := fetch(base + "checksums.txt")
	if err != nil {
		return "", fmt.Errorf("checksums: %w", err)
	}
	asset := assetName(tag)
	archive, err := fetch(base + asset)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	if err := verifyChecksum(sums, asset, archive); err != nil {
		return "", err
	}
	binary, err := extractBinary(asset, archive)
	if err != nil {
		return "", err
	}

	// Write beside the target, then rename over it — atomic on unix;
	// Windows needs the running binary moved aside first.
	tmp := exe + ".update"
	if err := os.WriteFile(tmp, binary, 0o755); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
		if err := os.Rename(exe, exe+".old"); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return exe, nil
}

func fetch(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

func verifyChecksum(sums []byte, asset string, archive []byte) error {
	sum := sha256.Sum256(archive)
	want := hex.EncodeToString(sum[:])
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			if fields[0] != want {
				return fmt.Errorf("checksum mismatch for %s", asset)
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum listed for %s", asset)
}

// extractBinary pulls the session-protect executable out of a release
// archive.
func extractBinary(asset string, archive []byte) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, file := range reader.File {
			if filepath.Base(file.Name) == "session-protect.exe" {
				rc, err := file.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 256<<20))
			}
		}
		return nil, fmt.Errorf("binary not found in %s", asset)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err != nil {
			return nil, fmt.Errorf("binary not found in %s", asset)
		}
		if filepath.Base(header.Name) == "session-protect" && header.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(reader, 256<<20))
		}
	}
}

// ThrottledCheck runs NewerAvailable at most once per interval, caching
// the result in stateDir; failures are quiet (no update nag on a plane).
func ThrottledCheck(stateDir string, interval time.Duration) (latest string, newer bool) {
	type state struct {
		CheckedAt int64  `json:"checked_at"`
		Latest    string `json:"latest"`
	}
	path := filepath.Join(stateDir, ".update-check.json")
	var cached state
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cached)
	}
	if time.Since(time.Unix(cached.CheckedAt, 0)) < interval && cached.Latest != "" {
		return cached.Latest, semverLess(version.Version, cached.Latest)
	}
	_, latest, newer, err := NewerAvailable()
	if err != nil {
		return "", false
	}
	if data, err := json.Marshal(state{CheckedAt: time.Now().Unix(), Latest: latest}); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
	return latest, newer
}

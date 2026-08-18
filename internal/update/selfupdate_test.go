package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.1", "v1.0.0", false},
		{"v1.2.0", "v1.10.0", true}, // numeric, not lexical
		{"v2.0.0", "v1.99.99", false},
		{"dev", "v1.0.0", true},                       // unstamped is always older
		{"v0.0.0-20260805-abc+dirty", "v1.0.0", true}, // pseudo-version older
		{"v1.0.0-rc.1", "v1.0.0", true},               // prerelease older than release
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "not-a-version", false}, // garbage latest never wins
	}
	for _, tc := range cases {
		if got := semverLess(tc.a, tc.b); got != tc.want {
			t.Errorf("semverLess(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func releaseArchive(t *testing.T, content string) (archive []byte, sums string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	name := "session-protect"
	if runtime.GOOS == "windows" {
		t.Skip("tar fixture is unix-shaped")
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:]) + "  " + assetName("v1.1.0") + "\n"
}

func TestApplyEndToEnd(t *testing.T) {
	archive, sums := releaseArchive(t, "NEW BINARY BYTES")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			_, _ = w.Write([]byte(sums))
		case strings.HasSuffix(r.URL.Path, assetName("v1.1.0")):
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	downloadBase = server.URL
	t.Cleanup(func() { downloadBase = "https://github.com" })

	target := filepath.Join(t.TempDir(), "session-protect")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return target, nil }
	t.Cleanup(func() { executablePath = os.Executable })

	path, err := Apply("v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "NEW BINARY BYTES" {
		t.Fatalf("binary not swapped: %q", data)
	}
	info, _ := os.Stat(path)
	if info.Mode()&0o111 == 0 {
		t.Fatal("updated binary not executable")
	}
}

func TestApplyRejectsBadChecksum(t *testing.T) {
	archive, _ := releaseArchive(t, "EVIL")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			_, _ = w.Write([]byte("deadbeef  " + assetName("v1.1.0") + "\n"))
			return
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	downloadBase = server.URL
	t.Cleanup(func() { downloadBase = "https://github.com" })
	target := filepath.Join(t.TempDir(), "session-protect")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return target, nil }
	t.Cleanup(func() { executablePath = os.Executable })

	if _, err := Apply("v1.1.0"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum must refuse: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "OLD" {
		t.Fatal("binary replaced despite checksum failure")
	}
}

func TestThrottledCheckCaches(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"tag_name":"v99.0.0"}`)
	}))
	defer server.Close()
	apiBase = server.URL
	t.Cleanup(func() { apiBase = "https://api.github.com" })

	dir := t.TempDir()
	latest, newer := ThrottledCheck(dir, time.Hour)
	if latest != "v99.0.0" || !newer {
		t.Fatalf("first check: %s %v", latest, newer)
	}
	latest, newer = ThrottledCheck(dir, time.Hour)
	if latest != "v99.0.0" || !newer {
		t.Fatalf("cached check: %s %v", latest, newer)
	}
	if calls != 1 {
		t.Fatalf("throttle failed: %d API calls", calls)
	}
}

func TestBrewManagedRouting(t *testing.T) {
	if !brewManaged("/opt/homebrew/Cellar/session-protect/1.0.0/bin/session-protect") {
		t.Fatal("Cellar path must be brew-managed")
	}
	if brewManaged("/Users/x/.local/bin/session-protect") {
		t.Fatal("local bin must not be brew-managed")
	}

	target := filepath.Join(t.TempDir(), "Cellar", "session-protect", "1.0.0", "bin")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(target, "session-protect")
	if err := os.WriteFile(bin, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	executablePath = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executablePath = os.Executable })

	if _, err := Apply("v1.1.0"); !errors.Is(err, ErrBrewManaged) {
		t.Fatalf("Apply in Cellar must refuse with ErrBrewManaged, got %v", err)
	}
	if data, _ := os.ReadFile(bin); string(data) != "OLD" {
		t.Fatal("brew-managed binary was touched")
	}
}

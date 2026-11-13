package hostbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestLinkedUserDirectory(t *testing.T) {
	real := tempDir(t)
	link := filepath.Join(tempDir(t), "user")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", link)
	} else {
		t.Setenv("HOME", link)
	}
	root, err := cacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(real, root); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("cache did not resolve user directory: %s (%v)", root, err)
	}
	fixture(t)
	if _, err := Ensure(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectSymlinkAncestor(t *testing.T) {
	fixture(t)
	root := tempDir(t)
	link := filepath.Join(root, "linked")
	if err := os.Symlink(tempDir(t), link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if _, err := EnsureAt(filepath.Join(link, "nested", "host")); err == nil {
		t.Fatal("accepted symlink ancestor")
	}
}
func TestReadonlyCorruptCache(t *testing.T) {
	info := fixture(t)
	root := tempDir(t)
	path, err := EnsureAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bad"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o700)
	_, err = EnsureAt(root)
	if err == nil {
		if err := verifyFile(path, info); err != nil {
			t.Fatal(err)
		}
	}
	// Windows 只读文件无法原子替换时必须报错，不能返回坏文件路径。
	if err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) Info {
	t.Helper()
	oldPayload, oldMetadata := payload, metadata
	t.Cleanup(func() { payload, metadata = oldPayload, oldMetadata })
	payload = []byte("test host executable")
	sum := sha256.Sum256(payload)
	info := Info{Version: "1.2.3", BuildID: "test-build", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SHA256: hex.EncodeToString(sum[:])}
	metadata, _ = json.Marshal(info)
	return info
}
func TestPreserveOldHashAndPrivateDirectories(t *testing.T) {
	fixture(t)
	root := filepath.Join(tempDir(t), "cache")
	old := filepath.Join(root, "old-hash", "keep")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := EnsureAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("old version was removed:", err)
	}
	if runtime.GOOS != "windows" {
		for _, dir := range []string{root, filepath.Dir(path)} {
			st, err := os.Stat(dir)
			if err != nil || st.Mode().Perm() != 0o700 {
				t.Fatalf("non-private directory %s: %v", dir, err)
			}
		}
	}
}
func TestEmbeddedMetadata(t *testing.T) {
	if !Available() {
		t.Skip("unbundled development build")
	}
	if _, err := Metadata(); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAt(tempDir(t)); err != nil {
		t.Fatal(err)
	}
}
func TestUnavailable(t *testing.T) {
	oldPayload := payload
	payload = nil
	defer func() { payload = oldPayload }()
	if Available() {
		t.Fatal("unexpected bundle")
	}
	if _, err := EnsureAt(tempDir(t)); err == nil {
		t.Fatal("expected unavailable error")
	}
}
func TestEnsureConcurrentAndRepair(t *testing.T) {
	info := fixture(t)
	root := tempDir(t)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			path, err := EnsureAt(root)
			if err != nil {
				t.Error(err)
				return
			}
			if path != expectedAt(root, info) {
				t.Error(path)
			}
		})
	}
	wg.Wait()
	path := expectedAt(root, info)
	if err := os.WriteFile(path, []byte("corrupted"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAt(root); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(path, info); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) && entry.Name() != ".extract.lock" {
			t.Errorf("temporary file left: %s", entry.Name())
		}
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o700 {
			t.Fatal(st.Mode())
		}
	}
}
func TestRejectBadMetadata(t *testing.T) {
	info := fixture(t)
	info.GOARCH = "wrong"
	metadata, _ = json.Marshal(info)
	if _, err := EnsureAt(tempDir(t)); err == nil {
		t.Fatal("accepted wrong platform")
	}
	info.GOARCH = runtime.GOARCH
	info.SHA256 = "../bad"
	metadata, _ = json.Marshal(info)
	if _, err := EnsureAt(tempDir(t)); err == nil {
		t.Fatal("accepted wrong hash")
	}
}
func TestRejectDirectoryTarget(t *testing.T) {
	info := fixture(t)
	root := tempDir(t)
	path := expectedAt(root, info)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAt(root); err == nil {
		t.Fatal("accepted directory target")
	}
}
func TestRejectSymlink(t *testing.T) {
	info := fixture(t)
	root := tempDir(t)
	elsewhere := tempDir(t)
	if err := os.Symlink(elsewhere, filepath.Join(root, info.SHA256)); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if _, err := EnsureAt(root); err == nil {
		t.Fatal("accepted symlink directory")
	}
}

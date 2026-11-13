// Package hostbundle 提供随桌面应用发布的 host；普通 go build 不含资源。
package hostbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ALFO111111/agent-sync/internal/fsutil"
)

var ErrUnavailable = errors.New("desktop build does not contain a host bundle")

type Info struct {
	Version string `json:"version"`
	BuildID string `json:"buildID"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
	SHA256  string `json:"sha256"`
}

func Available() bool { return len(payload) > 0 }
func Metadata() (Info, error) {
	if !Available() {
		return Info{}, ErrUnavailable
	}
	var info Info
	if err := json.Unmarshal(metadata, &info); err != nil {
		return info, fmt.Errorf("host metadata: %w", err)
	}
	if info.GOOS != runtime.GOOS || info.GOARCH != runtime.GOARCH {
		return info, fmt.Errorf("host platform %s/%s does not match %s/%s", info.GOOS, info.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	sum := sha256.Sum256(payload)
	if info.SHA256 != hex.EncodeToString(sum[:]) {
		return info, errors.New("host payload SHA256 mismatch")
	}
	if info.Version == "" || info.BuildID == "" {
		return info, errors.New("host metadata has no version/buildID")
	}
	return info, nil
}
func Version() string { info, _ := Metadata(); return info.Version }
func BuildID() string { info, _ := Metadata(); return info.BuildID }

func cacheRoot() (string, error) {
	if runtime.GOOS == "windows" {
		dir := os.Getenv("LOCALAPPDATA")
		if dir == "" {
			return "", errors.New("LOCALAPPDATA is not set")
		}
		dir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "AgentSync", "host"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "AgentSync", "host"), nil
	}
	return filepath.Join(home, ".cache", "AgentSync", "host"), nil
}
func expectedAt(root string, info Info) string {
	name := "agent-sync-host"
	if info.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, info.SHA256, name)
}
func ExpectedPath() (string, error) {
	info, err := Metadata()
	if err != nil {
		return "", err
	}
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	return expectedAt(root, info), nil
}

// Path 校验已提取的文件，但不会创建目录或启动进程。
func Path() (string, error) {
	path, err := ExpectedPath()
	if err != nil {
		return "", err
	}
	info, err := Metadata()
	if err != nil {
		return "", err
	}
	if err := verifyFile(path, info); err != nil {
		return "", err
	}
	return path, nil
}
func Ensure() (string, error) {
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	return EnsureAt(root)
}

// EnsureAt 在隔离的缓存根目录下按 hash 提取；不删除旧版本，不启动服务。
func EnsureAt(root string) (string, error) {
	info, err := Metadata()
	if err != nil {
		return "", err
	}
	path := expectedAt(root, info)
	dir := filepath.Dir(path)
	if err := privateDir(root); err != nil {
		return "", err
	}
	if err := privateDir(dir); err != nil {
		return "", err
	}
	lockPath := filepath.Join(dir, ".extract.lock")
	if st, err := os.Lstat(lockPath); err == nil && !st.Mode().IsRegular() {
		return "", errors.New("host extraction lock is not a regular file")
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ok, err := fsutil.TryLockFile(lock)
		if err != nil {
			return "", err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return "", errors.New("timed out waiting for host extraction lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer fsutil.UnlockFile(lock)
	if err := verifyFile(path, info); err == nil {
		if err := os.Chmod(path, 0o700); err != nil {
			return "", err
		}
		return path, nil
	}
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return "", errors.New("host cache target is not a regular file")
	}
	tmp, err := os.CreateTemp(dir, ".host-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o700); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	if err := verifyFile(path, info); err != nil {
		return "", err
	}
	return path, nil
}
func privateDir(dir string) error {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if err == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("host cache ancestor is not a real directory: %s", current)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("host cache is not a real directory: %s", dir)
	}
	return os.Chmod(dir, 0o700)
}
func verifyFile(path string, info Info) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("host cache is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != info.SHA256 {
		return errors.New("cached host SHA256 mismatch")
	}
	return nil
}

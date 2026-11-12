package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ALFO111111/agent-sync/internal/remotev1"
)

const (
	stateFile    = "host.json"
	keyFile      = "state.key"
	magic        = "AGS1"
	keychainID   = "agent-sync-host"
	keychainAcct = "state-key"
)

// fileStore 把 State 加密后写盘；加密密钥优先放 macOS 钥匙串。
type fileStore struct {
	dir string
	key []byte
}

func newFileStore(dir string) (*fileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := loadOrCreateKey(dir)
	if err != nil {
		return nil, err
	}
	return &fileStore{dir: dir, key: key}, nil
}

func (s *fileStore) path() string { return filepath.Join(s.dir, stateFile) }

func (s *fileStore) Load() (*remotev1.State, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) < len(magic)+12 || string(b[:len(magic)]) != magic {
		return nil, errors.New("state file is not agent-sync format")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := b[len(magic) : len(magic)+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, b[len(magic)+gcm.NonceSize():], []byte("agent-sync-host-v1"))
	if err != nil {
		return nil, errors.New("state file decrypt failed")
	}
	var st remotev1.State
	if err := json.Unmarshal(plain, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *fileStore) Save(st *remotev1.State) error {
	plain, err := json.Marshal(st)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	out := append([]byte(magic), nonce...)
	out = gcm.Seal(out, nonce, plain, []byte("agent-sync-host-v1"))
	return writeFileAtomic(s.path(), out, 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ags-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// loadOrCreateKey 优先使用 macOS 钥匙串；不可用时退回 0600 的密钥文件并提示。
func loadOrCreateKey(dir string) ([]byte, error) {
	if runtime.GOOS == "darwin" {
		if key, err := keychainRead(); err == nil && len(key) == 32 {
			return key, nil
		}
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		if err := keychainWrite(key); err == nil {
			return key, nil
		}
		// 钥匙串不可用：退回文件，打印明确警告。
		os.Stderr.WriteString("warning: macOS Keychain unavailable; state key stored in a 0600 file\n")
		return fileKey(dir)
	}
	return fileKey(dir)
}

func fileKey(dir string) ([]byte, error) {
	p := filepath.Join(dir, keyFile)
	b, err := os.ReadFile(p)
	if err == nil {
		key, derr := hex.DecodeString(strings.TrimSpace(string(b)))
		if derr != nil || len(key) != 32 {
			return nil, errors.New("state key file is invalid")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func keychainRead() ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", keychainID, "-a", keychainAcct, "-w").Output()
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimSpace(string(out)))
}

func keychainWrite(key []byte) error {
	// -U 存在则更新；密码通过参数传入，进程列表里短暂可见，避免写临时文件。
	return exec.Command("security", "add-generic-password", "-U", "-s", keychainID, "-a", keychainAcct, "-w", hex.EncodeToString(key)).Run()
}

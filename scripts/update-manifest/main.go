// 生成自托管更新清单：计算文件 SHA256、用 Ed25519 私钥签名、输出 latest.json。
//
//	go run ./scripts/update-manifest -genkey D:\secure\update.key
//	go run ./scripts/update-manifest -version 0.7.1 -file build/bin/agent-sync.exe \
//	  -url https://dl.example.com/agent-sync.exe -key D:\secure\update.key -out latest.json
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type asset struct {
	Platform string `json:"platform"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

type manifest struct {
	Version     string  `json:"version"`
	PublishedAt string  `json:"publishedAt"`
	Assets      []asset `json:"assets"`
	Signature   string  `json:"signature"`
}

func payload(m manifest) []byte {
	m.Signature = ""
	b, _ := json.Marshal(m)
	return b
}

func main() {
	genkey := flag.String("genkey", "", "generate a new private key (base64 32-byte seed) and print the public key")
	version := flag.String("version", "", "version, e.g. 0.7.1")
	platform := flag.String("platform", runtime.GOOS+"/"+runtime.GOARCH, "platform, e.g. windows/amd64")
	file := flag.String("file", "", "installer file path")
	url := flag.String("url", "", "installer download URL (https)")
	name := flag.String("name", "", "file name seen by clients (defaults to the file base name)")
	notes := flag.String("notes", "", "release notes")
	keyPath := flag.String("key", "", "Ed25519 private key file (base64 of a 32-byte seed)")
	out := flag.String("out", "latest.json", "output path")
	flag.Parse()

	if *genkey != "" {
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			fail(err)
		}
		if err := os.MkdirAll(filepath.Dir(*genkey), 0o700); err != nil && filepath.Dir(*genkey) != "." {
			fail(err)
		}
		if err := os.WriteFile(*genkey, []byte(base64.StdEncoding.EncodeToString(seed)), 0o600); err != nil {
			fail(err)
		}
		pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		fmt.Println("private key:", *genkey)
		fmt.Println("public key (paste into internal/update/manifest.go):", base64.StdEncoding.EncodeToString(pub))
		return
	}
	if *version == "" || *file == "" || *url == "" || *keyPath == "" {
		fail(fmt.Errorf("required: -version -file -url -key"))
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		fail(err)
	}
	if *name == "" {
		*name = filepath.Base(*file)
	}
	sum := sha256.Sum256(raw)
	m := manifest{
		Version:     *version,
		PublishedAt: time.Now().UTC().Format(time.RFC3339),
		Assets: []asset{{
			Platform: *platform, Name: *name, URL: *url,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw)), Notes: *notes,
		}},
	}
	keyB64, err := os.ReadFile(*keyPath)
	if err != nil {
		fail(err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyB64)))
	if err != nil || len(seed) != ed25519.SeedSize {
		fail(fmt.Errorf("private key must be base64 of a 32-byte seed"))
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), payload(m)))
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s (version %s, %s, %d bytes)\n", *out, m.Version, m.Assets[0].SHA256, len(raw))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

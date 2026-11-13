// build-desktop 是 Windows/macOS 共用的正式桌面打包入口。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type metadata struct {
	Version string `json:"version"`
	BuildID string `json:"buildID"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
	SHA256  string `json:"sha256"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "build-desktop:", err)
		os.Exit(1)
	}
}
func run() error {
	platform := flag.String("platform", runtime.GOOS+"/"+runtime.GOARCH, "windows/amd64 or darwin/arm64")
	version := flag.String("version", "dev", "release version (optional v prefix)")
	buildID := flag.String("build-id", "", "host build ID (default: git HEAD)")
	check := flag.Bool("check", true, "run the final desktop's offline host bundle probe")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *platform != "windows/amd64" && *platform != "darwin/arm64" {
		return fmt.Errorf("unsupported platform %q", *platform)
	}
	numeric, err := productVersion(*version)
	if err != nil {
		return err
	}
	root, err := projectRoot()
	if err != nil {
		return err
	}
	if *buildID == "" {
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("git build ID: %w (or pass -build-id)", err)
		}
		*buildID = strings.TrimSpace(string(out))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._+\-]+$`).MatchString(*buildID) {
		return errors.New("build-id contains unsupported characters")
	}
	parts := strings.Split(*platform, "/")
	if *check && parts[0] != runtime.GOOS {
		return errors.New("offline probe requires the target operating system (use -check=false for cross-compilation)")
	}
	generated := filepath.Join(root, "internal", "hostbundle", "generated")
	if err := os.MkdirAll(generated, 0o700); err != nil {
		return err
	}
	ldflags := "-s -w -X main.version=" + *version + " -X main.buildID=" + *buildID
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goExe += ".exe"
	}
	env := append(os.Environ(), "GOOS="+parts[0], "GOARCH="+parts[1], "CGO_ENABLED=0")
	// 必须构建整个包，不能只传 main.go（控制协议等实现位于同目录其他文件）。
	if err := command(root, env, goExe, "build", "-trimpath", "-ldflags", ldflags, "-o", filepath.Join(generated, "host.bin"), "./cmd/agent-sync-host"); err != nil {
		return err
	}
	payload, err := os.ReadFile(filepath.Join(generated, "host.bin"))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	info := metadata{*version, *buildID, parts[0], parts[1], hex.EncodeToString(sum[:])}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(generated, "metadata.json"), data, 0o600); err != nil {
		return err
	}
	if parts[0] == runtime.GOOS && parts[1] == runtime.GOARCH {
		if err := command(root, nil, goExe, "test", "-tags", "bundled_host", "./internal/hostbundle"); err != nil {
			return err
		}
	}
	// 修改构建所需的产品数字版本，结束后还原，不污染共享工作区。
	configPath := filepath.Join(root, "wails.json")
	original, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var config map[string]any
	if err := json.Unmarshal(original, &config); err != nil {
		return err
	}
	configInfo, ok := config["info"].(map[string]any)
	if !ok {
		return errors.New("wails.json has no info object")
	}
	configInfo["productVersion"] = numeric
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, append(configData, '\n'), 0o644); err != nil {
		return err
	}
	defer func() {
		if err := os.WriteFile(configPath, original, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "restore wails.json:", err)
		}
	}()
	md5Path := filepath.Join(root, "frontend", "package.json.md5")
	oldMD5, md5Err := os.ReadFile(md5Path)
	if md5Err != nil && !os.IsNotExist(md5Err) {
		return md5Err
	}
	defer func() {
		if md5Err == nil {
			_ = os.WriteFile(md5Path, oldMD5, 0o644)
		} else {
			_ = os.Remove(md5Path)
		}
	}()
	// 不覆盖 outputfilename：Windows exe 和 macOS app 继续使用原有名称。
	if err := command(root, nil, "wails", "build", "-clean", "-trimpath", "-platform", *platform, "-tags", "bundled_host", "-ldflags", ldflags); err != nil {
		return err
	}
	if *check {
		executable := filepath.Join(root, "build", "bin", "agent-sync.exe")
		if parts[0] == "darwin" {
			executable = filepath.Join(root, "build", "bin", "agent-sync.app", "Contents", "MacOS", "agent-sync")
		}
		dir, err := os.MkdirTemp("", "agent-sync-bundle-check-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		dir, err = filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		// cmd.Run 等待 Windows GUI exe 真正退出；不通过 shell 启动，避免假成功。
		cmd := exec.CommandContext(ctx, executable, "--check-host-bundle", dir)
		cmd.Dir = dir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("final desktop offline probe: %w", err)
		}
		fmt.Println("Offline host bundle check passed.")
	}
	return nil
}
func command(dir string, env []string, name string, args ...string) error {
	fmt.Println("+", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "wails.json")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run inside the agent-sync repository")
		}
		dir = parent
	}
}
func productVersion(version string) (string, error) {
	if version == "dev" {
		return "0.0.0", nil
	}
	match := regexp.MustCompile(`^v?(\d+\.\d+\.\d+)(?:[-+][A-Za-z0-9.+\-]+)?$`).FindStringSubmatch(version)
	if match == nil {
		return "", fmt.Errorf("invalid version %q", version)
	}
	return match[1], nil
}

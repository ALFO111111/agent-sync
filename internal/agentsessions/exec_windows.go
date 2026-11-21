//go:build windows

package agentsessions

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// dpRef 匹配 npm 生成的 .cmd 包装里对入口文件的引用：
// "%_prog%" "%dp0%\node_modules\...\cli.js" %*
// "%dp0%\node_modules\...\claude.exe" %*
var dpRef = regexp.MustCompile(`(?i)(?:%dp0%|%~dp0)[\\/]?([^"\s]+\.(?:exe|js|mjs|cjs))`)

// agentCommand 在 Windows 上构造子进程。.cmd/.bat 不再交给 cmd.exe，
// 而是解析出真正的 exe 或 node + JS 入口直接执行，避免参数里的 shell 元字符被解释。
func agentCommand(bin string, args []string) (*exec.Cmd, error) {
	lower := strings.ToLower(bin)
	if strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat") {
		target, script, err := resolveCmdShim(bin)
		if err != nil {
			return nil, err
		}
		if script == "" {
			return exec.Command(target, args...), nil
		}
		return exec.Command(target, append([]string{script}, args...)...), nil
	}
	return exec.Command(bin, args...), nil
}

// resolveCmdShim 读取 npm 风格的 .cmd 包装，返回可执行文件和脚本路径。
// 包装直接指向 .exe 时脚本为空。
func resolveCmdShim(path string) (exe, script string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	matches := dpRef.FindAllStringSubmatch(string(b), -1)
	if len(matches) == 0 {
		return "", "", fmt.Errorf("cannot safely execute %s: no entry point found; install the .exe version", filepath.Base(path))
	}
	rel := matches[len(matches)-1][1]
	dir := filepath.Dir(path)
	target := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(rel, `\`, `/`)))
	if _, err := os.Stat(target); err != nil {
		return "", "", fmt.Errorf("cannot safely execute %s: %w", filepath.Base(path), err)
	}
	if strings.EqualFold(filepath.Ext(target), ".exe") {
		return target, "", nil
	}
	node := filepath.Join(dir, "node.exe")
	if _, err := os.Stat(node); err != nil {
		if p, lookErr := exec.LookPath("node"); lookErr == nil {
			node = p
		} else {
			return "", "", fmt.Errorf("cannot safely execute %s: node not found", filepath.Base(path))
		}
	}
	return node, target, nil
}

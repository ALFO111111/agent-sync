//go:build windows

package agentsessions

import (
	"os/exec"
	"syscall"
)

// configureProcess 让 Claude/Codex 子进程不弹出独立控制台窗口。
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

//go:build windows

package service

import (
	"os/exec"
	"syscall"
)

// hideWindow 让从图形界面启动的主机组件不弹出黑色命令行窗口。
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

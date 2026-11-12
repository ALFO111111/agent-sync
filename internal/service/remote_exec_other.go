//go:build !windows

package service

import "os/exec"

// hideWindow 只在 Windows 上需要（避免弹出控制台窗口）。
func hideWindow(cmd *exec.Cmd) {}

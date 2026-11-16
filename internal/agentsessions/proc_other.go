//go:build !windows

package agentsessions

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}

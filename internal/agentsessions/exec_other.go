//go:build !windows

package agentsessions

import "os/exec"

func agentCommand(bin string, args []string) (*exec.Cmd, error) {
	return exec.Command(bin, args...), nil
}
